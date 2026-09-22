package lads

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/gopcua/opcua/ua"
)

// VariantToString renders a variant as a plain string. LocalizedText and
// QualifiedName are unwrapped so LADS names read cleanly.
func VariantToString(v *ua.Variant) string {
	if v == nil {
		return ""
	}
	switch val := v.Value().(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(val)
	case *ua.LocalizedText:
		if val == nil {
			return ""
		}
		return strings.TrimSpace(val.Text)
	case ua.LocalizedText:
		return strings.TrimSpace(val.Text)
	case *ua.QualifiedName:
		if val == nil {
			return ""
		}
		return strings.TrimSpace(val.Name)
	case *ua.EUInformation:
		return EUSymbolFrom(val)
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", val))
	}
}

// VariantToFloats converts a numeric scalar or array variant to []float64.
// Structured values (ExtensionObject, DataValue, Variant) are unwrapped first,
// which is how some array-sensor instruments deliver their curves.
func VariantToFloats(v *ua.Variant) ([]float64, bool) {
	if v == nil {
		return nil, false
	}
	return floatsFromAny(v.Value())
}

func floatsFromAny(raw any) ([]float64, bool) {
	if raw == nil {
		return nil, false
	}
	if f, ok := toFloat(raw); ok {
		return []float64{f}, true
	}
	if inner, ok := unwrap(raw); ok {
		return floatsFromAny(inner)
	}
	rv := reflect.ValueOf(raw)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		out := make([]float64, 0, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			nums, ok := floatsFromAny(rv.Index(i).Interface())
			if !ok {
				return nil, false
			}
			out = append(out, nums...)
		}
		return out, len(out) > 0
	default:
		return nil, false
	}
}

// unwrap peels one layer of OPC UA structure wrapping.
func unwrap(raw any) (any, bool) {
	switch val := raw.(type) {
	case *ua.ExtensionObject:
		if val == nil || val.Value == nil {
			return nil, false
		}
		return val.Value, true
	case *ua.Variant:
		if val == nil {
			return nil, false
		}
		return val.Value(), true
	case *ua.DataValue:
		if val == nil || val.Value == nil {
			return nil, false
		}
		return val.Value.Value(), true
	default:
		return nil, false
	}
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int8:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint8:
		return float64(n), true
	case uint16:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	default:
		return 0, false
	}
}

// EUSymbol extracts the unit symbol from an EngineeringUnits variant.
func EUSymbol(v *ua.Variant) string {
	if v == nil {
		return ""
	}
	if eu, ok := v.Value().(*ua.EUInformation); ok {
		return EUSymbolFrom(eu)
	}
	if ext, ok := v.Value().(*ua.ExtensionObject); ok && ext != nil {
		if eu, ok := ext.Value.(*ua.EUInformation); ok {
			return EUSymbolFrom(eu)
		}
	}
	return VariantToString(v)
}

// EUSymbolFrom resolves the standard OPC UA unit symbol, preferring the short
// DisplayName (e.g. "min", "mAU") over the verbose description.
func EUSymbolFrom(eu *ua.EUInformation) string {
	if eu == nil {
		return ""
	}
	if eu.DisplayName != nil && strings.TrimSpace(eu.DisplayName.Text) != "" {
		return normaliseUnit(strings.TrimSpace(eu.DisplayName.Text))
	}
	// Instruments are allowed to send only the UNECE unit code.
	if sym := UnitFromUnitID(eu.UnitID); sym != "" {
		return sym
	}
	if eu.Description != nil {
		return normaliseUnit(strings.TrimSpace(eu.Description.Text))
	}
	return ""
}

// normaliseUnit maps well-known vendor spellings onto the symbol LabNote shows.
func normaliseUnit(s string) string {
	if sym, ok := AbsorbanceUnits[strings.ToUpper(s)]; ok {
		return sym
	}
	return s
}
