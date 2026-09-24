package lads

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
)

// Some instruments (the Sartorius Cubis OPC UA balance, for example) publish a
// weight as a vendor structure: one value that bundles gross, net, tare and
// the unit. The OPC UA library only decodes structures it knows, so the
// connector registers a raw-capture type for each such structure and decodes
// it with the structure description the instrument itself publishes
// (the DataTypeDefinition attribute).

// RawStruct keeps the undecoded body of a vendor structure.
type RawStruct struct{ Body []byte }

// Decode implements ua.BinaryDecoder: take the whole body.
func (r *RawStruct) Decode(b []byte) (int, error) {
	r.Body = append([]byte(nil), b...)
	return len(b), nil
}

var registerMu sync.Mutex

func registerRaw(encodingID *ua.NodeID) {
	registerMu.Lock()
	defer registerMu.Unlock()
	defer func() { _ = recover() }()
	ua.RegisterExtensionObject(encodingID, new(RawStruct))
}

// structInfo is what the connector knows about one structured data type.
type structInfo struct {
	def *ua.StructureDefinition
}

// StructValue is a decoded vendor structure: its numeric fields (nested names
// joined with "/") and a unit when the structure carries one.
type StructValue struct {
	Numbers map[string]float64
	Order   []string
	Unit    string
}

// Primary picks the measured value of a structure: net weight first, then
// gross, then anything named like a weight or value, then the first number.
func (s StructValue) Primary() (string, float64, bool) {
	for _, want := range []string{"net", "gross", "weight", "value"} {
		for _, k := range s.Order {
			last := strings.ToLower(k[strings.LastIndex(k, "/")+1:])
			if strings.Contains(last, want) && !strings.Contains(last, "tare") {
				return k, s.Numbers[k], true
			}
		}
	}
	if len(s.Order) > 0 {
		return s.Order[0], s.Numbers[s.Order[0]], true
	}
	return "", 0, false
}

// PrepareStruct makes sure values of the given variable can be decoded: when
// its data type is a vendor structure, the raw-capture type is registered for
// the structure's binary encoding. It returns true when the variable is
// structured.
func (b *Browser) PrepareStruct(ctx context.Context, variable *ua.NodeID) bool {
	v, err := b.c.Node(variable).Attribute(ctx, ua.AttributeIDDataType)
	if err != nil || v == nil {
		return false
	}
	dt, ok := v.Value().(*ua.NodeID)
	if !ok || dt == nil || dt.Namespace() == 0 {
		return false
	}
	info, err := b.structDef(ctx, dt)
	if err != nil || info == nil {
		return false
	}
	return true
}

func (b *Browser) structDef(ctx context.Context, dt *ua.NodeID) (*structInfo, error) {
	key := dt.String()
	b.structMu.Lock()
	if info, ok := b.structs[key]; ok {
		b.structMu.Unlock()
		return info, nil
	}
	b.structMu.Unlock()

	node := b.c.Node(dt)
	v, err := node.Attribute(ctx, ua.AttributeIDDataTypeDefinition)
	if err != nil || v == nil {
		return nil, fmt.Errorf("data type %s publishes no structure definition", key)
	}
	var def *ua.StructureDefinition
	switch x := v.Value().(type) {
	case *ua.ExtensionObject:
		if x != nil {
			def, _ = x.Value.(*ua.StructureDefinition)
		}
	case *ua.StructureDefinition:
		def = x
	}
	if def == nil {
		return nil, fmt.Errorf("data type %s is not a structure", key)
	}
	// Register every binary encoding of the type.
	if def.DefaultEncodingID != nil && !isZeroNode(def.DefaultEncodingID) {
		registerRaw(def.DefaultEncodingID)
	}
	if refs, err := node.References(ctx, id.HasEncoding, ua.BrowseDirectionForward, ua.NodeClassAll, true); err == nil {
		for _, r := range refs {
			if r.BrowseName != nil && r.BrowseName.Name == "Default Binary" && r.NodeID != nil {
				registerRaw(r.NodeID.NodeID)
			}
		}
	}
	info := &structInfo{def: def}
	b.structMu.Lock()
	b.structs[key] = info
	b.structMu.Unlock()
	return info, nil
}

func isZeroNode(n *ua.NodeID) bool { return n.Namespace() == 0 && n.IntID() == 0 }

// DecodeStruct turns a captured vendor structure into its numeric fields,
// using the definition of the variable's data type.
func (b *Browser) DecodeStruct(ctx context.Context, variable *ua.NodeID, raw *RawStruct) (StructValue, error) {
	v, err := b.c.Node(variable).Attribute(ctx, ua.AttributeIDDataType)
	if err != nil || v == nil {
		return StructValue{}, errors.New("unknown data type")
	}
	dt, _ := v.Value().(*ua.NodeID)
	if dt == nil {
		return StructValue{}, errors.New("unknown data type")
	}
	out := StructValue{Numbers: map[string]float64{}}
	buf := ua.NewBuffer(raw.Body)
	if err := b.decodeInto(ctx, buf, dt, "", &out, 0); err != nil {
		return out, err
	}
	if len(out.Order) == 0 {
		return out, errors.New("structure holds no numbers")
	}
	return out, nil
}

