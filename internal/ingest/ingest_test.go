package ingest

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/labnote/labnote-device-connector/internal/certs"
	"github.com/labnote/labnote-device-connector/internal/config"
	"github.com/labnote/labnote-device-connector/internal/model"
	"github.com/labnote/labnote-device-connector/internal/state"
)

const token = "0123456789abcdef0123456789abcdef"

func TestParseJSONReport(t *testing.T) {
	body := []byte(`{"netWeight":"12.3456 g","sampleId":"S-42","method":"Pipette Check","user":"tw","timestamp":"2026-09-16T10:11:12Z"}`)
	rep, err := Parse(body, "application/json")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Value != 12.3456 || rep.Unit != "g" {
		t.Fatalf("value/unit = %v %q", rep.Value, rep.Unit)
	}
	if rep.Sample != "S-42" || rep.Method != "Pipette Check" || rep.Operator != "tw" {
		t.Fatalf("fields = %+v", rep)
	}
	if !rep.MeasuredAt.Equal(time.Date(2026, 9, 16, 10, 11, 12, 0, time.UTC)) {
		t.Fatalf("measuredAt = %v", rep.MeasuredAt)
	}
}

func TestParsePrintedReport(t *testing.T) {
	body := []byte("Sartorius\nModel: MCA225S\nDate: 16.09.2026\nTime: 10:11:12\nSample ID: PROBE-7\nN +   0,123456 g\n")
	rep, err := Parse(body, "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Value != 0.123456 || rep.Unit != "g" {
		t.Fatalf("value/unit = %v %q", rep.Value, rep.Unit)
	}
	if rep.Sample != "PROBE-7" {
		t.Fatalf("sample = %q", rep.Sample)
	}
	if rep.MeasuredAt.IsZero() {
		t.Fatal("no timestamp parsed")
	}
}

func TestParseRejectsReportWithoutValue(t *testing.T) {
	if _, err := Parse([]byte("Sample ID: X\n"), "text/plain"); err == nil {
		t.Fatal("expected an error for a report with no measurement value")
	}
}

func TestResultIsIdempotentForTheSameReport(t *testing.T) {
	ins := model.Instrument{ExternalDeviceID: "BAL-1", Kind: model.KindPush}
	body := []byte(`{"weight":1.5,"unit":"g","timestamp":"2026-09-16T10:00:00Z"}`)
	rep, err := Parse(body, "application/json")
	if err != nil {
		t.Fatal(err)
	}
	a := rep.Result(ins, body, time.Now())
	b := rep.Result(ins, body, time.Now().Add(time.Hour))
	if a.ExternalResultID != b.ExternalResultID {
		t.Fatalf("ids differ: %q vs %q", a.ExternalResultID, b.ExternalResultID)
	}
	if len(a.Points) != 1 || a.Points[0].Y != 1.5 || a.UnitY != "g" {
		t.Fatalf("result = %+v", a)
	}
}

type memSink struct {
	mu   sync.Mutex
	rows []model.Result
}

func (m *memSink) Enqueue(_ context.Context, r model.Result) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows = append(m.rows, r)
	return nil
}

func newServer(t *testing.T) (*Server, *memSink) {
	t.Helper()
	dir := t.TempDir()
	cfg, err := config.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Update(func(c *config.Config) error {
		c.Instruments = []model.Instrument{{
			ID: "1", Kind: model.KindPush, Name: "Balance", ExternalDeviceID: "BAL-1",
			IngestToken: token, DefaultUnitY: "g",
		}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	pki, err := certs.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	sink := &memSink{}
	return New(cfg, pki, sink, state.New(), slog.New(slog.DiscardHandler)), sink
}

func TestHTTPPushIsQueued(t *testing.T) {
	srv, sink := newServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/ingest/"+token,
		strings.NewReader(`{"weight":"2.5 g","sampleId":"S-9"}`))
	req.Header.Set("Content-Type", "application/json")
	srv.handle(rec, req)

	if rec.Code != http.StatusAccepted {
		body, _ := io.ReadAll(rec.Body)
		t.Fatalf("status = %d: %s", rec.Code, body)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(sink.rows) != 1 {
		t.Fatalf("queued %d rows", len(sink.rows))
	}
	got := sink.rows[0]
	if got.ExternalDeviceID != "BAL-1" || got.SampleCode != "S-9" || got.UnitY != "g" {
		t.Fatalf("result = %+v", got)
	}
}

func TestPushWithoutTokenIsRefused(t *testing.T) {
	srv, sink := newServer(t)
	rec := httptest.NewRecorder()
	srv.handle(rec, httptest.NewRequest(http.MethodPost, "/ingest/wrong-token", strings.NewReader(`{"weight":1}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if len(sink.rows) != 0 {
		t.Fatal("an unauthenticated report was queued")
	}
}

func TestTokenAcceptedInHeaderAndBasicAuth(t *testing.T) {
	srv, _ := newServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/whatever", strings.NewReader("N + 1,0 g"))
	req.Header.Set("X-LabNote-Token", token)
	srv.handle(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("header token: status = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/whatever", strings.NewReader("N + 2,0 g"))
	req.SetBasicAuth("labnote", token)
	srv.handle(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("basic auth token: status = %d", rec.Code)
	}
}
