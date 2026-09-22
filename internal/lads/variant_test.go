package lads

import (
	"testing"

	"github.com/gopcua/opcua/ua"
)

func variant(t *testing.T, v any) *ua.Variant {
	t.Helper()
	got, err := ua.NewVariant(v)
	if err != nil {
		t.Fatalf("NewVariant(%v): %v", v, err)
	}
	return got
}

func TestVariantToStringUnwrapsText(t *testing.T) {
	cases := []struct {
		name string
		in   *ua.Variant
		want string
	}{
		{"nil variant", nil, ""},
		{"plain string", variant(t, "  Luminescence  "), "Luminescence"},
		{"localized text", variant(t, &ua.LocalizedText{Text: " Finished "}), "Finished"},
		{"qualified name", variant(t, &ua.QualifiedName{Name: "Result"}), "Result"},
		{"number", variant(t, int32(42)), "42"},
	}
	for _, c := range cases {
		if got := VariantToString(c.in); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestVariantToFloatsAcceptsScalarsAndArrays(t *testing.T) {
	if got, ok := VariantToFloats(variant(t, 1.5)); !ok || len(got) != 1 || got[0] != 1.5 {
		t.Fatalf("scalar float: got %v ok=%v", got, ok)
	}
	if got, ok := VariantToFloats(variant(t, []float32{1, 2, 3})); !ok || len(got) != 3 || got[2] != 3 {
		t.Fatalf("float32 array: got %v ok=%v", got, ok)
	}
	if got, ok := VariantToFloats(variant(t, []int16{4, 5})); !ok || len(got) != 2 || got[1] != 5 {
		t.Fatalf("integer array: got %v ok=%v", got, ok)
	}
	// Text values are not measurements and must be refused rather than
	// silently reported as zero.
	if _, ok := VariantToFloats(variant(t, "not a number")); ok {
		t.Fatal("string value was accepted as a measurement")
	}
	if _, ok := VariantToFloats(nil); ok {
		t.Fatal("nil variant was accepted as a measurement")
	}
}
