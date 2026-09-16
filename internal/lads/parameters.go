package lads

import (
	"context"
	"sort"
	"strings"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
)

// Parameter is one measurable quantity an instrument reports.
//
// Kind tells the setup UI what it is:
//
//	series   - a curve (numeric array) inside a result, e.g. a spectrum
//	value    - a single number inside a result, e.g. a concentration
//	expected - a function the instrument offers that has not produced a
//	           result yet, so the exact node path is not known before the
//	           first run
//
// Path is relative to the result node, which is exactly what the mapping
// layer needs; it is empty for Kind "expected".
type Parameter struct {
	Name        string `json:"name" yaml:"name"`
	Path        string `json:"path" yaml:"path"`
	Unit        string `json:"unit,omitempty" yaml:"unit,omitempty"`
	Kind        string `json:"kind" yaml:"kind"`
	Recommended bool   `json:"recommended" yaml:"recommended"`
}

// Containers below a result that hold measured variables in practice.
var resultContainers = []string{"", "VariableSet", "Variables", "Data", "Results"}

// Names that are bookkeeping rather than measurements.
var notAMeasurement = map[string]bool{
	"nodeversion": true, "started": true, "stopped": true,
	"currentstate": true, "lastcalibrated": true, "totalruntime": true,
}

// Parameters lists what this device measures, by inspecting the results it has
// already produced and the functions it advertises. Read-only.
func (b *Browser) Parameters(ctx context.Context, deviceNodeID string) ([]Parameter, error) {
	out := []Parameter{}
	seen := map[string]bool{}

	resultSets, err := b.ResultSetNodes(ctx, deviceNodeID)
	if err == nil {
		for _, rs := range resultSets {
			results, err := b.Results(ctx, rs)
			if err != nil {
				continue
			}
			for _, res := range results {
				b.collectResultParams(ctx, res, &out, seen)
			}
		}
	}

	for _, name := range b.functionNames(ctx, deviceNodeID) {
		key := "expected:" + strings.ToLower(name)
		if seen[key] || seen["name:"+strings.ToLower(name)] {
			continue
		}
		seen[key] = true
		out = append(out, Parameter{Name: name, Kind: "expected"})
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Recommended != out[j].Recommended {
			return out[i].Recommended
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// collectResultParams walks the variables of one result object.
func (b *Browser) collectResultParams(ctx context.Context, result *ua.NodeID, out *[]Parameter, seen map[string]bool) {
	for _, container := range resultContainers {
		node := b.c.Node(result)
		if container != "" {
			n, err := b.resolvePath(ctx, node, container)
			if err != nil {
				continue
			}
			node = n
		}
		kids, err := node.Children(ctx, id.HierarchicalReferences, ua.NodeClassVariable)
		if err != nil {
			continue
		}
		for _, k := range kids {
			p, ok := b.describe(ctx, result, container, k)
			if !ok {
				continue
			}
			if seen[p.Path] {
				continue
			}
			seen[p.Path] = true
			seen["name:"+strings.ToLower(p.Name)] = true
			*out = append(*out, p)
		}
	}
}

// describe turns one variable node into a Parameter, skipping anything that is
// not a measurement.
func (b *Browser) describe(ctx context.Context, result *ua.NodeID, container string, node *opcua.Node) (Parameter, bool) {
	bn, err := node.BrowseName(ctx)
	if err != nil || bn == nil {
		return Parameter{}, false
	}
	name := bn.Name
	if notAMeasurement[strings.ToLower(name)] {
		return Parameter{}, false
	}
	v, err := node.Value(ctx)
	if err != nil || v == nil {
		return Parameter{}, false
	}
	nums, ok := VariantToFloats(v)
	if !ok || len(nums) == 0 {
		return Parameter{}, false
	}

	path := name
	if container != "" {
		path = container + "/" + name
	}
	kind := "value"
	if len(nums) > 1 {
		kind = "series"
	}
	unit, _ := b.ReadEngineeringUnit(ctx, result, path)
	// A real measurement carries a value; both curves and single numbers are
	// sensible defaults, which is why they are pre-ticked.
	return Parameter{Name: name, Path: path, Unit: unit, Kind: kind, Recommended: true}, true
}

// functionNames lists the FunctionSet functions of every functional unit, so
// the UI can already show what the instrument can measure before its first run.
func (b *Browser) functionNames(ctx context.Context, deviceNodeID string) []string {
	nid, err := ua.ParseNodeID(deviceNodeID)
	if err != nil {
		return nil
	}
	fus, err := b.childByName(ctx, b.c.Node(nid), BrowseFunctionalUnitSet)
	if err != nil {
		return nil
	}
	units, err := fus.Children(ctx, id.HierarchicalReferences, ua.NodeClassObject)
	if err != nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, u := range units {
		fs, err := b.childByName(ctx, u, BrowseFunctionSet)
		if err != nil {
			continue
		}
		fns, err := fs.Children(ctx, id.HierarchicalReferences, ua.NodeClassObject)
		if err != nil {
			continue
		}
		for _, fn := range fns {
			n, err := fn.BrowseName(ctx)
			if err != nil || seen[n.Name] {
				continue
			}
			seen[n.Name] = true
			out = append(out, n.Name)
		}
	}
	return out
}