func (b *Browser) decodeInto(ctx context.Context, buf *ua.Buffer, dt *ua.NodeID, prefix string, out *StructValue, depth int) error {
	if depth > 4 {
		return errors.New("structure nested too deeply")
	}
	info, err := b.structDef(ctx, dt)
	if err != nil {
		return err
	}
	var mask uint32
	optional := info.def.StructureType == ua.StructureTypeStructureWithOptionalFields
	if optional {
		mask = buf.ReadUint32()
	}
	if info.def.StructureType == ua.StructureTypeUnion {
		sel := buf.ReadUint32()
		if sel == 0 || int(sel) > len(info.def.Fields) {
			return buf.Error()
		}
		f := info.def.Fields[sel-1]
		return b.decodeField(ctx, buf, f, join(prefix, f.Name), out, depth)
	}
	bit := 0
	for _, f := range info.def.Fields {
		if optional && f.IsOptional {
			present := mask&(1<<bit) != 0
			bit++
			if !present {
				continue
			}
		}
		if err := b.decodeField(ctx, buf, f, join(prefix, f.Name), out, depth); err != nil {
			return err
		}
	}
	return buf.Error()
}

func join(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "/" + name
}

func (b *Browser) decodeField(ctx context.Context, buf *ua.Buffer, f *ua.StructureField, path string, out *StructValue, depth int) error {
	if f.DataType == nil {
		return errors.New("field without data type")
	}
	count := 1
	if f.ValueRank >= 1 {
		n := int32(buf.ReadUint32())
		if n < 0 {
			n = 0
		}
		count = int(n)
	}
	for i := 0; i < count; i++ {
		p := path
		if f.ValueRank >= 1 {
			p = fmt.Sprintf("%s[%d]", path, i)
		}
		if err := b.decodeScalar(ctx, buf, f.DataType, p, out, depth); err != nil {
			return err
		}
	}
	return buf.Error()
}

func (out *StructValue) add(path string, v float64) {
	if _, ok := out.Numbers[path]; !ok {
		out.Order = append(out.Order, path)
	}
	out.Numbers[path] = v
}

func (b *Browser) decodeScalar(ctx context.Context, buf *ua.Buffer, dt *ua.NodeID, path string, out *StructValue, depth int) error {
	if dt.Namespace() == 0 {
		switch dt.IntID() {
		case id.Boolean:
			buf.ReadBool()
		case id.SByte:
			out.add(path, float64(buf.ReadInt8()))
		case id.Byte:
			out.add(path, float64(buf.ReadByte()))
		case id.Int16:
			out.add(path, float64(buf.ReadInt16()))
		case id.UInt16:
			out.add(path, float64(buf.ReadUint16()))
		case id.Int32, id.Enumeration:
			out.add(path, float64(buf.ReadInt32()))
		case id.UInt32:
			out.add(path, float64(buf.ReadUint32()))
		case id.Int64:
			out.add(path, float64(buf.ReadInt64()))
		case id.UInt64:
			out.add(path, float64(buf.ReadUint64()))
		case id.Float:
			out.add(path, float64(buf.ReadFloat32()))
		case id.Double, id.Duration:
			out.add(path, buf.ReadFloat64())
		case id.DateTime, id.UtcTime:
			buf.ReadTime()
		case id.String, id.LocaleID, id.NumericRange:
			buf.ReadString()
		case id.ByteString, id.XMLElement:
			buf.ReadBytes()
		case id.GUID:
			buf.ReadN(16)
		case id.StatusCode:
			buf.ReadUint32()
		case id.LocalizedText:
			lt := new(ua.LocalizedText)
			buf.ReadStruct(lt)
		case id.QualifiedName:
			qn := new(ua.QualifiedName)
			buf.ReadStruct(qn)
		case id.NodeID:
			n := new(ua.NodeID)
			buf.ReadStruct(n)
		case id.EUInformation:
			eu := new(ua.EUInformation)
			buf.ReadStruct(eu)
			if out.Unit == "" {
				out.Unit = EUSymbolFrom(eu)
			}
		case id.Range:
			buf.ReadFloat64()
			buf.ReadFloat64()
		default:
			return fmt.Errorf("field %s has unsupported type %s", path, dt)
		}
		return buf.Error()
	}
	// Vendor enumerations are Int32 on the wire; vendor structures recurse.
	if _, err := b.structDef(ctx, dt); err != nil {
		out.add(path, float64(buf.ReadInt32()))
		return buf.Error()
	}
	return b.decodeInto(ctx, buf, dt, path, out, depth+1)
}
