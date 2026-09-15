// Package manager owns the instrument supervisors: it starts one per
// configured instrument and restarts the set when the configuration changes.
// It also implements the trust store on top of the config file.
package manager

import (
	"context"
	"log/slog"
	"sync"

	"github.com/labnote/labnote-device-connector/internal/certs"
	"github.com/labnote/labnote-device-connector/internal/config"
	"github.com/labnote/labnote-device-connector/internal/device"
	"github.com/labnote/labnote-device-connector/internal/model"
	"github.com/labnote/labnote-device-connector/internal/profiles"
	"github.com/labnote/labnote-device-connector/internal/state"
)

// Manager supervises all instruments.
type Manager struct {
	cfg   *config.Store
	pki   *certs.Store
	profs *profiles.Set
	sink  device.Sink
	st    *state.Store
	log   *slog.Logger

	mu      sync.Mutex
	ctx     context.Context
	cancels map[string]context.CancelFunc
	wg      sync.WaitGroup
}

// New returns a manager.
func New(cfg *config.Store, pki *certs.Store, profs *profiles.Set, sink device.Sink, st *state.Store, log *slog.Logger) *Manager {
	return &Manager{
		cfg: cfg, pki: pki, profs: profs, sink: sink, st: st, log: log,
		cancels: map[string]context.CancelFunc{},
	}
}

// Start launches supervisors for the current configuration. It returns once
// they are running; Wait blocks until they stop.
func (m *Manager) Start(ctx context.Context) {
	m.mu.Lock()
	m.ctx = ctx
	m.mu.Unlock()
	m.Reconcile()
	go func() {
		<-ctx.Done()
		m.stopAll()
	}()
}

// Wait blocks until every supervisor has exited.
func (m *Manager) Wait() { m.wg.Wait() }

// Reconcile starts supervisors for new instruments and stops removed ones.
// Called after every configuration change from the setup UI.
func (m *Manager) Reconcile() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ctx == nil || m.ctx.Err() != nil {
		return
	}

	cfg := m.cfg.Get()
	wanted := map[string]model.Instrument{}
	for _, ins := range cfg.Instruments {
		wanted[ins.ID] = ins
	}

	for id, cancel := range m.cancels {
		if _, keep := wanted[id]; !keep {
			cancel()
			delete(m.cancels, id)
			m.st.Forget(id)
		}
	}

	for id, ins := range wanted {
		if _, running := m.cancels[id]; running {
			continue
		}
		ctx, cancel := context.WithCancel(m.ctx)
		m.cancels[id] = cancel
		sup := device.New(ins, m.profs.Get(ins.Profile), m.pki, m.sink, m.st, m, m.log)
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			sup.Run(ctx)
		}()
	}
}

// Restart stops and restarts the supervisor for one instrument (used after the
// customer edits it or confirms a certificate).
func (m *Manager) Restart(instrumentID string) {
	m.mu.Lock()
	if cancel, ok := m.cancels[instrumentID]; ok {
		cancel()
		delete(m.cancels, instrumentID)
	}
	m.mu.Unlock()
	m.Reconcile()
}

func (m *Manager) stopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, cancel := range m.cancels {
		cancel()
		delete(m.cancels, id)
	}
}

// Pinned implements device.TrustStore.
func (m *Manager) Pinned(instrumentID string) string {
	for _, ins := range m.cfg.Get().Instruments {
		if ins.ID == instrumentID {
			return ins.ServerCertSHA256
		}
	}
	return ""
}

// Pin implements device.TrustStore: it persists a confirmed fingerprint.
func (m *Manager) Pin(instrumentID, fingerprint string) error {
	return m.cfg.Update(func(c *config.Config) error {
		for i := range c.Instruments {
			if c.Instruments[i].ID == instrumentID {
				c.Instruments[i].ServerCertSHA256 = fingerprint
			}
		}
		return nil
	})
}

var _ device.TrustStore = (*Manager)(nil)
