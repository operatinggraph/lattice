package substrate

import "github.com/operatinggraph/lattice/internal/substrate/keys"

// Contract #1 key shapes and NanoIDs are defined in internal/substrate/keys,
// a leaf package that depends on nothing but the standard library. They are
// re-exported here because they read as substrate vocabulary at every call
// site in the platform, and because importing this package means linking a
// NATS client — which a browser-hosted Edge engine (edge-browser-node-design.md
// §3.2) must not do for the sake of a string parser. Code that needs only the
// key shapes imports internal/substrate/keys directly.

// Key shape constants. Per Contract #1 §1.1:
//
//	vertex: vtx.<type>.<id>                                     (3 segments)
//	aspect: vtx.<type>.<id>.<localName>                         (4 segments)
//	link:   lnk.<type1>.<id1>.<localName>.<type2>.<id2>         (6 segments)
const (
	VertexPrefix = keys.VertexPrefix
	LinkPrefix   = keys.LinkPrefix
)

// KeyKind classifies a Core KV key by its segment shape.
type KeyKind = keys.KeyKind

const (
	KindUnknown = keys.KindUnknown
	KindVertex  = keys.KindVertex
	KindAspect  = keys.KindAspect
	KindLink    = keys.KindLink
)

// Alphabet is the canonical 58-character custom NanoID alphabet for Lattice.
const Alphabet = keys.Alphabet

// NanoIDLength is the canonical primary-key NanoID length (20 chars).
const NanoIDLength = keys.NanoIDLength

// ShortCodeLength is the human-facing short-code NanoID length (8 chars).
const ShortCodeLength = keys.ShortCodeLength

// VertexKey constructs a Contract #1 vertex key: vtx.<type>.<id>.
func VertexKey(vertexType, id string) string { return keys.VertexKey(vertexType, id) }

// AspectKey constructs a Contract #1 aspect key: vtx.<type>.<id>.<localName>.
func AspectKey(vtxKey, localName string) string { return keys.AspectKey(vtxKey, localName) }

// LinkKey constructs a Contract #1 link key:
// lnk.<type1>.<id1>.<linkName>.<type2>.<id2>.
func LinkKey(type1, id1, linkName, type2, id2 string) string {
	return keys.LinkKey(type1, id1, linkName, type2, id2)
}

// ClassifyKey returns the KeyKind for a Core KV key. Returns KindUnknown for
// malformed input.
func ClassifyKey(key string) KeyKind { return keys.ClassifyKey(key) }

// ParseVertexKey extracts the type and id from a vertex key.
func ParseVertexKey(key string) (vertexType, id string, ok bool) { return keys.ParseVertexKey(key) }

// ParseAspectKey extracts the parent vertex key, type, id, and local name from
// an aspect key.
func ParseAspectKey(key string) (vertexKey, vertexType, id, localName string, ok bool) {
	return keys.ParseAspectKey(key)
}

// ParseLinkKey extracts the six components from a link key.
func ParseLinkKey(key string) (type1, id1, linkName, type2, id2 string, ok bool) {
	return keys.ParseLinkKey(key)
}

// NewNanoID returns a freshly generated 20-character NanoID drawn from the
// custom Lattice alphabet (Contract #1).
func NewNanoID() (string, error) { return keys.NewNanoID() }

// NewShortCode returns a freshly generated 8-character display short code.
func NewShortCode() (string, error) { return keys.NewShortCode() }

// IsValidNanoID reports whether s is a canonical 20-character NanoID.
func IsValidNanoID(s string) bool { return keys.IsValidNanoID(s) }

// IsValidShortCode reports whether s is a canonical 8-character short code.
func IsValidShortCode(s string) bool { return keys.IsValidShortCode(s) }
