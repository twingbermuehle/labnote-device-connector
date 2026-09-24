package device

import (
	"fmt"
	"sync"
	"time"
)

// valueGate decides whether a new reading of the trigger variable is a new
// measurement. Some instruments omit the source timestamp or restamp the value
// on every read; relying on the timestamp alone then turns every poll into a
// "new" result. The gate therefore compares the value itself: an unchanged
// value is only accepted again when the instrument supplies a timestamp that
// was stable across reads and has now moved on (a genuine repeat weighing of
// the same weight).
type valueGate struct {
	mu       sync.Mutex
	started  bool
	lastSig  string
	lastTS   time.Time
	tsStable bool
}

func (g *valueGate) accept(values []float64, ts time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	sig := fmt.Sprint(values)
	if !g.started {
		g.started, g.lastSig, g.lastTS = true, sig, ts
		return true
	}
	if sig != g.lastSig {
		g.lastSig, g.lastTS, g.tsStable = sig, ts, false
		return true
	}
	// Same value as before.
	if ts.IsZero() || ts.Equal(g.lastTS) {
		if !ts.IsZero() {
			g.tsStable = true
		}
		return false
	}
	accept := g.tsStable
	g.lastTS, g.tsStable = ts, false
	return accept
}
