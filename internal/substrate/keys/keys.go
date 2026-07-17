package keys

import (
	"fmt"
	"strings"
)

// Key shape constants. Per Contract #1 §1.1:
//
//	vertex: vtx.<type>.<id>                                     (3 segments)
//	aspect: vtx.<type>.<id>.<localName>                         (4 segments)
//	link:   lnk.<type1>.<id1>.<localName>.<type2>.<id2>         (6 segments)
const (
	VertexPrefix = "vtx"
	LinkPrefix   = "lnk"
)

// KeyKind classifies a Core KV key by its segment shape.
type KeyKind int

const (
	KindUnknown KeyKind = iota
	KindVertex
	KindAspect
	KindLink
)

// VertexKey constructs a Contract #1 vertex key: vtx.<type>.<id>.
//
// Both segments are validated. Invalid input panics — keys are constructed
// from typed Go values inside the platform, never from untrusted input
// (the parser is the trust boundary).
func VertexKey(vertexType, id string) string {
	mustValidateType(vertexType)
	mustValidateNanoID(id)
	return VertexPrefix + "." + vertexType + "." + id
}

// AspectKey constructs a Contract #1 aspect key:
// vtx.<type>.<id>.<localName>. The vtxKey parameter must itself be a valid
// 3-segment vertex key.
func AspectKey(vtxKey, localName string) string {
	t, id, ok := splitVertexKey(vtxKey)
	if !ok {
		panic(fmt.Sprintf("substrate/keys: AspectKey: invalid vertex key %q", vtxKey))
	}
	mustValidateLocalName(localName)
	return VertexPrefix + "." + t + "." + id + "." + localName
}

// LinkKey constructs a Contract #1 link key:
// lnk.<type1>.<id1>.<linkName>.<type2>.<id2>.
//
// Per Contract #1 §1.1, id1 is the SOURCE side and id2 the TARGET side, in
// the link DDL's declared direction (source typically added later, target
// typically pre-exists). The convention is semantic, not algorithmic: there
// is NO auto-sort by type, NanoID, or createdAt. LinkKey constructs the key
// in caller-provided order; the authorized caller (the DDL's Starlark script)
// is responsible for emitting endpoints in the declared direction.
func LinkKey(type1, id1, linkName, type2, id2 string) string {
	mustValidateType(type1)
	mustValidateNanoID(id1)
	mustValidateLocalName(linkName)
	mustValidateType(type2)
	mustValidateNanoID(id2)
	return LinkPrefix + "." + type1 + "." + id1 + "." + linkName + "." + type2 + "." + id2
}

// ClassifyKey returns the KeyKind for a Core KV key. Returns KindUnknown for
// malformed input.
func ClassifyKey(key string) KeyKind {
	parts := strings.Split(key, ".")
	switch len(parts) {
	case 3:
		if parts[0] == VertexPrefix && isValidTypeSegment(parts[1]) && IsValidNanoID(parts[2]) {
			return KindVertex
		}
	case 4:
		if parts[0] == VertexPrefix && isValidTypeSegment(parts[1]) && IsValidNanoID(parts[2]) && isValidLocalName(parts[3]) {
			return KindAspect
		}
	case 6:
		if parts[0] == LinkPrefix &&
			isValidTypeSegment(parts[1]) && IsValidNanoID(parts[2]) &&
			isValidLocalName(parts[3]) &&
			isValidTypeSegment(parts[4]) && IsValidNanoID(parts[5]) {
			return KindLink
		}
	}
	return KindUnknown
}

// ParseVertexKey extracts the type and id from a vertex key. Returns
// (_, _, false) on malformed input.
func ParseVertexKey(key string) (vertexType, id string, ok bool) {
	return splitVertexKey(key)
}

// ParseAspectKey extracts the parent vertex key, type, id, and local name
// from an aspect key.
func ParseAspectKey(key string) (vertexKey, vertexType, id, localName string, ok bool) {
	parts := strings.Split(key, ".")
	if len(parts) != 4 || parts[0] != VertexPrefix {
		return "", "", "", "", false
	}
	if !isValidTypeSegment(parts[1]) || !IsValidNanoID(parts[2]) || !isValidLocalName(parts[3]) {
		return "", "", "", "", false
	}
	return parts[0] + "." + parts[1] + "." + parts[2], parts[1], parts[2], parts[3], true
}

// ParseLinkKey extracts the six components from a link key.
func ParseLinkKey(key string) (type1, id1, linkName, type2, id2 string, ok bool) {
	parts := strings.Split(key, ".")
	if len(parts) != 6 || parts[0] != LinkPrefix {
		return "", "", "", "", "", false
	}
	if !isValidTypeSegment(parts[1]) || !IsValidNanoID(parts[2]) ||
		!isValidLocalName(parts[3]) ||
		!isValidTypeSegment(parts[4]) || !IsValidNanoID(parts[5]) {
		return "", "", "", "", "", false
	}
	return parts[1], parts[2], parts[3], parts[4], parts[5], true
}

// splitVertexKey is the internal vertex-key parser.
func splitVertexKey(key string) (vertexType, id string, ok bool) {
	parts := strings.Split(key, ".")
	if len(parts) != 3 || parts[0] != VertexPrefix {
		return "", "", false
	}
	if !isValidTypeSegment(parts[1]) || !IsValidNanoID(parts[2]) {
		return "", "", false
	}
	return parts[1], parts[2], true
}

// --- segment validators ---

// isValidTypeSegment matches Contract #1 type pattern: [a-z][a-z0-9]*.
func isValidTypeSegment(s string) bool {
	if len(s) == 0 {
		return false
	}
	if !(s[0] >= 'a' && s[0] <= 'z') {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}

// isValidLocalName matches Contract #1 local-name pattern:
// [a-z][a-zA-Z0-9]*. Underscore prefix is reserved (Contract #1 §1.4) and
// is NOT a substrate-level validation concern — Processor enforces that at
// commit step 6. Substrate accepts underscore-prefixed names so platform-
// generated system metadata can be written through these helpers.
func isValidLocalName(s string) bool {
	if len(s) == 0 {
		return false
	}
	first := s[0]
	if !((first >= 'a' && first <= 'z') || first == '_') {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}

// --- panic helpers (programmer errors) ---

func mustValidateType(s string) {
	if !isValidTypeSegment(s) {
		panic(fmt.Sprintf("substrate/keys: invalid type segment %q (must match [a-z][a-z0-9]*)", s))
	}
}

func mustValidateNanoID(s string) {
	if !IsValidNanoID(s) {
		panic(fmt.Sprintf("substrate/keys: invalid NanoID %q (must be 20 chars from Contract #1 alphabet)", s))
	}
}

func mustValidateLocalName(s string) {
	if !isValidLocalName(s) {
		panic(fmt.Sprintf("substrate/keys: invalid local name %q (must match [a-z_][a-zA-Z0-9]*)", s))
	}
}
