package lads

import "strings"

// UnitFromUnitID resolves an OPC UA EUInformation UnitId into a unit symbol.
//
// Per OPC UA Part 8 the UnitId is the UNECE Recommendation 20 common code
// packed into an integer, one ASCII character per byte (for example "GRM"
// = 0x47524D = gram). Instruments are allowed to send only the UnitId and no
// display name, in which case the symbol has to be derived from the code.
func UnitFromUnitID(unitID int32) string {
	code := commonCode(unitID)
	if code == "" {
		return ""
	}
	if sym, ok := uneceSymbols[code]; ok {
		return sym
	}
	return code
}

// commonCode unpacks the ASCII characters of a packed UNECE common code.
func commonCode(unitID int32) string {
	if unitID <= 0 {
		return ""
	}
	var b [4]byte
	n := 0
	for shift := 24; shift >= 0; shift -= 8 {
		c := byte((unitID >> uint(shift)) & 0xff)
		if c == 0 {
			continue
		}
		if c < 0x20 || c > 0x7e {
			return ""
		}
		b[n] = c
		n++
	}
	return strings.TrimSpace(string(b[:n]))
}

// uneceSymbols maps the UNECE common codes seen on lab instruments to the
// symbol LabNote displays. Unknown codes fall through to the code itself,
// which is still far more useful than an empty unit.
var uneceSymbols = map[string]string{
	// mass
	"GRM": "g", "KGM": "kg", "MGM": "mg", "MC": "µg", "E4": "µg", "NGM": "ng",
	// volume
	"LTR": "L", "MLT": "mL", "4G": "µL", "K6": "kL", "MMQ": "mm³", "CMQ": "cm³",
	// length
	"MTR": "m", "MMT": "mm", "CMT": "cm", "4H": "µm", "C45": "nm", "KMT": "km",
	// time
	"SEC": "s", "MIN": "min", "HUR": "h", "DAY": "d", "C26": "ms", "B98": "µs",
	// temperature
	"CEL": "°C", "KEL": "K", "FAH": "°F",
	// amount / concentration
	"MOL": "mol", "C34": "mmol", "B45": "kmol", "GL": "g/L", "M1": "mg/L",
	"GP": "g/100g", "P1": "%", "59": "ppm", "61": "ppb",
	"MMO": "mmol", "C38": "mol/L", "M9": "mol/kg",
	// pressure
	"BAR": "bar", "MBR": "mbar", "PAL": "Pa", "KPA": "kPa", "A97": "hPa",
	// electrical / optical
	"VLT": "V", "2Z": "mV", "AMP": "A", "4K": "mA", "OHM": "Ω", "HTZ": "Hz",
	"D46": "kV", "A86": "GHz", "KHZ": "kHz",
	// flow / speed
	"2N": "mL/min", "G51": "L/h", "MQH": "m³/h", "MTS": "m/s",
	// misc lab
	"P75": "pH", "B22": "kA", "C62": "1", "A94": "g/mol",
	"E32": "L/kg", "G24": "AU", "H61": "mAU",
	"RPM": "rpm", "M45": "rev/min",
}

// AbsorbanceUnits are LADS/vendor unit spellings the UNECE table does not
// cover; kept separate so it is obvious they are conventions, not standards.
var AbsorbanceUnits = map[string]string{
	"AU":  "AU",
	"MAU": "mAU",
	"RLU": "RLU",
	"OD":  "OD",
}
