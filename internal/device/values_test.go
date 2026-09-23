package device

import (
	"testing"

	"github.com/labnote/labnote-device-connector/internal/model"
	"github.com/labnote/labnote-device-connector/internal/profiles"
)

func TestLeafName(t *testing.T) {
	for in, want := range map[string]string{
		"RegisteredWeight":              "RegisteredWeight",
		"Simple Scale/RegisteredWeight": "RegisteredWeight",
		"":                              "",
	} {
		if got := leafName(in); got != want {
			t.Errorf("leafName(%q) = %q, want %q", in, got, want)
		}
	}
}

// The extra values reported alongside the trigger come from the instrument's
// ticked parameters, fall back to the profile, and never repeat the trigger.
func TestValuePaths(t *testing.T) {
	prof := profiles.Profile{ValuePaths: []string{"CurrentWeight"}}

	s := &Supervisor{profile: prof}
	if got := s.valuePaths("RegisteredWeight"); len(got) != 1 || got[0] != "CurrentWeight" {
		t.Fatalf("profile fallback = %v", got)
	}

	s = &Supervisor{profile: prof, ins: model.Instrument{Parameters: []model.Parameter{
		{Path: "RegisteredWeight", Kind: "value", Enabled: true},
		{Path: "CurrentWeight", Kind: "value", Enabled: true},
		{Path: "Ignored", Kind: "value"},
	}}}
	got := s.valuePaths("RegisteredWeight")
	if len(got) != 1 || got[0] != "CurrentWeight" {
		t.Fatalf("enabled parameters = %v, want only CurrentWeight", got)
	}
}
