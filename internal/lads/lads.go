// Package lads walks the LADS companion-specification information model:
//
//	Device -> DeviceSet -> FunctionalUnitSet -> FunctionalUnits ->
//	FunctionSet -> Functions -> (results, programs)
//
// Everything here is read-only: the connector never calls a Method and never
// writes a node.
package lads

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
)

// Well-known browse names of the companion specification.
const (
	BrowseDeviceSet         = "DeviceSet"
	BrowseFunctionalUnitSet = "FunctionalUnitSet"
	BrowseFunctionSet       = "FunctionSet"
	BrowseProgramManager    = "ProgramManager"
	BrowseResultSet         = "ResultSet"
	BrowseActiveProgram     = "ActiveProgram"
	BrowseCurrentState      = "CurrentState"
)

// Device is a discovered LADS device.
type Device struct {
	NodeID string `json:"node_id"`
	// NamespaceURI is the namespace the node id belongs to. It is stored with
	// the instrument so the node can be found again after the instrument
	// renumbers its namespaces (which OPC UA explicitly allows).
	NamespaceURI    string   `json:"namespace_uri,omitempty"`
	Name            string   `json:"name"`
	Manufacturer    string   `json:"manufacturer,omitempty"`
	Model           string   `json:"model,omitempty"`
	SerialNumber    string   `json:"serial_number,omitempty"`
	FunctionalUnits []string `json:"functional_units,omitempty"`
	ResultSets      []string `json:"result_sets,omitempty"`
}

// Browser reads the information model of one connected instrument.
type Browser struct {
	c *opcua.Client
}

// NewBrowser wraps a connected client.
func NewBrowser(c *opcua.Client) *Browser { return &Browser{c: c} }

// Devices discovers the devices below Objects/DeviceSet.
func (b *Browser) Devices(ctx context.Context) ([]Device, error) {
	objects := b.c.Node(ua.NewNumericNodeID(0, id.ObjectsFolder))
	deviceSet, err := b.childByName(ctx, objects, BrowseDeviceSet)
	if err != nil {
		return nil, fmt.Errorf("DeviceSet not found — is this a LADS server? %w", err)
	}

	children, err := deviceSet.Children(ctx, id.HierarchicalReferences, ua.NodeClassObject)
	if err != nil {
		return nil, err
	}

	var out []Device
	for _, child := range children {
		name, err := child.BrowseName(ctx)
		if err != nil {
			continue
		}

		// DeviceSet also holds non-device folders such as DeviceFeatures.
		// A LADS device always carries a FunctionalUnitSet, so use that as
		// the discriminator instead of trusting every child.
		fus, err := b.childByName(ctx, child, BrowseFunctionalUnitSet)
		if err != nil {
			continue
		}

		d := Device{NodeID: child.ID.String(), Name: name.Name}
		d.Manufacturer, _ = b.readStringChild(ctx, child, "Manufacturer")
		d.Model, _ = b.readStringChild(ctx, child, "Model")
		d.SerialNumber, _ = b.readStringChild(ctx, child, "SerialNumber")

		units, _ := fus.Children(ctx, id.HierarchicalReferences, ua.NodeClassObject)
		seenUnit := map[string]bool{}
		for _, u := range units {
			// Servers may expose the same unit through several references.
			if seenUnit[u.ID.String()] {
				continue
			}
			seenUnit[u.ID.String()] = true
			if n, err := u.BrowseName(ctx); err == nil {
				d.FunctionalUnits = append(d.FunctionalUnits, n.Name)
			}
			for _, rs := range b.resultSetsBelowUnit(ctx, u) {
				d.ResultSets = append(d.ResultSets, rs.String())
			}

		}
		out = append(out, d)
	}
	if len(out) == 0 {
		return nil, errors.New("DeviceSet contains no LADS devices")
	}
	return out, nil
}

// ResultSetRef is one ResultSet together with the functional unit it belongs
// to, so results of a multi-part device stay distinguishable.
type ResultSetRef struct {
	Node           *ua.NodeID
	FunctionalUnit string
}

