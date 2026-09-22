package lads

import "testing"

func TestUnitFromUnitIDDecodesPackedCommonCodes(t *testing.T) {
	cases := map[int32]string{
		0x47524D: "g",  // GRM
		0x43454C: "°C", // CEL
		0x4D4C54: "mL", // MLT
		0x0053EC: "",   // not printable ASCII
		0:        "",   // unset
	}
	for id, want := range cases {
		if got := UnitFromUnitID(id); got != want {
			t.Errorf("UnitFromUnitID(%#x) = %q, want %q", id, got, want)
		}
	}
}

func TestUnknownCommonCodeFallsBackToTheCodeItself(t *testing.T) {
	// "XYZ" is not in the table; showing the code beats showing nothing.
	if got := UnitFromUnitID(0x58595A); got != "XYZ" {
		t.Fatalf("expected the raw code, got %q", got)
	}
}
