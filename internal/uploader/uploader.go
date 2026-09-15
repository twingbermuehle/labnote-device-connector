// Package uploader drains the outbox in order and posts each result to the
// LabNote ingest API.
package uploader

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/labnote/labnote-device-connector/internal/labnote"
	"github.com/labnote/labnote-device-connector/internal/outbox"
	"github.com/labnote/labnote-device-connector/internal/state"
)

// Worker drains the outbox.
type Worker struct {
	box    *outbox.Outbox
	client func() *labnote.Client
	st     *state.Store
	log    *slog.Logger
	tick   time.Duration
}

// New returns a worker. client is a function so a reconfigured LabNote URL or
// API key takes effect without a restart.
func New(box *outbox.Outbox, client func() *labnote.Client, st *state.Store, log *slog.Logger) *Worker {
	return &Worker{box: box, client: client, st: st, log: log, tick: 3 * time.Second}
}

// Run drains until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) {
	prune := time.NewTicker(time.Hour)
	defer prune.Stop()
	t := time.NewTicker(w.tick)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-prune.C:
			if dropped, err := w.box.Prune(ctx); err == nil && dropped > 0 {
				w.log.Warn("gave up on undelivered results older than 24h", "count", dropped)
			}
		case <-t.C:
			w.drain(ctx)
		}
	}
}

// drain sends due rows strictly in insertion order. It stops at the first
// retryable failure so ordering is preserved.
func (w *Worker) drain(ctx context.Context) {
	if depth, err := w.box.Depth(ctx); err == nil {
		w.st.SetQueueDepth(depth)
	}

	client := w.client()
	if client == nil {
		return
	}

	rows, err := w.box.Next(ctx, 50)
	if err != nil {
		w.log.Error("read outbox failed", "error", err)
		return
	}
	now := time.Now().UTC()
	for _, row := range rows {
		if ctx.Err() != nil {
			return
		}
		// The queue head owns ordering: while it is backing off, nothing
		// behind it may overtake it.
		if row.DueAt.After(now) {
			return
		}
		err := client.SendResult(ctx, row.Result)
		if err == nil {
			_ = w.box.MarkSent(ctx, row.ID)
			w.st.SetUploadHealth(true, "")
			continue
		}

		var apiErr *labnote.Error
		if errors.As(err, &apiErr) {
			switch {
			case apiErr.Duplicate():
				// Server-side idempotency already has it: treat as delivered.
				_ = w.box.MarkSent(ctx, row.ID)
				continue
			case apiErr.Permanent():
				w.log.Error("result rejected permanently", "id", row.ID, "status", apiErr.StatusCode)
				_ = w.box.MarkSent(ctx, row.ID)
				w.st.SetUploadHealth(false, apiErr.Error())
				continue
			case apiErr.Unauthorized():
				w.st.SetUploadHealth(false, "ingest API key rejected — re-enter it in the connector setup screen")
			}
		}
		_ = w.box.MarkFailed(ctx, row.ID, row.Attempts, err.Error())
		w.st.SetUploadHealth(false, err.Error())
		// Stop the batch: results must arrive in order.
		return
	}

	if depth, err := w.box.Depth(ctx); err == nil {
		w.st.SetQueueDepth(depth)
	}
}