// ResultSetNodes returns every ResultSet below the given device node, so
// the supervisor can subscribe to their result state variables.
func (b *Browser) ResultSetNodes(ctx context.Context, deviceNodeID string) ([]ResultSetRef, error) {
	nid, err := ua.ParseNodeID(deviceNodeID)
	if err != nil {
		return nil, err
	}
	device := b.c.Node(nid)

	var out []ResultSetRef
	seen := map[string]bool{}
	add := func(n *ua.NodeID, unit string) {
		if n == nil || seen[n.String()] {
			return
		}
		seen[n.String()] = true
		out = append(out, ResultSetRef{Node: n, FunctionalUnit: unit})
	}

	fus, err := b.childByName(ctx, device, BrowseFunctionalUnitSet)
	if err != nil {
		return nil, err
	}
	units, err := fus.Children(ctx, id.HierarchicalReferences, ua.NodeClassObject)
	if err != nil {
		return nil, err
	}
	for _, u := range units {
		unitName := ""
		if n, err := u.BrowseName(ctx); err == nil {
			unitName = n.Name
		}
		for _, rs := range b.resultSetsBelowUnit(ctx, u) {
			add(rs, unitName)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no ResultSet found below device")
	}
	return out, nil
}

// NamespaceURI returns the namespace URI a node id belongs to, read from the
// server's NamespaceArray. Empty when it cannot be resolved.
func (b *Browser) NamespaceURI(ctx context.Context, nodeID string) string {
	nid, err := ua.ParseNodeID(nodeID)
	if err != nil {
		return ""
	}
	uris, err := b.namespaceArray(ctx)
	if err != nil || int(nid.Namespace()) >= len(uris) {
		return ""
	}
	return uris[nid.Namespace()]
}

// ResolveNodeID re-points a saved node id at the namespace it was saved from.
//
// Namespace indices are assigned per server session and are NOT guaranteed to
// survive an instrument restart. Without this, a restart that reorders the
// namespace array silently points the connector at a different node.
func (b *Browser) ResolveNodeID(ctx context.Context, nodeID, namespaceURI string) (string, error) {
	nid, err := ua.ParseNodeID(nodeID)
	if err != nil {
		return "", err
	}
	if namespaceURI == "" {
		return nodeID, nil
	}
	uris, err := b.namespaceArray(ctx)
	if err != nil {
		return nodeID, nil
	}
	want := -1
	for i, u := range uris {
		if u == namespaceURI {
			want = i
			break
		}
	}
	if want < 0 {
		return "", fmt.Errorf("instrument no longer exposes namespace %q — re-detect the instrument in the setup screen", namespaceURI)
	}
	if uint16(want) == nid.Namespace() {
		return nodeID, nil
	}
	moved := ua.NewStringNodeID(uint16(want), nid.StringID())
	switch nid.Type() {
	case ua.NodeIDTypeNumeric, ua.NodeIDTypeTwoByte, ua.NodeIDTypeFourByte:
		moved = ua.NewNumericNodeID(uint16(want), nid.IntID())
	case ua.NodeIDTypeGUID:
		moved = ua.NewStringNodeID(uint16(want), nid.StringID())
	case ua.NodeIDTypeByteString:
		moved = ua.NewByteStringNodeID(uint16(want), nid.ByteString())
	}
	return moved.String(), nil
}

func (b *Browser) namespaceArray(ctx context.Context) ([]string, error) {
	v, err := b.c.Node(ua.NewNumericNodeID(0, id.Server_NamespaceArray)).Value(ctx)
	if err != nil || v == nil {
		return nil, errors.New("namespace array unavailable")
	}
	switch val := v.Value().(type) {
	case []string:
		return val, nil
	case string:
		return []string{val}, nil
	default:
		return nil, errors.New("unexpected namespace array type")
	}
}

// resultSetsBelowUnit collects the ResultSet nodes of one functional unit.
// The companion specification places the ResultSet under the unit's
// ProgramManager; some servers put it directly on the unit or on individual
// functions, so all three layouts are accepted.
func (b *Browser) resultSetsBelowUnit(ctx context.Context, unit *opcua.Node) []*ua.NodeID {
	var out []*ua.NodeID
	seen := map[string]bool{}
	add := func(n *ua.NodeID) {
		if n == nil || seen[n.String()] {
			return
		}
		seen[n.String()] = true
		out = append(out, n)
	}

	if pm, err := b.childByName(ctx, unit, BrowseProgramManager); err == nil {
		if rs, err := b.childByName(ctx, pm, BrowseResultSet); err == nil {
			add(rs.ID)
		}
	}
	if rs, err := b.childByName(ctx, unit, BrowseResultSet); err == nil {
		add(rs.ID)
	}
	if fs, err := b.childByName(ctx, unit, BrowseFunctionSet); err == nil {
		fns, _ := fs.Children(ctx, id.HierarchicalReferences, ua.NodeClassObject)
		for _, fn := range fns {
			if rs, err := b.childByName(ctx, fn, BrowseResultSet); err == nil {
				add(rs.ID)
			}
		}
	}
	return out
}

// StoppedTime returns the result's Stopped timestamp. LADS results carry
// Started/Stopped properties; a non-zero Stopped means the run has finished,
// which is the only completion signal some servers expose (the companion
// specification makes the result state machine optional).
func (b *Browser) StoppedTime(ctx context.Context, result *ua.NodeID) (time.Time, bool) {
	for _, p := range []string{"Stopped", "Properties/Stopped", "EndTime"} {
		v, err := b.ReadPath(ctx, result, p)
		if err != nil || v == nil {
			continue
		}
		if ts, ok := v.Value().(time.Time); ok && !ts.IsZero() && ts.Year() > 1601 {
			return ts.UTC(), true
		}
	}
	return time.Time{}, false
}

// ChangeWatchNodes returns the variables worth subscribing to for a ResultSet:
// its NodeVersion (bumped whenever a result is added) and the Stopped/state
// variable of the most recent results.
//
// limit caps how many result variables are watched. Instruments keep their own
// monitored-item budget, and an archive with thousands of stored results would
// otherwise exhaust it and make the subscription fail as a whole. Newer results
// are kept, since those are the ones that can still change.
func (b *Browser) ChangeWatchNodes(ctx context.Context, resultSet *ua.NodeID, limit int) []*ua.NodeID {
	var out []*ua.NodeID
	if n, err := b.resolvePath(ctx, b.c.Node(resultSet), "NodeVersion"); err == nil {
		out = append(out, n.ID)
	}
	results, err := b.Results(ctx, resultSet)
	if err != nil {
		return out
	}
	if limit > 0 && len(results) > limit {
		results = results[len(results)-limit:]
	}
	for _, res := range results {
		if n, err := b.StateVariable(ctx, res); err == nil {
			out = append(out, n)
			continue
		}
		if n, err := b.resolvePath(ctx, b.c.Node(res), "Stopped"); err == nil {
			out = append(out, n.ID)
		}
	}
	return out
}

// FirstNumericArrayBelow searches the children of the given container paths for
// the first variable holding a numeric array, so results whose variable names
// are vendor specific still produce a series.
func (b *Browser) FirstNumericArrayBelow(ctx context.Context, base *ua.NodeID, containers []string) ([]float64, string, bool) {
	for _, c := range containers {
		node, err := b.resolvePath(ctx, b.c.Node(base), c)
		if err != nil {
			continue
		}
		kids, err := node.Children(ctx, id.HierarchicalReferences, ua.NodeClassVariable)
		if err != nil {
			continue
		}
		for _, k := range kids {
			name, err := k.BrowseName(ctx)
			if err != nil || name.Name == "NodeVersion" {
				continue
			}
			v, err := k.Value(ctx)
			if err != nil || v == nil {
				continue
			}
			nums, ok := VariantToFloats(v)
			if !ok || len(nums) < 2 {
				continue
			}
			unit, _ := b.ReadEngineeringUnit(ctx, base, c+"/"+name.Name)
			return nums, unit, true
		}
	}
	return nil, "", false
}

// ReadPropertyKey reads a KeyValuePair array (LADS Properties) and returns the
// value of the first matching key.
func (b *Browser) ReadPropertyKey(ctx context.Context, base *ua.NodeID, paths, keys []string) string {
	for _, p := range paths {
		v, err := b.ReadPath(ctx, base, p)
		if err != nil || v == nil {
			continue
		}
		for _, kv := range keyValues(v) {
			for _, want := range keys {
				if kv.Key != nil && strings.EqualFold(kv.Key.Name, want) {
					if s := VariantToString(kv.Value); s != "" {
						return s
					}
				}
			}
		}
	}
	return ""
}

func keyValues(v *ua.Variant) []*ua.KeyValuePair {
	var out []*ua.KeyValuePair
	switch val := v.Value().(type) {
	case []*ua.KeyValuePair:
		out = val
	case *ua.KeyValuePair:
		out = []*ua.KeyValuePair{val}
	case []*ua.ExtensionObject:
		for _, eo := range val {
			if eo == nil {
				continue
			}
			if kv, ok := eo.Value.(*ua.KeyValuePair); ok {
				out = append(out, kv)
			}
		}
	case *ua.ExtensionObject:
		if kv, ok := val.Value.(*ua.KeyValuePair); ok {
			out = append(out, kv)
		}
	}
	return out
}

// Results lists the result objects currently held in a ResultSet.
func (b *Browser) Results(ctx context.Context, resultSet *ua.NodeID) ([]*ua.NodeID, error) {
	children, err := b.c.Node(resultSet).Children(ctx, id.HierarchicalReferences, ua.NodeClassObject)
	if err != nil {
		return nil, err
	}
	out := make([]*ua.NodeID, 0, len(children))
	for _, c := range children {
		out = append(out, c.ID)
	}
	return out, nil
}

// StateVariable returns the node carrying the result's state, used as the
// MonitoredItem the connector subscribes to (no polling).
func (b *Browser) StateVariable(ctx context.Context, result *ua.NodeID) (*ua.NodeID, error) {
	node := b.c.Node(result)
	for _, path := range []string{"CurrentState", "ResultState/CurrentState", "State/CurrentState"} {
		if n, err := b.resolvePath(ctx, node, path); err == nil {
			return n.ID, nil
		}
	}
	return nil, errors.New("result has no state variable")
}

// ResultState reads the result's state as text and, where the instrument
// exposes it, as the state machine's numeric state.
//
// Some servers leave CurrentState empty and only fill CurrentState/Name or
// CurrentState/Number, so all three are tried before giving up.
func (b *Browser) ResultState(ctx context.Context, result *ua.NodeID) (text string, number int, ok bool) {
	for _, base := range []string{"CurrentState", "ResultState/CurrentState", "State/CurrentState"} {
		if v, err := b.ReadPath(ctx, result, base); err == nil && v != nil {
			if s := strings.TrimSpace(VariantToString(v)); s != "" {
				text, ok = s, true
			}
		}
		if text == "" {
			if v, err := b.ReadPath(ctx, result, base+"/Name"); err == nil && v != nil {
				if s := strings.TrimSpace(VariantToString(v)); s != "" {
					text, ok = s, true
				}
			}
		}
		if v, err := b.ReadPath(ctx, result, base+"/Number"); err == nil && v != nil {
			if nums, good := VariantToFloats(v); good && len(nums) == 1 {
				number, ok = int(nums[0]), true
			}
		}
		if ok {
			return text, number, true
		}
	}
	return "", 0, false
}

// ReadPath resolves a browse-name chain such as "Properties/SampleId" relative
// to base and reads its value.
func (b *Browser) ReadPath(ctx context.Context, base *ua.NodeID, path string) (*ua.Variant, error) {
	node, err := b.resolvePath(ctx, b.c.Node(base), path)
	if err != nil {
		return nil, err
	}
	v, err := node.Value(ctx)
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, errors.New("empty value")
	}
	return v, nil
}

