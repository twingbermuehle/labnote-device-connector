// Package heartbeat reports the connector and its devices to LabNote every
// 60 seconds (and once on start). The server upserts the connector and its
// devices; external_device_id is the stable key.
package heartbeat

import (
	"context"
	"log/slog"
	"time"

	"github.com/labnote/labnote-device-connector/internal/config"
	"github.com/labnote/labnote-device-connector/internal/labnote"
	"github.com/labnote/labnote-device-connector/internal/model"
	"github.com/labnote/labnote-device-connector/internal/state"
)

// Interval between heartbeats.
const Interval = 60 * time.Second

// Worker posts heartbeats.
type Worker struct {
	cfg     *config.Store
	st      *state.Store
	client  func() *labnote.Client
	version string
	log     *slog.Logger
}

// New returns a heartbeat worker.
func New(cfg *config.Store, st *state.Store, client func() *labnote.Client, version string, log *slog.Logger) *Worker {
	return &Worker{cfg: cfg, st: st, client: client, version: version, log: log}
}

// Run sends immediately and then every Interval until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) {
	w.send(ctx)
	t := time.NewTicker(Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			// Best-effort offline notice.
			offCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			w.sendWith(offCtx, model.ConnectorOffline)
			cancel()
			return
		case <-t.C:
			w.send(ctx)
		}
	}
}

func (w *Worker) send(ctx context.Context) {
	w.sendWith(ctx, "")
}

func (w *Worker) sendWith(ctx context.Context, forceStatus string) {
	client := w.client()
	if client == nil {
		return
	}
	hb := w.Payload()
	if forceStatus != "" {
		hb.Status = forceStatus
	}
	if err := client.SendHeartbeat(ctx, hb); err != nil {
		w.log.Warn("heartbeat failed", "error", err)
		return
	}
	w.log.Debug("heartbeat sent", "status", hb.Status, "queue_depth", hb.QueueDepth)
}

// Payload builds the current heartbeat body (also shown in the local UI).
func (w *Worker) Payload() model.Heartbeat {
	cfg := w.cfg.Get()
	snap := w.st.Snapshot()

	statusByDevice := map[string]string{}
	for _, d := range snap.Devices {
		statusByDevice[d.ExternalDeviceID] = d.ConnectionStatus
	}

	devices := make([]model.HeartbeatDevice, 0, len(cfg.Instruments))
	for _, ins := range cfg.Instruments {
		status := statusByDevice[ins.ExternalDeviceID]
		if status == "" {
			status = model.StatusUnknown
		}
		devices = append(devices, model.HeartbeatDevice{
			ExternalDeviceID: ins.ExternalDeviceID,
			Name:             ins.Name,
			Vendor:           ins.Vendor,
			Model:            ins.Model,
			DeviceType:       ins.DeviceType,
			EndpointURL:      ins.EndpointURL,
			LADSNodeID:       ins.LADSNodeID,
			SecurityMode:     ins.SecurityMode,
			ConnectionStatus: status,
			DefaultUnitX:     ins.DefaultUnitX,
			DefaultUnitY:     ins.DefaultUnitY,
		})
	}

	return model.Heartbeat{
		Name:       cfg.Name,
		Location:   cfg.Location,
		Version:    w.version,
		Status:     snap.Status,
		QueueDepth: snap.QueueDepth,
		LastError:  w.st.LastError(),
		Devices:    devices,
	}
}
