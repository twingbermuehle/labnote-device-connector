// Package labnote is the HTTP client for LabNote's existing REST ingest API.
// Outbound HTTPS only; TLS verification is always on. The connector never
// exposes an inbound port for this.
package labnote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/labnote/labnote-device-connector/internal/model"
)

// API paths on the LabNote instance. The function name is part of the path.
const (
	PathConnectors = "/functions/v1/api-v1/v1/connectors"
	PathResults    = "/functions/v1/api-v1/v1/results"
)

// Client posts heartbeats and results.
type Client struct {
	baseURL string
	apiKey  func() (string, error)
	http    *http.Client
	version string
}

// New returns a client for baseURL. apiKey is a function so the key is read
// from the keychain at call time and never held in a long-lived field.
func New(baseURL string, apiKey func() (string, error), version string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		version: version,
		http: &http.Client{
			Timeout: 30 * time.Second,
			// Default transport: TLS verification on, no proxy of our own.
		},
	}
}

// Error carries the HTTP status so callers can distinguish permanent
// rejections from retryable failures.
type Error struct {
	StatusCode int
	Body       string
}

func (e *Error) Error() string {
	return fmt.Sprintf("labnote api %d: %s", e.StatusCode, e.Body)
}

// Duplicate reports whether the server rejected the record as an already
// ingested duplicate (idempotency key hit). Such rows count as delivered.
func (e *Error) Duplicate() bool {
	return e.StatusCode == http.StatusConflict
}

// Permanent reports whether retrying can never succeed.
func (e *Error) Permanent() bool {
	return e.StatusCode == http.StatusBadRequest ||
		e.StatusCode == http.StatusUnprocessableEntity ||
		e.StatusCode == http.StatusRequestEntityTooLarge
}

// Unauthorized reports an invalid or missing API key.
func (e *Error) Unauthorized() bool {
	return e.StatusCode == http.StatusUnauthorized || e.StatusCode == http.StatusForbidden
}

// SendHeartbeat upserts the connector and its devices.
func (c *Client) SendHeartbeat(ctx context.Context, hb model.Heartbeat) error {
	return c.post(ctx, PathConnectors, hb, nil)
}

// SendResult ingests one finished measurement.
func (c *Client) SendResult(ctx context.Context, r model.Result) error {
	return c.post(ctx, PathResults, r, nil)
}

// Ping verifies URL + API key during setup.
func (c *Client) Ping(ctx context.Context) error {
	req, err := c.newRequest(ctx, http.MethodGet, PathConnectors, nil)
	if err != nil {
		return err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	if res.StatusCode >= 400 {
		return &Error{StatusCode: res.StatusCode, Body: string(body)}
	}
	return nil
}

func (c *Client) post(ctx context.Context, path string, payload any, out any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := c.newRequest(ctx, http.MethodPost, path, raw)
	if err != nil {
		return err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 8192))
	if res.StatusCode >= 400 {
		return &Error{StatusCode: res.StatusCode, Body: string(body)}
	}
	if out != nil && len(body) > 0 {
		return json.Unmarshal(body, out)
	}
	return nil
}

func (c *Client) newRequest(ctx context.Context, method, path string, body []byte) (*http.Request, error) {
	if c.baseURL == "" {
		return nil, errors.New("labnote url is not configured")
	}
	if !strings.HasPrefix(c.baseURL, "https://") {
		return nil, errors.New("labnote url must use https")
	}
	key, err := c.apiKey()
	if err != nil {
		return nil, err
	}
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "labnote-device-connector/"+c.version)
	return req, nil
}
