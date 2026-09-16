// Package ingest accepts measurement reports that an instrument pushes to the
// connector over HTTP(S) — used by balances such as the Sartorius Cubis II,
// which have no OPC UA server but can send every weighing to a web service.
package ingest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/labnote/labnote-device-connector/internal/model"
)

// Report is one parsed push payload.
type Report struct {
	Value      float64
	HasValue   bool
	Unit       string
	Sample     string
	Method     string
	Operator   string
	ResultID   string
	MeasuredAt time.Time
	Fields     map[string]any
}

var (
	valueKeys    = []string{"net weight", "netweight", "net", "netto", "weight", "gewicht", "value", "wert", "result", "ergebnis", "mass", "masse", "quantity"}
	unitKeys     = []string{"unit", "einheit", "uom"}
	sampleKeys   = []string{"sample code", "sample id", "sampleid", "sample", "probe", "probenname", "probennummer", "barcode", "lot", "batch", "charge"}
	methodKeys   = []string{"method", "methode", "task", "application", "anwendung", "program", "programm", "qapp", "workflow"}
	operatorKeys = []string{"operator", "user", "username", "benutzer", "bediener", "anwender"}
	timeKeys     = []string{"measured at", "measuredat", "timestamp", "datetime", "date time", "date", "datum", "time", "zeit", "created", "completed"}
	idKeys       = []string{"external result id", "result id", "resultid", "record id", "recordid", "transaction id", "alibi", "record", "uuid", "guid"}

	numberRE = regexp.MustCompile(`^\s*[+-]?\s*([0-9]+(?:[.,][0-9]+)?)\s*([^\s0-9]*)\s*$`)
	// SBI style print line: "N +     12.34567 g" or "G      0.0012 mg"
	sbiRE = regexp.MustCompile(`(?i)^\s*(?:N|G|T|Net|Gross|Tare)?\s*([+-]?\s*[0-9]+(?:[.,][0-9]+)?)\s*(mg|µg|ug|g|kg|ct|oz|lb)\s*$`)

	timeLayouts = []string{
		time.RFC3339Nano, time.RFC3339,
		"2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02 15:04",
		"02.01.2006 15:04:05", "02.01.2006 15:04", "02.01.2006",
		"01/02/2006 15:04:05", "01/02/2006 3:04:05 PM", "2006-01-02",
	}
)

// Parse understands both a JSON body and the plain-text / key-value report
// formats balances print. Unknown fields are preserved in Fields.
func Parse(body []byte, contentType string) (Report, error) {
	rep := Report{Fields: map[string]any{}}
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return rep, fmt.Errorf("the report was empty")
	}

	switch {
	case strings.HasPrefix(trimmed, "{"), strings.HasPrefix(trimmed, "["), strings.Contains(strings.ToLower(contentType), "json"):
		var any1 any
		if err := json.Unmarshal([]byte(trimmed), &any1); err != nil {
			return rep, fmt.Errorf("the report is not valid JSON: %w", err)
		}
		flatten("", any1, rep.Fields)
	case strings.Contains(strings.ToLower(contentType), "x-www-form-urlencoded"):
		for _, pair := range strings.Split(trimmed, "&") {
			k, v, _ := strings.Cut(pair, "=")
			rep.Fields[unescapeForm(k)] = unescapeForm(v)
		}
	default:
		parseText(trimmed, rep.Fields)
	}

	rep.Unit = strings.TrimSpace(lookupString(rep.Fields, unitKeys))
	if v, unit, ok := lookupNumber(rep.Fields, valueKeys); ok {
		rep.Value, rep.HasValue = v, true
		if rep.Unit == "" {
			rep.Unit = unit
		}
	}
	rep.Sample = lookupString(rep.Fields, sampleKeys)
	rep.Method = lookupString(rep.Fields, methodKeys)
	rep.Operator = lookupString(rep.Fields, operatorKeys)
	rep.ResultID = lookupString(rep.Fields, idKeys)
	if ts, ok := lookupTime(rep.Fields, timeKeys); ok {
		rep.MeasuredAt = ts
	}
	if !rep.HasValue {
		return rep, fmt.Errorf("the report contained no numeric weight or measurement value")
	}
	return rep, nil
}

