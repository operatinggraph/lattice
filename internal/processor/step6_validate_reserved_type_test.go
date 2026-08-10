package processor

import (
	"context"
	"testing"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// Two further valid Contract #1 NanoIDs (20 chars, Alphabet without I/l/O/0),
// for the meta-vertex roots these tests register names under. Distinct from
// testNanoID1..3 so a meta-vertex key can never collide with the instance keys
// the surrounding suite writes.
const (
	testMetaNanoID1 = "Mm5Nn6Pp7Qq8Rr9Ss1Tt"
	testMetaNanoID2 = "Uu2Vv3Ww4Xx5Yy6Zz7Aa"
)

// seededMetaVertex is one already-installed meta-vertex for
// buildValidatorWithSeededMeta: a NanoID root carrying `class`, plus the
// separate `.canonicalName` aspect — the shape pkgmgr's install batch actually
// writes, and the shape the DDL cache indexes by meta key.
type seededMetaVertex struct {
	nanoID        string
	class         string
	canonicalName string
}

// seedMetaVertices commits the supplied meta-vertices to Core KV.
func seedMetaVertices(t *testing.T, ctx context.Context, conn substrateConn, seeds ...seededMetaVertex) {
	t.Helper()
	for _, s := range seeds {
		root := "vtx.meta." + s.nanoID
		rootDoc := []byte(`{"class":"` + s.class + `","isDeleted":false,"data":{}}`)
		if _, err := conn.KVPut(ctx, testCoreBucket, root, rootDoc); err != nil {
			t.Fatalf("seed meta root %s: %v", root, err)
		}
		cnDoc := []byte(`{"class":"canonicalName","isDeleted":false,"vertexKey":"` + root +
			`","localName":"canonicalName","data":{"value":"` + s.canonicalName + `"}}`)
		if _, err := conn.KVPut(ctx, testCoreBucket, root+".canonicalName", cnDoc); err != nil {
			t.Fatalf("seed canonicalName aspect %s: %v", root, err)
		}
	}
}

// buildValidatorWithSeededMeta seeds the supplied meta-vertices, refreshes a
// DDL cache over them, and returns a Validator reading that cache.
func buildValidatorWithSeededMeta(t *testing.T, seeds ...seededMetaVertex) (*ValidatorImpl, context.Context) {
	t.Helper()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	seedMetaVertices(t, ctx, conn, seeds...)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	return NewValidator(cache, conn, testCoreBucket, testLogger()), ctx
}

// canonicalNameAspectMutation builds the `.canonicalName` aspect write that
// registers `name` for the meta-vertex at nanoID — shape (1) of the two a
// registration can take, and the one loadMetaVertex prefers.
func canonicalNameAspectMutation(op, nanoID, name string) MutationOp {
	root := "vtx.meta." + nanoID
	return MutationOp{
		Op:  op,
		Key: root + ".canonicalName",
		Document: map[string]interface{}{
			"class":     "canonicalName",
			"vertexKey": root,
			"localName": "canonicalName",
			"data":      map[string]interface{}{"value": name},
		},
	}
}

// metaRootMutation builds the meta-vertex root write for `class`, optionally
// carrying the name in `data.canonicalName` — shape (2), the registration
// loadMetaVertex falls back to when no live aspect supplies a name. An empty
// name leaves `data` bare, which is how the root looks when the name rides on
// the aspect.
func metaRootMutation(op, nanoID, class, name string) MutationOp {
	data := map[string]interface{}{}
	if name != "" {
		data["canonicalName"] = name
	}
	return MutationOp{
		Op:  op,
		Key: "vtx.meta." + nanoID,
		Document: map[string]interface{}{
			"class": class,
			"data":  data,
		},
	}
}

func expectReservedTypeViolation(t *testing.T, err error) {
	t.Helper()
	expectAbstractViolation(t, err, "reservedVertexTypeName")
}

// TestValidate_ReservedTypeName_CanonicalNameAspect_Rejected pins the shape a
// package install actually writes — root and `.canonicalName` aspect in ONE
// batch — for both reserved names.
func TestValidate_ReservedTypeName_CanonicalNameAspect_Rejected(t *testing.T) {
	for _, name := range []string{"meta", "op"} {
		t.Run(name, func(t *testing.T) {
			v, ctx := buildValidatorWithSeededMeta(t)
			env := newTestEnvelope(testNanoID1)
			result := ScriptResult{
				Mutations: []MutationOp{
					metaRootMutation("create", testMetaNanoID1, "meta.ddl.vertexType", ""),
					canonicalNameAspectMutation("create", testMetaNanoID1, name),
				},
			}
			err := v.Validate(ctx, env, result, HydratedState{})
			expectReservedTypeViolation(t, err)
		})
	}
}

// TestValidate_ReservedTypeName_RootDocument_Rejected pins shape (2): the name
// carried on the ROOT's own `data.canonicalName`, with no aspect anywhere.
// loadMetaVertex reads this shape as a registration whenever no live aspect
// outranks it, so a gate watching only the aspect would leave it as a
// single-mutation bypass.
func TestValidate_ReservedTypeName_RootDocument_Rejected(t *testing.T) {
	for _, name := range []string{"meta", "op"} {
		t.Run(name, func(t *testing.T) {
			v, ctx := buildValidatorWithSeededMeta(t)
			env := newTestEnvelope(testNanoID1)
			result := ScriptResult{
				Mutations: []MutationOp{
					metaRootMutation("create", testMetaNanoID1, "meta.ddl.vertexType", name),
				},
			}
			err := v.Validate(ctx, env, result, HydratedState{})
			expectReservedTypeViolation(t, err)
		})
	}
}

// TestValidate_ReservedTypeName_AnyMetaVertexClass_Rejected pins the decision
// the whole gate rests on: the reservation is on the NAME AS INDEXED, not on
// the vertexType kind. DDLCache.Refresh indexes every named meta-vertex into
// byName regardless of class, and validateAbstractKeySegments resolves a key's
// type segment through DDLs.Lookup with no Kind filter — so a lens or a pane
// named "meta" is as much a type-segment authority as a vertexType DDL, and is
// refused in both registration shapes.
func TestValidate_ReservedTypeName_AnyMetaVertexClass_Rejected(t *testing.T) {
	for _, class := range []string{"meta.lens", "meta.pane", "meta.weaverTarget", "meta.ddl.aspectType"} {
		t.Run(class+"/aspect", func(t *testing.T) {
			v, ctx := buildValidatorWithSeededMeta(t)
			env := newTestEnvelope(testNanoID1)
			result := ScriptResult{
				Mutations: []MutationOp{
					metaRootMutation("create", testMetaNanoID1, class, ""),
					canonicalNameAspectMutation("create", testMetaNanoID1, "meta"),
				},
			}
			err := v.Validate(ctx, env, result, HydratedState{})
			expectReservedTypeViolation(t, err)
		})
		t.Run(class+"/root", func(t *testing.T) {
			v, ctx := buildValidatorWithSeededMeta(t)
			env := newTestEnvelope(testNanoID1)
			result := ScriptResult{
				Mutations: []MutationOp{metaRootMutation("create", testMetaNanoID1, class, "op")},
			}
			err := v.Validate(ctx, env, result, HydratedState{})
			expectReservedTypeViolation(t, err)
		})
	}
}

// TestValidate_ReservedTypeName_AbstractLensBrickShape_Rejected pins the
// concrete catastrophe the kind-agnostic rule exists to prevent: a `meta.lens`
// root marked abstract and named "meta". The DDL cache would index it as
// byName["meta"] with Abstract true, after which validateAbstractKeySegments
// refuses every non-tombstone mutation whose key carries the `meta` type
// segment — which is every meta-vertex write, i.e. every package install for
// the life of the deployment. Nothing short of refusing the registration
// recovers from it.
func TestValidate_ReservedTypeName_AbstractLensBrickShape_Rejected(t *testing.T) {
	v, ctx := buildValidatorWithSeededMeta(t)
	env := newTestEnvelope(testNanoID1)
	root := MutationOp{
		Op:  "create",
		Key: "vtx.meta." + testMetaNanoID1,
		Document: map[string]interface{}{
			"class": "meta.lens",
			"data":  map[string]interface{}{"abstract": true},
		},
	}
	result := ScriptResult{
		Mutations: []MutationOp{root, canonicalNameAspectMutation("create", testMetaNanoID1, "meta")},
	}
	err := v.Validate(ctx, env, result, HydratedState{})
	expectReservedTypeViolation(t, err)
}

// TestValidate_ReservedTypeName_LensThenClassFlip_DiesAtTheRegistration walks
// the two-operation path end to end. The hazard is real: a committed lens named
// "meta" whose root class is later flipped to meta.ddl.vertexType leaves the
// cache serving byName["meta"] with Kind "vertexType", and the FLIP itself
// carries no name to gate. The first half of this test proves that by seeding
// the committed state directly; the second half proves op A — the only mutation
// in the sequence that carries the name — is refused, so the committed state
// the flip depends on can never exist.
func TestValidate_ReservedTypeName_LensThenClassFlip_DiesAtTheRegistration(t *testing.T) {
	ctx, conn, _, _, _ := setupTestPipeline(t)

	// The hazard, demonstrated: a lens named "meta", then a class flip.
	seedMetaVertices(t, ctx, conn, seededMetaVertex{
		nanoID: testMetaNanoID1, class: "meta.lens", canonicalName: "meta",
	})
	flipped := []byte(`{"class":"meta.ddl.vertexType","isDeleted":false,"data":{}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, "vtx.meta."+testMetaNanoID1, flipped); err != nil {
		t.Fatalf("flip root class: %v", err)
	}
	hazard := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := hazard.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	ref, ok := hazard.Lookup("meta")
	if !ok || ref.Kind != "vertexType" {
		t.Fatalf("expected the flip to leave a vertexType DDL indexed under %q; got ok=%v kind=%q", "meta", ok, ref.Kind)
	}

	// The gate, against a clean world: op A is the only mutation in the whole
	// sequence that carries the name, and it is refused.
	v, vctx := buildValidatorWithSeededMeta(t)
	opA := ScriptResult{Mutations: []MutationOp{
		metaRootMutation("create", testMetaNanoID1, "meta.lens", ""),
		canonicalNameAspectMutation("create", testMetaNanoID1, "meta"),
	}}
	err := v.Validate(vctx, newTestEnvelope(testNanoID1), opA, HydratedState{})
	expectReservedTypeViolation(t, err)
}

// TestValidate_ReservedTypeName_InstalledMetaVertexRenamed_Rejected pins the
// rename path: the meta-vertex is long committed under an ordinary name and
// only its `.canonicalName` aspect is rewritten. Renaming a live meta-vertex
// INTO a reserved name is the same registration as declaring one.
func TestValidate_ReservedTypeName_InstalledMetaVertexRenamed_Rejected(t *testing.T) {
	v, ctx := buildValidatorWithSeededMeta(t, seededMetaVertex{
		nanoID:        testMetaNanoID1,
		class:         "meta.ddl.vertexType",
		canonicalName: "workorder",
	})
	env := newTestEnvelope(testNanoID1)
	result := ScriptResult{
		Mutations: []MutationOp{canonicalNameAspectMutation("update", testMetaNanoID1, "meta")},
	}
	err := v.Validate(ctx, env, result, HydratedState{})
	expectReservedTypeViolation(t, err)
}

// TestValidate_ReservedTypeName_AspectAloneRejected pins the split-across-two-
// operations shape: the aspect arrives with its owner in neither the batch nor
// the cache. Nothing about the owner is consulted, so there is no state in
// which this lands.
func TestValidate_ReservedTypeName_AspectAloneRejected(t *testing.T) {
	v, ctx := buildValidatorWithSeededMeta(t)
	env := newTestEnvelope(testNanoID1)
	result := ScriptResult{
		Mutations: []MutationOp{canonicalNameAspectMutation("create", testMetaNanoID2, "op")},
	}
	err := v.Validate(ctx, env, result, HydratedState{})
	expectReservedTypeViolation(t, err)
}

// TestValidate_ReservedTypeName_OrdinaryNameAccepted is the positive vector
// beside every rejection above: the identical shapes, differing only in the
// NAME they register, must commit cleanly. Without it a gate that refused all
// meta-vertex registrations — or a fixture too malformed to reach the gate at
// all — would look exactly like a working one.
func TestValidate_ReservedTypeName_OrdinaryNameAccepted(t *testing.T) {
	t.Run("aspect shape", func(t *testing.T) {
		v, ctx := buildValidatorWithSeededMeta(t)
		env := newTestEnvelope(testNanoID1)
		result := ScriptResult{
			Mutations: []MutationOp{
				metaRootMutation("create", testMetaNanoID1, "meta.ddl.vertexType", ""),
				canonicalNameAspectMutation("create", testMetaNanoID1, "workorder"),
			},
		}
		if err := v.Validate(ctx, env, result, HydratedState{}); err != nil {
			t.Fatalf("registering an ordinary name must pass: %v", err)
		}
	})
	t.Run("root shape", func(t *testing.T) {
		v, ctx := buildValidatorWithSeededMeta(t)
		env := newTestEnvelope(testNanoID1)
		result := ScriptResult{
			Mutations: []MutationOp{
				metaRootMutation("create", testMetaNanoID1, "meta.ddl.vertexType", "workorder"),
			},
		}
		if err := v.Validate(ctx, env, result, HydratedState{}); err != nil {
			t.Fatalf("registering an ordinary name must pass: %v", err)
		}
	})
	t.Run("lens shape", func(t *testing.T) {
		v, ctx := buildValidatorWithSeededMeta(t)
		env := newTestEnvelope(testNanoID1)
		result := ScriptResult{
			Mutations: []MutationOp{
				metaRootMutation("create", testMetaNanoID1, "meta.lens", ""),
				canonicalNameAspectMutation("create", testMetaNanoID1, "availableListings"),
			},
		}
		if err := v.Validate(ctx, env, result, HydratedState{}); err != nil {
			t.Fatalf("registering an ordinary lens name must pass: %v", err)
		}
	})
}

// TestValidate_ReservedTypeName_TombstoneExempt pins the exemption, in both
// shapes: tombstoning a reserved-name registration is the corrective path off
// one that should never have existed, and a tombstone can only ever remove a
// registration, never create one. loadMetaVertex reads a tombstoned
// canonicalName aspect as absent, so the removal genuinely retires the name —
// TestDDLCache_TombstonedCanonicalNameRetiresTheEntry pins that half.
func TestValidate_ReservedTypeName_TombstoneExempt(t *testing.T) {
	t.Run("canonicalName aspect", func(t *testing.T) {
		v, ctx := buildValidatorWithSeededMeta(t, seededMetaVertex{
			nanoID:        testMetaNanoID1,
			class:         "meta.ddl.vertexType",
			canonicalName: "workorder",
		})
		env := newTestEnvelope(testNanoID1)
		result := ScriptResult{
			Mutations: []MutationOp{canonicalNameAspectMutation("tombstone", testMetaNanoID1, "meta")},
		}
		if err := v.Validate(ctx, env, result, HydratedState{}); err != nil {
			t.Fatalf("tombstoning a reserved-name registration must be exempt: %v", err)
		}
	})
	t.Run("meta-vertex root", func(t *testing.T) {
		v, ctx := buildValidatorWithSeededMeta(t)
		env := newTestEnvelope(testNanoID1)
		result := ScriptResult{
			Mutations: []MutationOp{
				metaRootMutation("tombstone", testMetaNanoID1, "meta.ddl.vertexType", "op"),
			},
		}
		if err := v.Validate(ctx, env, result, HydratedState{}); err != nil {
			t.Fatalf("tombstoning a reserved-name registration must be exempt: %v", err)
		}
	})
}

// TestValidate_ReservedTypeName_NonMetaVertexUnaffected proves the gate reads
// the key's OWNER position and not merely the aspect's local name: a
// `canonicalName` aspect on a business vertex — a role vertex is the shipping
// example — never reaches the canonicalName index the reservation protects, so
// even the literal value "meta" passes.
func TestValidate_ReservedTypeName_NonMetaVertexUnaffected(t *testing.T) {
	v, ctx := buildValidatorWithSeededMeta(t)
	env := newTestEnvelope(testNanoID1)
	roleKey := "vtx.role." + testNanoID2
	result := ScriptResult{
		Mutations: []MutationOp{{
			Op:  "create",
			Key: roleKey + ".canonicalName",
			Document: map[string]interface{}{
				"class":     "canonicalName",
				"vertexKey": roleKey,
				"localName": "canonicalName",
				"data":      map[string]interface{}{"value": "meta"},
			},
		}},
	}
	if err := v.Validate(ctx, env, result, HydratedState{}); err != nil {
		t.Fatalf("a canonicalName aspect on a business vertex reaches no registration index and must pass: %v", err)
	}
}

// TestValidate_ReservedTypeName_OtherMetaAspectUnaffected proves the gate reads
// the LOCAL NAME too: a reserved word sitting in some other aspect of a
// meta-vertex — a description, a spec field — is not a registration and must
// not be refused.
func TestValidate_ReservedTypeName_OtherMetaAspectUnaffected(t *testing.T) {
	v, ctx := buildValidatorWithSeededMeta(t)
	env := newTestEnvelope(testNanoID1)
	root := "vtx.meta." + testMetaNanoID1
	result := ScriptResult{
		Mutations: []MutationOp{{
			Op:  "create",
			Key: root + ".description",
			Document: map[string]interface{}{
				"class":     "description",
				"vertexKey": root,
				"localName": "description",
				"data":      map[string]interface{}{"value": "meta", "text": "meta"},
			},
		}},
	}
	if err := v.Validate(ctx, env, result, HydratedState{}); err != nil {
		t.Fatalf("a non-canonicalName aspect of a meta-vertex registers nothing and must pass: %v", err)
	}
}

// TestValidate_ReservedTypeName_LinkMutationUnaffected closes the remaining key
// kind: a link whose endpoints are meta-vertices carries no canonicalName at
// all, and must fall straight through the gate.
func TestValidate_ReservedTypeName_LinkMutationUnaffected(t *testing.T) {
	v, ctx := buildValidatorWithSeededMeta(t)
	env := newTestEnvelope(testNanoID1)
	linkKey := "lnk.meta." + testMetaNanoID1 + ".subtypeOf.meta." + testMetaNanoID2
	result := ScriptResult{
		Mutations: []MutationOp{{
			Op:  "create",
			Key: linkKey,
			Document: map[string]interface{}{
				"class":        "subtypeOf",
				"sourceVertex": "vtx.meta." + testMetaNanoID1,
				"targetVertex": "vtx.meta." + testMetaNanoID2,
				"data":         map[string]interface{}{"canonicalName": "meta"},
			},
		}},
	}
	if err := v.Validate(ctx, env, result, HydratedState{}); err != nil {
		t.Fatalf("a link mutation registers no name and must pass: %v", err)
	}
	if substrate.ClassifyKey(linkKey) != substrate.KindLink {
		t.Fatalf("fixture is not a link key: %q", linkKey)
	}
}
