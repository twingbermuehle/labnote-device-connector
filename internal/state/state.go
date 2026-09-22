// Package state holds the live runtime state shown in the local UI and
// reported in the heartbeat.
package state

import (
	"sync"
	"time"

	"github.com/labnote/labnote-device-connector/internal/model"
)

// Store is the in-memory runtime state.
type Store struct {
	mu         sync.RWMutex
	devices    map[string]*model.DeviceState
	lastError  string
	lastErrAt  time.Time
	queueDepth int
	uploadOK   bool
	startedAt  time.Time
}

// New returns an empty store.
func New() *Store {
	return &Store{devices: map[string]*model.DeviceState{}, startedAt: time.Now(), uploadOK: true}
}

// SetDeviceStatus records a device's connection status and optional error.
func (s *Store) SetDeviceStatus(ins model.Instrument, status, errMsg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.device(ins)
	d.ConnectionStatus = status
	d.LastError = errMsg
	if errMsg != "" {
		s.lastError = ins.ExternalDeviceID + ": " + errMsg
		s.lastErrAt = time.Now()
	}
}

// SetPendingTrust flags that an instrument certificate awaits confirmation.
func (s *Store) SetPendingTrust(ins model.Instrument, fingerprint string, pending bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.device(ins)
	d.PendingTrust = pending
	d.ServerCertSHA256 = fingerprint
}

// SetSecurity records what the session actually negotiated and when the
// pinned instrument certificate expires, so the setup screen can show whether
// the connection is encrypted or only signed.
func (s *Store) SetSecurity(ins model.Instrument, policy, mode string, notAfter *time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.device(ins)
	d.NegotiatedPolicy = policy
	d.NegotiatedMode = mode
	d.ServerCertNotAfter = notAfter
}

// SetWarning records a non-fatal note about an otherwise healthy device.
func (s *Store) SetWarning(ins model.Instrument, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.device(ins).Warning = msg
}

// RecordResult stamps the time of the last result received from a device.
func (s *Store) RecordResult(ins model.Instrument) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	d := s.device(ins)
	d.LastResultAt = &now
}

// Forget drops a removed instrument.
func (s *Store) Forget(instrumentID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, d := range s.devices {
		if d.InstrumentID == instrumentID {
			delete(s.devices, k)
		}
	}
}

// SetQueueDepth stores the current outbox size.
func (s *Store) SetQueueDepth(n int) {
	s.mu.Lock()
	s.queueDepth = n
	s.mu.Unlock()
}

// SetUploadHealth records whether uploads are currently succeeding.
func (s *Store) SetUploadHealth(ok bool, errMsg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.uploadOK = ok
	if !ok && errMsg != "" {
		s.lastError = "upload: " + errMsg
		s.lastErrAt = time.Now()
	}
}

// Snapshot is the state rendered by the local UI.
type Snapshot struct {
	Status     string              `json:"status"`
	QueueDepth int                 `json:"queue_depth"`
	LastError  string              `json:"last_error,omitempty"`
	UptimeSecs int                 `json:"uptime_seconds"`
	Devices    []model.DeviceState `json:"devices"`
}

// Snapshot returns a copy of the current state.
func (s *Store) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	snap := Snapshot{
		QueueDepth: s.queueDepth,
		LastError:  s.lastError,
		UptimeSecs: int(time.Since(s.startedAt).Seconds()),
	}
	connected, total := 0, 0
	for _, d := range s.devices {
		total++
		if d.ConnectionStatus == model.StatusConnected {
			connected++
		}
		snap.Devices = append(snap.Devices, *d)
	}
	switch {
	case !s.uploadOK:
		snap.Status = model.ConnectorDegraded
	case total == 0:
		snap.Status = model.ConnectorOnline
	case connected == 0:
		snap.Status = model.ConnectorDegraded
	case connected < total:
		snap.Status = model.ConnectorDegraded
	default:
		snap.Status = model.ConnectorOnline
	}
	return snap
}

// LastError returns the most recent error message, or nil when clean.
func (s *Store) LastError() *string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.lastError == "" {
		return nil
	}
	msg := s.lastError
	return &msg
}

// ClearError resets the last error (used after a successful recovery).
func (s *Store) ClearError() {
	s.mu.Lock()
	s.lastError = ""
	s.mu.Unlock()
}

func (s *Store) device(ins model.Instrument) *model.DeviceState {
	d, ok := s.devices[ins.ExternalDeviceID]
	if !ok {
		d = &model.DeviceState{
			InstrumentID:     ins.ID,
			ExternalDeviceID: ins.ExternalDeviceID,
			Name:             ins.Name,
			ConnectionStatus: model.StatusUnknown,
		}
		s.devices[ins.ExternalDeviceID] = d
	}
	d.Name = ins.Name
	d.InstrumentID = ins.ID
	return d
}
