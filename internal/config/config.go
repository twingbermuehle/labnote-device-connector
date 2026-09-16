// Package config stores the connector's non-secret configuration on disk.
// Secrets (the LabNote ingest API key) never touch this file — they live in
// the OS keychain, see internal/keychain.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/labnote/labnote-device-connector/internal/model"
)

// DefaultIngestPort is the lab-network port instruments push their reports to.
const DefaultIngestPort = 8421

// Config is the persisted connector configuration.
type Config struct {
	LabNoteURL    string             `json:"labnote_url"`
	Name          string            `json:"name"`
	Location      string            `json:"location"`
	AutoUpdate    bool              `json:"auto_update"`
	SetupComplete bool              `json:"setup_complete"`
	// IngestPort and IngestTLS configure the listener that receives reports
	// pushed by instruments without OPC UA.
	IngestPort int `json:"ingest_port"`
	// IngestInsecureHTTP disables TLS on that listener (plain HTTP). TLS is
	// the default; only use plain HTTP when the instrument cannot be told to
	// trust the connector's certificate.
	IngestInsecureHTTP bool `json:"ingest_insecure_http"`
	Instruments []model.Instrument `json:"instruments"`
}

// Store is a concurrency-safe, file-backed Config.
type Store struct {
	path string
	mu   sync.RWMutex
	cfg  Config
}

// DataDir returns the per-platform directory holding config, outbox and certs.
func DataDir() string {
	if v := os.Getenv("LABNOTE_CONNECTOR_DATA_DIR"); v != "" {
		return v
	}
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(os.Getenv("ProgramData"), "LabNoteConnector")
	case "darwin":
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "Library", "Application Support", "LabNoteConnector")
	default:
		return "/var/lib/labnote-connector"
	}
}

// Open loads the config from dir, creating an empty one when absent.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	s := &Store{path: filepath.Join(dir, "config.json"), cfg: Config{AutoUpdate: true}}
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	if err := json.Unmarshal(raw, &s.cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return s, nil
}

// Get returns a copy of the current config.
func (s *Store) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c := s.cfg
	c.Instruments = append([]model.Instrument(nil), s.cfg.Instruments...)
	return c
}

// Update applies fn to the config and persists the result atomically.
func (s *Store) Update(fn func(*Config) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.cfg
	next.Instruments = append([]model.Instrument(nil), s.cfg.Instruments...)
	if err := fn(&next); err != nil {
		return err
	}
	if err := next.validate(); err != nil {
		return err
	}
	if err := s.write(next); err != nil {
		return err
	}
	s.cfg = next
	return nil
}

func (s *Store) write(c Config) error {
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// ErrInsecureEndpoint is returned when an instrument does not require
// SignAndEncrypt. Anonymous / None endpoints are never accepted.
var ErrInsecureEndpoint = errors.New("instrument must use Basic256Sha256 / SignAndEncrypt; None and anonymous auth are rejected")

func (c *Config) validate() error {
	if c.IngestPort == 0 {
		c.IngestPort = DefaultIngestPort
	}
	if c.IngestPort < 1024 || c.IngestPort > 65535 {
		return errors.New("ingest_port must be between 1024 and 65535")
	}
	if c.LabNoteURL != "" {
		u, err := url.Parse(c.LabNoteURL)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return errors.New("labnote_url must be an https URL")
		}
	}
	seen := map[string]bool{}
	tokens := map[string]bool{}
	for i := range c.Instruments {
		ins := &c.Instruments[i]
		ins.Name = strings.TrimSpace(ins.Name)
		ins.ExternalDeviceID = strings.TrimSpace(ins.ExternalDeviceID)
		if ins.ExternalDeviceID == "" {
			return errors.New("every instrument needs an external_device_id")
		}
		if seen[ins.ExternalDeviceID] {
			return fmt.Errorf("duplicate external_device_id %q", ins.ExternalDeviceID)
		}
		seen[ins.ExternalDeviceID] = true
		if ins.Kind == "" {
			ins.Kind = model.KindOPCUA
		}
		if ins.Kind == model.KindPush {
			// The instrument connects to us, so there is no endpoint and no
			// OPC UA security to validate — the token is the credential.
			ins.IngestToken = strings.TrimSpace(ins.IngestToken)
			if len(ins.IngestToken) < 24 {
				return fmt.Errorf("instrument %q: a push instrument needs an ingest token", ins.ExternalDeviceID)
			}
			if tokens[ins.IngestToken] {
				return fmt.Errorf("instrument %q: duplicate ingest token", ins.ExternalDeviceID)
			}
			tokens[ins.IngestToken] = true
			continue
		}
		if ins.Kind != model.KindOPCUA {
			return fmt.Errorf("instrument %q: unknown kind %q", ins.ExternalDeviceID, ins.Kind)
		}
		if !strings.HasPrefix(ins.EndpointURL, "opc.tcp://") {
			return fmt.Errorf("instrument %q: endpoint must start with opc.tcp://", ins.ExternalDeviceID)
		}
		if ins.SecurityMode != model.SecurityModeSignAndEncrypt {
			return fmt.Errorf("instrument %q: %w", ins.ExternalDeviceID, ErrInsecureEndpoint)
		}
		if ins.SecurityPolicy == "" {
			ins.SecurityPolicy = model.SecurityPolicyBasic256Sha256
		}
		if ins.SecurityPolicy != model.SecurityPolicyBasic256Sha256 {
			return fmt.Errorf("instrument %q: %w", ins.ExternalDeviceID, ErrInsecureEndpoint)
		}
		if ins.Profile == "" {
			ins.Profile = "generic-lads"
		}
	}
	return nil
}
