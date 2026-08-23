package weaver

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	// internal/processor/opwire is imported by this TEST FILE ONLY, to pin the
	// envelope shape actuator.go's contextHint restates
	// (TestContextHint_MirrorsOpwire) — the same test-file-only technique
	// registry_internal_test.go uses for the step-kind vocabulary. Weaver's
	// production code must never import it — boundary_test's
	// TestModuleBoundary_OnlySubstrate checks `go list -deps` on the non-test
	// package, which this import does not enter.
	"github.com/operatinggraph/lattice/internal/processor/opwire"
)

// opwireOmissions are the opwire.ContextHint fields Weaver's mirror
// deliberately does not carry, each with the reason it cannot arise here.
//
// The list exists so a NEW opwire field cannot be absorbed silently: adding one
// without mirroring it fails this test until someone either mirrors the field
// or writes down here why Weaver never declares it.
var opwireOmissions = map[string]string{
	"egressReads": "class (f) is the external plane's declared egress, and every " +
		"external-plane dispatch is a Loom externalTask step; a gap's playbook " +
		"entry dispatches internal-plane ops only.",
}

// TestContextHint_MirrorsOpwire pins actuator.go's contextHint against its
// source of truth, opwire.ContextHint. Weaver's PRODUCTION code may not import
// internal/processor (boundary_test's TestModuleBoundary_OnlySubstrate enforces
// that via `go list -deps`, which reads the non-test package's imports only), so
// the envelope's contextHint shape is hand-copied into actuator.go and compared
// here — the same restated-constant hazard TestPatternStepKinds_MatchLoomVocabulary
// covers for the step-kind strings.
//
// It checks BOTH directions, because the two failures are different bugs:
//
//   - Every field the mirror declares must exist in opwire under the same JSON
//     name AND the same shape. A mirror field that opwire does not parse is a
//     declaration Loom writes onto the wire and the Processor drops.
//   - Every opwire field must be either mirrored or listed in opwireOmissions
//     with a reason. Otherwise a field added to the envelope stays permanently
//     undeclarable by Weaver, and nothing fails.
//
// Shape, not Go type: the mirror uses weaver.GapEnumeration where opwire uses
// opwire.EnumerationHint, so the comparison is over the JSON shape each
// marshals to, recursively.
func TestContextHint_MirrorsOpwire(t *testing.T) {
	t.Parallel()

	mirror := jsonShape(t, reflect.TypeOf(contextHint{}))
	canonical := jsonShape(t, reflect.TypeOf(opwire.ContextHint{}))

	for _, name := range sortedKeys(mirror) {
		want, ok := canonical[name]
		if !ok {
			t.Errorf("weaver's contextHint declares %q, which opwire.ContextHint does not parse — the Processor would drop it", name)
			continue
		}
		if mirror[name] != want {
			t.Errorf("weaver's contextHint field %q has shape %s but opwire.ContextHint's is %s", name, mirror[name], want)
		}
	}

	for _, name := range sortedKeys(canonical) {
		if _, ok := mirror[name]; ok {
			continue
		}
		if _, excused := opwireOmissions[name]; !excused {
			t.Errorf("opwire.ContextHint carries %q and weaver's contextHint does not mirror it — mirror the field, or record in opwireOmissions why Weaver never declares it", name)
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
