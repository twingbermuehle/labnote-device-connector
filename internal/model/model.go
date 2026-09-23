// Package model holds the shared value types used across the connector.
// It has no dependencies on other internal packages so every package can
// import it without creating cycles.
package model

import "time"

// SecurityMode values accepted by the connector. SignAndEncrypt is the default
// and always preferred; Sign is accepted only when the instrument offers
// nothing better and the operator opted in. None / anonymous is always refused.
const (
	SecurityModeSignAndEncrypt = "SignAndEncrypt"
	SecurityModeSign           = "Sign"

	// Security policies the connector can negotiate, weakest to strongest.
	SecurityPolicyBasic256Sha256      = "Basic256Sha256"
	SecurityPolicyAes128Sha256RsaOaep = "Aes128_Sha256_RsaOaep"
	SecurityPolicyAes256Sha256RsaPss  = "Aes256_Sha256_RsaPss"
	// SecurityPolicyAuto lets the connector pick the strongest policy the
	// instrument offers. This is the default for new instruments.
	SecurityPolicyAuto = "auto"
)

// SecurityPolicyRank scores the accepted policies; higher is stronger.
var SecurityPolicyRank = map[string]int{
	SecurityPolicyBasic256Sha256:      1,
	SecurityPolicyAes128Sha256RsaOaep: 2,
	SecurityPolicyAes256Sha256RsaPss:  3,
}

// AcceptedSecurityPolicy reports whether the connector can use a policy name.
func AcceptedSecurityPolicy(p string) bool {
	if p == SecurityPolicyAuto {
		return true
	}
	_, ok := SecurityPolicyRank[p]
	return ok
}

// Connection states reported per device.
const (
	StatusConnected    = "connected"
	StatusDisconnected = "disconnected"
	StatusError        = "error"
	StatusUnknown      = "unknown"
)

// Connector states reported in the heartbeat.
const (
	ConnectorOnline   = "online"
	ConnectorDegraded = "degraded"
	ConnectorOffline  = "offline"
)

// Instrument kinds. KindOPCUA is an OPC UA / LADS instrument the connector
// subscribes to. KindPush is an instrument without OPC UA (for example a
// Sartorius Cubis II balance) that sends every finished measurement to the
// connector over HTTP(S).
const (
	KindOPCUA = "opcua"
	KindPush  = "push"
)

// OPC UA reading modes, see Instrument.OPCUAMode.
const (
	ModeAuto   = "auto"
	ModeLADS   = "lads"
	ModeValues = "values"
)

// Instrument is one configured instrument.
type Instrument struct {
	ID   string `json:"id" yaml:"id"`
	Kind string `json:"kind" yaml:"kind"`
	// IngestToken authenticates a push instrument's reports. Only used when
	// Kind is KindPush.
	IngestToken      string `json:"ingest_token" yaml:"ingest_token"`
	Name             string `json:"name" yaml:"name"`
	ExternalDeviceID string `json:"external_device_id" yaml:"external_device_id"`
	EndpointURL      string `json:"opcua_endpoint_url" yaml:"opcua_endpoint_url"`
	// Username is the OPC UA user account the instrument expects. When set,
	// the connector logs in with username/password (the password lives in the
	// OS credential store, never in this file). When empty it logs in with its
	// client certificate.
	Username       string `json:"opcua_username" yaml:"opcua_username"`
	SecurityPolicy string `json:"security_policy" yaml:"security_policy"`
	SecurityMode   string `json:"security_mode" yaml:"security_mode"`
	Vendor         string `json:"vendor" yaml:"vendor"`
	Model          string `json:"model" yaml:"model"`
	DeviceType     string `json:"device_type" yaml:"device_type"`
	LADSNodeID     string `json:"lads_node_id" yaml:"lads_node_id"`
	// LADSNamespaceURI is the namespace the saved device node belongs to.
	// Namespace indices are not stable across instrument restarts, so the node
	// id is re-resolved against this URI on every connect.
	LADSNamespaceURI string `json:"lads_namespace_uri,omitempty" yaml:"lads_namespace_uri,omitempty"`
	// OPCUAMode decides how the instrument is read:
	//
	//	auto   - try the LADS model first, fall back to plain variables
	//	lads   - LADS only (fail loudly when the model is missing)
	//	values - read plain OPC UA variables (balances and other simple
	//	         servers that publish no LADS result set)
	//
	// Empty means ModeAuto, which is what older configurations expect.
	OPCUAMode string `json:"opcua_mode,omitempty" yaml:"opcua_mode,omitempty"`
	// TriggerPath is the browse-name chain, relative to the device node, of the
	// variable whose change means "a new measurement was taken" in values mode
	// (for a Sartorius balance: RegisteredWeight). Empty lets the mapping
	// profile decide.
	TriggerPath string `json:"trigger_path,omitempty" yaml:"trigger_path,omitempty"`
	// AllowSignOnly lets the connector fall back to a signed-but-unencrypted
	// session when the instrument offers nothing stronger. Off by default.
	AllowSignOnly bool `json:"allow_sign_only" yaml:"allow_sign_only"`
	// MaxPoints caps the number of curve points sent per result; larger curves
	// are evenly down-sampled. 0 uses DefaultMaxPoints.
	MaxPoints int    `json:"max_points,omitempty" yaml:"max_points,omitempty"`
	Profile   string `json:"profile" yaml:"profile"`
	// Parameters are the measurable quantities detected on the instrument.
	// Only enabled ones are sent to LabNote; an empty list means "send what
	// the mapping profile finds", which is the behaviour of older configs.
	Parameters   []Parameter `json:"parameters,omitempty" yaml:"parameters,omitempty"`
	DefaultUnitX string      `json:"default_unit_x" yaml:"default_unit_x"`
	DefaultUnitY string      `json:"default_unit_y" yaml:"default_unit_y"`

	// ServerCertSHA256 is the pinned server certificate fingerprint (TOFU).
	// Empty means "not yet pinned" and the first connect requires an explicit
	// confirmation in the local UI.
	ServerCertSHA256 string `json:"server_cert_sha256" yaml:"server_cert_sha256"`
}

