// Package outbox is the local store-and-forward queue. Every finished result
// is written here BEFORE any upload is attempted, so nothing is lost when the
// network or the LabNote instance is unavailable.
package outbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver, no cgo

	"github.com/labnote/labnote-device-connector/internal/model"
)

// Retention of already delivered rows.
const SentRetention = 7 * 24 * time.Hour

// MaxAge is how long an undelivered row keeps being retried.
const MaxAge = 24 * time.Hour

// Outbox is a SQLite-backed FIFO queue.
type Outbox struct {
	db *sql.DB
}

// Row is one queued result.
type Row struct {
	ID         int64
	Result     model.Result
	Attempts   int
	CreatedAt  time.Time
	LastError  string
}

const schema = `
PRAGMA journal_mode=WAL;
PRAGMA synchronous=NORMAL;
CREATE TABLE IF NOT EXISTS outbox (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  external_device_id TEXT NOT NULL,
  external_result_id TEXT NOT NULL,
  payload TEXT NOT NULL,
  attempts INTEGER NOT NULL DEFAULT 0,
  last_error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  next_attempt_at TEXT NOT NULL,
  sent_at TEXT,
  UNIQUE(external_result_id)
);
CREATE INDEX IF NOT EXISTS outbox_pending ON outbox(sent_at, next_attempt_at, id);
`

// Open opens (and migrates) the outbox database at path.
func Open(path string) (*Outbox, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("migrate outbox: %w", err)
	}
	return &Outbox{db: db}, nil
}

// Close releases the database handle.
func (o *Outbox) Close() error { return o.db.Close() }

// Enqueue appends a result. Re-enqueuing the same external_result_id is a
// no-op, which makes the local side idempotent as well as the server side.
func (o *Outbox) Enqueue(ctx context.Context, r model.Result) error {
	payload, err := json.Marshal(r)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = o.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO outbox
		   (external_device_id, external_result_id, payload, created_at, next_attempt_at)
		 VALUES (?, ?, ?, ?, ?)`,
		r.ExternalDeviceID, r.ExternalResultID, string(payload), now, now)
	return err
}

// Next returns up to limit due rows in insertion order (strict ordering).
func (o *Outbox) Next(ctx context.Context, limit int) ([]Row, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	rows, err := o.db.QueryContext(ctx,
		`SELECT id, payload, attempts, created_at, last_error
		   FROM outbox
		  WHERE sent_at IS NULL AND next_attempt_at <= ?
		  ORDER BY id ASC LIMIT ?`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Row
	for rows.Next() {
		var (
			r         Row
			payload   string
			createdAt string
		)
		if err := rows.Scan(&r.ID, &payload, &r.Attempts, &createdAt, &r.LastError); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(payload), &r.Result); err != nil {
			return nil, err
		}
		r.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		out = append(out, r)
	}
	return out, rows.Err()
}

// MarkSent records a successful delivery.
func (o *Outbox) MarkSent(ctx context.Context, id int64) error {
	_, err := o.db.ExecContext(ctx, `UPDATE outbox SET sent_at = ?, last_error = '' WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339Nano), id)
	return err
}

// MarkFailed records a failed attempt and schedules the next one.
func (o *Outbox) MarkFailed(ctx context.Context, id int64, attempts int, reason string) error {
	_, err := o.db.ExecContext(ctx,
		`UPDATE outbox SET attempts = attempts + 1, last_error = ?, next_attempt_at = ? WHERE id = ?`,
		reason, time.Now().Add(Backoff(attempts+1)).UTC().Format(time.RFC3339Nano), id)
	return err
}

// Backoff is the retry delay for attempt n, capped at 15 minutes.
func Backoff(attempt int) time.Duration {
	d := time.Duration(1<<min(attempt, 10)) * time.Second
	if d > 15*time.Minute {
		d = 15 * time.Minute
	}
	return d
}

// Depth returns the number of undelivered rows (reported as queue_depth).
func (o *Outbox) Depth(ctx context.Context) (int, error) {
	var n int
	err := o.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM outbox WHERE sent_at IS NULL`).Scan(&n)
	return n, err
}

// Prune deletes delivered rows older than SentRetention and gives up on
// undelivered rows older than MaxAge (they stay visible in the log).
func (o *Outbox) Prune(ctx context.Context) (dropped int64, err error) {
	cutoffSent := time.Now().Add(-SentRetention).UTC().Format(time.RFC3339Nano)
	if _, err = o.db.ExecContext(ctx, `DELETE FROM outbox WHERE sent_at IS NOT NULL AND sent_at < ?`, cutoffSent); err != nil {
		return 0, err
	}
	cutoffOld := time.Now().Add(-MaxAge).UTC().Format(time.RFC3339Nano)
	res, err := o.db.ExecContext(ctx, `DELETE FROM outbox WHERE sent_at IS NULL AND created_at < ?`, cutoffOld)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
