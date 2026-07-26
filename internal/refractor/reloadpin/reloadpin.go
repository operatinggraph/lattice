// Package reloadpin names the lens-spec changes a running Refractor cannot
// carry through a hot reload, and answers that question from two stored spec
// documents alone.
//
// Refractor is the authority: cmd/refractor decides refusals against the
// RUNNING pipeline, which knows things a spec does not (the guard the built
// adapter actually carries, the surface the lens has already written to). What
// it cannot do is reach the operator who caused the edit. A package upgrade
// updates a lens spec in place — lens IDs are version-independent — so
// `lattice-pkg apply` commits successfully and reports success while the
// refusal happens later, inside a process the operator is not watching, and the
// lens keeps serving its old spec until it is re-activated.
//
// This package closes that gap from the other end, at apply time, on the
// document pair the installer already holds. It is deliberately a LEAF: it
// imports nothing of Refractor's, because pkgmgr must not import
// internal/refractor/lens (see internal/pkgmgr/definition.go), and it decides
// nothing — it only predicts, so a prediction that drifts costs a missing
// warning, never a wrong refusal.
package reloadpin

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// PinnedField is one spec field a hot reload cannot carry, with the reason it
// cannot, phrased for the operator who is about to be surprised by it.
type PinnedField struct {
	// Path is the field's location in the lens spec document, outermost first.
	// A path whose parent is absent in BOTH documents is not a change.
	Path []string
	// Why states the consequence of applying the edit to a running lens — not
	// the mechanism. An operator needs to know what would break, not which
	// function refuses.
	Why string
}

// PinnedFields is the set cmd/refractor refuses, restated over the stored spec
// document. Keeping it here rather than deriving it from Refractor's typed
// check is what lets pkgmgr consume it at all; TestPinnedFieldsMatchTheRefusalSet
// in cmd/refractor is what keeps the two from drifting.
//
// The write-surface pins (target/bucket/table/dsn/grantSource) are deliberately
// ABSENT. They are refused only for a lens whose built adapter carries the §6.2
// guard, which is a property of the running pipeline and not of any document —
// predicting them from a spec pair would warn about edits that are legitimately
// applied.
var PinnedFields = []PinnedField{
	{
		Path: []string{"output"},
		Why:  "the envelope, delete key and sweep plan are installed when the lens activates, so a running lens cannot adopt a new Output descriptor",
	},
	{
		Path: []string{"targetConfig", "grantTable"},
		Why:  "the rows the lens already wrote to the shared grant table become unaddressable — no producer claims them, so nothing can ever revoke them",
	},
	{
		Path: []string{"targetConfig", "protected"},
		Why:  "it would retire the monotonic write guard on a live read model, on the surface that carries read-path authorization",
	},
	{
		Path: []string{"targetConfig", "secureColumns"},
		Why:  "the decryptor is built at activation, so a running lens would keep projecting under the old column set",
	},
}

// RefusedChange reports the first pinned difference between two lens spec
// documents, or "" when a running Refractor could carry the edit.
//
// Both arguments are the `spec` aspect body. A document that does not parse is
// not a refusal: the installer validates specs on its own path, and guessing
// from an unparseable pair would be a warning nobody could act on.
func RefusedChange(oldSpec, newSpec []byte) string {
	var oldDoc, newDoc map[string]any
	if json.Unmarshal(oldSpec, &oldDoc) != nil || json.Unmarshal(newSpec, &newDoc) != nil {
		return ""
	}
	for _, f := range PinnedFields {
		before, hadBefore := lookup(oldDoc, f.Path)
		after, hadAfter := lookup(newDoc, f.Path)
		if !hadBefore && !hadAfter {
			continue
		}
		// An omitted field and its spelled-out default are the same spec. Every
		// pinned field is `omitempty`, so a re-serialized document drops them and
		// a hand-written one may not — comparing raw presence would warn on every
		// upgrade that merely round-tripped through a marshaller.
		if !hadBefore && isZero(after) {
			continue
		}
		if !hadAfter && isZero(before) {
			continue
		}
		if equalJSON(before, after) {
			continue
		}
		return fmt.Sprintf("%s changed — %s", pathString(f.Path), f.Why)
	}
	return ""
}

// lookup walks a path through nested JSON objects. A non-object encountered
// mid-path is a miss rather than an error: a malformed spec is the installer's
// to reject, not this predictor's to interpret.
func lookup(doc map[string]any, path []string) (any, bool) {
	cur := any(doc)
	for _, seg := range path {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = obj[seg]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// equalJSON compares two decoded values by their canonical JSON encoding, so
// absence and an explicit zero compare equal to themselves and a re-serialized
// document does not read as an edit. Ordering IS significant here, unlike
// Refractor's typed check, which compares BodyColumns as a multiset: this
// predictor errs toward warning, and a spurious warning about a reordered
// column list is a far cheaper mistake than a silent unapplied upgrade.
func equalJSON(a, b any) bool {
	ab, aerr := json.Marshal(a)
	bb, berr := json.Marshal(b)
	if aerr != nil || berr != nil {
		return false
	}
	return bytes.Equal(ab, bb)
}

// isZero reports whether a decoded JSON value is the zero its Go field would
// have marshalled away under omitempty — which is what an ABSENT field means.
func isZero(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case bool:
		return !t
	case string:
		return t == ""
	case float64:
		return t == 0
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	default:
		return false
	}
}

func pathString(path []string) string {
	out := ""
	for i, seg := range path {
		if i > 0 {
			out += "."
		}
		out += seg
	}
	return out
}