// Parameter is one measurable quantity of an instrument, as detected during
// setup. Path is relative to the instrument's result object.
type Parameter struct {
	Name    string `json:"name" yaml:"name"`
	Path    string `json:"path" yaml:"path"`
	Unit    string `json:"unit,omitempty" yaml:"unit,omitempty"`
	Kind    string `json:"kind" yaml:"kind"`
	Enabled bool   `json:"enabled" yaml:"enabled"`
}

// EnabledParameters splits the instrument's enabled parameters into curve paths
// and single-value paths, in the order the operator sees them.
func (i Instrument) EnabledParameters() (series []string, values []string) {
	for _, p := range i.Parameters {
		if !p.Enabled || p.Path == "" {
			continue
		}
		if p.Kind == "series" {
			series = append(series, p.Path)
			continue
		}
		values = append(values, p.Path)
	}
	return series, values
}

// DeviceState is the live runtime state of one instrument.
type DeviceState struct {
	InstrumentID     string     `json:"instrument_id"`
	ExternalDeviceID string     `json:"external_device_id"`
	Name             string     `json:"name"`
	ConnectionStatus string     `json:"connection_status"`
	LastResultAt     *time.Time `json:"last_result_at,omitempty"`
	LastError        string     `json:"last_error,omitempty"`
	PendingTrust     bool       `json:"pending_trust"`
	ServerCertSHA256 string     `json:"server_cert_sha256,omitempty"`
	// ServerCertNotAfter is the expiry of the pinned instrument certificate,
	// so a planned renewal can be told apart from an unexpected change.
	ServerCertNotAfter *time.Time `json:"server_cert_not_after,omitempty"`
	// NegotiatedPolicy / NegotiatedMode record what the session actually used.
	NegotiatedPolicy string `json:"negotiated_policy,omitempty"`
	NegotiatedMode   string `json:"negotiated_mode,omitempty"`
	// Warning is a non-fatal note, e.g. the instrument refused to watch some
	// items because it hit its own monitored-item limit.
	Warning string `json:"warning,omitempty"`
}

// DefaultMaxPoints caps a single uploaded curve. Larger curves are evenly
// down-sampled so one huge spectrum cannot stall the upload queue.
const DefaultMaxPoints = 20000

// Point is one x/y sample of a measurement series.
type Point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Result is a finished measurement ready for the LabNote ingest API.
type Result struct {
	ExternalDeviceID string         `json:"external_device_id"`
	ExternalResultID string         `json:"external_result_id"`
	Method           string         `json:"method,omitempty"`
	MeasuredAt       time.Time      `json:"measured_at"`
	SampleCode       string         `json:"sample_code,omitempty"`
	Operator         string         `json:"operator,omitempty"`
	UnitX            string         `json:"unit_x,omitempty"`
	UnitY            string         `json:"unit_y,omitempty"`
	Points           []Point        `json:"points,omitempty"`
	Summary          map[string]any `json:"summary,omitempty"`
	LADS             map[string]any `json:"lads,omitempty"`
}

// HeartbeatDevice is the per-device payload of the heartbeat.
type HeartbeatDevice struct {
	ExternalDeviceID string `json:"external_device_id"`
	Name             string `json:"name,omitempty"`
	Vendor           string `json:"vendor,omitempty"`
	Model            string `json:"model,omitempty"`
	DeviceType       string `json:"device_type,omitempty"`
	EndpointURL      string `json:"opcua_endpoint_url,omitempty"`
	LADSNodeID       string `json:"lads_node_id,omitempty"`
	SecurityMode     string `json:"security_mode,omitempty"`
	ConnectionStatus string `json:"connection_status"`
	DefaultUnitX     string `json:"default_unit_x,omitempty"`
	DefaultUnitY     string `json:"default_unit_y,omitempty"`
}

// Heartbeat is the body posted to POST /v1/connectors.
type Heartbeat struct {
	Name       string            `json:"name"`
	Location   string            `json:"location"`
	Version    string            `json:"version"`
	Status     string            `json:"status"`
	QueueDepth int               `json:"queue_depth"`
	LastError  *string           `json:"last_error"`
	Devices    []HeartbeatDevice `json:"devices"`
}
