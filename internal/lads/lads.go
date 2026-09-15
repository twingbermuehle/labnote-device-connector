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
	NodeID          string   `json:"node_id"`
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
		d := Device{NodeID: child.ID.String(), Name: name.Name}
		d.Manufacturer, _ = b.readStringChild(ctx, child, "Manufacturer")
		d.Model, _ = b.readStringChild(ctx, child, "Model")
		d.SerialNumber, _ = b.readStringChild(ctx, child, "SerialNumber")

		if fus, err := b.childByName(ctx, child, BrowseFunctionalUnitSet); err == nil {
			units, _ := fus.Children(ctx, id.HierarchicalReferences, ua.NodeClassObject)
			for _, u := range units {
				if n, err := u.BrowseName(ctx); err == nil {
					d.FunctionalUnits = append(d.FunctionalUnits, n.Name)
				}
				if rs, err := b.childByName(ctx, u, BrowseResultSet); err == nil {
					d.ResultSets = append(d.ResultSets, rs.ID.String())
				}
			}
		}
		out = append(out, d)
	}
	if len(out) == 0 {
		return nil, errors.New("DeviceSet contains no devices")
	}
	return out, nil
}

// ResultSetNodes returns every ResultSet node below the given device node, so
// the supervisor can subscribe to their result state variables.
func (b *Browser) ResultSetNodes(ctx context.Context, deviceNodeID string) ([]*ua.NodeID, error) {
	nid, err := ua.ParseNodeID(deviceNodeID)
	if err != nil {
		return nil, err
	}
	device := b.c.Node(nid)

	var out []*ua.NodeID
	fus, err := b.childByName(ctx, device, BrowseFunctionalUnitSet)
	if err != nil {
		return nil, err
	}
	units, err := fus.Children(ctx, id.HierarchicalReferences, ua.NodeClassObject)
	if err != nil {
		return nil, err
	}
	for _, u := range units {
		if rs, err := b.childByName(ctx, u, BrowseResultSet); err == nil {
			out = append(out, rs.ID)
		}
		// Some servers hang the ResultSet off the individual functions.
		if fs, err := b.childByName(ctx, u, BrowseFunctionSet); err == nil {
			fns, _ := fs.Children(ctx, id.HierarchicalReferences, ua.NodeClassObject)
			for _, fn := range fns {
				if rs, err := b.childByName(ctx, fn, BrowseResultSet); err == nil {
					out = append(out, rs.ID)
				}
			}
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no ResultSet found below device")
	}
	return out, nil
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
