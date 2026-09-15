// Package logging sets up rotating local logs. Logs contain metadata only —
// never result payloads — and are never sent anywhere (no telemetry).
package logging

import (
	"archive/zip"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
)

const (
	maxBytes = 8 << 20 // rotate at 8 MiB
	maxFiles = 5
)

// Rotator is an io.Writer that rotates the log file by size.
type Rotator struct {
	dir  string
	base string
	mu   sync.Mutex
	f    *os.File
	n    int64
}

// New opens the log directory and returns a logger plus the rotator (kept so
// the UI can zip the logs).
func New(dir string, debug bool) (*slog.Logger, *Rotator, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, nil, err
	}
	r := &Rotator{dir: dir, base: "connector.log"}
	if err := r.open(); err != nil {
		return nil, nil, err
	}
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	handler := slog.NewJSONHandler(io.MultiWriter(os.Stdout, r), &slog.HandlerOptions{Level: level})
	return slog.New(handler), r, nil
}

// Dir returns the log directory.
func (r *Rotator) Dir() string { return r.dir }

func (r *Rotator) open() error {
	path := filepath.Join(r.dir, r.base)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		return err
	}
	r.f, r.n = f, info.Size()
	return nil
}

// Write implements io.Writer with size-based rotation.
func (r *Rotator) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.n+int64(len(p)) > maxBytes {
		if err := r.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := r.f.Write(p)
	r.n += int64(n)
	return n, err
}

func (r *Rotator) rotate() error {
	if err := r.f.Close(); err != nil {
		return err
	}
	base := filepath.Join(r.dir, r.base)
	for i := maxFiles - 1; i >= 1; i-- {
		_ = os.Rename(base+"."+itoa(i), base+"."+itoa(i+1))
	}
	if err := os.Rename(base, base+".1"); err != nil {
		return err
	}
	return r.open()
}

// Zip writes all log files as a zip archive to w (the Export logs button).
func (r *Rotator) Zip(w io.Writer) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	entries, err := filepath.Glob(filepath.Join(r.dir, r.base+"*"))
	if err != nil {
		return err
	}
	sort.Strings(entries)

	zw := zip.NewWriter(w)
	for _, path := range entries {
		src, err := os.Open(path)
		if err != nil {
			continue
		}
		dst, err := zw.Create(filepath.Base(path))
		if err != nil {
			_ = src.Close()
			return err
		}
		_, copyErr := io.Copy(dst, src)
		_ = src.Close()
		if copyErr != nil {
			return copyErr
		}
	}
	return zw.Close()
}

func itoa(i int) string { return strconv.Itoa(i) }
