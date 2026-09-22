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
	"github.com/labnote/labnote-device-connector/internal/keychain"
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
	ins     model.Instrument
	profile profiles.Profile
	pki     *certs.Store
	sink    Sink
	st      *state.Store
	trust   TrustStore
	log     *slog.Logger
	// password is an OPC UA user password supplied for a one-shot setup test.
	// The running supervisor reads it from the OS credential store instead.
	password string

	mu     sync.Mutex
	client *opcua.Client
	seen   map[string]bool // external_result_id already forwarded this session
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
	deviceNode, err := s.resolveDevice(ctx, browser)
	if err != nil {
		return err
	}
	resultSets, err := browser.ResultSetNodes(ctx, deviceNode)
	if err != nil {
		return fmt.Errorf("browse LADS model: %w", err)
	}

	// Drain results that already finished while the connector was away.
	for _, rs := range resultSets {
		s.scan(ctx, browser, rs)
	}

	notifications := make(chan *opcua.PublishNotificationData, 64)
	// Lifetime must outlast the keep-alive by a wide margin, otherwise a server
	// that publishes rarely deletes the subscription underneath us.
	sub, err := client.Subscribe(ctx, &opcua.SubscriptionParameters{
		Interval:                   500 * time.Millisecond,
		MaxNotificationsPerPublish: 1000,
		LifetimeCount:              2400, // 20 min at 500 ms
		MaxKeepAliveCount:          20,   // server pings every 10 s
		Priority:                   0,
	}, notifications)
	if err != nil {
		return fmt.Errorf("create subscription: %w", err)
	}
	defer func() { _ = sub.Cancel(context.WithoutCancel(ctx)) }()

	// Watch every signal that can indicate a new or finished result: the
	// ResultSet NodeVersion, plus each result's state or Stopped variable.
	// Any notification triggers a rescan, which is cheap and order-safe.
	var handle uint32
	monitored := 0
	for _, rs := range resultSets {
		for _, node := range browser.ChangeWatchNodes(ctx, rs) {
			handle++
			req := opcua.NewMonitoredItemCreateRequestWithDefaults(node, ua.AttributeIDValue, handle)
			if _, err := sub.Monitor(ctx, ua.TimestampsToReturnSource, req); err != nil {
				s.log.Warn("monitor node failed", "error", err)
				continue
			}
			monitored++
		}
	}
	s.log.Info("subscribed", "monitored_items", monitored)

	keepAlive := time.NewTicker(30 * time.Second)
	defer keepAlive.Stop()
	// Safety net for servers that publish neither NodeVersion nor result state.
	poll := time.NewTicker(5 * time.Second)
	defer poll.Stop()

	rescan := func() {
		for _, rs := range resultSets {
			s.scan(ctx, browser, rs)
		}
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-poll.C:
			rescan()
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
			if _, ok := n.Value.(*ua.DataChangeNotification); !ok {
				continue
			}
			rescan()
		}
	}
}

