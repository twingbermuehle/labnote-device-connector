// Package updater checks the GitHub Releases feed for a newer connector,
// verifies the release signature against the public key baked into the binary
// and swaps the running executable.
//
// Auto-update can be disabled in the config: regulated labs validate changes
// manually and only want the "update available" indication.
package updater

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ReleasePublicKeyPEM is the cosign/ECDSA public key of the release signing
// key. It is replaced at build time with -ldflags for signed releases.
var ReleasePublicKeyPEM = ""

const feedURL = "https://api.github.com/repos/labnote/labnote-device-connector/releases/latest"

// Status is what the local UI shows.
type Status struct {
	CurrentVersion  string    `json:"current_version"`
	LatestVersion   string    `json:"latest_version,omitempty"`
	UpdateAvailable bool      `json:"update_available"`
	AutoUpdate      bool      `json:"auto_update"`
	CheckedAt       time.Time `json:"checked_at,omitempty"`
	LastError       string    `json:"last_error,omitempty"`
}

type release struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

// Checker polls the release feed.
type Checker struct {
	version    string
	autoUpdate func() bool
	log        *slog.Logger
	http       *http.Client
	status     Status
}

// New returns a checker.
func New(version string, autoUpdate func() bool, log *slog.Logger) *Checker {
	return &Checker{
		version:    version,
		autoUpdate: autoUpdate,
		log:        log,
		http:       &http.Client{Timeout: 30 * time.Second},
		status:     Status{CurrentVersion: version},
	}
}

// Status returns the last known update status.
func (c *Checker) Status() Status {
	s := c.status
	s.AutoUpdate = c.autoUpdate()
	return s
}

// Run checks on start and then every 6 hours.
func (c *Checker) Run(ctx context.Context) {
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for {
		c.checkOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (c *Checker) checkOnce(ctx context.Context) {
	rel, err := c.latest(ctx)
	c.status.CheckedAt = time.Now().UTC()
	if err != nil {
		c.status.LastError = err.Error()
		return
	}
	c.status.LastError = ""
	c.status.LatestVersion = rel.TagName
	c.status.UpdateAvailable = newer(rel.TagName, c.version)

	if !c.status.UpdateAvailable || !c.autoUpdate() {
		return
	}
	if err := c.apply(ctx, rel); err != nil {
		c.status.LastError = err.Error()
		c.log.Error("auto-update failed", "error", err)
		return
	}
	c.log.Info("update installed, restart pending", "version", rel.TagName)
}

func (c *Checker) latest(ctx context.Context) (*release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		return nil, fmt.Errorf("release feed returned %d", res.StatusCode)
	}
	var rel release
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

// apply downloads the platform binary plus its .sig, verifies the signature
// and replaces the running executable. The service manager restarts it.
func (c *Checker) apply(ctx context.Context, rel *release) error {
	if strings.TrimSpace(ReleasePublicKeyPEM) == "" {
		return errors.New("no release public key baked into this build; refusing to self-update")
	}
	wantBinary := fmt.Sprintf("labnote-connector_%s_%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		wantBinary += ".exe"
	}

	var binURL, sigURL string
	for _, a := range rel.Assets {
		switch a.Name {
		case wantBinary:
			binURL = a.BrowserDownloadURL
		case wantBinary + ".sig":
			sigURL = a.BrowserDownloadURL
		}
	}
	if binURL == "" || sigURL == "" {
		return fmt.Errorf("release %s has no signed asset for %s/%s", rel.TagName, runtime.GOOS, runtime.GOARCH)
	}

	binary, err := c.download(ctx, binURL, 200<<20)
	if err != nil {
		return err
	}
	sig, err := c.download(ctx, sigURL, 1<<16)
	if err != nil {
		return err
	}
	if err := verify(binary, sig); err != nil {
		return fmt.Errorf("signature verification failed: %w", err)
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	tmp := exe + ".new"
	if err := os.WriteFile(tmp, binary, 0o755); err != nil {
		return err
	}
	// Windows cannot overwrite a running image: move it aside first.
	old := exe + ".old"
	_ = os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		return err
	}
	if err := os.Rename(tmp, exe); err != nil {
		_ = os.Rename(old, exe)
		return err
	}
	return nil
}

func (c *Checker) download(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		return nil, fmt.Errorf("download %s: %d", url, res.StatusCode)
	}
	return io.ReadAll(io.LimitReader(res.Body, limit))
}

// verify checks an ECDSA signature (base64, as produced by cosign sign-blob)
// over the SHA-256 digest of the payload.
func verify(payload, signature []byte) error {
	block, _ := pem.Decode([]byte(ReleasePublicKeyPEM))
	if block == nil {
		return errors.New("release public key is not valid PEM")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return err
	}
	key, ok := pub.(*ecdsa.PublicKey)
	if !ok {
		return errors.New("release public key is not ECDSA")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(signature)))
	if err != nil {
		raw = signature
	}
	digest := sha256.Sum256(payload)
	if !ecdsa.VerifyASN1(key, digest[:], raw) {
		return errors.New("signature does not match")
	}
	return nil
}

// newer compares semantic version tags such as v1.2.3.
func newer(candidate, current string) bool {
	c := parseVersion(candidate)
	n := parseVersion(current)
	for i := 0; i < 3; i++ {
		if c[i] != n[i] {
			return c[i] > n[i]
		}
	}
	return false
}

func parseVersion(v string) [3]int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i > 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	var out [3]int
	for i := 0; i < 3 && i < len(parts); i++ {
		n := 0
		for _, ch := range parts[i] {
			if ch < '0' || ch > '9' {
				break
			}
			n = n*10 + int(ch-'0')
		}
		out[i] = n
	}
	return out
}
