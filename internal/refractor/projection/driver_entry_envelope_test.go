package projection_test

import (
	"testing"

	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/projection"
)

// readableAnchorsDesc mirrors the §3.3 example: a roster column of
// {anchorId, anchorType, via} maps, split per-entry on anchorId.
func readableAnchorsDesc(t *testing.T) projection.OutputDescriptor {
	t.Helper()
	d, err := projection.ParseOutputDescriptor(&lens.OutputDescriptorSpec{
		AnchorType:       "identity",
		OutputKeyPattern: "cap-read.residence.{actorSuffix}",
		BodyColumns:      []string{"readableAnchors"},
		EmptyBehavior:    "delete",
		RealnessFilter:   "anchorId",
		Freshness:        "auto",
		EntryKeyColumn:   "anchorId",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return d
}

const (
	entryActor  = "vtx.identity.Hj4kPmRtw9nbCxz5vQ2y"
	anchorUnit1 = "Lk2Pn6mQrtwzKbcXvP3q"
	anchorUnit2 = "Lk2Pn6mQrtwzKbcXvP3r"
)

func TestDriver_EntryEnvelope_PerEntryKeys_Shape(t *testing.T) {
	fn := readableAnchorsDesc(t).EntryEnvelopeFn()
	row := map[string]any{
		"actorKey": entryActor,
		"readableAnchors": []any{
			map[string]any{"anchorId": anchorUnit1, "anchorType": "unit", "via": []any{"residesIn"}},
			map[string]any{"anchorId": anchorUnit2, "anchorType": "unit", "via": []any{"residesIn"}},
		},
	}
	entries, err := fn(row, nil, map[string]any{"projectedAt": "2026-07-25T14:32:18.142Z"})
	if err != nil {
		t.Fatalf("entry envelope: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 entries, got %d: %+v", len(entries), entries)
	}

	byKey := map[string]pipeline.Envelope{}
	for _, e := range entries {
		byKey[e.Keys["key"].(string)] = e
	}

	wantKey1 := "cap-read.residence.identity.Hj4kPmRtw9nbCxz5vQ2y." + anchorUnit1
	e1, ok := byKey[wantKey1]
	if !ok {
		t.Fatalf("missing key %q; got %v", wantKey1, byKey)
	}
	if e1.Row["key"] != wantKey1 {
		t.Fatalf("row key: %v", e1.Row["key"])
	}
	if e1.Row["actor"] != entryActor {
		t.Fatalf("row actor: %v", e1.Row["actor"])
	}
	if e1.Row["version"] != projection.Version {
		t.Fatalf("row version: %v", e1.Row["version"])
	}
	if e1.Row["projectedAt"] != "2026-07-25T14:32:18.142Z" {
		t.Fatalf("row projectedAt: %v", e1.Row["projectedAt"])
	}
	if e1.Row["anchorType"] != "unit" {
		t.Fatalf("row anchorType: %v", e1.Row["anchorType"])
	}
	if _, dup := e1.Row["anchorId"]; dup {
		t.Fatalf("the key field must not be duplicated into the body: %v", e1.Row)
	}
	if _, has := e1.Row["projectedFromRevisions"]; has {
		t.Fatalf("a per-anchor entry must carry no projectedFromRevisions (§3.2): %v", e1.Row)
	}

	wantKey2 := "cap-read.residence.identity.Hj4kPmRtw9nbCxz5vQ2y." + anchorUnit2
	if _, ok := byKey[wantKey2]; !ok {
		t.Fatalf("missing key %q; got %v", wantKey2, byKey)
	}
}

func TestDriver_EntryEnvelope_RealnessFilter_DropsUnrealEntries(t *testing.T) {
	fn := readableAnchorsDesc(t).EntryEnvelopeFn()
	row := map[string]any{
		"actorKey": entryActor,
		"readableAnchors": []any{
			map[string]any{"anchorId": anchorUnit1, "anchorType": "unit"},
			map[string]any{"anchorId": "", "anchorType": "unit"}, // degenerate OPTIONAL-match artifact
		},
	}
	entries, err := fn(row, nil, map[string]any{"projectedAt": "2026-07-25T14:32:18.142Z"})
	if err != nil {
		t.Fatalf("entry envelope: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 entry after realness filter, got %d: %+v", len(entries), entries)
	}
}

func TestDriver_EntryEnvelope_ZeroRealEntries_EmptyNotError(t *testing.T) {
	fn := readableAnchorsDesc(t).EntryEnvelopeFn()
	row := map[string]any{
		"actorKey":        entryActor,
		"readableAnchors": []any{},
	}
	entries, err := fn(row, nil, map[string]any{"projectedAt": "2026-07-25T14:32:18.142Z"})
	if err != nil {
		t.Fatalf("entry envelope: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("want 0 entries, got %d", len(entries))
	}
}

func TestDriver_EntryEnvelope_EmptyActorKey_Skips(t *testing.T) {
	fn := readableAnchorsDesc(t).EntryEnvelopeFn()
	row := map[string]any{
		"readableAnchors": []any{map[string]any{"anchorId": anchorUnit1}},
	}
	_, err := fn(row, nil, map[string]any{})
	if err != pipeline.ErrSkipProjection {
		t.Fatalf("want ErrSkipProjection, got %v", err)
	}
}

func TestDriver_EntryEnvelope_WrongAnchorType_Skips(t *testing.T) {
	fn := readableAnchorsDesc(t).EntryEnvelopeFn()
	row := map[string]any{
		"actorKey":        "vtx.role.Hj4kPmRtw9nbCxz5vQ2y",
		"readableAnchors": []any{map[string]any{"anchorId": anchorUnit1}},
	}
	_, err := fn(row, nil, map[string]any{})
	if err != pipeline.ErrSkipProjection {
		t.Fatalf("want ErrSkipProjection, got %v", err)
	}
}

func TestDriver_EntryEnvelope_NonMapEntry_ErrorsWholeEvaluation(t *testing.T) {
	fn := readableAnchorsDesc(t).EntryEnvelopeFn()
	row := map[string]any{
		"actorKey":        entryActor,
		"readableAnchors": []any{"bare-string-entry"},
	}
	_, err := fn(row, nil, map[string]any{"projectedAt": "2026-07-25T14:32:18.142Z"})
	if err == nil {
		t.Fatalf("want an error for a non-map roster entry, got nil")
	}
}

func TestDriver_EntryEnvelope_MissingKeyFieldButRealnessGated_DroppedNotErrored(t *testing.T) {
	// When RealnessFilter names the same field as EntryKeyColumn (the common
	// case, §3.3's own example), an entry missing that field never reaches
	// the key-extraction error path — RealnessFiltered has already dropped
	// it as unreal.
	fn := readableAnchorsDesc(t).EntryEnvelopeFn()
	row := map[string]any{
		"actorKey": entryActor,
		"readableAnchors": []any{
			map[string]any{"anchorType": "unit"}, // no anchorId
		},
	}
	entries, err := fn(row, nil, map[string]any{"projectedAt": "2026-07-25T14:32:18.142Z"})
	if err != nil {
		t.Fatalf("an entry missing the realness field is dropped as unreal, not errored: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("want 0 entries, got %d", len(entries))
	}
}

func TestDriver_EntryEnvelope_MissingKeyFieldNotRealnessGated_Errors(t *testing.T) {
	// When RealnessFilter differs from EntryKeyColumn, an entry can survive
	// the realness filter while still missing its key field — that must
	// error the whole evaluation (§3.3: absent ⇒ fail-closed), never
	// silently drop the grant.
	d, err := projection.ParseOutputDescriptor(&lens.OutputDescriptorSpec{
		AnchorType:       "identity",
		OutputKeyPattern: "cap-read.residence.{actorSuffix}",
		BodyColumns:      []string{"readableAnchors"},
		EmptyBehavior:    "delete",
		RealnessFilter:   "via",
		Freshness:        "auto",
		EntryKeyColumn:   "anchorId",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	fn := d.EntryEnvelopeFn()
	row := map[string]any{
		"actorKey": entryActor,
		"readableAnchors": []any{
			map[string]any{"via": "residesIn"}, // real by RealnessFilter, but no anchorId
		},
	}
	_, err = fn(row, nil, map[string]any{"projectedAt": "2026-07-25T14:32:18.142Z"})
	if err == nil {
		t.Fatalf("want an error for a real entry missing its key field, got nil")
	}
}

func TestDriver_EntryEnvelope_KeyFieldPresentButNotRealnessGated_NonStringErrors(t *testing.T) {
	// A descriptor whose RealnessFilter differs from EntryKeyColumn lets a
	// non-string key-field value survive the realness filter and reach the
	// NanoID validation, which must reject it loudly.
	d, err := projection.ParseOutputDescriptor(&lens.OutputDescriptorSpec{
		AnchorType:       "identity",
		OutputKeyPattern: "cap-read.residence.{actorSuffix}",
		BodyColumns:      []string{"readableAnchors"},
		EmptyBehavior:    "delete",
		RealnessFilter:   "via",
		Freshness:        "auto",
		EntryKeyColumn:   "anchorId",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	fn := d.EntryEnvelopeFn()
	row := map[string]any{
		"actorKey": entryActor,
		"readableAnchors": []any{
			map[string]any{"anchorId": 12345, "via": "residesIn"},
		},
	}
	_, err = fn(row, nil, map[string]any{"projectedAt": "2026-07-25T14:32:18.142Z"})
	if err == nil {
		t.Fatalf("want an error for a non-string key field, got nil")
	}
}

func TestDriver_EntryEnvelope_MalformedNanoID_ErrorsWholeEvaluation(t *testing.T) {
	fn := readableAnchorsDesc(t).EntryEnvelopeFn()
	row := map[string]any{
		"actorKey": entryActor,
		"readableAnchors": []any{
			map[string]any{"anchorId": "not-a-nanoid.with.dots", "anchorType": "unit"},
		},
	}
	_, err := fn(row, nil, map[string]any{"projectedAt": "2026-07-25T14:32:18.142Z"})
	if err == nil {
		t.Fatalf("want an error for a malformed NanoID key token, got nil")
	}
}

// TestDriver_EntryEnvelope_MetacharacterAtValidLength_Errors is the sharp
// vector finding A's length check alone would miss: a value exactly 20
// characters long (the required NanoID length) that still carries a subject
// metacharacter. A key built from this must never reach the adapter — the
// whole point of Contract #1's NanoID-alphabet check (§3.3: "a subject
// metacharacter must never reach a key").
func TestDriver_EntryEnvelope_MetacharacterAtValidLength_Errors(t *testing.T) {
	const twentyCharsWithADot = "abcdefghijk.mnopqrst"
	if len(twentyCharsWithADot) != 20 {
		t.Fatalf("test fixture must be exactly 20 chars, got %d", len(twentyCharsWithADot))
	}
	fn := readableAnchorsDesc(t).EntryEnvelopeFn()
	row := map[string]any{
		"actorKey": entryActor,
		"readableAnchors": []any{
			map[string]any{"anchorId": twentyCharsWithADot, "anchorType": "unit"},
		},
	}
	_, err := fn(row, nil, map[string]any{"projectedAt": "2026-07-25T14:32:18.142Z"})
	if err == nil {
		t.Fatalf("want an error for a 20-char key token carrying a subject metacharacter, got nil")
	}
}

// TestDriver_EntryEnvelope_ReservedFieldName_Errors pins finding A of the
// increment-2 adversarial review: an entry field sharing a name with the
// envelope metadata (isDeleted, projectionSeq — the guard's own tombstone
// and watermark fields) must never be allowed into the body, whether by
// silently overwriting the metadata or by the metadata silently overwriting
// it. A roster copied from a Core KV vertex body (which always carries
// isDeleted) is the realistic trigger.
func TestDriver_EntryEnvelope_ReservedFieldName_Errors(t *testing.T) {
	fn := readableAnchorsDesc(t).EntryEnvelopeFn()
	row := map[string]any{
		"actorKey": entryActor,
		"readableAnchors": []any{
			map[string]any{"anchorId": anchorUnit1, "anchorType": "unit", "isDeleted": false},
		},
	}
	_, err := fn(row, nil, map[string]any{"projectedAt": "2026-07-25T14:32:18.142Z"})
	if err == nil {
		t.Fatalf("want an error for an entry field named isDeleted, got nil")
	}
}

// TestDriver_EntryEnvelope_ActorFieldCollision_Errors pins that a cypher-
// supplied entry field sharing d.ActorField's name ("actor" by default) is
// refused, not silently overwritten by or allowed to overwrite the envelope's
// own actor field — the ActorField check is separate from entryReservedFields
// (it is configurable, not a literal) but must be exactly as fail-closed.
func TestDriver_EntryEnvelope_ActorFieldCollision_Errors(t *testing.T) {
	fn := readableAnchorsDesc(t).EntryEnvelopeFn()
	row := map[string]any{
		"actorKey": entryActor,
		"readableAnchors": []any{
			map[string]any{"anchorId": anchorUnit1, "anchorType": "unit", "actor": "vtx.identity.forgedforgedforged99"},
		},
	}
	_, err := fn(row, nil, map[string]any{"projectedAt": "2026-07-25T14:32:18.142Z"})
	if err == nil {
		t.Fatalf("want an error for an entry field colliding with ActorField, got nil")
	}
}

func TestDriver_EntryEnvelope_DuplicateKeys_LastEntryWins(t *testing.T) {
	fn := readableAnchorsDesc(t).EntryEnvelopeFn()
	row := map[string]any{
		"actorKey": entryActor,
		"readableAnchors": []any{
			map[string]any{"anchorId": anchorUnit1, "anchorType": "unit", "via": []any{"residesIn"}},
			map[string]any{"anchorId": anchorUnit1, "anchorType": "unit", "via": []any{"manages"}},
		},
	}
	entries, err := fn(row, nil, map[string]any{"projectedAt": "2026-07-25T14:32:18.142Z"})
	if err != nil {
		t.Fatalf("entry envelope: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("duplicate anchorId across walk branches must collapse to one key, got %d entries", len(entries))
	}
	via, _ := entries[0].Row["via"].([]any)
	if len(via) != 1 || via[0] != "manages" {
		t.Fatalf("last entry's audit fields must win, got via=%v", entries[0].Row["via"])
	}
}

// TestDriver_EntryEnvelope_EmittedKeysRoundTripThroughAnchorFromKey closes the
// loop the sweep's own tests cannot: every other perEntry AnchorFromKey test
// (in this package and in pipeline's sweep_perentry_test.go, which cannot
// import this one — projection depends on pipeline, not the reverse) builds
// its sample keys by hand, string-concatenating a shape that MIRRORS
// EntryEnvelopeFn rather than exercising it. A parser divergence between what
// production actually writes and what the inverse expects would read as an
// unclaimable orphan forever (a stale grant that never retracts), and none of
// those hand-built tests could catch it. This one drives the real emission
// path and feeds its real output back into the real inverse.
func TestDriver_EntryEnvelope_EmittedKeysRoundTripThroughAnchorFromKey(t *testing.T) {
	desc := readableAnchorsDesc(t)
	fn := desc.EntryEnvelopeFn()
	row := map[string]any{
		"actorKey": entryActor,
		"readableAnchors": []any{
			map[string]any{"anchorId": anchorUnit1, "anchorType": "unit", "via": []any{"residesIn"}},
			map[string]any{"anchorId": anchorUnit2, "anchorType": "unit", "via": []any{"residesIn"}},
		},
	}
	entries, err := fn(row, nil, map[string]any{"projectedAt": "2026-07-25T14:32:18.142Z"})
	if err != nil {
		t.Fatalf("entry envelope: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 entries, got %d", len(entries))
	}
	for _, e := range entries {
		key, _ := e.Keys["key"].(string)
		if key == "" {
			t.Fatalf("entry carries no key: %+v", e)
		}
		got, ok := desc.AnchorFromKey(key)
		if !ok {
			t.Fatalf("AnchorFromKey rejected a key EntryEnvelopeFn actually emitted: %q", key)
		}
		if got != entryActor {
			t.Fatalf("AnchorFromKey(%q) = %q, want %q", key, got, entryActor)
		}
	}
}
