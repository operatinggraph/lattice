package processor

import (
	"context"
	"testing"
)

// Two further valid Contract #1 NanoIDs (20 chars, Alphabet without I/l/O/0),
// for the meta-vertex roots these tests register types under. Distinct from
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

// buildValidatorWithSeededMeta commits the supplied meta-vertices to Core KV,
// refreshes a DDL cache over them, and returns a Validator reading that cache.
// It is how the reserved-name gate's already-installed fallback is exercised:
// the mutation under test carries only the aspect, so the owner's class can be
// learned from nowhere but the cache.
func buildValidatorWithSeededMeta(t *testing.T, seeds ...seededMetaVertex) (*ValidatorImpl, context.Context) {
	t.Helper()
	ctx, conn, _, _, _ := setupTestPipeline(t)
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
	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	return NewValidator(cache, conn, testCoreBucket, testLogger()), ctx
}

// canonicalNameAspectMutation builds the `.canonicalName` aspect write that
// registers `name` for the meta-vertex at nanoID — shape (1) of the two a
// registration can take.
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
// carrying the name in `data.canonicalName` — shape (2), the root-only
// registration the DDL cache reads as its fallback. An empty name leaves
// `data` bare, which is how the root looks when the name rides on the aspect.
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

// TestValidate_ReservedTypeName_CanonicalNameAspect_RootInSameBatch_Rejected
// pins the shape a package install actually writes — root and `.canonicalName`
// aspect in ONE batch — for both reserved names. The owner's vertexType class
// is knowable only from the batch here: the cache has never seen this
// meta-vertex.
func TestValidate_ReservedTypeName_CanonicalNameAspect_RootInSameBatch_Rejected(t *testing.T) {
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
// carried on the ROOT's own `data.canonicalName`, with no aspect anywhere. The
// DDL cache reads this shape as a registration, so a gate that only watched the
// aspect would leave it as a single-mutation bypass.
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

// TestValidate_ReservedTypeName_InstalledOwner_Rejected pins the fallback: the
// meta-vertex is already installed as a vertexType DDL and only its
// `.canonicalName` aspect is rewritten, so the owner's class comes from the DDL
// cache rather than the batch. Renaming a live type INTO a reserved name is the
// same registration as declaring one.
func TestValidate_ReservedTypeName_InstalledOwner_Rejected(t *testing.T) {
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

// TestValidate_ReservedTypeName_UnresolvableOwner_Rejected pins the fail-closed
// direction. The aspect arrives with its owner in neither the batch nor the
// cache — exactly what splitting a registration across two operations produces
// — and is rejected rather than admitted, because an owner whose class nothing
// declares cannot be shown NOT to be a vertex type.
func TestValidate_ReservedTypeName_UnresolvableOwner_Rejected(t *testing.T) {
	v, ctx := buildValidatorWithSeededMeta(t)
	env := newTestEnvelope(testNanoID1)
	result := ScriptResult{
		Mutations: []MutationOp{canonicalNameAspectMutation("create", testMetaNanoID2, "op")},
	}
	err := v.Validate(ctx, env, result, HydratedState{})
	expectReservedTypeViolation(t, err)
}

// TestValidate_ReservedTypeName_OrdinaryVertexTypeAccepted is the positive
// vector beside every rejection above: the identical two shapes, differing only
// in the NAME they register, must commit cleanly. Without it a gate that
// rejected all vertexType registrations — or a fixture too malformed to reach
// the gate at all — would look exactly like a working one.
func TestValidate_ReservedTypeName_OrdinaryVertexTypeAccepted(t *testing.T) {
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
			t.Fatalf("registering an ordinary vertex type must pass: %v", err)
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
			t.Fatalf("registering an ordinary vertex type must pass: %v", err)
		}
	})
}

// TestValidate_ReservedTypeName_NonVertexTypeMetaVertexAllowed pins the gate's
// boundary: §1.2 reserves the names against vertex TYPE registration, not
// against every meta-vertex that happens to carry a canonicalName. A lens named
// "meta" registers no type — its name lands in a lens reference, never in a
// key's type segment — so it passes, whether its class is declared by the batch
// or already installed.
func TestValidate_ReservedTypeName_NonVertexTypeMetaVertexAllowed(t *testing.T) {
	t.Run("class from the batch", func(t *testing.T) {
		v, ctx := buildValidatorWithSeededMeta(t)
		env := newTestEnvelope(testNanoID1)
		result := ScriptResult{
			Mutations: []MutationOp{
				metaRootMutation("create", testMetaNanoID1, "meta.lens", ""),
				canonicalNameAspectMutation("create", testMetaNanoID1, "meta"),
			},
		}
		if err := v.Validate(ctx, env, result, HydratedState{}); err != nil {
			t.Fatalf("a lens meta-vertex named %q registers no vertex type and must pass: %v", "meta", err)
		}
	})
	t.Run("class from the installed cache", func(t *testing.T) {
		v, ctx := buildValidatorWithSeededMeta(t, seededMetaVertex{
			nanoID:        testMetaNanoID1,
			class:         "meta.lens",
			canonicalName: "listing",
		})
		env := newTestEnvelope(testNanoID1)
		result := ScriptResult{
			Mutations: []MutationOp{canonicalNameAspectMutation("update", testMetaNanoID1, "meta")},
		}
		if err := v.Validate(ctx, env, result, HydratedState{}); err != nil {
			t.Fatalf("renaming an installed lens to %q registers no vertex type and must pass: %v", "meta", err)
		}
	})
}

// TestValidate_ReservedTypeName_TombstoneExempt pins the exemption, in both
// shapes: tombstoning a reserved-name registration is the corrective path off
// one that should never have existed, and a tombstone can only ever remove a
// registration, never create one.
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

// TestValidate_ReservedTypeName_OrdinaryVertexUnaffected proves the gate reads
// the OWNER's type segment and not merely the aspect's local name: a
// `canonicalName` aspect on a business vertex — a role vertex is the shipping
// example — carries no type registration at all, so even the literal value
// "meta" passes.
func TestValidate_ReservedTypeName_OrdinaryVertexUnaffected(t *testing.T) {
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
		t.Fatalf("a canonicalName aspect on a business vertex registers no type and must pass: %v", err)
	}
}
