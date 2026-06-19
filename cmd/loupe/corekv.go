package main

import "strings"

// keyClass is the Contract #1 entity class a Core KV key belongs to.
type keyClass string

const (
	classVertex  keyClass = "vertex"
	classAspect  keyClass = "aspect"
	classLink    keyClass = "link"
	classMeta    keyClass = "meta"
	classUnknown keyClass = "unknown"
)

// classifyKey labels a Core KV key by its Contract #1 shape. Per Contract #1:
// aspects are 4-segment vtx.<type>.<id>.<localName>; links are 6-segment
// lnk.<typeA>.<idA>.<relation>.<typeB>.<idB>; a meta-vertex is the 3-segment
// vtx.meta.<id>. A 3-segment vtx.<type>.<id> with type != "meta" is a plain
// vertex root. Meta is checked before the generic vertex/aspect split so a
// meta-vertex root is not mislabelled a vertex.
func classifyKey(key string) keyClass {
	segs := strings.Split(key, ".")
	switch {
	case strings.HasPrefix(key, "lnk."):
		if len(segs) == 6 {
			return classLink
		}
		return classUnknown
	case strings.HasPrefix(key, "vtx.meta."):
		// vtx.meta.<id> is a meta-vertex root; vtx.meta.<id>.<localName> is an
		// aspect hanging off it.
		switch len(segs) {
		case 3:
			return classMeta
		case 4:
			return classAspect
		default:
			return classUnknown
		}
	case strings.HasPrefix(key, "vtx."):
		switch len(segs) {
		case 3:
			return classVertex
		case 4:
			return classAspect
		default:
			return classUnknown
		}
	default:
		return classUnknown
	}
}

// coreKVKey is one classified key row returned by GET /api/corekv.
type coreKVKey struct {
	Key   string   `json:"key"`
	Class keyClass `json:"class"`
}

// filterAndClassify selects keys matching prefix, classifies each, and caps the
// result at limit. A blank prefix matches everything. The cap protects the UI
// from an unbounded bucket; truncated reports whether the cap was hit so the UI
// can surface "showing first N".
func filterAndClassify(keys []string, prefix string, limit int) (rows []coreKVKey, truncated bool) {
	rows = make([]coreKVKey, 0, limit)
	for _, k := range keys {
		if prefix != "" && !strings.HasPrefix(k, prefix) {
			continue
		}
		if len(rows) >= limit {
			truncated = true
			break
		}
		rows = append(rows, coreKVKey{Key: k, Class: classifyKey(k)})
	}
	return rows, truncated
}
