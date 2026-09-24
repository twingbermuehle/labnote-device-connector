package device

import (
	"testing"
	"time"
)

func TestValueGateNoTimestamp(t *testing.T) {
	var g valueGate
	if !g.accept([]float64{1}, time.Time{}) {
		t.Fatal("first reading must pass")
	}
	for i := 0; i < 5; i++ {
		if g.accept([]float64{1}, time.Time{}) {
			t.Fatal("unchanged value without timestamp must be dropped")
		}
	}
	if !g.accept([]float64{2}, time.Time{}) {
		t.Fatal("changed value must pass")
	}
}

func TestValueGateRestampedValue(t *testing.T) {
	var g valueGate
	base := time.Now()
	g.accept([]float64{5}, base)
	// Instrument restamps every read: never stable, so never a repeat.
	for i := 1; i < 5; i++ {
		if g.accept([]float64{5}, base.Add(time.Duration(i)*2*time.Second)) {
			t.Fatal("restamped unchanged value must be dropped")
		}
	}
}

func TestValueGateGenuineRepeat(t *testing.T) {
	var g valueGate
	t1 := time.Now()
	g.accept([]float64{5}, t1)
	g.accept([]float64{5}, t1) // stable timestamp
	if !g.accept([]float64{5}, t1.Add(time.Minute)) {
		t.Fatal("same weight registered again with a new stable timestamp must pass")
	}
}
