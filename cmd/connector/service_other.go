//go:build !windows

package main

import "context"

// startService is a no-op outside Windows: systemd and Docker run the
// connector as a normal foreground process.
func startService(_ func(context.Context) error) (bool, error) { return false, nil }
