// Package mapping turns a finished LADS result object into exactly one
// LabNote ingest record.
package mapping

import (
	"context"
	"fmt"
	"time"

	"github.com/gopcua/opcua/ua"

	"github.com/labnote/labnote-device-connector/internal/lads"
	"github.com/labnote/labnote-device-connector/internal/model"
	"github.com/labnote/labnote-device-connector/internal/profiles"
)

// Option tunes Build.
type Option func(*options)

type options struct{ functionalUnit string }

// WithFunctionalUnit records which functional unit of the device produced the
// result, so results of a multi-part instrument stay distinguishable.
func WithFunctionalUnit(name string) Option {
	return func(o *options) { o.functionalUnit = name }
}

// Build reads the full result and maps it to a model.Result.
//
// external_result_id is built from device-stable fields only (the instrument's
// own result id when it reports one, otherwise node id plus the instrument's
// stop timestamp). It is the idempotency key: replays of the same result are
// rejected server-side.
func Build(
	ctx context.Context,
	b *lads.Browser,
	ins model.Instrument,
	prof profiles.Profile,
	resultNode *ua.NodeID,
	opts ...Option,
) (model.Result, error) {
	var o options
	for _, fn := range opts {
		fn(&o)
	}
	// The measurement time comes from the instrument whenever it reports one.
	// Only as a last resort is the current time used, and that case is flagged
	// so LabNote can tell an exact time from an estimate.
	measuredAt, estimated := measurementTime(ctx, b, resultNode)

	// The idempotency key must be derived from device-stable fields only:
	// a wall-clock fallback would make the same physical result look new on
	// every read. When the instrument reports no identity of its own, the node
	// id alone is the key, and the server-side duplicate check does the rest.
	r := model.Result{
		ExternalDeviceID: ins.ExternalDeviceID,
		ExternalResultID: resultID(ctx, b, ins, resultNode, measuredAt, estimated),
		MeasuredAt:       measuredAt,
		Method:           firstNonEmpty(b.ReadStringPath(ctx, resultNode, prof.MethodPaths), b.ReadPropertyKey(ctx, resultNode, prof.PropertyPaths, prof.MethodKeys)),
		SampleCode:       firstNonEmpty(b.ReadStringPath(ctx, resultNode, prof.SampleCodePaths), b.ReadPropertyKey(ctx, resultNode, prof.PropertyPaths, prof.SampleCodeKeys)),
		Operator:         firstNonEmpty(b.ReadStringPath(ctx, resultNode, prof.OperatorPaths), b.ReadPropertyKey(ctx, resultNode, prof.PropertyPaths, prof.OperatorKeys)),
		Summary:          map[string]any{},
		LADS:             map[string]any{},
	}

	// Parameters chosen during setup win over the profile's guesses.
	chosenSeries, chosenValues := ins.EnabledParameters()

	ys, unitY, okY := b.ReadFloatsPath(ctx, resultNode, append(chosenSeries, prof.SeriesYPaths...))
	if !okY {
		ys, unitY, okY = b.FirstNumericArrayBelow(ctx, resultNode, prof.SeriesContainerPaths)
	}
	rawPointCount := 0
	if okY {
		xs, unitX, okX := b.ReadFloatsPath(ctx, resultNode, prof.SeriesXPaths)
		points := zip(xs, ys, okX)
		rawPointCount = len(points)
		r.Points = downsample(points, maxPoints(ins))
		r.UnitX = firstNonEmpty(unitX, ins.DefaultUnitX, prof.DefaultUnitX)
		r.UnitY = firstNonEmpty(unitY, ins.DefaultUnitY, prof.DefaultUnitY)
	}

	// Scalars and everything else the profile points at go into summary, and
	// the raw node values are preserved under "lads" for audit reproducibility.
	rawNodes := map[string]any{}
	for _, path := range append(chosenValues, prof.ScalarPaths...) {
		v, err := b.ReadPath(ctx, resultNode, path)
		if err != nil || v == nil {
			continue
		}
		if nums, ok := lads.VariantToFloats(v); ok && len(nums) == 1 {
			r.Summary[path] = nums[0]
			rawNodes[path] = nums[0]
			continue
		}
		s := lads.VariantToString(v)
		if s == "" {
			continue
		}
		r.Summary[path] = s
		rawNodes[path] = s
	}

	r.LADS = map[string]any{
		"node_id":          resultNode.String(),
		"endpoint_url":     ins.EndpointURL,
		"device_node_id":   ins.LADSNodeID,
		"profile":          prof.ID,
		"source_timestamp": measuredAt.Format(time.RFC3339Nano),
		"nodes":            rawNodes,
	}
	if estimated {
		// The instrument reported no measurement time: say so instead of
		// letting the receiving end believe this clock reading came from it.
		r.LADS["measured_at_estimated"] = true
		r.Summary["measured_at_estimated"] = true
	}
	if o.functionalUnit != "" {
		// A device with several functional units (autosampler, detector, ...)
		// would otherwise produce indistinguishable records.
		r.LADS["functional_unit"] = o.functionalUnit
		r.Summary["functional_unit"] = o.functionalUnit
	}
	if r.Operator != "" {
		r.Summary["operator"] = r.Operator
	}
	if len(r.Points) > 0 {
		r.Summary["point_count"] = len(r.Points)
		if rawPointCount > len(r.Points) {
			r.Summary["point_count_measured"] = rawPointCount
			r.Summary["downsampled"] = true
			r.LADS["point_count_measured"] = rawPointCount
			r.LADS["downsampled"] = true
		}
	}
	// The raw LADS metadata is also mirrored inside summary, as the ingest API
	// documents, so audit consumers find it in both places.
	r.Summary["lads"] = r.LADS

	return r, nil
}

