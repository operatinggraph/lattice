package bridge

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	// internal/processor/opwire is imported by this TEST FILE ONLY, to pin the
	// envelope shape actuator.go's contextHint restates
	// (TestContextHint_MirrorsOpwire) — the same test-file-only technique the
	// loom and weaver mirrors of this test use. The bridge's production code
	// must never import it — boundary_test's TestModuleBoundary_OnlySubstrate
	// checks `go list -deps` on the non-test package, which this import does
	// not enter.
	"github.com/operatinggraph/lattice/internal/processor/opwire"
)

// opwireOmissions are the opwire.ContextHint fields the bridge's mirror
// deliberately does not carry, each with the reason it cannot arise here. The
// bridge submits exactly one op shape — an adapter's replyOp, carrying an
// external outcome back onto the claim vertex the instanceOp minted — and its
// whole declarable read-set is derivable from externalRef, which is all the
// bridge ever knows about a reply.
//
// The list exists so a NEW opwire field cannot be absorbed silently: adding one
// without mirroring it fails this test until someone either mirrors the field
// or writes down here why the bridge never declares it. That is the gap this
// test closes — the two engines' mirrors were pinned and this one was not, so
// an envelope field could stay undeclarable from the bridge forever with
// nothing red.
var opwireOmissions = map[string]string{
	"optionalReads": "the replyOp's read-set is the claim vertex the reply resolves, " +
		"a key whose absence is a fault rather than a branch; the bridge declares no " +
		"absence-tolerant read.",
	"enumerations": "a replyOp records an outcome onto the claim vertex it was handed " +
		"by externalRef; it walks no links.",
	"egressReads": "class (f) declares what leaves the platform, which is the instanceOp's " +
		"outbound leg (Loom's externalTask, inferExternalTaskReads); the bridge carries " +
		"the return leg inward.",
}

// TestContextHint_MirrorsOpwire pins actuator.go's contextHint against its
// source of truth, opwire.ContextHint. The bridge's PRODUCTION code may not
// import internal/processor (boundary_test's TestModuleBoundary_OnlySubstrate
// enforces that via `go list -deps`, which reads the non-test package's imports
// only), so the envelope's contextHint shape is hand-copied into actuator.go
// and compared here — the bridge is the third such hand-copy, alongside loom's
// and weaver's.
//
// It checks BOTH directions, because the two failures are different bugs:
//
//   - Every field the mirror declares must exist in opwire under the same JSON
//     name AND the same shape. A mirror field that opwire does not parse is a
//     declaration Loom writes onto the wire and the Processor drops.
//   - Every opwire field must be either mirrored or listed in opwireOmissions
//     with a reason. Otherwise a field added to the envelope stays permanently
//     undeclarable by the bridge, and nothing fails.
//
// Shape, not Go type: a mirror may restate a nested shape under its own local
// type, so the comparison is over the JSON shape each marshals to, recursively.
func TestContextHint_MirrorsOpwire(t *testing.T) {
	t.Parallel()

	mirror := jsonShape(t, reflect.TypeOf(contextHint{}))
	canonical := jsonShape(t, reflect.TypeOf(opwire.ContextHint{}))

	for _, name := range sortedKeys(mirror) {
		want, ok := canonical[name]
		if !ok {
			t.Errorf("the bridge's contextHint declares %q, which opwire.ContextHint does not parse — the Processor would drop it", name)
			continue
		}
		if mirror[name] != want {
			t.Errorf("the bridge's contextHint field %q has shape %s but opwire.ContextHint's is %s", name, mirror[name], want)
		}
	}

	for _, name := range sortedKeys(canonical) {
		if _, ok := mirror[name]; ok {
			continue
		}
		// A blank reason does not excuse: the ledger's whole value is the
		// written justification, and `{"newField": ""}` would otherwise be a
		// one-word way to silence this gate permanently.
		if reason := strings.TrimSpace(opwireOmissions[name]); reason == "" {
			t.Errorf("opwire.ContextHint carries %q and the bridge's contextHint does not mirror it — mirror the field, or record in opwireOmissions, with a non-blank reason, why the bridge never declares it", name)
		}
	}
}

// sortedKeys yields a map's keys in a deterministic order so a failure names
// fields in a stable sequence across runs.
func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// jsonShape renders a struct's marshaled shape as a JSON-name → shape-string
// map, recursing through slices and nested structs. It compares what travels on
// the wire rather than Go identity, which is the only comparison available
// across a module boundary that exists precisely to keep the two types
// unrelated. A field tagged `json:"-"` is skipped (it never reaches the wire);
// an untagged exported field is keyed by its Go name, which is what
// encoding/json would emit.
func jsonShape(t *testing.T, typ reflect.Type) map[string]string {
	t.Helper()
	if typ.Kind() != reflect.Struct {
		t.Fatalf("jsonShape wants a struct, got %s", typ.Kind())
	}
	shape := make(map[string]string, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if !f.IsExported() {
			continue
		}
		name, ok := jsonName(f)
		if !ok {
			continue
		}
		shape[name] = shapeOf(f.Type)
	}
	return shape
}

// jsonName resolves a struct field's wire name, reporting false for a field
// json never emits.
func jsonName(f reflect.StructField) (string, bool) {
	tag, tagged := f.Tag.Lookup("json")
	if !tagged {
		return f.Name, true
	}
	name, _, _ := strings.Cut(tag, ",")
	if name == "-" {
		return "", false
	}
	if name == "" {
		return f.Name, true
	}
	return name, true
}

// shapeOf renders one type as a canonical wire-shape string: a slice as
// "[]<elem>", a struct as its sorted "name:shape" pairs in braces, anything
// else as its Go kind.
func shapeOf(typ reflect.Type) string {
	switch typ.Kind() {
	case reflect.Slice, reflect.Array:
		return "[]" + shapeOf(typ.Elem())
	case reflect.Pointer:
		return shapeOf(typ.Elem())
	case reflect.Struct:
		parts := make([]string, 0, typ.NumField())
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if !f.IsExported() {
				continue
			}
			name, ok := jsonName(f)
			if !ok {
				continue
			}
			parts = append(parts, name+":"+shapeOf(f.Type))
		}
		sort.Strings(parts)
		return "{" + strings.Join(parts, ",") + "}"
	default:
		return fmt.Sprint(typ.Kind())
	}
}
