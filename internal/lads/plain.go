package lads

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
)

// This file supports instruments that are plain OPC UA servers rather than
// LADS servers: they publish their measured quantities as ordinary variables
// (a balance publishing CurrentWeight / RegisteredWeight, for example) and have
// neither a FunctionalUnitSet nor a ResultSet. The connector watches one of
// those variables and turns every new value into a measurement.

// Folders below Objects that never hold an instrument's live values.
var infrastructureFolders = map[string]bool{
	"server": true, "aliases": true, "devicetopology": true,
	"locations": true, "machines": true, "networkset": true,
	"packmlobjects": true, "devicefeatures": true, "types": true,
	"views": true, "serverconfiguration": true,
}

// Variable names that are identity or bookkeeping, not measurements.
var notAValue = map[string]bool{
	"deviceclass": true, "deviceid": true, "devicemanual": true,
	"devicerevision": true, "hardwarerevision": true, "softwarerevision": true,
	"manufacturer": true, "manufactureruri": true, "model": true,
	"modelname": true, "serialnumber": true, "productcode": true,
	"producturi": true, "revisioncounter": true, "nodeversion": true,
	"enabledstate": true,
}

// PlainDevices lists candidate device objects on a server that exposes no LADS
// model: every object below Objects (and below DeviceSet) that carries at least
// one readable numeric variable.
func (b *Browser) PlainDevices(ctx context.Context) ([]Device, error) {
	objects := b.c.Node(ua.NewNumericNodeID(0, id.ObjectsFolder))
	candidates, err := objects.Children(ctx, id.HierarchicalReferences, ua.NodeClassObject)
	if err != nil {
		return nil, err
	}
	// Devices are often nested one level below DeviceSet.
	if ds, err := b.childByName(ctx, objects, BrowseDeviceSet); err == nil {
		if kids, err := ds.Children(ctx, id.HierarchicalReferences, ua.NodeClassObject); err == nil {
			candidates = append(candidates, kids...)
		}
	}

	var out []Device
	seen := map[string]bool{}
	for _, c := range candidates {
		if seen[c.ID.String()] {
			continue
		}
		seen[c.ID.String()] = true
		bn, err := c.BrowseName(ctx)
		if err != nil || bn == nil {
			continue
		}
		if infrastructureFolders[strings.ToLower(bn.Name)] {
			continue
		}
		vars := b.Variables(ctx, c.ID.String())
		if len(vars) == 0 {
			continue
		}
		d := Device{NodeID: c.ID.String(), Name: bn.Name}
		d.NamespaceURI = b.NamespaceURI(ctx, d.NodeID)
		d.Manufacturer, _ = b.readStringChild(ctx, c, "Manufacturer")
		d.Model, _ = b.readStringChild(ctx, c, "Model")
		if d.Model == "" {
			d.Model, _ = b.readStringChild(ctx, c, "ModelName")
		}
		d.SerialNumber, _ = b.readStringChild(ctx, c, "SerialNumber")
		if d.SerialNumber == "" {
			d.SerialNumber, _ = b.readStringChild(ctx, c, "DeviceID")
		}
		out = append(out, d)
	}
	if len(out) == 0 {
		return nil, errors.New("this instrument publishes no readable values below Objects")
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Variables lists the readable numeric variables of a plain device object,
// including those one object level below it. Paths are browse-name chains
// relative to the device node, the same form the mapping profiles use.
func (b *Browser) Variables(ctx context.Context, deviceNodeID string) []Parameter {
	nid, err := ua.ParseNodeID(deviceNodeID)
	if err != nil {
		return nil
	}
	root := b.c.Node(nid)
	out := []Parameter{}
	seen := map[string]bool{}

	collect := func(node *opcua.Node, prefix string) {
		kids, err := node.Children(ctx, id.HierarchicalReferences, ua.NodeClassVariable)
		if err != nil {
			return
		}
		for _, k := range kids {
			bn, err := k.BrowseName(ctx)
			if err != nil || bn == nil {
				continue
			}
			name := bn.Name
			if notAValue[strings.ToLower(name)] {
				continue
			}
			path := name
			if prefix != "" {
				path = prefix + "/" + name
			}
			if seen[path] {
				continue
			}
			v, err := k.Value(ctx)
			if err != nil || v == nil {
				continue
			}
			nums, ok := VariantToFloats(v)
			if !ok || len(nums) == 0 {
				continue
			}
			seen[path] = true
			kind := "value"
			if len(nums) > 1 {
				kind = "series"
			}
			unit, _ := b.ReadEngineeringUnit(ctx, nid, path)
			out = append(out, Parameter{
				Name: name, Path: path, Unit: unit, Kind: kind,
				Recommended: kind == "value",
			})
		}
	}

	collect(root, "")
	if objs, err := root.Children(ctx, id.HierarchicalReferences, ua.NodeClassObject); err == nil {
		for _, o := range objs {
			bn, err := o.BrowseName(ctx)
			if err != nil || bn == nil || infrastructureFolders[strings.ToLower(bn.Name)] {
				continue
			}
			collect(o, bn.Name)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// NodeForPath resolves a browse-name chain relative to base into a node id, so
// the supervisor can subscribe to a configured value variable.
func (b *Browser) NodeForPath(ctx context.Context, base *ua.NodeID, path string) (*ua.NodeID, error) {
	node, err := b.resolvePath(ctx, b.c.Node(base), path)
	if err != nil {
		return nil, err
	}
	return node.ID, nil
}

// Reading is one value read from a plain variable together with the timestamp
// the instrument itself stamped it with.
type Reading struct {
	Value     float64
	Values    []float64
	Unit      string
	Timestamp ua.Time
}

// ua.Time is not a real type in gopcua; the concrete timestamp handling lives
// in ReadValue below, which returns the source timestamp as time.Time.

// ReadValue reads one variable relative to the device node and returns its
// numeric value(s), unit and source timestamp.
func (b *Browser) ReadValue(ctx context.Context, base *ua.NodeID, path string) (nums []float64, unit string, err error) {
	node, err := b.NodeForPath(ctx, base, path)
	if err != nil {
		return nil, "", err
	}
	dv, err := b.SourceTimestamp(ctx, node)
	if err != nil {
		return nil, "", err
	}
	if dv.Value == nil {
		return nil, "", fmt.Errorf("%s: empty value", path)
	}
	nums, ok := VariantToFloats(dv.Value)
	if !ok || len(nums) == 0 {
		return nil, "", fmt.Errorf("%s: not a numeric value", path)
	}
	unit, _ = b.ReadEngineeringUnit(ctx, base, path)
	return nums, unit, nil
}
