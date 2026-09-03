package pkgmgr

import (
	"encoding/json"
	"strings"
	"testing"
)

// specDoc builds a stored lens-spec aspect document in the envelope-wrapped
// shape the installer commits, so the test exercises the same unwrap the
// running Refractor performs rather than a convenient bare spec.
func specDoc(t *testing.T, bodyColumns []string, grantTable bool) map[string]any {
	t.Helper()
	raw := map[string]any{
		"id":             "lens-1",
		"canonicalName":  "appointmentReminders",
		"targetType":     "nats_kv",
		"cypherRule":     "MATCH (i:identity) RETURN i.key AS actorKey",
		"projectionKind": "actorAggregate",
		"targetConfig":   map[string]any{"bucket": "weaver-targets", "key": []any{"key"}},
		"output": map[string]any{
			"anchorType":       "identity",
			"outputKeyPattern": "appointmentReminders.{actorSuffix}",
			"bodyColumns":      toAny(bodyColumns),
			"emptyBehavior":    "delete",
			"freshness":        "auto",
		},
	}
	if grantTable {
		raw["targetConfig"].(map[string]any)["grantTable"] = true
	}
	// Round-trip so the document is exactly what a JSON decode of stored bytes
	// would produce (float64 numbers, []any slices).
	encoded, err := json.Marshal(map[string]any{"key": "vtx.meta.lens-1.spec", "data": raw})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(encoded, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return doc
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// The commonest package-lens edit there is: an upgrade re-authors an
// actor-aggregate lens's Output. Refractor carries it live, by re-activating the
// lens, so an apply that lands one has nothing to tell the operator — and a note
// pointing at a remedy for a change that already applied would train them to
// ignore the notes that still mean something.
func TestReactivationNote_OutputEditOnALensSpecIsSilent(t *testing.T) {
	old := specDoc(t, []string{"reminders"}, false)
	new := specDoc(t, []string{"reminders", "escalations"}, false)

	if note := reactivationNote("vtx.meta.lens-1.spec", old, new); note != "" {
		t.Fatalf("an Output edit re-activates the lens and must not warn: %q", note)
	}
}

// The positive vector: flipping grantTable strands every row the lens wrote, and
// no restart gives those rows back — so it is still refused, and the note is
// still what carries that refusal to the operator who caused it.
func TestReactivationNote_GrantTableFlipWarns(t *testing.T) {
	note := reactivationNote("vtx.meta.lens-1.spec", specDoc(t, []string{"r"}, false), specDoc(t, []string{"r"}, true))
	if note == "" {
		t.Fatal("flipping grantTable strands every row the lens wrote and must warn")
	}
	if !strings.Contains(note, "vtx.meta.lens-1.spec") {
		t.Fatalf("the note must name the key so an operator can find the lens: %q", note)
	}
	if !strings.Contains(note, "re-activated") {
		t.Fatalf("the note must name the remedy: %q", note)
	}
}

// A cypher-only edit is exactly what hot reload carries. Warning on it would
// train operators to ignore the warning that matters.
func TestReactivationNote_HotReloadableEditIsSilent(t *testing.T) {
	old := specDoc(t, []string{"reminders"}, false)
	new := specDoc(t, []string{"reminders"}, false)
	new["data"].(map[string]any)["cypherRule"] = "MATCH (i:identity) WHERE i.active RETURN i.key AS actorKey"

	if note := reactivationNote("vtx.meta.lens-1.spec", old, new); note != "" {
		t.Fatalf("a MATCH-only edit hot-reloads and must not warn: %q", note)
	}
}

// The meta keyspace carries DDLs, op descriptors and roles too. Only a lens
// spec has a hot-reload posture at all.
func TestReactivationNote_NonLensMetaUpdateIsSilent(t *testing.T) {
	ddl := map[string]any{"key": "vtx.meta.ddl-1.spec", "data": map[string]any{"targetClass": "meta.ddl.vertexType", "canonicalName": "patient"}}
	other := map[string]any{"key": "vtx.meta.ddl-1.spec", "data": map[string]any{"targetClass": "meta.ddl.vertexType", "canonicalName": "patient", "description": "changed"}}

	if note := reactivationNote("vtx.meta.ddl-1.spec", ddl, other); note != "" {
		t.Fatalf("a non-lens meta update has no reload posture and must not warn: %q", note)
	}
}

// Only the `.spec` aspect carries the lens definition; a sibling aspect update
// on the same vertex is not a spec change.
func TestReactivationNote_NonSpecKeyIsSilent(t *testing.T) {
	if note := reactivationNote("vtx.meta.lens-1.description",
		specDoc(t, []string{"a"}, false), specDoc(t, []string{"a", "b"}, false)); note != "" {
		t.Fatalf("only the .spec aspect carries the lens definition: %q", note)
	}
}
