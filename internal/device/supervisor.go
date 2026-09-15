// Package device supervises one instrument: it keeps an OPC UA session alive,
// subscribes to result state changes and hands every finished result to the
// outbox. Read-only — no Method calls, no Writes.
package device

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/ua"

	"github.com/labnote/labnote-device-connector/internal/certs"
	"github.com/labnote/labnote-device-connector/internal/lads"
	"github.com/labnote/labnote-device-connector/internal/mapping"
	"github.com/labnote/labnote-device-connector/internal/model"
	"github.com/labnote/labnote-device-connector/internal/outbox"
	"github.com/labnote/labnote-device-connector/internal/profiles"
	"github.com/labnote/labnote-device-connector/internal/state"
)

// Sink receives finished results (the outbox in production).
type Sink interface {
	Enqueue(ctx context.Context, r model.Result) error
}

// TrustStore persists a pinned instrument certificate fingerprint.
type TrustStore interface {
	// Pinned returns the fingerprint stored for the instrument, "" when none.
	Pinned(instrumentID string) string
	// Pin stores a confirmed fingerprint.
	Pin(instrumentID, fingerprint string) error
}

// Supervisor runs the connect/subscribe/reconnect loop for one instrument.
type Supervisor struct {
	ins      model.Instrument
	profile  profiles.Profile
	pki      *certs.Store
	sink     Sink
	st       *state.Store
	trust    TrustStore
	log      *slog.Logger

	mu       sync.Mutex
	client   *opcua.Client
	seen     map[string]bool // external_result_id already forwarded this session
}

// New creates a supervisor.
func New(
	ins model.Instrument,
	prof profiles.Profile,
	pki *certs.Store,
	sink Sink,
	st *state.Store,
	trust TrustStore,
	log *slog.Logger,
) *Supervisor {
	return &Supervisor{
		ins: ins, profile: prof, pki: pki, sink: sink, st: st, trust: trust,
		log:  log.With("device", ins.ExternalDeviceID),
		seen: map[string]bool{},
	}
}

// Instrument returns the supervised instrument.
func (s *Supervisor) Instrument() model.Instrument { return s.ins }

