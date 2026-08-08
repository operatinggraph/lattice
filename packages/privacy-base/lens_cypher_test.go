package privacybase

// Rule-engine proof of the shredStatus lens (Fire 4b): drives the spec
// through the `full` engine — the engine selected at activation via
// engine:"full" — against an embedded NATS Core/Adjacency KV (the same
// harness clinic-domain / lease-signing use for their lens cypher tests).
//
// What it proves the unit/structure tests cannot:
//   - the boolean WHERE (`i.piiKey.data.shredded = true`) keeps un-shredded
//     piiKey holders and piiKey-less identities OUT — the read model is a
//     shred ledger, not a key inventory;
//   - the null-safe aspect-hops project null for not-yet-recorded
//     finalization steps (the "in flight" rendering) and true once recorded.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/lenstest"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

func putShredVtx(t *testing.T, coreKV *substrate.KV, id string, piiKeyData map[string]any) string {
	t.Helper()
	ctx := context.Background()
	key := "vtx.identity." + id
	body := map[string]any{"key": key, "class": "identity", "isDeleted": false, "data": map[string]any{}}
	raw, _ := json.Marshal(body)
	_, err := coreKV.Put(ctx, key, raw)
	require.NoError(t, err)
	if piiKeyData != nil {
		aKey := key + ".piiKey"
		aBody := map[string]any{"key": aKey, "class": "piiKey", "vertexKey": key, "localName": "piiKey", "isDeleted": false, "data": piiKeyData}
		aRaw, _ := json.Marshal(aBody)
		_, err = coreKV.Put(ctx, aKey, aRaw)
		require.NoError(t, err)
	}
	return key
}

func TestShredStatusLens_ProjectsOnlyShreddedIdentities(t *testing.T) {
	adjKV, coreKV := lenstest.KVs(t)

	// In-flight shred: shredded, neither finalization step recorded yet.
	inflightKey := putShredVtx(t, coreKV, "AAshredPendingAAAAAA", map[string]any{
		"wrappedDEK": "abc", "shredded": true, "shreddedAt": "2026-07-02T10:10:00Z",
	})
	// Fully finalized shred: both steps recorded.
	doneKey := putShredVtx(t, coreKV, "AAshredFinishedAAAAA", map[string]any{
		"wrappedDEK": "def", "shredded": true, "shreddedAt": "2026-07-02T10:11:00Z",
		"vaultKeyDestroyed": true, "vaultKeyDestroyedAt": "2026-07-02T10:12:00Z",
		"projectionsNullified": true, "projectionsNullifiedAt": "2026-07-02T10:13:00Z",
	})
	// Excluded: an unshredded piiKey holder and a piiKey-less identity.
	putShredVtx(t, coreKV, "AAshredUnshreddedAAA", map[string]any{"wrappedDEK": "ghi", "shredded": false})
	putShredVtx(t, coreKV, "AAshredNoPiiKeyAAAAA", nil)

	eng := full.New()
	cr, err := eng.Parse(shredStatusSpec)
	require.NoError(t, err, "shredStatus cypher must parse on the full engine")
	now := time.Now().UTC().Format(time.RFC3339)
	rows, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"now": now, "projectedAt": now,
	}}, adjKV, coreKV)
	require.NoError(t, err)

	byKey := map[string]ruleengine.ProjectionResult{}
	for _, r := range rows {
		k, _ := r.Values["key"].(string)
		byKey[k] = r
	}
	require.Len(t, byKey, 2, "only the two SHREDDED identities may project; got %v", byKey)

	inflight := byKey[inflightKey].Values
	require.Equal(t, true, inflight["shredded"])
	require.Nil(t, inflight["vaultKeyDestroyed"], "not-yet-recorded step must project null (in flight)")
	require.Nil(t, inflight["projectionsNullified"])

	done := byKey[doneKey].Values
	require.Equal(t, true, done["vaultKeyDestroyed"])
	require.Equal(t, true, done["projectionsNullified"])
	require.Equal(t, "2026-07-02T10:12:00Z", done["vaultKeyDestroyedAt"])
	require.Equal(t, "2026-07-02T10:13:00Z", done["projectionsNullifiedAt"])
}

