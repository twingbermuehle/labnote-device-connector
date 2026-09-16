// Package certs manages the per-installation OPC UA client certificate and
// the trust-on-first-use pinning of instrument server certificates.
package certs

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Paths of the client key pair inside the data directory.
type Store struct {
	dir      string
	certPath string
	keyPath  string
}

// New returns a Store rooted at dir/pki.
func New(dir string) (*Store, error) {
	pki := filepath.Join(dir, "pki")
	if err := os.MkdirAll(pki, 0o750); err != nil {
		return nil, err
	}
	return &Store{
		dir:      pki,
		certPath: filepath.Join(pki, "client-cert.pem"),
		keyPath:  filepath.Join(pki, "client-key.pem"),
	}, nil
}

// CertPath and KeyPath expose the PEM files for the OPC UA client.
func (s *Store) CertPath() string { return s.certPath }
func (s *Store) KeyPath() string  { return s.keyPath }

// EnsureClientCert generates a self-signed client certificate on first run.
// applicationURI must match the OPC UA application URI the client announces,
// otherwise strict servers reject the session.
func (s *Store) EnsureClientCert(applicationURI, hostname string) error {
	if _, err := os.Stat(s.certPath); err == nil {
		if _, err := os.Stat(s.keyPath); err == nil {
			return nil
		}
	}

	key, err := rsa.GenerateKey(rand.Reader, 3072)
	if err != nil {
		return fmt.Errorf("generate client key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	tmpl := x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "LabNote Device Connector",
			Organization: []string{"LabNote"},
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(5, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageDataEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{hostname},
		URIs:                  nil,
	}
	if uri, err := parseURI(applicationURI); err == nil && uri != nil {
		tmpl.URIs = append(tmpl.URIs, uri)
	}

	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("create client certificate: %w", err)
	}
	if err := writePEM(s.certPath, "CERTIFICATE", der, 0o644); err != nil {
		return err
	}
	return writePEM(s.keyPath, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(key), 0o600)
}

// Fingerprint returns the SHA-256 fingerprint of the client certificate,
// formatted for display so the customer can trust it on each instrument.
func (s *Store) Fingerprint() (string, error) {
	raw, err := os.ReadFile(s.certPath)
	if err != nil {
		return "", err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return "", errors.New("client certificate is not valid PEM")
	}
	return FingerprintDER(block.Bytes), nil
}

// ClientCertDER returns the raw DER bytes of the client certificate, as needed
// for OPC UA certificate-based user authentication.
func (s *Store) ClientCertDER() ([]byte, error) {
	raw, err := os.ReadFile(s.certPath)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("client certificate is not valid PEM")
	}
	return block.Bytes, nil
}

// ClientPrivateKey returns the client RSA private key. OPC UA
// certificate-based user authentication signs the server nonce with it.
func (s *Store) ClientPrivateKey() (*rsa.PrivateKey, error) {
	raw, err := os.ReadFile(s.keyPath)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("client private key is not valid PEM")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse client private key: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("client private key is not an RSA key")
	}
	return key, nil
}

// EnsureServerCert returns the TLS certificate used by the push ingest
// listener, generating a long-lived self-signed one on first use. Instruments
// that push their reports trust this certificate once, the same way they trust
// the OPC UA client certificate.
func (s *Store) EnsureServerCert() (tls.Certificate, error) {
	certPath := filepath.Join(s.dir, "ingest-cert.pem")
	keyPath := filepath.Join(s.dir, "ingest-key.pem")
	if cert, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		return cert, nil
	}

	key, err := rsa.GenerateKey(rand.Reader, 3072)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, err
	}
	host, _ := os.Hostname()
	if host == "" {
		host = "labnote-connector"
	}
	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: host, Organization: []string{"LabNote"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(5, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{host, "localhost"},
		IPAddresses:           localIPs(),
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("create ingest certificate: %w", err)
	}
	if err := writePEM(certPath, "CERTIFICATE", der, 0o644); err != nil {
		return tls.Certificate{}, err
	}
	if err := writePEM(keyPath, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(key), 0o600); err != nil {
		return tls.Certificate{}, err
	}
	return tls.LoadX509KeyPair(certPath, keyPath)
}

// IngestFingerprint returns the SHA-256 fingerprint of the push ingest
// certificate, or "" when it has not been generated yet.
func (s *Store) IngestFingerprint() string {
	raw, err := os.ReadFile(filepath.Join(s.dir, "ingest-cert.pem"))
	if err != nil {
		return ""
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return ""
	}
	return FingerprintDER(block.Bytes)
}

// localIPs lists this machine's addresses so the certificate is valid however
// the instrument addresses the connector.
func localIPs() []net.IP {
	out := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return out
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			out = append(out, ipnet.IP)
		}
	}
	return out
}

// FingerprintDER formats a SHA-256 fingerprint as AA:BB:CC...
func FingerprintDER(der []byte) string {
	sum := sha256.Sum256(der)
	hexs := hex.EncodeToString(sum[:])
	var b strings.Builder
	for i := 0; i < len(hexs); i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(strings.ToUpper(hexs[i : i+2]))
	}
	return b.String()
}

// SameFingerprint compares two fingerprints case- and separator-insensitively.
func SameFingerprint(a, b string) bool {
	norm := func(s string) string {
		return strings.ToLower(strings.NewReplacer(":", "", " ", "", "-", "").Replace(s))
	}
	return a != "" && norm(a) == norm(b)
}

// SaveServerCert stores a pinned instrument certificate for the audit trail.
func (s *Store) SaveServerCert(externalDeviceID string, der []byte) error {
	name := strings.NewReplacer("/", "_", "\\", "_", ":", "_").Replace(externalDeviceID)
	return writePEM(filepath.Join(s.dir, "trusted-"+name+".pem"), "CERTIFICATE", der, 0o644)
}

func writePEM(path, blockType string, der []byte, mode os.FileMode) error {
	buf := pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der})
	if buf == nil {
		return errors.New("encode pem failed")
	}
	return os.WriteFile(path, buf, mode)
}

func parseURI(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, errors.New("empty uri")
	}
	return url.Parse(raw)
}