// scan walks a ResultSet and forwards every finished result. A result counts as
// finished when its state variable says so, or - for servers without a result
// state machine - when it carries a Stopped timestamp.
func (s *Supervisor) scan(ctx context.Context, b *lads.Browser, resultSet *ua.NodeID) {
	results, err := b.Results(ctx, resultSet)
	if err != nil {
		return
	}
	for _, res := range results {
		if stateNode, err := b.StateVariable(ctx, res); err == nil {
			dv, err := b.SourceTimestamp(ctx, stateNode)
			if err == nil && dv != nil && mapping.IsFinished(lads.VariantToString(dv.Value), s.profile) {
				s.forward(ctx, b, res)
			}
			continue
		}
		if _, ok := b.StoppedTime(ctx, res); ok {
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

	want := ua.UserTokenTypeCertificate
	login := "certificate login"
	if strings.TrimSpace(s.ins.Username) != "" {
		want = ua.UserTokenTypeUserName
		login = "username and password"
	}
	ep := selectSecureEndpoint(endpoints, s.ins.SecurityPolicy, want)
	if ep == nil {
		return nil, fmt.Errorf(
			"instrument offers no %s / SignAndEncrypt endpoint that accepts %s — unencrypted connections are refused",
			s.ins.SecurityPolicy, login)
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
	clientKey, err := s.pki.ClientPrivateKey()
	if err != nil {
		return nil, fmt.Errorf("read client private key: %w", err)
	}

	// Connect to the address the operator configured, not the one the server
	// advertises: instruments frequently advertise an internal hostname that
	// does not resolve from the connector host. The security settings still
	// come from the discovered endpoint.
	opts := []opcua.Option{
		opcua.CertificateFile(s.pki.CertPath()),
		opcua.PrivateKeyFile(s.pki.KeyPath()),
		opcua.AutoReconnect(false), // the Run loop owns reconnection
		opcua.RequestTimeout(20 * time.Second),
	}
	if user := strings.TrimSpace(s.ins.Username); user != "" {
		// The instrument expects a user account. The channel stays
		// Basic256Sha256 / SignAndEncrypt; only the login differs.
		pass := s.password
		if pass == "" {
			pass = keychain.InstrumentPassword(s.ins.ID)
		}
		opts = append(opts,
			opcua.SecurityFromEndpoint(ep, ua.UserTokenTypeUserName),
			opcua.AuthUsername(user, pass),
		)
	} else {
		opts = append(opts,
			opcua.SecurityFromEndpoint(ep, ua.UserTokenTypeCertificate),
			opcua.AuthCertificate(clientDER),
			// Certificate login signs the server nonce with this key; without
			// it the server rejects the session with BadSecurityChecksFailed.
			opcua.AuthPrivateKey(clientKey),
		)
	}
	client, err := opcua.NewClient(s.ins.EndpointURL, opts...)
	if err != nil {
		return nil, fmt.Errorf("create client: %w", err)
	}
	if err := client.Connect(ctx); err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	return client, nil
}

// selectSecureEndpoint returns the first endpoint that uses SignAndEncrypt
// with the requested policy and accepts the wanted user login (certificate or
// username/password). Unencrypted endpoints are always skipped.
func selectSecureEndpoint(endpoints []*ua.EndpointDescription, policy string, want ua.UserTokenType) *ua.EndpointDescription {
	for _, ep := range endpoints {
		if ep.SecurityMode != ua.MessageSecurityModeSignAndEncrypt {
			continue
		}
		if !strings.HasSuffix(ep.SecurityPolicyURI, "#"+policy) {
			continue
		}
		for _, token := range ep.UserIdentityTokens {
			if token.TokenType == want {
				return ep
			}
		}
	}
	return nil
}

// TestConnection browses an instrument once and reports what it found. Used by
// the Test connection button in the setup UI.
type TestReport struct {
	OK                bool          `json:"ok"`
	Message           string        `json:"message"`
	ServerFingerprint string        `json:"server_fingerprint,omitempty"`
	TrustRequired     bool          `json:"trust_required"`
	Devices           []lads.Device `json:"devices,omitempty"`
}

// TestConnection is a one-shot dial + browse used during setup.
func TestConnection(ctx context.Context, ins model.Instrument, pki *certs.Store, trust TrustStore, st *state.Store, log *slog.Logger, password ...string) TestReport {
	sup := New(ins, profiles.Profile{}, pki, nopSink{}, st, trust, log)
	if len(password) > 0 {
		sup.password = password[0]
	}
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

// ParameterReport is the answer to "what does this instrument measure?".
type ParameterReport struct {
	OK         bool             `json:"ok"`
	Message    string           `json:"message"`
	DeviceName string           `json:"device_name,omitempty"`
	NodeID     string           `json:"lads_node_id,omitempty"`
	Parameters []lads.Parameter `json:"parameters"`
}

// DetectParameters dials the instrument once and lists its measurable
// quantities, so the setup UI can offer them for selection.
func DetectParameters(ctx context.Context, ins model.Instrument, pki *certs.Store, trust TrustStore, st *state.Store, log *slog.Logger, password ...string) ParameterReport {
	sup := New(ins, profiles.Profile{}, pki, nopSink{}, st, trust, log)
	if len(password) > 0 {
		sup.password = password[0]
	}
	client, err := sup.dial(ctx)
	if err != nil {
		return ParameterReport{Message: err.Error(), Parameters: []lads.Parameter{}}
	}
	defer func() { _ = client.Close(context.WithoutCancel(ctx)) }()

	b := lads.NewBrowser(client)
	node := ins.LADSNodeID
	name := ""
	if node == "" {
		devices, err := b.Devices(ctx)
		if err != nil {
			return ParameterReport{Message: err.Error(), Parameters: []lads.Parameter{}}
		}
		node, name = devices[0].NodeID, devices[0].Name
	}
	params, err := b.Parameters(ctx, node)
	if err != nil {
		return ParameterReport{Message: err.Error(), Parameters: []lads.Parameter{}}
	}
	msg := fmt.Sprintf("Found %d measurable parameter(s).", len(params))
	if len(params) == 0 {
		msg = "Connected, but the instrument reports no measurable parameters yet. Run one measurement and detect again."
	}
	return ParameterReport{OK: true, Message: msg, DeviceName: name, NodeID: node, Parameters: params}
}
