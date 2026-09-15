package outbox

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/labnote/labnote-device-connector/internal/model"
)

func newTestOutbox(t *testing.T) *Outbox {
	t.Helper()
	box, err := Open(filepath.Join(t.TempDir(), "outbox.db"))
	if err != nil {
		t.Fatalf("open outbox: %v", err)
	}
	t.Cleanup(func() { _ = box.Close() })
	return box
}

func result(id string) model.Result {
	return model.Result{
		ExternalDeviceID: "HPLC-07",
		ExternalResultID: id,
		MeasuredAt:       time.Now().UTC(),
	}
}

// Results must leave the outbox in the order they arrived.
func TestFIFOOrdering(t *testing.T) {
	ctx := context.Background()
	box := newTestOutbox(t)

	for _, id := range []string{"a", "b", "c"} {
		if err := box.Enqueue(ctx, result(id)); err != nil {
			t.Fatalf("enqueue %s: %v", id, err)
		}
	}

	rows, err := box.Next(ctx, 10)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("want 3 rows, got %d", len(rows))
	}
	for i, want := range []string{"a", "b", "c"} {
		if rows[i].Result.ExternalResultID != want {
			t.Fatalf("row %d: want %s, got %s", i, want, rows[i].Result.ExternalResultID)
		}
	}
}

// The same result may never be queued twice, so a replay after a reconnect
// cannot produce a duplicate upload.
func TestEnqueueIsIdempotent(t *testing.T) {
	ctx := context.Background()
	box := newTestOutbox(t)

	for i := 0; i < 3; i++ {
		if err := box.Enqueue(ctx, result("same-id")); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}
	depth, err := box.Depth(ctx)
	if err != nil {
		t.Fatalf("depth: %v", err)
	}
	if depth != 1 {
		t.Fatalf("want depth 1, got %d", depth)
	}
}

func TestMarkSentClearsQueueDepth(t *testing.T) {
	ctx := context.Background()
	box := newTestOutbox(t)
	if err := box.Enqueue(ctx, result("x")); err != nil {
		t.Fatal(err)
	}
	rows, err := box.Next(ctx, 1)
	if err != nil || len(rows) != 1 {
		t.Fatalf("next: %v rows=%d", err, len(rows))
	}
	if err := box.MarkSent(ctx, rows[0].ID); err != nil {
		t.Fatal(err)
	}
	depth, _ := box.Depth(ctx)
	if depth != 0 {
		t.Fatalf("want depth 0, got %d", depth)
	}
}

// A failed attempt must be retried later, not immediately, and must not be
// handed out again before its next attempt time.
func TestMarkFailedSchedulesRetry(t *testing.T) {
	ctx := context.Background()
	box := newTestOutbox(t)
	if err := box.Enqueue(ctx, result("y")); err != nil {
		t.Fatal(err)
	}
	rows, _ := box.Next(ctx, 1)
	if err := box.MarkFailed(ctx, rows[0].ID, rows[0].Attempts, "network unreachable"); err != nil {
		t.Fatal(err)
	}
	again, _ := box.Next(ctx, 1)
	if len(again) != 0 {
		t.Fatalf("row was handed out again before its retry time")
	}
	depth, _ := box.Depth(ctx)
	if depth != 1 {
		t.Fatalf("failed row must stay queued, depth=%d", depth)
	}
}

func TestBackoffIsCapped(t *testing.T) {
	if d := Backoff(50); d != 15*time.Minute {
		t.Fatalf("want 15m cap, got %s", d)
	}
	if d := Backoff(1); d != 2*time.Second {
		t.Fatalf("want 2s for first retry, got %s", d)
	}
}
