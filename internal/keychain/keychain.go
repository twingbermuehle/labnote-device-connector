// Package keychain stores the LabNote ingest API key in the operating
// system's credential store: Windows Credential Manager, macOS Keychain or
// the freedesktop Secret Service on Linux. The key is never written to a
// config file and never logged.
//
// Containers usually have no Secret Service. For the Docker image the key is
// supplied by the LABNOTE_INGEST_API_KEY environment variable instead, which
// takes precedence over the keychain.
package keychain

import (
	"errors"
	"os"
	"strings"

	keyring "github.com/zalando/go-keyring"
)

const (
	service = "labnote-device-connector"
	account = "ingest-api-key"
	envVar  = "LABNOTE_INGEST_API_KEY"
)

// ErrNotFound means no API key has been stored yet.
var ErrNotFound = errors.New("no ingest API key stored")

// Set stores the API key in the OS credential store.
func Set(key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("api key must not be empty")
	}
	return keyring.Set(service, account, key)
}

// Get returns the stored API key, preferring the environment variable used by
// the container image.
func Get() (string, error) {
	if v := strings.TrimSpace(os.Getenv(envVar)); v != "" {
		return v, nil
	}
	v, err := keyring.Get(service, account)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return v, nil
}

// Delete removes the stored API key.
func Delete() error {
	err := keyring.Delete(service, account)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

// Has reports whether a key is available without returning it.
func Has() bool {
	_, err := Get()
	return err == nil
}
