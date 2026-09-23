package profiles

import "testing"

// The Sartorius balance profile must ship with the binary and preselect value
// reading, because those instruments expose no LADS result set.
func TestCubisProfileIsBuiltIn(t *testing.T) {
	set, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	p := set.Get("sartorius-cubis-opcua")
	if p.ID != "sartorius-cubis-opcua" {
		t.Fatalf("profile not loaded, got %q", p.ID)
	}
	if p.OPCUAMode != "values" {
		t.Errorf("opcua_mode = %q, want values", p.OPCUAMode)
	}
	if len(p.ValueTriggerPaths) == 0 || p.ValueTriggerPaths[0] != "RegisteredWeight" {
		t.Errorf("value_trigger_paths = %v", p.ValueTriggerPaths)
	}
	if p.DefaultUnitY != "g" {
		t.Errorf("default_unit_y = %q, want g", p.DefaultUnitY)
	}
}
