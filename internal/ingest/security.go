package ingest

import (
	"crypto/subtle"
	"crypto/tls"
)

// subtleEqual compares two tokens in constant time.
func subtleEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// tlsConfig serves the connector's own certificate with modern TLS only.
func tlsConfig(cert tls.Certificate) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}
}