// Run blocks until ctx is cancelled, reconnecting with exponential backoff
// capped at 5 minutes.
func (s *Supervisor) Run(ctx context.Context) {
	backoff := 2 * time.Second
	const maxBackoff = 5 * time.Minute

	for {
		if ctx.Err() != nil {
			return
		}
		err := s.session(ctx)
		switch {
		case ctx.Err() != nil:
			s.st.SetDeviceStatus(s.ins, model.StatusDisconnected, "")
			return
		case err != nil:
			s.st.SetDeviceStatus(s.ins, model.StatusError, err.Error())
			s.log.Warn("session ended", "error", err, "retry_in", backoff.String())
		default:
			s.st.SetDeviceStatus(s.ins, model.StatusDisconnected, "")
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func (s *Supervisor) session(ctx context.Context) error {
	client, err := s.dial(ctx)
	if err != nil {
		return err
	}
	defer func() {
		_ = client.Close(context.WithoutCancel(ctx))
		s.mu.Lock()
		s.client = nil
		s.mu.Unlock()
	}()

	s.mu.Lock()
	s.client = client
	s.mu.Unlock()
	s.st.SetDeviceStatus(s.ins, model.StatusConnected, "")
	s.log.Info("connected", "endpoint", s.ins.EndpointURL)

	browser := lads.NewBrowser(client)
	resultSets, err := browser.ResultSetNodes(ctx, s.ins.LADSNodeID)
	if err != nil {
		return fmt.Errorf("browse LADS model: %w", err)
	}

	// Drain results that already finished while the connector was away.
	for _, rs := range resultSets {
		s.scan(ctx, browser, rs)
	}

	notifications := make(chan *opcua.PublishNotificationData, 64)
	sub, err := client.Subscribe(ctx, &opcua.SubscriptionParameters{
		Interval: 500 * time.Millisecond,
	}, notifications)
	if err != nil {
		return fmt.Errorf("create subscription: %w", err)
	}
	defer func() { _ = sub.Cancel(context.WithoutCancel(ctx)) }()

	handles := map[uint32]*ua.NodeID{}
	var handle uint32
	for _, rs := range resultSets {
		results, err := browser.Results(ctx, rs)
		if err != nil {
			continue
		}
		for _, res := range results {
			stateNode, err := browser.StateVariable(ctx, res)
			if err != nil {
				continue
			}
			handle++
			handles[handle] = res
			req := opcua.NewMonitoredItemCreateRequestWithDefaults(stateNode, ua.AttributeIDValue, handle)
			if _, err := sub.Monitor(ctx, ua.TimestampsToReturnSource, req); err != nil {
				s.log.Warn("monitor result state failed", "error", err)
			}
		}
		// Watch the ResultSet itself so new results are picked up too.
		handle++
		handles[handle] = rs
		req := opcua.NewMonitoredItemCreateRequestWithDefaults(rs, ua.AttributeIDValue, handle)
		_, _ = sub.Monitor(ctx, ua.TimestampsToReturnSource, req)
	}
	if len(handles) == 0 {
		return errors.New("no result state variables to subscribe to")
	}
	s.log.Info("subscribed", "monitored_items", len(handles))

	keepAlive := time.NewTicker(30 * time.Second)
	defer keepAlive.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-keepAlive.C:
			if _, err := client.Node(ua.NewNumericNodeID(0, 2258)).Value(ctx); err != nil {
				return fmt.Errorf("keep-alive failed: %w", err)
			}
		case n, ok := <-notifications:
			if !ok {
				return errors.New("notification channel closed")
			}
			if n.Error != nil {
				return fmt.Errorf("subscription error: %w", n.Error)
			}
			dcn, ok := n.Value.(*ua.DataChangeNotification)
			if !ok {
				continue
			}
			for _, item := range dcn.MonitoredItems {
				node := handles[item.ClientHandle]
				if node == nil {
					continue
				}
				value := ""
				if item.Value != nil {
					value = lads.VariantToString(item.Value.Value)
				}
				if mapping.IsFinished(value, s.profile) {
					s.forward(ctx, browser, node)
					continue
				}
				// Unknown notification: rescan the whole set, cheap and safe.
				s.scan(ctx, browser, node)
			}
		}
	}
}

// scan walks a ResultSet and forwards every finished result.
func (s *Supervisor) scan(ctx context.Context, b *lads.Browser, resultSet *ua.NodeID) {
	results, err := b.Results(ctx, resultSet)
	if err != nil {
		return
	}
	for _, res := range results {
		stateNode, err := b.StateVariable(ctx, res)
		if err != nil {
			continue
		}
		dv, err := b.SourceTimestamp(ctx, stateNode)
		if err != nil || dv == nil {
			continue
		}
		if mapping.IsFinished(lads.VariantToString(dv.Value), s.profile) {
			s.forward(ctx, b, res)
		}
	}
}

func (s *Supervisor) forward(ctx context.Context, b *lads.Browser, resultNode *ua.NodeID) {
	rec, err := mapping.Build(ctx, b, s.ins, s.profile, resultNode)
	if err != nil {
		s.log.Warn("map result failed", "error", err)
		return
	}
	s.mu.Lock()
	already := s.seen[rec.ExternalResultID]
	s.seen[rec.ExternalResultID] = true
	s.mu.Unlock()
	if already {
		return
	}
	if err := s.sink.Enqueue(ctx, rec); err != nil {
		s.log.Error("enqueue result failed", "error", err)
		return
	}
	s.st.RecordResult(s.ins)
	// Metadata only — result payloads are never logged.
	s.log.Info("result queued",
		"external_result_id", rec.ExternalResultID,
		"points", len(rec.Points),
		"has_sample_code", rec.SampleCode != "")
}

// dial selects a SignAndEncrypt endpoint, verifies the pinned server
// certificate and opens the session.
func (s *Supervisor) dial(ctx context.Context) (*opcua.Client, error) {
	endpoints, err := opcua.GetEndpoints(ctx, s.ins.EndpointURL)
	if err != nil {
		return nil, fmt.Errorf("get endpoints: %w", err)
	}

	ep := selectSecureEndpoint(endpoints, s.ins.SecurityPolicy)
	if ep == nil {
		return nil, fmt.Errorf(
			"instrument offers no %s / SignAndEncrypt endpoint — unencrypted and anonymous connections are refused",
			s.ins.SecurityPolicy)
	}

	fingerprint := certs.FingerprintDER(ep.ServerCertificate)
	pinned := s.trust.Pinned(s.ins.ID)
	if pinned == "" {
		s.st.SetPendingTrust(s.ins, fingerprint, true)
		return nil, fmt.Errorf(
			"instrument certificate is not trusted yet (fingerprint %s) — confirm it in the connector setup screen",
			fingerprint)
	}
	if !certs.SameFingerprint(pinned, fingerprint) {
		s.st.SetPendingTrust(s.ins, fingerprint, true)
		return nil, fmt.Errorf(
			"instrument certificate changed (expected %s, got %s) — connection refused; re-confirm it in the connector setup screen if the change was planned",
			pinned, fingerprint)
	}
	s.st.SetPendingTrust(s.ins, fingerprint, false)
	_ = s.pki.SaveServerCert(s.ins.ExternalDeviceID, ep.ServerCertificate)

	clientDER, err := s.pki.ClientCertDER()
	if err != nil {
		return nil, fmt.Errorf("read client certificate: %w", err)
	}

	client, err := opcua.NewClient(ep.EndpointURL,
		opcua.SecurityFromEndpoint(ep, ua.UserTokenTypeCertificate),
		opcua.CertificateFile(s.pki.CertPath()),
		opcua.PrivateKeyFile(s.pki.KeyPath()),
		opcua.AuthCertificate(clientDER),
		opcua.AutoReconnect(false), // the Run loop owns reconnection
		opcua.RequestTimeout(20*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("create client: %w", err)
	}
	if err := client.Connect(ctx); err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	return client, nil
}

// selectSecureEndpoint returns the first endpoint that both uses
// SignAndEncrypt with the requested policy and offers certificate-based user
// authentication. Anonymous-only endpoints are skipped.
func selectSecureEndpoint(endpoints []*ua.EndpointDescription, policy string) *ua.EndpointDescription {
	for _, ep := range endpoints {
		if ep.SecurityMode != ua.MessageSecurityModeSignAndEncrypt {
			continue
		}
		if !strings.HasSuffix(ep.SecurityPolicyURI, "#"+policy) {
			continue
		}
		for _, token := range ep.UserIdentityTokens {
			if token.TokenType == ua.UserTokenTypeCertificate {
				return ep
			}
		}
	}
	return nil
}

// TestConnection browses an instrument once and reports what it found. Used by
// the Test connection button in the setup UI.
type TestReport struct {
	OK                 bool          `json:"ok"`
	Message            string        `json:"message"`
	ServerFingerprint  string        `json:"server_fingerprint,omitempty"`
	TrustRequired      bool          `json:"trust_required"`
	Devices            []lads.Device `json:"devices,omitempty"`
}

// TestConnection is a one-shot dial + browse used during setup.
func TestConnection(ctx context.Context, ins model.Instrument, pki *certs.Store, trust TrustStore, st *state.Store, log *slog.Logger) TestReport {
	sup := New(ins, profiles.Profile{}, pki, nopSink{}, st, trust, log)
	client, err := sup.dial(ctx)
	if err != nil {
		fp := ""
		if snap := st.Snapshot(); true {
			for _, d := range snap.Devices {
				if d.ExternalDeviceID == ins.ExternalDeviceID {
					fp = d.ServerCertSHA256
				}
			}
		}
		return TestReport{
			OK:                false,
			Message:           err.Error(),
			ServerFingerprint: fp,
			TrustRequired:     fp != "" && trust.Pinned(ins.ID) == "",
		}
	}
	defer func() { _ = client.Close(context.WithoutCancel(ctx)) }()

	devices, err := lads.NewBrowser(client).Devices(ctx)
	if err != nil {
		return TestReport{OK: false, Message: err.Error()}
	}
	return TestReport{
		OK:      true,
		Message: fmt.Sprintf("Connected. Found %d LADS device(s).", len(devices)),
		Devices: devices,
	}
}

type nopSink struct{}

func (nopSink) Enqueue(context.Context, model.Result) error { return nil }

// compile-time assertion that the outbox satisfies Sink.
var _ Sink = (*outbox.Outbox)(nil)
