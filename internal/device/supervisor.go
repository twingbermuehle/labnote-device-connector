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
	monitored, refused := 0, 0
	// Instruments enforce their own monitored-item budget; watching a whole
	// result archive would exhaust it, so only recent results are watched and
	// the periodic rescan covers the rest.
	const watchPerResultSet = 50
	for _, rs := range resultSets {
		for _, node := range browser.ChangeWatchNodes(ctx, rs.Node, watchPerResultSet) {
			handle++
			req := opcua.NewMonitoredItemCreateRequestWithDefaults(node, ua.AttributeIDValue, handle)
			if _, err := sub.Monitor(ctx, ua.TimestampsToReturnSource, req); err != nil {
				refused++
				s.log.Warn("monitor node failed", "error", err)
				continue
			}
			monitored++
		}
	}
	if refused > 0 {
		s.st.SetWarning(s.ins, fmt.Sprintf(
			"%d of %d instrument signals could not be watched live; results are still collected by regular polling.",
			refused, refused+monitored))
	} else {
		s.st.SetWarning(s.ins, "")
	}
	if monitored == 0 {
		s.log.Warn("no live signals available, relying on polling")
	}
	s.log.Info("subscribed", "monitored_items", monitored, "refused", refused)

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
			// Data changes and events both mean "something happened on the
			// instrument". Some devices only report completion as an event, so
			// both kinds trigger the same rescan.
			switch n.Value.(type) {
			case *ua.DataChangeNotification, *ua.EventNotificationList:
				rescan()
			}
		}
	}
}

// scan walks a ResultSet and forwards every finished result. A result counts as
// finished when its state says so, or - for servers without a result state
// machine - when it carries a Stopped timestamp. A state that only means "not
// running" (Ready, Idle) counts only together with a stop timestamp, so an
// empty result slot is never uploaded as a measurement.
func (s *Supervisor) scan(ctx context.Context, b *lads.Browser, rs lads.ResultSetRef) {
	results, err := b.Results(ctx, rs.Node)
	if err != nil {
		return
	}
	for _, res := range results {
		_, stopped := b.StoppedTime(ctx, res)
		text, number, ok := b.ResultState(ctx, res)
		switch {
		case ok && mapping.IsFinished(text, s.profile):
		case ok && mapping.IsFinishedNumber(number, s.profile):
		case ok && stopped && mapping.IsAmbiguous(text, s.profile):
		case !ok && stopped:
			// No readable state machine at all: the stop timestamp is the only
			// completion signal the companion specification guarantees.
		default:
			continue
		}
		s.forward(ctx, b, res, rs.FunctionalUnit)
	}
}

