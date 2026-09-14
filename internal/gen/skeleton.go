package gen

import (
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// DefaultProtoDepth is how deep a generated message skeleton nests before it
// stops. It is a size control, not a correctness one — the cycle guard below is
// what makes termination guaranteed.
const DefaultProtoDepth = 4

// skeleton renders a protojson request body for a message descriptor: every
// field, with a type-appropriate zero value.
//
// proto3 has no `required`, so unlike the OpenAPI skeleton there is nothing to
// filter on and every field is emitted. What makes the result usable instead of
// unbounded is the pair of controls below, which are deliberately separate:
//
//   - maxDepth caps nesting, so a deep but finite message tree does not produce
//     a wall of JSON;
//   - a cycle guard tracks the message types already on the path, so a
//     self-referential message terminates.
//
// Conflating the two would mean raising --depth reintroduces a hang, which is
// exactly the failure a depth cap looks like it already prevents.
func skeleton(md protoreflect.MessageDescriptor, maxDepth int) (string, error) {
	v := messageValue(md, maxDepth, map[protoreflect.FullName]bool{})
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode %s skeleton: %w", md.FullName(), err)
	}
	return string(out), nil
}

func messageValue(md protoreflect.MessageDescriptor, depth int, path map[protoreflect.FullName]bool) map[string]any {
	out := map[string]any{}
	if md == nil || depth <= 0 {
		return out
	}

	// A oneof's members are mutually exclusive: setting two is invalid. Emit the
	// first member of each and exclude the rest from the ordinary field walk —
	// otherwise the chosen member is emitted twice, once here and once there.
	skip := map[protoreflect.Name]bool{}
	oneofs := md.Oneofs()
	for i := range oneofs.Len() {
		o := oneofs.Get(i)
		if o.IsSynthetic() { // optional fields are modelled as a synthetic oneof
			continue
		}
		fields := o.Fields()
		for j := range fields.Len() {
			fd := fields.Get(j)
			if j == 0 {
				out[fd.JSONName()] = fieldValue(fd, depth, path)
			}
			skip[fd.Name()] = true
		}
	}

	fields := md.Fields()
	for i := range fields.Len() {
		fd := fields.Get(i)
		if skip[fd.Name()] {
			continue
		}
		out[fd.JSONName()] = fieldValue(fd, depth, path)
	}
	return out
}

// fieldValue is the zero value protojson expects for one field.
//
// The order of these checks is load-bearing. A map field is a repeated
// synthetic message in the descriptor model, so a naive message check recurses
// into its MapEntry type and emits `{"key":"","value":""}` instead of `{}`;
// and a repeated message would otherwise recurse rather than produce `[]`.
func fieldValue(fd protoreflect.FieldDescriptor, depth int, path map[protoreflect.FullName]bool) any {
	switch {
	case fd.IsMap():
		return map[string]any{}
	case fd.IsList():
		return []any{}
	}

	switch fd.Kind() {
	case protoreflect.StringKind, protoreflect.BytesKind:
		// protojson encodes bytes as base64, and "" is valid base64 for empty.
		return ""
	case protoreflect.BoolKind:
		return false
	case protoreflect.EnumKind:
		return enumZeroName(fd.Enum())
	case protoreflect.MessageKind, protoreflect.GroupKind:
		return nestedMessage(fd.Message(), depth, path)
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind,
		protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		// 64-bit integers are JSON *strings* in protojson, because a JSON number
		// cannot hold them exactly. Emitting 0 here produces a body the wire
		// rejects — the same class of defect as a snake_case field name.
		return "0"
	default:
		return 0
	}
}

// enumZeroName is the name of the enum value numbered 0, falling back to the
// first declared value. protojson accepts the name, and a name is what a person
// editing the generated file can act on; the number is legal and unreadable.
func enumZeroName(ed protoreflect.EnumDescriptor) string {
	if ed == nil {
		return ""
	}
	if v := ed.Values().ByNumber(0); v != nil {
		return string(v.Name())
	}
	if ed.Values().Len() > 0 {
		return string(ed.Values().Get(0).Name())
	}
	return ""
}

func nestedMessage(md protoreflect.MessageDescriptor, depth int, path map[protoreflect.FullName]bool) any {
	if md == nil {
		return map[string]any{}
	}
	if v, ok := wellKnownValue(md.FullName()); ok {
		return v
	}
	// Cycle guard first, and independent of depth: this is the control that
	// makes termination a property of the algorithm rather than of the cap.
	if path[md.FullName()] {
		return map[string]any{}
	}
	if depth <= 1 {
		return map[string]any{}
	}

	next := make(map[protoreflect.FullName]bool, len(path)+1)
	for k := range path {
		next[k] = true
	}
	next[md.FullName()] = true
	return messageValue(md, depth-1, next)
}

// wellKnownValue gives the protojson form of the well-known types, which are
// never recursed into: a Timestamp is an RFC 3339 string on the wire, not
// `{"seconds":0,"nanos":0}`, and emitting the struct produces a body the server
// rejects while every local test passes.
//
// Anything else under google.protobuf. is recursed normally rather than guessed
// at — it will either be right or produce an obvious diff in a golden.
func wellKnownValue(name protoreflect.FullName) (any, bool) {
	switch name {
	case "google.protobuf.Timestamp", "google.protobuf.Duration",
		"google.protobuf.FieldMask", "google.protobuf.StringValue",
		"google.protobuf.BytesValue":
		return "", true
	case "google.protobuf.Struct", "google.protobuf.Value", "google.protobuf.Empty":
		return map[string]any{}, true
	case "google.protobuf.ListValue":
		return []any{}, true
	case "google.protobuf.Any":
		return map[string]any{"@type": ""}, true
	case "google.protobuf.BoolValue":
		return false, true
	case "google.protobuf.Int64Value", "google.protobuf.UInt64Value":
		return "0", true // 64-bit wrappers are strings, as their bare kinds are
	case "google.protobuf.Int32Value", "google.protobuf.UInt32Value",
		"google.protobuf.FloatValue", "google.protobuf.DoubleValue":
		return 0, true
	default:
		return nil, false
	}
}
