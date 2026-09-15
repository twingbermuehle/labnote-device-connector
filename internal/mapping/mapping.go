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

// Build reads the full result and maps it to a model.Result.
//
// external_result_id = OPC UA NodeId + result timestamp, which is the
// idempotency key: replays of the same result are rejected server-side.
func Build(
	ctx context.Context,
	b *lads.Browser,
	ins model.Instrument,
	prof profiles.Profile,
	resultNode *ua.NodeID,
) (model.Result, error) {
	measuredAt := time.Now().UTC()
	if ts, ok := b.StoppedTime(ctx, resultNode); ok {
		measuredAt = ts
	} else if dv, err := b.SourceTimestamp(ctx, resultNode); err == nil && dv != nil && !dv.SourceTimestamp.IsZero() {
		measuredAt = dv.SourceTimestamp.UTC()
	} else if ts := readTimestamp(ctx, b, resultNode); !ts.IsZero() {
		measuredAt = ts
	}

	r := model.Result{
		ExternalDeviceID: ins.ExternalDeviceID,
		ExternalResultID: fmt.Sprintf("%s@%s", resultNode.String(), measuredAt.Format(time.RFC3339Nano)),
		MeasuredAt:       measuredAt,
		Method:           firstNonEmpty(b.ReadStringPath(ctx, resultNode, prof.MethodPaths), b.ReadPropertyKey(ctx, resultNode, prof.PropertyPaths, prof.MethodKeys)),
		SampleCode:       firstNonEmpty(b.ReadStringPath(ctx, resultNode, prof.SampleCodePaths), b.ReadPropertyKey(ctx, resultNode, prof.PropertyPaths, prof.SampleCodeKeys)),
		Operator:         firstNonEmpty(b.ReadStringPath(ctx, resultNode, prof.OperatorPaths), b.ReadPropertyKey(ctx, resultNode, prof.PropertyPaths, prof.OperatorKeys)),
		Summary:          map[string]any{},
		LADS:             map[string]any{},
	}

	ys, unitY, okY := b.ReadFloatsPath(ctx, resultNode, prof.SeriesYPaths)
	if !okY {
		ys, unitY, okY = b.FirstNumericArrayBelow(ctx, resultNode, prof.SeriesContainerPaths)
	}
	if okY {
		xs, unitX, okX := b.ReadFloatsPath(ctx, resultNode, prof.SeriesXPaths)
		r.Points = zip(xs, ys, okX)
		r.UnitX = firstNonEmpty(unitX, ins.DefaultUnitX, prof.DefaultUnitX)
		r.UnitY = firstNonEmpty(unitY, ins.DefaultUnitY, prof.DefaultUnitY)
	}

	// Scalars and everything else the profile points at go into summary, and
	// the raw node values are preserved under "lads" for audit reproducibility.
	rawNodes := map[string]any{}
	for _, path := range prof.ScalarPaths {
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
	if r.Operator != "" {
		r.Summary["operator"] = r.Operator
	}
	if len(r.Points) > 0 {
		r.Summary["point_count"] = len(r.Points)
	}
	// The raw LADS metadata is also mirrored inside summary, as the ingest API
	// documents, so audit consumers find it in both places.
	r.Summary["lads"] = r.LADS

	return r, nil
}

// IsFinished reports whether a state value counts as finished for this profile.
func IsFinished(state string, prof profiles.Profile) bool {
	for _, s := range prof.FinishedStates {
		if equalFold(state, s) {
			return true
		}
	}
	return false
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
