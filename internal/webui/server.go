// Package webui serves the local setup and monitoring interface. It listens on
// 127.0.0.1:8420 only — it is never exposed to the lab network, and the
// connector opens no other inbound port.
package webui

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/labnote/labnote-device-connector/internal/certs"
	"github.com/labnote/labnote-device-connector/internal/config"
	"github.com/labnote/labnote-device-connector/internal/device"
	"github.com/labnote/labnote-device-connector/internal/heartbeat"
	"github.com/labnote/labnote-device-connector/internal/keychain"
	"github.com/labnote/labnote-device-connector/internal/labnote"
	"github.com/labnote/labnote-device-connector/internal/logging"
	"github.com/labnote/labnote-device-connector/internal/manager"
	"github.com/labnote/labnote-device-connector/internal/model"
	"github.com/labnote/labnote-device-connector/internal/profiles"
	"github.com/labnote/labnote-device-connector/internal/state"
	"github.com/labnote/labnote-device-connector/internal/updater"
)

//go:embed static
var staticFS embed.FS

// Addr is the loopback address of the setup UI.
const Addr = "127.0.0.1:8420"

// listenAddr returns the bind address. It is 127.0.0.1:8420 everywhere except
// inside the container image, where the loopback of the container is not the
// loopback of the host and the published port does the confinement instead.
func listenAddr() string {
	if v := strings.TrimSpace(os.Getenv("LABNOTE_CONNECTOR_UI_ADDR")); v != "" {
		return v
	}
	return Addr
}

// Server is the local UI.
type Server struct {
	cfg     *config.Store
	pki     *certs.Store
	profs   *profiles.Set
	st      *state.Store
	mgr     *manager.Manager
	hb      *heartbeat.Worker
	upd     *updater.Checker
	logs    *logging.Rotator
	client  func() *labnote.Client
	version string
	log     *slog.Logger
}

// Deps groups everything the UI needs.
type Deps struct {
	Config    *config.Store
	PKI       *certs.Store
	Profiles  *profiles.Set
	State     *state.Store
	Manager   *manager.Manager
	Heartbeat *heartbeat.Worker
	Updater   *updater.Checker
	Logs      *logging.Rotator
	Client    func() *labnote.Client
	Version   string
	Log       *slog.Logger
}

// New returns a server.
func New(d Deps) *Server {
	return &Server{
		cfg: d.Config, pki: d.PKI, profs: d.Profiles, st: d.State, mgr: d.Manager,
		hb: d.Heartbeat, upd: d.Updater, logs: d.Logs, client: d.Client,
		version: d.Version, log: d.Log,
	}
}

