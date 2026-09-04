package processor

import (
	"context"
	"errors"
	"testing"
)

// A second, distinct valid Contract #1 NanoID for tests that need more than
// the two envelope_test.go constants (e.g. both endpoints of a link plus its
// own request/vertex ids). Drawn from the same Alphabet (no I/l/O/0), 20
// chars, exactly like testNanoID1/testNanoID2.
const testNanoID3 = "Aa1Bb2Cc3Dd4Ee5Ff6Gg"

// seedAbstractDDL writes a vtx.meta.<canonicalName> DDL declaring
// data.abstract=true — the shadow-key convention buildValidatorWithCache's
// sibling seedSensitiveAspectDDL already uses (the last key segment doubling
// as the canonical name for a non-NanoID test fixture).
func seedAbstractDDL(t *testing.T, ctx context.Context, conn substrateConn, canonicalName string) {
	t.Helper()
	root := "vtx.meta." + canonicalName
	doc := []byte(`{"class":"meta.ddl.vertexType","isDeleted":false,"data":{"canonicalName":"` + canonicalName + `","abstract":true}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, root, doc); err != nil {
		t.Fatalf("seed abstract DDL %s: %v", root, err)
	}
}

// buildValidatorWithAbstract seeds an abstract "location" DDL alongside the
// baseline "identity" fixture setupTestPipeline already provides, then
// returns a Validator built against the refreshed cache.
func buildValidatorWithAbstract(t *testing.T) (*ValidatorImpl, context.Context) {
	t.Helper()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	seedAbstractDDL(t, ctx, conn, "location")
	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	return NewValidator(cache, conn, testCoreBucket, testLogger()), ctx
}

func expectAbstractViolation(t *testing.T, err error, wantConstraint string) {
	t.Helper()
	var ddlErr *DDLViolation
	if !errors.As(err, &ddlErr) {
		t.Fatalf("expected *DDLViolation, got %T: %v", err, err)
	}
	if ddlErr.ViolatedConstraint != wantConstraint {
		t.Fatalf("ViolatedConstraint = %q, want %q", ddlErr.ViolatedConstraint, wantConstraint)
	}
}

// TestValidate_AbstractTypeSegment_VertexRoot_Rejected pins §8 row 2: an
// abstract type may not appear as a vertex key's own type segment. The
// mutation's class is deliberately left unresolvable (no DDL named
// "unrelated") so gate 2 (class-resolves-to-abstract) never fires — this
// isolates gate 1 (the key-segment gate).
func TestValidate_AbstractTypeSegment_VertexRoot_Rejected(t *testing.T) {
	v, ctx := buildValidatorWithAbstract(t)
	env := newTestEnvelope(testNanoID1)
	result := ScriptResult{
		Mutations: []MutationOp{{
			Op:  "create",
			Key: "vtx.location." + testNanoID2,
			Document: map[string]interface{}{
				"class": "unrelated",
				"data":  map[string]interface{}{},
			},
		}},
	}
	err := v.Validate(ctx, env, result, HydratedState{}, nil)
	expectAbstractViolation(t, err, "abstractTypeSegment")
}

// TestValidate_AbstractTypeSegment_AspectOwner_Rejected pins §8 row 2's
// aspect-owner half: an aspect key's owner type segment is the same key
// position, four segments instead of three.
func TestValidate_AbstractTypeSegment_AspectOwner_Rejected(t *testing.T) {
	v, ctx := buildValidatorWithAbstract(t)
	env := newTestEnvelope(testNanoID1)
	result := ScriptResult{
		Mutations: []MutationOp{{
			Op:  "create",
			Key: "vtx.location." + testNanoID2 + ".someAspect",
			Document: map[string]interface{}{
				"class":     "unrelated",
				"vertexKey": "vtx.location." + testNanoID2,
				"localName": "someAspect",
				"data":      map[string]interface{}{},
			},
		}},
	}
	err := v.Validate(ctx, env, result, HydratedState{}, nil)
	expectAbstractViolation(t, err, "abstractTypeSegment")
}

// TestValidate_AbstractTypeSegment_LinkSourceEndpoint_Rejected pins §8 row 3:
// a link's SOURCE endpoint type is a restatement of that endpoint's own
// vertex key, so an abstract there names no vertex either.
func TestValidate_AbstractTypeSegment_LinkSourceEndpoint_Rejected(t *testing.T) {
	v, ctx := buildValidatorWithAbstract(t)
	env := newTestEnvelope(testNanoID1)
	result := ScriptResult{
		Mutations: []MutationOp{{
			Op:  "create",
			Key: "lnk.location." + testNanoID2 + ".someRel.identity." + testNanoID3,
			Document: map[string]interface{}{
				"class":        "someRel",
				"sourceVertex": "vtx.location." + testNanoID2,
				"targetVertex": "vtx.identity." + testNanoID3,
				"data":         map[string]interface{}{},
			},
		}},
	}
	err := v.Validate(ctx, env, result, HydratedState{}, nil)
	expectAbstractViolation(t, err, "abstractTypeSegment")
}

// TestValidate_AbstractTypeSegment_LinkTargetEndpoint_Rejected is the target-
// side twin of the source-endpoint test — §8 row 3 covers EITHER endpoint,
// and covering only the source (or only the vertex root) would leave a real
// hole.
func TestValidate_AbstractTypeSegment_LinkTargetEndpoint_Rejected(t *testing.T) {
	v, ctx := buildValidatorWithAbstract(t)
	env := newTestEnvelope(testNanoID1)
	result := ScriptResult{
		Mutations: []MutationOp{{
			Op:  "create",
			Key: "lnk.identity." + testNanoID2 + ".someRel.location." + testNanoID3,
			Document: map[string]interface{}{
				"class":        "someRel",
				"sourceVertex": "vtx.identity." + testNanoID2,
				"targetVertex": "vtx.location." + testNanoID3,
				"data":         map[string]interface{}{},
			},
		}},
	}
	err := v.Validate(ctx, env, result, HydratedState{}, nil)
	expectAbstractViolation(t, err, "abstractTypeSegment")
}

// TestValidate_AbstractTypeSegment_ConcreteTypeStillAccepted is the positive
// vector beside the four negatives above: an ordinary concrete-typed
// vertex/aspect/link mutation, in a cache that ALSO carries an abstract DDL,
// must still pass cleanly — proving the gate is about the SEGMENT's own
// resolution, not a blanket new rejection.
func TestValidate_AbstractTypeSegment_ConcreteTypeStillAccepted(t *testing.T) {
	v, ctx := buildValidatorWithAbstract(t)
	env := newTestEnvelope(testNanoID1)
	result := ScriptResult{
		Mutations: []MutationOp{{
			Op:  "create",
			Key: "vtx.identity." + testNanoID2,
			Document: map[string]interface{}{
				"class": "identity",
				"data":  map[string]interface{}{"name": "Andrew"},
			},
		}},
	}
	if err := v.Validate(ctx, env, result, HydratedState{}, nil); err != nil {
		t.Fatalf("a concrete-typed mutation must pass even when an abstract DDL is in the cache: %v", err)
	}
}

// TestValidate_AbstractClass_Rejected pins §8 row 4: a document whose `class`
// resolves to an abstract DDL is rejected even though its KEY's type segment
// is an ordinary concrete type ("identity") — proving this is an addition to
// Contract #1 §1.5's permissive default (a DDL IS found here), not the same
// gate as the key-segment check.
func TestValidate_AbstractClass_Rejected(t *testing.T) {
	v, ctx := buildValidatorWithAbstract(t)
	env := newTestEnvelope(testNanoID1)
	result := ScriptResult{
		Mutations: []MutationOp{{
			Op:  "create",
			Key: "vtx.identity." + testNanoID2,
			Document: map[string]interface{}{
				"class": "location",
				"data":  map[string]interface{}{},
			},
		}},
	}
	err := v.Validate(ctx, env, result, HydratedState{}, nil)
	expectAbstractViolation(t, err, "abstractClass")
}

// TestValidate_AbstractTypeSegment_TombstoneExempt pins the exemption: a
// tombstone against a key whose type segment resolves to an abstract DDL
// must pass — removing an instance is the corrective path off a type that
// was (or became) abstract, and a tombstone can never CREATE an instance.
func TestValidate_AbstractTypeSegment_TombstoneExempt(t *testing.T) {
	v, ctx := buildValidatorWithAbstract(t)
	env := newTestEnvelope(testNanoID1)
	result := ScriptResult{
		Mutations: []MutationOp{{
			Op:  "tombstone",
			Key: "vtx.location." + testNanoID2,
		}},
	}
	if err := v.Validate(ctx, env, result, HydratedState{}, nil); err != nil {
		t.Fatalf("a tombstone of an abstract-typed key must be exempt from the segment gate: %v", err)
	}
}

// TestValidate_AbstractClass_TombstoneExempt is the class-gate twin: a
// tombstone whose document class resolves to an abstract DDL must also pass.
func TestValidate_AbstractClass_TombstoneExempt(t *testing.T) {
	v, ctx := buildValidatorWithAbstract(t)
	env := newTestEnvelope(testNanoID1)
	result := ScriptResult{
		Mutations: []MutationOp{{
			Op:  "tombstone",
			Key: "vtx.identity." + testNanoID2,
			Document: map[string]interface{}{
				"class": "location",
				"data":  map[string]interface{}{},
			},
		}},
	}
	if err := v.Validate(ctx, env, result, HydratedState{}, nil); err != nil {
		t.Fatalf("a tombstone whose class resolves to an abstract DDL must be exempt from the class gate: %v", err)
	}
}

// buildValidatorWithRealAbstract seeds an abstract DDL in the SAME shape
// pkgmgr's buildInstallBatch actually writes — a NanoID root carrying
// data.abstract, with the canonicalName on a SEPARATE aspect key — rather
// than the shadow-key (root-only) fixture the rest of this file uses. The
// canonicalName is "reallocation" to avoid colliding with the shadow-key
// "location" fixture other tests seed.
func buildValidatorWithRealAbstract(t *testing.T) (*ValidatorImpl, context.Context) {
	t.Helper()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	const nanoID = "Bb2Cc3Dd4Ee5Ff6Gg7Hh"
	root := "vtx.meta." + nanoID
	rootDoc := []byte(`{"class":"meta.ddl.vertexType","isDeleted":false,"data":{"abstract":true}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, root, rootDoc); err != nil {
		t.Fatalf("seed root: %v", err)
	}
	cnDoc := []byte(`{"class":"canonicalName","isDeleted":false,"data":{"value":"reallocation"},"vertexKey":"` + root + `","localName":"canonicalName"}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, root+".canonicalName", cnDoc); err != nil {
		t.Fatalf("seed canonicalName aspect: %v", err)
	}
	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	return NewValidator(cache, conn, testCoreBucket, testLogger()), ctx
}

// TestValidate_AbstractTypeSegment_RealInstallShape closes the test seam
// item: the segment gate rejects an abstract-typed key when the DDL was
// loaded from the REAL install shape (NanoID root + separate .canonicalName
// aspect), not just the shadow-key fixture every other test in this file
// exercises.
func TestValidate_AbstractTypeSegment_RealInstallShape(t *testing.T) {
	v, ctx := buildValidatorWithRealAbstract(t)
	env := newTestEnvelope(testNanoID1)
	result := ScriptResult{
		Mutations: []MutationOp{{
			Op:  "create",
			Key: "vtx.reallocation." + testNanoID2,
			Document: map[string]interface{}{
				"class": "unrelated",
				"data":  map[string]interface{}{},
			},
		}},
	}
	err := v.Validate(ctx, env, result, HydratedState{}, nil)
	expectAbstractViolation(t, err, "abstractTypeSegment")
}

// TestValidate_AbstractClass_RealInstallShape is the class-gate twin against
// the real install shape.
func TestValidate_AbstractClass_RealInstallShape(t *testing.T) {
	v, ctx := buildValidatorWithRealAbstract(t)
	env := newTestEnvelope(testNanoID1)
	result := ScriptResult{
		Mutations: []MutationOp{{
			Op:  "create",
			Key: "vtx.identity." + testNanoID2,
			Document: map[string]interface{}{
				"class": "reallocation",
				"data":  map[string]interface{}{},
			},
		}},
	}
	err := v.Validate(ctx, env, result, HydratedState{}, nil)
	expectAbstractViolation(t, err, "abstractClass")
}

// TestValidate_AbstractEventClass_Rejected pins the event-side twin of §8 row
// 4: an event whose class resolves to an abstract DDL is rejected exactly
// like a document whose class does, via the same resolveGoverningDDL path.
func TestValidate_AbstractEventClass_Rejected(t *testing.T) {
	v, ctx := buildValidatorWithAbstract(t)
	env := newTestEnvelope(testNanoID1)
	result := ScriptResult{
		Events: []EventSpec{{Class: "location", Data: map[string]interface{}{}}},
	}
	err := v.Validate(ctx, env, result, HydratedState{}, nil)
	expectAbstractViolation(t, err, "abstractEventClass")
}

// TestValidate_AbstractEventClass_ConcreteAccepted is the positive vector
// beside the negative above: an event whose class resolves EXACTLY to a
// CONCRETE DDL ("identity", the baseline fixture setupTestPipeline seeds —
// the same DDL TestValidate_CleanPass's mutation resolves against), in a
// cache that also carries an abstract DDL, must still pass. This exercises
// the ok==true, ref.Abstract==false branch specifically — a class that
// resolves to NOTHING would only prove the ok==false branch, which is
// already the well-established permissive default exercised throughout this
// file (e.g. every "unrelated"-classed fixture above).
func TestValidate_AbstractEventClass_ConcreteAccepted(t *testing.T) {
	v, ctx := buildValidatorWithAbstract(t)
	env := newTestEnvelope(testNanoID1)
	result := ScriptResult{
		Events: []EventSpec{{Class: "identity", Data: map[string]interface{}{}}},
	}
	if err := v.Validate(ctx, env, result, HydratedState{}, nil); err != nil {
		t.Fatalf("an event whose class resolves to a concrete DDL must pass: %v", err)
	}
}

// TestValidate_AbstractEventClass_RealInstallShape closes the same test seam
// item the mutation-side gate closes: the event-class gate must reject an
// abstract-typed class when the DDL was loaded from the REAL install shape
// (NanoID root + separate .canonicalName aspect), not just the shadow-key
// fixture the other tests in this file exercise.
func TestValidate_AbstractEventClass_RealInstallShape(t *testing.T) {
	v, ctx := buildValidatorWithRealAbstract(t)
	env := newTestEnvelope(testNanoID1)
	result := ScriptResult{
		Events: []EventSpec{{Class: "reallocation", Data: map[string]interface{}{}}},
	}
	err := v.Validate(ctx, env, result, HydratedState{}, nil)
	expectAbstractViolation(t, err, "abstractEventClass")
}

// TestValidate_NonAbstractWorld_Regression proves an abstract DDL's mere
// PRESENCE in the cache changes nothing for writes that never touch it —
// the inertness invariant this whole increment depends on (§17.1: "no
// package sets Abstract" today, but this test proves the co-existence case
// too, ahead of Fire B actually declaring one).
func TestValidate_NonAbstractWorld_Regression(t *testing.T) {
	v, ctx := buildValidatorWithAbstract(t)
	env := newTestEnvelope(testNanoID1)
	result := ScriptResult{
		Mutations: []MutationOp{{
			Op:  "create",
			Key: "vtx.identity." + testNanoID2 + ".workEmail",
			Document: map[string]interface{}{
				"class":     "identity",
				"vertexKey": "vtx.identity." + testNanoID2,
				"localName": "workEmail",
				"data":      map[string]interface{}{"value": "x@y"},
			},
		}},
	}
	if err := v.Validate(ctx, env, result, HydratedState{}, nil); err != nil {
		t.Fatalf("an ordinary write must be unaffected by an unrelated abstract DDL's presence: %v", err)
	}
}
