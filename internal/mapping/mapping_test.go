package mapping

import (
	"testing"

	"github.com/labnote/labnote-device-connector/internal/model"
	"github.com/labnote/labnote-device-connector/internal/profiles"
)

func testProfile(t *testing.T) profiles.Profile {
	t.Helper()
	var p profiles.Profile
	p.ApplyDefaults()
	return p
}

func TestIsFinishedAcceptsVendorSpellings(t *testing.T) {
	prof := testProfile(t)
	for _, s := range []string{"Completed", "complete", "2:CompleteState", "Stopped", "Aborted", "Done"} {
		if !IsFinished(s, prof) {
			t.Errorf("state %q should count as finished", s)
		}
	}
	for _, s := range []string{"Running", "Ready", "", "Idle"} {
		if IsFinished(s, prof) {
			t.Errorf("state %q must not count as finished", s)
		}
	}
}

func TestAmbiguousStatesNeedAStopTime(t *testing.T) {
	prof := testProfile(t)
	if !IsAmbiguous("Ready", prof) || !IsAmbiguous("2:IdleState", prof) {
		t.Fatal("Ready/Idle should be treated as ambiguous, not finished")
	}
	if IsAmbiguous("Completed", prof) {
		t.Fatal("a definite finished state must not be ambiguous")
	}
}

func TestFinishedStateNumbers(t *testing.T) {
	prof := testProfile(t)
	prof.FinishedStateNumbers = []int{4, 7}
	if !IsFinishedNumber(7, prof) || IsFinishedNumber(3, prof) {
		t.Fatal("numeric state matching is wrong")
	}
}

func TestDownsampleKeepsEndsAndCap(t *testing.T) {
	points := make([]model.Point, 0, 1000)
	for i := 0; i < 1000; i++ {
		points = append(points, model.Point{X: float64(i), Y: float64(i)})
	}
	out := downsample(points, 100)
	if len(out) != 100 {
		t.Fatalf("expected 100 points, got %d", len(out))
	}
	if out[0].X != 0 || out[len(out)-1].X != 999 {
		t.Fatalf("first/last sample not preserved: %v .. %v", out[0], out[len(out)-1])
	}
	if same := downsample(points, 5000); len(same) != 1000 {
		t.Fatalf("short curves must pass through untouched, got %d", len(same))
	}
}

func TestMaxPointsFallsBackToDefault(t *testing.T) {
	if got := maxPoints(model.Instrument{}); got != model.DefaultMaxPoints {
		t.Fatalf("expected default cap, got %d", got)
	}
	if got := maxPoints(model.Instrument{MaxPoints: 42}); got != 42 {
		t.Fatalf("expected configured cap, got %d", got)
	}
}