// ListenAndServe blocks until ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context) error {
	assets, err := fs.Sub(staticFS, "static")
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(assets)))
	mux.HandleFunc("GET /api/state", s.handleState)
	mux.HandleFunc("POST /api/setup", s.handleSetup)
	mux.HandleFunc("POST /api/instruments", s.handleSaveInstrument)
	mux.HandleFunc("DELETE /api/instruments/{id}", s.handleDeleteInstrument)
	mux.HandleFunc("POST /api/instruments/test", s.handleTestInstrument)
	mux.HandleFunc("POST /api/instruments/{id}/trust", s.handleTrust)
	mux.HandleFunc("POST /api/settings", s.handleSettings)
	mux.HandleFunc("GET /api/logs.zip", s.handleLogs)

	srv := &http.Server{
		Handler:           loopbackOnly(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}
	addr := listenAddr()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	s.log.Info("setup interface listening", "url", "http://"+addr)
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// allowNonLoopback is only true for the container image, where requests arrive
// from the Docker bridge rather than the container's own loopback interface.
var allowNonLoopback = os.Getenv("LABNOTE_CONNECTOR_UI_ADDR") != ""

// loopbackOnly rejects anything that did not arrive over the loopback
// interface, as belt-and-braces on top of the loopback bind.
func loopbackOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil || (!net.ParseIP(host).IsLoopback() && !allowNonLoopback) {
			http.Error(w, "the connector setup interface is only reachable from this machine", http.StatusForbidden)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

type stateResponse struct {
	Version       string             `json:"version"`
	SetupComplete bool               `json:"setup_complete"`
	LabNoteURL    string             `json:"labnote_url"`
	APIKeyStored  bool               `json:"api_key_stored"`
	Name          string             `json:"name"`
	Location      string             `json:"location"`
	AutoUpdate    bool               `json:"auto_update"`
	Fingerprint   string             `json:"client_certificate_fingerprint"`
	Instruments   []model.Instrument `json:"instruments"`
	Ingest        ingestInfo         `json:"ingest"`
	Profiles      []profileInfo      `json:"profiles"`
	Runtime       state.Snapshot     `json:"runtime"`
	Heartbeat     model.Heartbeat    `json:"heartbeat"`
	Update        updater.Status     `json:"update"`
}

// ingestInfo tells the UI how instruments that push their reports should be
// configured on the instrument side.
type ingestInfo struct {
	Port        int    `json:"port"`
	Scheme      string `json:"scheme"`
	Host        string `json:"host"`
	Fingerprint string `json:"certificate_fingerprint,omitempty"`
}

type profileInfo struct {
	ID          string `json:"id"`
	Description string `json:"description"`
}

func (s *Server) handleState(w http.ResponseWriter, _ *http.Request) {
	cfg := s.cfg.Get()
	fp, _ := s.pki.Fingerprint()

	profs := make([]profileInfo, 0)
	for _, p := range s.profs.List() {
		profs = append(profs, profileInfo{ID: p.ID, Description: p.Description})
	}

	writeJSON(w, http.StatusOK, stateResponse{
		Version:       s.version,
		SetupComplete: cfg.SetupComplete,
		LabNoteURL:    cfg.LabNoteURL,
		APIKeyStored:  keychain.Has(),
		Name:          cfg.Name,
		Location:      cfg.Location,
		AutoUpdate:    cfg.AutoUpdate,
		Fingerprint:   fp,
		Instruments:   cfg.Instruments,
		Ingest:        s.ingestInfo(cfg),
		Profiles:      profs,
		Runtime:       s.st.Snapshot(),
		Heartbeat:     s.hb.Payload(),
		Update:        s.upd.Status(),
	})
}

func (s *Server) ingestInfo(cfg config.Config) ingestInfo {
	scheme := "https"
	if cfg.IngestInsecureHTTP {
		scheme = "http"
	}
	host, _ := os.Hostname()
	if ip := primaryIP(); ip != "" {
		host = ip
	}
	return ingestInfo{Port: cfg.IngestPort, Scheme: scheme, Host: host, Fingerprint: s.pki.IngestFingerprint()}
}

// primaryIP returns this machine's address on the lab network, so the UI can
// show the exact URL to type into the instrument.
func primaryIP() string {
	conn, err := net.Dial("udp", "192.0.2.1:9")
	if err != nil {
		return ""
	}
	defer conn.Close()
	host, _, _ := net.SplitHostPort(conn.LocalAddr().String())
	return host
}

type setupRequest struct {
	LabNoteURL string `json:"labnote_url"`
	APIKey     string `json:"api_key"`
	Name       string `json:"name"`
	Location   string `json:"location"`
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	var req setupRequest
	if !readJSON(w, r, &req) {
		return
	}
	req.LabNoteURL = strings.TrimRight(strings.TrimSpace(req.LabNoteURL), "/")

	// The key goes straight into the OS credential store; it is never written
	// to the config file and never echoed back to the UI.
	if strings.TrimSpace(req.APIKey) != "" {
		if err := keychain.Set(req.APIKey); err != nil {
			writeError(w, http.StatusInternalServerError, "could not store the API key in the operating system credential store: "+err.Error())
			return
		}
	}
	if !keychain.Has() {
		writeError(w, http.StatusBadRequest, "an ingest API key is required")
		return
	}

	if err := s.cfg.Update(func(c *config.Config) error {
		c.LabNoteURL = req.LabNoteURL
		c.Name = strings.TrimSpace(req.Name)
		c.Location = strings.TrimSpace(req.Location)
		return nil
	}); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if client := s.client(); client != nil {
		if err := client.Ping(ctx); err != nil {
			writeError(w, http.StatusBadGateway, "stored, but LabNote did not accept the connection: "+err.Error())
			return
		}
	}

	if err := s.cfg.Update(func(c *config.Config) error {
		c.SetupComplete = true
		return nil
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleSaveInstrument(w http.ResponseWriter, r *http.Request) {
	var ins model.Instrument
	if !readJSON(w, r, &ins) {
		return
	}
	if ins.Kind == "" {
		ins.Kind = model.KindOPCUA
	}
	ins.SecurityMode = model.SecurityModeSignAndEncrypt
	if ins.SecurityPolicy == "" {
		ins.SecurityPolicy = model.SecurityPolicyBasic256Sha256
	}
	if ins.ID == "" {
		ins.ID = newID()
	}
	if ins.Kind == model.KindPush && len(strings.TrimSpace(ins.IngestToken)) < 24 {
		ins.IngestToken = newToken()
	}

	if err := s.cfg.Update(func(c *config.Config) error {
		for i := range c.Instruments {
			if c.Instruments[i].ID == ins.ID {
				// Keep the pin unless the endpoint changed.
				if c.Instruments[i].EndpointURL == ins.EndpointURL && ins.ServerCertSHA256 == "" {
					ins.ServerCertSHA256 = c.Instruments[i].ServerCertSHA256
				}
				c.Instruments[i] = ins
				return nil
			}
		}
		c.Instruments = append(c.Instruments, ins)
		return nil
	}); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.mgr.Restart(ins.ID)
	writeJSON(w, http.StatusOK, ins)
}

func (s *Server) handleDeleteInstrument(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.cfg.Update(func(c *config.Config) error {
		out := c.Instruments[:0]
		for _, ins := range c.Instruments {
			if ins.ID != id {
				out = append(out, ins)
			}
		}
		c.Instruments = out
		return nil
	}); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.mgr.Reconcile()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleTestInstrument(w http.ResponseWriter, r *http.Request) {
	var ins model.Instrument
	if !readJSON(w, r, &ins) {
		return
	}
	ins.SecurityMode = model.SecurityModeSignAndEncrypt
	if ins.SecurityPolicy == "" {
		ins.SecurityPolicy = model.SecurityPolicyBasic256Sha256
	}
	if ins.ID == "" {
		ins.ID = newID()
	}
	if ins.ExternalDeviceID == "" {
		ins.ExternalDeviceID = "test-" + ins.ID
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, device.TestConnection(ctx, ins, s.pki, s.mgr, s.st, s.log))
}

type trustRequest struct {
	Fingerprint string `json:"fingerprint"`
}

func (s *Server) handleTrust(w http.ResponseWriter, r *http.Request) {
	var req trustRequest
	if !readJSON(w, r, &req) {
		return
	}
	id := r.PathValue("id")
	if strings.TrimSpace(req.Fingerprint) == "" {
		writeError(w, http.StatusBadRequest, "a certificate fingerprint is required")
		return
	}
	if err := s.mgr.Pin(id, req.Fingerprint); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.mgr.Restart(id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type settingsRequest struct {
	AutoUpdate *bool `json:"auto_update"`
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	var req settingsRequest
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.cfg.Update(func(c *config.Config) error {
		if req.AutoUpdate != nil {
			c.AutoUpdate = *req.AutoUpdate
		}
		return nil
	}); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleLogs(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="labnote-connector-logs.zip"`)
	if err := s.logs.Zip(w); err != nil {
		s.log.Error("log export failed", "error", err)
	}
}

// newToken returns the shared secret an instrument sends with every report.
func newToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func readJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(out); err != nil {
		writeError(w, http.StatusBadRequest, "could not read the request: "+err.Error())
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
