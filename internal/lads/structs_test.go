package lads

import "testing"

func TestStructPrimaryPrefersNet(t *testing.T) {
	sv := StructValue{Numbers: map[string]float64{"Gross": 12.5, "Tare": 2.5, "Net": 10}, Order: []string{"Gross", "Tare", "Net"}}
	if k, v, ok := sv.Primary(); !ok || k != "Net" || v != 10 {
		t.Fatalf("got %s=%v", k, v)
	}
	sv = StructValue{Numbers: map[string]float64{"Tare": 1, "GrossWeight": 5}, Order: []string{"Tare", "GrossWeight"}}
	if k, _, _ := sv.Primary(); k != "GrossWeight" {
		t.Fatalf("got %s", k)
	}
}