func (s *Supervisor) forward(ctx context.Context, b *lads.Browser, resultNode *ua.NodeID, functionalUnit string) {
	rec, err := mapping.Build(ctx, b, s.ins, s.profile, resultNode, mapping.WithFunctionalUnit(functionalUnit))
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

// resolveDevice returns the device node to watch.
//
// Saved node ids are re-resolved against the namespace they were saved from:
// OPC UA namespace indices are per-session and may change when an instrument
// is restarted or updated, which would otherwise silently point the connector
// at an unrelated node.
func (s *Supervisor) resolveDevice(ctx context.Context, browser *lads.Browser) (string, error) {
	if node := strings.TrimSpace(s.ins.LADSNodeID); node != "" {
		resolved, err := browser.ResolveNodeID(ctx, node, s.ins.LADSNamespaceURI)
		if err != nil {
			return "", err
		}
		if resolved != node {
			s.log.Info("device node re-resolved after namespace change", "from", node, "to", resolved)
		}
		return resolved, nil
	}
	devices, err := browser.Devices(ctx)
	if err != nil {
		return "", fmt.Errorf("discover LADS devices: %w", err)
	}
	if len(devices) == 0 {
		return "", errors.New("instrument exposes no LADS device")
	}
	if len(devices) > 1 {
		// Picking one silently would send another device's measurements under
		// this instrument's name, so say which one is being used.
		names := make([]string, 0, len(devices))
		for _, d := range devices {
			names = append(names, d.Name)
		}
		s.st.SetWarning(s.ins, fmt.Sprintf(
			"This instrument reports %d devices (%s). Measurements of %q are collected. Choose the right one in the setup screen if this is not it.",
			len(devices), strings.Join(names, ", "), devices[0].Name))
	}
	s.log.Info("device auto-selected", "node_id", devices[0].NodeID, "name", devices[0].Name, "device_count", len(devices))
	return devices[0].NodeID, nil
}

// dial selects the strongest secure endpoint the instrument offers, verifies
// the pinned server certificate and opens the session.
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
	ep := selectSecureEndpoint(endpoints, s.ins.SecurityPolicy, want, s.ins.AllowSignOnly)
	if ep == nil {
		policy := s.ins.SecurityPolicy
		if policy == "" || policy == model.SecurityPolicyAuto {
			policy = "encrypted"
		}
		hint := ""
		if !s.ins.AllowSignOnly && hasSignOnly(endpoints, want) {
			hint = " The instrument only offers a signed (unencrypted) connection; allow that for this instrument in the setup screen if your policy permits it."
		}
		return nil, fmt.Errorf(
			"instrument offers no %s endpoint that accepts %s — insecure connections are refused.%s",
			policy, login, hint)
	}
	s.st.SetSecurity(s.ins, policyName(ep.SecurityPolicyURI), modeName(ep.SecurityMode), certs.NotAfterDER(ep.ServerCertificate))

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
		// The instrument expects a user account. The channel keeps the
		// negotiated policy and mode; only the login differs.
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

// selectSecureEndpoint picks the strongest endpoint the instrument offers that
// accepts the wanted login. Encryption always beats signing, and a stronger
// policy beats a weaker one. Endpoints without security are never used, and
// signed-but-unencrypted ones only when the operator allowed them explicitly.
//
// A fixed policy ("Basic256Sha256") restricts the choice to that policy; the
// default "auto" lets the instrument's best offer win, which is what makes
// devices that do not implement Basic256Sha256 work at all.
func selectSecureEndpoint(endpoints []*ua.EndpointDescription, policy string, want ua.UserTokenType, allowSignOnly bool) *ua.EndpointDescription {
	var best *ua.EndpointDescription
	bestScore := 0
	for _, ep := range endpoints {
		if !acceptsToken(ep, want) {
			continue
		}
		name := policyName(ep.SecurityPolicyURI)
		rank, ok := model.SecurityPolicyRank[name]
		if !ok {
			continue // None, deprecated Basic128Rsa15/Basic256: refused
		}
		if policy != "" && policy != model.SecurityPolicyAuto && name != policy {
			continue
		}
		modeScore := 0
		switch ep.SecurityMode {
		case ua.MessageSecurityModeSignAndEncrypt:
			modeScore = 100
		case ua.MessageSecurityModeSign:
			if !allowSignOnly {
				continue
			}
			modeScore = 10
		default:
			continue
		}
		if score := modeScore + rank; score > bestScore {
			best, bestScore = ep, score
		}
	}
	return best
}

// hasSignOnly reports whether the only secure option is sign-without-encrypt,
// so the error message can suggest the opt-in instead of a dead end.
func hasSignOnly(endpoints []*ua.EndpointDescription, want ua.UserTokenType) bool {
	for _, ep := range endpoints {
		if ep.SecurityMode == ua.MessageSecurityModeSign && acceptsToken(ep, want) {
			if _, ok := model.SecurityPolicyRank[policyName(ep.SecurityPolicyURI)]; ok {
				return true
			}
		}
	}
	return false
}

func acceptsToken(ep *ua.EndpointDescription, want ua.UserTokenType) bool {
	for _, token := range ep.UserIdentityTokens {
		if token.TokenType == want {
			return true
		}
	}
	return false
}

// policyName reduces a security policy URI to its short name.
func policyName(uri string) string {
	if i := strings.LastIndex(uri, "#"); i >= 0 {
		return uri[i+1:]
	}
	return uri
}

func modeName(m ua.MessageSecurityMode) string {
	switch m {
	case ua.MessageSecurityModeSignAndEncrypt:
		return model.SecurityModeSignAndEncrypt
	case ua.MessageSecurityModeSign:
		return model.SecurityModeSign
	default:
		return "None"
	}
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
	OK         bool   `json:"ok"`
	Message    string `json:"message"`
	DeviceName string `json:"device_name,omitempty"`
	NodeID     string `json:"lads_node_id,omitempty"`
	// NamespaceURI is saved with the instrument so the device node survives a
	// namespace renumbering on the instrument.
	NamespaceURI string           `json:"lads_namespace_uri,omitempty"`
	Parameters   []lads.Parameter `json:"parameters"`
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
	node := strings.TrimSpace(ins.LADSNodeID)
	name, namespace := "", ins.LADSNamespaceURI
	if node == "" {
		devices, err := b.Devices(ctx)
		if err != nil {
			return ParameterReport{Message: err.Error(), Parameters: []lads.Parameter{}}
		}
		if len(devices) == 0 {
			return ParameterReport{Message: "The instrument exposes no LADS device.", Parameters: []lads.Parameter{}}
		}
		node, name, namespace = devices[0].NodeID, devices[0].Name, devices[0].NamespaceURI
	} else if resolved, err := b.ResolveNodeID(ctx, node, namespace); err == nil {
		node = resolved
	}
	if namespace == "" {
		namespace = b.NamespaceURI(ctx, node)
	}
	params, err := b.Parameters(ctx, node)
	if err != nil {
		return ParameterReport{Message: err.Error(), Parameters: []lads.Parameter{}}
	}
	msg := fmt.Sprintf("Found %d measurable parameter(s).", len(params))
	if len(params) == 0 {
		msg = "Connected, but the instrument reports no measurable parameters yet. Run one measurement and detect again."
	}
	return ParameterReport{OK: true, Message: msg, DeviceName: name, NodeID: node, NamespaceURI: namespace, Parameters: params}
}