// Result turns a parsed report into exactly one LabNote ingest record.
func (rep Report) Result(ins model.Instrument, body []byte, receivedAt time.Time) model.Result {
	measuredAt := rep.MeasuredAt
	if measuredAt.IsZero() {
		measuredAt = receivedAt
	}
	measuredAt = measuredAt.UTC()

	unit := rep.Unit
	if unit == "" {
		unit = ins.DefaultUnitY
	}

	resultID := strings.TrimSpace(rep.ResultID)
	if resultID == "" {
		// No identifier in the payload: derive a stable one so a retried push
		// of the same report is deduplicated instead of duplicated. When the
		// report carries no timestamp either, the receive second is mixed in
		// so two identical weighings stay distinct.
		seed := ins.ExternalDeviceID + "|" + string(body)
		if rep.MeasuredAt.IsZero() {
			seed += "|" + receivedAt.UTC().Format("2006-01-02T15:04:05")
		}
		sum := sha256.Sum256([]byte(seed))
		resultID = "push-" + hex.EncodeToString(sum[:12])
	} else {
		resultID = ins.ExternalDeviceID + ":" + resultID
	}

	summary := map[string]any{"value": rep.Value}
	if unit != "" {
		summary["unit"] = unit
	}

	return model.Result{
		ExternalDeviceID: ins.ExternalDeviceID,
		ExternalResultID: resultID,
		Method:           rep.Method,
		MeasuredAt:       measuredAt,
		SampleCode:       rep.Sample,
		Operator:         rep.Operator,
		UnitX:            ins.DefaultUnitX,
		UnitY:            unit,
		Points:           []model.Point{{X: 0, Y: rep.Value}},
		Summary:          summary,
		LADS:             map[string]any{"push_report": rep.Fields},
	}
}

func parseText(text string, out map[string]any) {
	for i, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(strings.ReplaceAll(raw, "\r", ""))
		if line == "" {
			continue
		}
		if m := sbiRE.FindStringSubmatch(line); m != nil {
			if _, exists := out["weight"]; !exists {
				out["weight"] = strings.ReplaceAll(m[1], " ", "") + " " + m[2]
				continue
			}
		}
		key, value, ok := cutKeyValue(line)
		if !ok {
			out[fmt.Sprintf("line_%d", i+1)] = line
			continue
		}
		out[key] = value
	}
}

func cutKeyValue(line string) (string, string, bool) {
	for _, sep := range []string{":", "\t", ";", "=", "  "} {
		if k, v, ok := strings.Cut(line, sep); ok {
			k, v = strings.TrimSpace(k), strings.TrimSpace(v)
			if k != "" && v != "" {
				return k, v, true
			}
		}
	}
	return "", "", false
}

func flatten(prefix string, v any, out map[string]any) {
	switch t := v.(type) {
	case map[string]any:
		for k, sub := range t {
			key := k
			if prefix != "" {
				key = prefix + "." + k
			}
			flatten(key, sub, out)
		}
	case []any:
		for i, sub := range t {
			flatten(fmt.Sprintf("%s.%d", prefix, i), sub, out)
		}
	default:
		if prefix == "" {
			prefix = "value"
		}
		out[prefix] = t
	}
}

func normKey(k string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.NewReplacer("_", " ", "-", " ", ".", " ").Replace(k))), " ")
}

// candidates returns the map entries whose (normalised) key matches one of the
// wanted names, best (exact) match first.
func candidates(fields map[string]any, wanted []string) []any {
	var exact, partial []any
	for _, want := range wanted {
		for k, v := range fields {
			nk := normKey(k)
			switch {
			case nk == want:
				exact = append(exact, v)
			case strings.Contains(nk, want):
				partial = append(partial, v)
			}
		}
	}
	return append(exact, partial...)
}

func lookupString(fields map[string]any, wanted []string) string {
	for _, v := range candidates(fields, wanted) {
		if s := strings.TrimSpace(fmt.Sprintf("%v", v)); s != "" && s != "<nil>" {
			return s
		}
	}
	return ""
}

func lookupNumber(fields map[string]any, wanted []string) (float64, string, bool) {
	for _, v := range candidates(fields, wanted) {
		switch t := v.(type) {
		case float64:
			return t, "", true
		case int:
			return float64(t), "", true
		case string:
			if m := numberRE.FindStringSubmatch(t); m != nil {
				f, err := strconv.ParseFloat(strings.ReplaceAll(m[1], ",", "."), 64)
				if err == nil {
					return f, strings.TrimSpace(m[2]), true
				}
			}
		}
	}
	return 0, "", false
}

func lookupTime(fields map[string]any, wanted []string) (time.Time, bool) {
	// A date and a time in separate fields are common on printed reports.
	var date, clock string
	for k, v := range fields {
		s := strings.TrimSpace(fmt.Sprintf("%v", v))
		switch normKey(k) {
		case "date", "datum":
			date = s
		case "time", "zeit", "uhrzeit":
			clock = s
		}
	}
	if date != "" && clock != "" {
		if ts, ok := parseTime(date + " " + clock); ok {
			return ts, true
		}
	}
	for _, v := range candidates(fields, wanted) {
		if ts, ok := parseTime(strings.TrimSpace(fmt.Sprintf("%v", v))); ok {
			return ts, true
		}
	}
	return time.Time{}, false
}

func parseTime(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range timeLayouts {
		if ts, err := time.Parse(layout, s); err == nil {
			return ts, true
		}
		if ts, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return ts, true
		}
	}
	return time.Time{}, false
}

func unescapeForm(s string) string {
	s = strings.ReplaceAll(s, "+", " ")
	out := strings.Builder{}
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			if b, err := strconv.ParseUint(s[i+1:i+3], 16, 8); err == nil {
				out.WriteByte(byte(b))
				i += 2
				continue
			}
		}
		out.WriteByte(s[i])
	}
	return strings.TrimSpace(out.String())
}