// ReadStringPath is ReadPath rendered as a trimmed string ("" when absent).
func (b *Browser) ReadStringPath(ctx context.Context, base *ua.NodeID, paths []string) string {
	for _, p := range paths {
		v, err := b.ReadPath(ctx, base, p)
		if err != nil || v == nil {
			continue
		}
		if s := VariantToString(v); s != "" {
			return s
		}
	}
	return ""
}

// ReadFloatsPath returns the first path that yields a numeric array (or scalar).
func (b *Browser) ReadFloatsPath(ctx context.Context, base *ua.NodeID, paths []string) ([]float64, string, bool) {
	for _, p := range paths {
		v, err := b.ReadPath(ctx, base, p)
		if err != nil || v == nil {
			continue
		}
		if nums, ok := VariantToFloats(v); ok {
			unit, _ := b.ReadEngineeringUnit(ctx, base, p)
			return nums, unit, true
		}
	}
	return nil, "", false
}

// ReadEngineeringUnit reads the EUInformation of a value node and returns its
// display name (the unit symbol used by LabNote).
func (b *Browser) ReadEngineeringUnit(ctx context.Context, base *ua.NodeID, path string) (string, error) {
	v, err := b.ReadPath(ctx, base, path+"/EngineeringUnits")
	if err != nil {
		// LADS often puts the unit next to the variable rather than below it.
		v, err = b.ReadPath(ctx, base, parentPath(path)+"/EngineeringUnits")
		if err != nil {
			return "", err
		}
	}
	return EUSymbol(v), nil
}