// TestPiiKeyEnvelopeLens_ProjectsOnlyIdentitiesWithAnEnvelope proves the
// piiKeyEnvelope lens (object-store-crypto-shred-design.md §9 Fire 4
// Increment 1 — the P5-compliant read seam a vertical app uses instead of
// Loupe's direct Core-KV read): the `keyId <> null` aspect-presence guard
// admits both a real envelope AND a ShredIdentityKey empty-wrappedDEK
// placeholder (a shredded identity's row still projects — WrapKey/UnwrapKey
// then fails closed on it, which is correct), and keeps piiKey-less
// identities out entirely.
func TestPiiKeyEnvelopeLens_ProjectsOnlyIdentitiesWithAnEnvelope(t *testing.T) {
	adjKV, coreKV := lenstest.KVs(t)

	realKey := putShredVtx(t, coreKV, "AArea1Enve1opeAAAAAA", map[string]any{
		"wrappedDEK": "d2FyID09PT0=", "keyId": "vtx.identity.AArea1Enve1opeAAAAAA",
		"kekVersion": "v1", "alg": "AES-256-GCM", "shredded": false,
	})
	placeholderKey := putShredVtx(t, coreKV, "AAp1aceho1derAAAAAAA", map[string]any{
		"wrappedDEK": "", "keyId": "vtx.identity.AAp1aceho1derAAAAAAA",
		"kekVersion": "", "alg": "", "shredded": true,
	})
	putShredVtx(t, coreKV, "AAnoPiiKeyAAAAAAAAAA", nil)

	eng := full.New()
	cr, err := eng.Parse(piiKeyEnvelopeSpec)
	require.NoError(t, err, "piiKeyEnvelope cypher must parse on the full engine")
	now := time.Now().UTC().Format(time.RFC3339)
	rows, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"now": now, "projectedAt": now,
	}}, adjKV, coreKV)
	require.NoError(t, err)

	byKey := map[string]ruleengine.ProjectionResult{}
	for _, r := range rows {
		k, _ := r.Values["key"].(string)
		byKey[k] = r
	}
	require.Len(t, byKey, 2, "only identities WITH a piiKey aspect may project; got %v", byKey)

	real := byKey[realKey].Values
	require.Equal(t, "d2FyID09PT0=", real["wrappedDEK"])
	require.Equal(t, "v1", real["kekVersion"])
	require.Equal(t, "AES-256-GCM", real["alg"])
	require.Equal(t, false, real["shredded"], "an unshredded identity's row must project shredded=false — a bridge/app consumer OR's this into its Decrypt/Encrypt shred check (sensitive-param-egress-design.md §3.2/§3.5)")

	placeholder := byKey[placeholderKey].Values
	require.Equal(t, "", placeholder["wrappedDEK"], "a shredded placeholder still projects — WrapKey/UnwrapKey fails closed on the empty key, not this lens")
	require.Equal(t, true, placeholder["shredded"], "shredded must be projected (not silently dropped) so a Vault-process restart cannot re-admit a shredded identity's PII via this lens")
}

// putRetentionClassVtx seeds a retention-class holder — the vertex plus its
// `.retentionPolicy` declaration, and optionally a `.piiKey` envelope — the
// shape internal/pkgmgr/build.go writes at install.
func putRetentionClassVtx(t *testing.T, coreKV *substrate.KV, id string,
	policyData, piiKeyData map[string]any) string {
	t.Helper()
	ctx := context.Background()
	key := "vtx.retentionclass." + id
	body := map[string]any{"key": key, "class": "retentionclass", "isDeleted": false, "data": map[string]any{}}
	raw, _ := json.Marshal(body)
	_, err := coreKV.Put(ctx, key, raw)
	require.NoError(t, err)
	put := func(localName string, data map[string]any) {
		if data == nil {
			return
		}
		aKey := key + "." + localName
		aBody := map[string]any{"key": aKey, "class": localName, "vertexKey": key,
			"localName": localName, "isDeleted": false, "data": data}
		aRaw, _ := json.Marshal(aBody)
		_, err := coreKV.Put(ctx, aKey, aRaw)
		require.NoError(t, err)
	}
	put("retentionPolicy", policyData)
	put("piiKey", piiKeyData)
	return key
}

