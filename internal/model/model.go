// Package model holds the shared value types used across the connector.
// It has no dependencies on other internal packages so every package can
// import it without creating cycles.
package model

import "time"

// SecurityMode values accepted by the connector. Only SignAndEncrypt is
// allowed; None and Sign are rejected at configuration time.
const (
	SecurityModeSignAndEncrypt = "SignAndEncrypt"
	SecurityPolicyBasic256Sha256 = "Basic256Sha256"
)

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

// Instrument is one configured instrument.
type Instrument struct {
	ID               string `json:"id" yaml:"id"`
	Kind             string `json:"kind" yaml:"kind"`
	// IngestToken authenticates a push instrument's reports. Only used when
	// Kind is KindPush.
	IngestToken      string `json:"ingest_token" yaml:"ingest_token"`
	Name             string `json:"name" yaml:"name"`
	ExternalDeviceID string `json:"external_device_id" yaml:"external_device_id"`
	EndpointURL      string `json:"opcua_endpoint_url" yaml:"opcua_endpoint_url"`
	SecurityPolicy   string `json:"security_policy" yaml:"security_policy"`
	SecurityMode     string `json:"security_mode" yaml:"security_mode"`
	Vendor           string `json:"vendor" yaml:"vendor"`
	Model            string `json:"model" yaml:"model"`
	DeviceType       string `json:"device_type" yaml:"device_type"`
	LADSNodeID       string `json:"lads_node_id" yaml:"lads_node_id"`
	Profile          string `json:"profile" yaml:"profile"`
	DefaultUnitX     string `json:"default_unit_x" yaml:"default_unit_x"`
	DefaultUnitY     string `json:"default_unit_y" yaml:"default_unit_y"`

	// ServerCertSHA256 is the pinned server certificate fingerprint (TOFU).
	// Empty means "not yet pinned" and the first connect requires an explicit
	// confirmation in the local UI.
	ServerCertSHA256 string `json:"server_cert_sha256" yaml:"server_cert_sha256"`
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
}

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