// IsFinished reports whether a state value counts as finished for this profile.
// State machine state names often carry a "...State" suffix and may arrive
// namespace-qualified ("2:CompleteState"), so both are normalised away.
func IsFinished(state string, prof profiles.Profile) bool {
	state = normaliseState(state)
	if state == "" {
		return false
	}
	for _, s := range prof.FinishedStates {
		if equalFold(state, normaliseState(s)) {
			return true
		}
	}
	return false
}

// IsAmbiguous reports a state that means "not running" but does not by itself
// prove a result exists (Ready, Idle, ...). The caller treats it as finished
// only together with a stop timestamp.
func IsAmbiguous(state string, prof profiles.Profile) bool {
	state = normaliseState(state)
	if state == "" {
		return false
	}
	for _, s := range prof.AmbiguousStates {
		if equalFold(state, normaliseState(s)) {
			return true
		}
	}
	return false
}

// IsFinishedNumber matches a state machine's numeric state, used by
// instruments whose state text is not human readable.
func IsFinishedNumber(number int, prof profiles.Profile) bool {
	for _, n := range prof.FinishedStateNumbers {
		if n == number {
			return true
		}
	}
	return false
}

func normaliseState(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, ":"); i >= 0 {
		s = s[i+1:]
	}
	s = strings.TrimSpace(s)
	if len(s) > len("State") && strings.EqualFold(s[len(s)-len("State"):], "State") {
		s = s[:len(s)-len("State")]
	}
	return s
}

// measurementTime returns the instrument's own measurement time and whether it
// had to be estimated from the connector's clock.
func measurementTime(ctx context.Context, b *lads.Browser, node *ua.NodeID) (time.Time, bool) {
	if ts, ok := b.StoppedTime(ctx, node); ok {
		return ts, false
	}
	if ts := readTimestamp(ctx, b, node); !ts.IsZero() {
		return ts, false
	}
	if dv, err := b.SourceTimestamp(ctx, node); err == nil && dv != nil && !dv.SourceTimestamp.IsZero() {
		return dv.SourceTimestamp.UTC(), false
	}
	return time.Now().UTC(), true
}

// resultID builds the idempotency key. It never mixes in the connector's own
// clock: an estimated time would make the same result look new on every read.
func resultID(ctx context.Context, b *lads.Browser, ins model.Instrument, node *ua.NodeID, measuredAt time.Time, estimated bool) string {
	// Prefer an identifier the instrument itself assigns to the run.
	if own := b.ReadStringPath(ctx, node, []string{
		"Properties/ResultId", "ResultId", "Properties/RunId", "RunId", "Identifier",
	}); own != "" {
		return fmt.Sprintf("%s:%s", ins.ExternalDeviceID, own)
	}
	if estimated {
		// Instruments that reuse one mutable result node and report no time at
		// all cannot be told apart run-by-run; the node id keeps repeats out.
		return node.String()
	}
	return fmt.Sprintf("%s@%s", node.String(), measuredAt.UTC().Format(time.RFC3339Nano))
}

// maxPoints resolves the configured curve cap.
func maxPoints(ins model.Instrument) int {
	if ins.MaxPoints > 0 {
		return ins.MaxPoints
	}
	return model.DefaultMaxPoints
}

// downsample evenly reduces a curve to at most max points, always keeping the
// first and last sample so the time axis still spans the whole run.
func downsample(points []model.Point, max int) []model.Point {
	if max <= 0 || len(points) <= max {
		return points
	}
	out := make([]model.Point, 0, max)
	step := float64(len(points)-1) / float64(max-1)
	for i := 0; i < max-1; i++ {
		out = append(out, points[int(float64(i)*step)])
	}
	return append(out, points[len(points)-1])
}

func readTimestamp(ctx context.Context, b *lads.Browser, node *ua.NodeID) time.Time {
	for _, p := range []string{"Properties/EndTime", "EndTime", "Properties/StopTime", "StartTime"} {
		v, err := b.ReadPath(ctx, node, p)
		if err != nil || v == nil {
			continue
		}
		if ts, ok := v.Value().(time.Time); ok && !ts.IsZero() {
			return ts.UTC()
		}
	}
	return time.Time{}
}

func zip(xs, ys []float64, haveX bool) []model.Point {
	out := make([]model.Point, 0, len(ys))
	for i, y := range ys {
		x := float64(i)
		if haveX && i < len(xs) {
			x = xs[i]
		}
		out = append(out, model.Point{X: x, Y: y})
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