// TestRetentionKeyStatusLens_ProjectsEveryDeclaredClass proves the operator
// read model for the OTHER holder kind (retention-class-key-custody-design.md
// §4.4), and specifically the one way it deliberately differs from
// shredStatus: its anchor predicate is the class's own DECLARATION, not
// `shredded = true`.
//
// That difference is the assertion worth making. A retention class is a
// standing declaration with a policy and a period, and the operator question
// is "which of my classes are live and which have expired" — unanswerable from
// a read model filtered to the shredded ones. So a live, never-shredded class
// must project with null shred fields, which is exactly what a shredStatus-
// shaped WHERE would have dropped.
func TestRetentionKeyStatusLens_ProjectsEveryDeclaredClass(t *testing.T) {
	adjKV, coreKV := lenstest.KVs(t)

	// A live class that has never been shredded — the case a shredded-only
	// filter would wrongly drop.
	liveKey := putRetentionClassVtx(t, coreKV, "RCkeyLVEabcdefghijkm", map[string]any{
		"canonicalName": "clinicalRecord", "policy": "eraseOnExpiry",
		"retentionPeriod": "P7Y", "description": "retained clinical records",
	}, nil)
	// A shredded class whose Vault destruction landed but whose projection
	// rebuild has not yet been attested — the in-flight rendering.
	inflightKey := putRetentionClassVtx(t, coreKV, "RCkeyNFLGHTabcdefghi", map[string]any{
		"canonicalName": "underwritingRecord", "policy": "eraseOnExpiry",
		"retentionPeriod": "P3Y", "description": "retained underwriting records",
	}, map[string]any{
		"wrappedDEK": "abc", "shredded": true, "shreddedAt": "2026-08-08T10:10:00Z",
		"vaultKeyDestroyed": true, "vaultKeyDestroyedAt": "2026-08-08T10:11:00Z",
	})
	// A fully finalized destruction.
	doneKey := putRetentionClassVtx(t, coreKV, "RCkeyDNEabcdefghijkm", map[string]any{
		"canonicalName": "expiredRecord", "policy": "eraseOnExpiry",
		"retentionPeriod": "P1Y", "description": "an expired class",
	}, map[string]any{
		"wrappedDEK": "def", "shredded": true, "shreddedAt": "2026-08-08T10:12:00Z",
		"vaultKeyDestroyed": true, "vaultKeyDestroyedAt": "2026-08-08T10:13:00Z",
		"projectionsRebuilt": true, "projectionsRebuiltAt": "2026-08-08T10:14:00Z",
	})
	// Excluded: a bare retentionclass vertex carrying no declaration. Install
	// never mints one, but a hand-seeded vertex is not a retention class and
	// must not read as one.
	putRetentionClassVtx(t, coreKV, "RCkeyBAREabcdefghijk", nil, nil)

	eng := full.New()
	cr, err := eng.Parse(retentionKeyStatusSpec)
	require.NoError(t, err, "retentionKeyStatus cypher must parse on the full engine")
	now := time.Now().UTC().Format(time.RFC3339)
	rows, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"now": now, "projectedAt": now,
	}}, adjKV, coreKV)
	require.NoError(t, err)

	byKey := map[string]ruleengine.ProjectionResult{}
	for _, r := range rows {
		k, _ := r.Values["key"].(string)
		byKey[k] = r
	}
	require.Len(t, byKey, 3, "every DECLARED class projects, shredded or not; got %v", byKey)

	live := byKey[liveKey].Values
	require.Equal(t, "clinicalRecord", live["canonicalName"])
	require.Equal(t, "eraseOnExpiry", live["policy"])
	require.Equal(t, "P7Y", live["retentionPeriod"])
	require.Nil(t, live["shredded"], "a live class projects a null shred state, not a missing row")
	require.Nil(t, live["shreddedAt"])

	inflight := byKey[inflightKey].Values
	require.Equal(t, true, inflight["shredded"])
	require.Equal(t, true, inflight["vaultKeyDestroyed"])
	require.Nil(t, inflight["projectionsRebuilt"], "an un-attested rebuild must project null (in flight)")

	done := byKey[doneKey].Values
	require.Equal(t, true, done["projectionsRebuilt"])
	require.Equal(t, "2026-08-08T10:14:00Z", done["projectionsRebuiltAt"])
	require.Equal(t, doneKey, done["retentionClassKey"])
}