// SourceTimestamp reads a node and returns its source timestamp.
func (b *Browser) SourceTimestamp(ctx context.Context, node *ua.NodeID) (dv *ua.DataValue, err error) {
	req := &ua.ReadRequest{
		TimestampsToReturn: ua.TimestampsToReturnSource,
		NodesToRead: []*ua.ReadValueID{
			{NodeID: node, AttributeID: ua.AttributeIDValue},
		},
	}
	res, err := b.c.Read(ctx, req)
	if err != nil {
		return nil, err
	}
	if len(res.Results) == 0 {
		return nil, errors.New("empty read response")
	}
	return res.Results[0], nil
}

func (b *Browser) resolvePath(ctx context.Context, node *opcua.Node, path string) (*opcua.Node, error) {
	cur := node
	for _, seg := range strings.Split(path, "/") {
		if seg == "" {
			continue
		}
		// Numeric segment: index into the ordered children (e.g. "Results/0").
		if idx, err := strconv.Atoi(seg); err == nil {
			children, err := cur.Children(ctx, id.HierarchicalReferences, ua.NodeClassObject|ua.NodeClassVariable)
			if err != nil {
				return nil, err
			}
			if idx < 0 || idx >= len(children) {
				return nil, fmt.Errorf("index %d out of range", idx)
			}
			cur = children[idx]
			continue
		}
		next, err := b.childByName(ctx, cur, seg)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		cur = next
	}
	return cur, nil
}

func (b *Browser) childByName(ctx context.Context, node *opcua.Node, name string) (*opcua.Node, error) {
	children, err := node.Children(ctx, id.HierarchicalReferences, ua.NodeClassObject|ua.NodeClassVariable)
	if err != nil {
		return nil, err
	}
	for _, c := range children {
		bn, err := c.BrowseName(ctx)
		if err != nil {
			continue
		}
		if strings.EqualFold(bn.Name, name) {
			return c, nil
		}
	}
	return nil, fmt.Errorf("child %q not found", name)
}

func (b *Browser) readStringChild(ctx context.Context, node *opcua.Node, name string) (string, error) {
	child, err := b.childByName(ctx, node, name)
	if err != nil {
		return "", err
	}
	v, err := child.Value(ctx)
	if err != nil || v == nil {
		return "", err
	}
	return VariantToString(v), nil
}

func parentPath(path string) string {
	i := strings.LastIndex(path, "/")
	if i < 0 {
		return ""
	}
	return path[:i]
}
