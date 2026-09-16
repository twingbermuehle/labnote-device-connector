package ingest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/labnote/labnote-device-connector/internal/certs"
	"github.com/labnote/labnote-device-connector/internal/config"
	"github.com/labnote/labnote-device-connector/internal/model"
	"github.com/labnote/labnote-device-connector/internal/state"
)

// Sink receives the parsed results (the outbox in production).
type Sink interface {
	Enqueue(ctx context.Context, r model.Result) error
}

// MaxBody caps a single pushed report.
const MaxBody = 1 << 20

// Server listens on the lab network for reports pushed by instruments.
// Every request must carry the per-instrument token, either in the URL path
// (/ingest/<token>), as ?token=, in the X-LabNote-Token header, or as the
// password of HTTP basic auth.
type Server struct {
	cfg  *config.Store
	pki  *certs.Store
	sink Sink
	st   *state.Store
	log  *slog.Logger
}

// New returns a push ingest server.
func New(cfg *config.Store, pki *certs.Store, sink Sink, st *state.Store, log *slog.Logger) *Server {
	return &Server{cfg: cfg, pki: pki, sink: sink, st: st, log: log.With("component", "push-ingest")}
}

// ListenAndServe blocks until ctx is cancelled. It is a no-op while no
// push instrument is configured, and re-checks that every 15 seconds.
func (s *Server) ListenAndServe(ctx context.Context) error {
	for ctx.Err() == nil {
		cfg := s.cfg.Get()
		if !hasPush(cfg) {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(15 * time.Second):
				continue
			}
		}
		if err := s.serve(ctx, cfg); err != nil && ctx.Err() == nil {
			s.log.Error("push ingest listener stopped", "error", err)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(30 * time.Second):
			}
		}
	}
	return nil
}

func (s *Server) serve(ctx context.Context, cfg config.Config) error {
	addr := fmt.Sprintf(":%d", cfg.IngestPort)
	srv := &http.Server{
		Handler:           http.HandlerFunc(s.handle),
		ReadHeaderTimeout: 15 * time.Second,
		ReadTimeout:       time.Minute,
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	scheme := "http"
	if !cfg.IngestInsecureHTTP {
		scheme = "https"
		cert, err := s.pki.EnsureServerCert()
		if err != nil {
			_ = ln.Close()
			return fmt.Errorf("ingest server certificate: %w", err)
		}
		srv.TLSConfig = tlsConfig(cert)
	}
	s.log.Info("push ingest listening", "url", fmt.Sprintf("%s://<this-computer>:%d/ingest/<token>", scheme, cfg.IngestPort))

	if !cfg.IngestInsecureHTTP {
		err = srv.ServeTLS(ln, "", "")
	} else {
		err = srv.Serve(ln)
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		// Lets the instrument's "test connection" button succeed.
		if _, ok := s.instrumentFor(r); ok {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		http.Error(w, "unknown or missing token", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		http.Error(w, "use POST", http.StatusMethodNotAllowed)
		return
	}

	ins, ok := s.instrumentFor(r)
	if !ok {
		s.log.Warn("rejected push with unknown token", "remote", r.RemoteAddr)
		http.Error(w, "unknown or missing token", http.StatusUnauthorized)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBody))
	if err != nil {
		http.Error(w, "could not read the report", http.StatusBadRequest)
		return
	}
	rep, err := Parse(body, r.Header.Get("Content-Type"))
	if err != nil {
		s.log.Warn("unusable report", "device", ins.ExternalDeviceID, "error", err)
		s.st.SetDeviceStatus(ins, model.StatusError, err.Error())
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	result := rep.Result(ins, body, time.Now())
	if err := s.sink.Enqueue(r.Context(), result); err != nil {
		s.log.Error("could not queue the report", "device", ins.ExternalDeviceID, "error", err)
		s.st.SetDeviceStatus(ins, model.StatusError, err.Error())
		http.Error(w, "could not store the report", http.StatusInternalServerError)
		return
	}
	s.st.SetDeviceStatus(ins, model.StatusConnected, "")
	s.st.RecordResult(ins)
	s.log.Info("queued pushed result",
		"device", ins.ExternalDeviceID, "result", result.ExternalResultID,
		"value", rep.Value, "unit", result.UnitY, "sample", result.SampleCode)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_, _ = fmt.Fprintf(w, `{"ok":true,"external_result_id":%q}`+"\n", result.ExternalResultID)
}

// instrumentFor resolves the request's token to a configured push instrument.
func (s *Server) instrumentFor(r *http.Request) (model.Instrument, bool) {
	token := tokenFrom(r)
	if token == "" {
		return model.Instrument{}, false
	}
	for _, ins := range s.cfg.Get().Instruments {
		if ins.Kind == model.KindPush && ins.IngestToken != "" && subtleEqual(ins.IngestToken, token) {
			return ins, true
		}
	}
	return model.Instrument{}, false
}

func tokenFrom(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get("X-LabNote-Token")); v != "" {
		return v
	}
	if v := strings.TrimSpace(r.URL.Query().Get("token")); v != "" {
		return v
	}
	if _, pass, ok := r.BasicAuth(); ok && strings.TrimSpace(pass) != "" {
		return strings.TrimSpace(pass)
	}
	if v := strings.TrimSpace(r.Header.Get("Authorization")); strings.HasPrefix(strings.ToLower(v), "bearer ") {
		return strings.TrimSpace(v[len("bearer "):])
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) > 0 {
		return strings.TrimSpace(parts[len(parts)-1])
	}
	return ""
}

func hasPush(cfg config.Config) bool {
	for _, ins := range cfg.Instruments {
		if ins.Kind == model.KindPush {
			return true
		}
	}
	return false
}
