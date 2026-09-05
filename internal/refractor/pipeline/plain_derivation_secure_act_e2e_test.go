// A Secure Lens retracting a two-hop drop-out, end to end over a real embedded
// NATS server — Increment 2 of secure-plain-lens-retraction-and-audit-design.md
// (§4.2, §10). It is the Secure twin of
// TestPlainDerivationAct_RetractsOnANonIncidentLinkRemoval_E2E and keeps that
// test's pair structure: the SUBJECT carries an enrolled, ticking auditor and
// the CONTROL does not, so the licence is the one thing that differs and the
// subject's retraction cannot be an outcome both lenses would have reached.
//
// The target is a GUARDED, soft-delete NATS-KV bucket, not `adapter.
// ProtectedAdapter`'s Postgres table — this family stands up an embedded NATS
// server and no Postgres. What the two share is exactly the property under
// test — `NatsKVAdapter.SetGuarded(true)` makes every Delete a soft tombstone
// stamped with the event's projection sequence, the same watermark-carrying
// retraction `PostgresAdapter` writes for a protected table, and `GetRow`
// reports the row absent through it.
//
// What this does NOT exercise: `adapter.ProtectedAdapter`'s own guarded-delete
// path and the RLS read-back a real Protected Postgres target serves through.
// A Postgres-backed e2e fixture exists
// (internal/refractor/structural_pause_recovery_e2e_test.go), gated on
// POSTGRES_TEST_DSN because it needs a live database; the ephemeral-stack
// check (design §10) is what covers RLS.
package pipeline_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/vault"
)

// securePlainActSpec is plainActEmploymentSpec plus a decrypt-at-projection
// column: a REQUIRED two-hop chain whose middle link is incident on neither the
// anchor nor the row's value source, so removing `org -locatedIn-> location`
// drops the provider out of the matched set with no event ever naming it.
const securePlainActSpec = `
MATCH (pr:provider)-[:employedBy]->(org:org)-[:locatedIn]->(loc:location)
RETURN pr.key AS key, loc.data.city AS city, pr.ssn.data AS ssn
`

// securePlainActLens is one installed Secure Lens plus the handles the
// assertions read. The adapter is kept because the retraction under test is a
// TOMBSTONE — a live NATS-KV key whose document says the row is gone — so
// "the row reads absent" is a question only the read path answers.
type securePlainActLens struct {
	pipeline *pipeline.Pipeline
	targetKV *substrate.KV
	adapter  *adapter.NatsKVAdapter
}

// newSecurePlainActVault returns a local vault backend for the fixture's
// decrypt-at-projection columns.
func newSecurePlainActVault(t *testing.T) *vault.LocalBackend {
	t.Helper()
	kek := make([]byte, 32)
	_, err := rand.Read(kek)
	require.NoError(t, err)
	v, err := vault.NewLocalBackend(kek, "secure-plain-act-kek-v1")
	require.NoError(t, err)
	return v
}

// putSecureAspect gives holderKey a `ssn` aspect holding a real ciphertext
// envelope plus the piiKey aspect that opens it — the two Core KV writes the
// Processor's own commit path makes for a sensitive aspect, so the decryptor
// under test resolves custody exactly as it does live.
func putSecureAspect(t *testing.T, env *pipelineEnv, v *vault.LocalBackend, holderKey, value string) {
	t.Helper()
	ctx := context.Background()
	envelope, err := v.CreateIdentityKey(ctx, holderKey)
	require.NoError(t, err)
	plaintext, err := json.Marshal(map[string]any{"value": value})
	require.NoError(t, err)
	ct, err := v.Encrypt(ctx, holderKey, envelope, plaintext)
	require.NoError(t, err)
	raw, err := json.Marshal(ct)
	require.NoError(t, err)
	var ctMap map[string]any
	require.NoError(t, json.Unmarshal(raw, &ctMap))

	putAspectDoc(t, env, holderKey+".ssn", map[string]any{
		"key": holderKey + ".ssn", "class": "ssn", "vertexKey": holderKey,
		"localName": "ssn", "isDeleted": false, "data": ctMap,
	})
	putAspectDoc(t, env, holderKey+".piiKey", map[string]any{
		"class": "piiKey", "vertexKey": holderKey, "localName": "piiKey",
		"isDeleted": false, "data": envelope,
	})
}

func putAspectDoc(t *testing.T, env *pipelineEnv, key string, body map[string]any) {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	_, err = env.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
}

// installSecurePlainActLens builds and runs a Secure Lens over env's graph in
// `act` mode against a guarded, soft-delete target. audited is the single
// conjunct that separates the subject from the control, exactly as
// installPlainActLens uses it.
func installSecurePlainActLens(t *testing.T, env *pipelineEnv, v *vault.LocalBackend,
	ruleID, bucket string, audited bool) *securePlainActLens {
	t.Helper()
	ctx := context.Background()
	eng, cr := compileFullRule(t, securePlainActSpec, []string{"key"})

	_, err := env.js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: bucket})
	require.NoError(t, err)
	targetKV, err := env.conn.OpenKV(ctx, bucket)
	require.NoError(t, err)
	adpt, err := adapter.New(targetKV, []string{"key"}, adapter.DeleteModeSoft)
	require.NoError(t, err)
	adpt.SetGuarded(true)

	p, err := pipeline.New(ruleID, "nats_kv", coreKVBucket, env.adjKV, env.coreKV, adpt,
		newHealthReporter(t, env, ruleID))
	require.NoError(t, err)
	require.NoError(t, p.UseFullEngine(eng, cr))
	dec, err := pipeline.NewSecureDecryptor(v, env.coreKV, []pipeline.SecureColumn{
		{Column: "ssn", HolderTypes: []string{"provider"}, Field: "value"},
	}, nil)
	require.NoError(t, err)
	p.SetSecureDecryptor(dec)
	p.SetAnchorDerivationMode(pipeline.DerivationModeAct)

	if audited {
		enrolled, refusal := p.InstallAudit(pipeline.AuditOptions{Interval: plainActAuditInterval})
		require.True(t, enrolled, "a Secure Lens must enrol under the mask, or its licence can never hold; refusal: %s", refusal)
		ctx, cancel := context.WithCancel(context.Background())
		wg := &sync.WaitGroup{}
		wg.Add(1)
		go func() { defer wg.Done(); p.RunAudit(ctx) }()
		t.Cleanup(func() { cancel(); wg.Wait() })
	} else {
		require.Nil(t, p.Auditor(), "the control lens must carry no auditor at all")
	}

	startPipeline(t, env, p, ruleID)
	return &securePlainActLens{pipeline: p, targetKV: targetKV, adapter: adpt}
}

// awaitVerdict blocks until the lens's audit has COMPARED an anchor, which is
// the licence's own precondition — see plainActLens.awaitPlainActVerdict for
// why it cannot be waited out at install time.
func (l *securePlainActLens) awaitVerdict(t *testing.T) {
	t.Helper()
	a := l.pipeline.Auditor()
	require.NotNil(t, a, "only an audited lens has a verdict to wait for")
	pollUntil(t, 30*time.Second, func() bool {
		st := a.Status()
		return !st.LastPassAt.IsZero() && st.Audited > 0
	})
	require.Empty(t, a.Status().Suppression,
		"a suppressed audit refuses the licence just as a missing one does")
}

// liveRow returns the row the read path serves for key, and whether it serves
// one at all. A soft tombstone is a live NATS-KV key, so this is the only
// question whose answer distinguishes a retracted row from a standing one.
func (l *securePlainActLens) liveRow(t *testing.T, key string) (map[string]any, bool) {
	t.Helper()
	row, present, err := l.adapter.GetRow(context.Background(), map[string]any{"key": key})
	require.NoError(t, err)
	return row, present
}

// storedDoc returns the raw document behind key — the tombstone itself, which
// the read path deliberately hides.
func (l *securePlainActLens) storedDoc(t *testing.T, key string) map[string]any {
	t.Helper()
	entry, err := l.targetKV.Get(context.Background(), key)
	require.NoError(t, err)
	require.NotNil(t, entry)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(entry.Value, &doc))
	return doc
}

// TestPlainDerivation_SecureLensNeighbourDropOutRetracts is §10's Increment-2
// e2e: a Secure Lens in `act` mode retracts a two-hop drop-out.
//
// The derived path is the row's ONLY retraction transport here: this lens
// declares no whole-target diff, and the filter-retraction presence check
// derives its key from the EVENT vertex, which is an org. The control lens is
// what proves it — it sees the same tombstone, runs the same unseeded rescan,
// and keeps the orphan.
func TestPlainDerivation_SecureLensNeighbourDropOutRetracts(t *testing.T) {
	env := startPipelineEnv(t)
	v := newSecurePlainActVault(t)

	subject := installSecurePlainActLens(t, env, v, "secure-act-retract", "secure-act-retract-target", true)
	control := installSecurePlainActLens(t, env, v, "secure-act-retract-control", "secure-act-retract-control-target", false)
	lenses := []*securePlainActLens{subject, control}

	pr1, org1, loc1 := narrowedID(t, "SecPrA"), narrowedID(t, "SecFirmA"), narrowedID(t, "SecSiteA")
	pr2, org2, loc2 := narrowedID(t, "SecPrB"), narrowedID(t, "SecFirmB"), narrowedID(t, "SecSiteB")
	pr1Key, pr2Key := substrate.VertexKey("provider", pr1), substrate.VertexKey("provider", pr2)
	loc2Key := substrate.VertexKey("location", loc2)

	for _, o := range []string{org1, org2} {
		key := substrate.VertexKey("org", o)
		putNode(t, env.coreKV, key, map[string]any{"key": key, "class": "org"})
	}
	for _, l := range []struct{ id, city string }{{loc1, "Bristol"}, {loc2, "Leeds"}} {
		key := substrate.VertexKey("location", l.id)
		putNode(t, env.coreKV, key, map[string]any{
			"key": key, "class": "location", "data": map[string]any{"city": l.city},
		})
	}
	// The secure aspects are written BEFORE the provider vertices, so the very
	// first projection of each row already carries plaintext: a row that gained
	// its secure column on a later event would leave the retraction under test
	// running against a shape production never has.
	for i, p := range []string{pr1, pr2} {
		key := substrate.VertexKey("provider", p)
		putSecureAspect(t, env, v, key, []string{"111-11-1111", "222-22-2222"}[i])
		putNode(t, env.coreKV, key, map[string]any{"key": key, "class": "provider"})
	}
	putLink(t, env.coreKV, "provider", pr1, "employedBy", "org", org1)
	putLink(t, env.coreKV, "provider", pr2, "employedBy", "org", org2)
	putLink(t, env.coreKV, "org", org1, "locatedIn", "location", loc1)
	putLink(t, env.coreKV, "org", org2, "locatedIn", "location", loc2)

	city := func(l *securePlainActLens, key string) string {
		row, present := l.liveRow(t, key)
		if !present {
			return ""
		}
		s, _ := row["city"].(string)
		return s
	}
	for _, l := range lenses {
		pollUntil(t, 30*time.Second, func() bool { return city(l, pr1Key) == "Bristol" })
		pollUntil(t, 30*time.Second, func() bool { return city(l, pr2Key) == "Leeds" })
		row, _ := l.liveRow(t, pr1Key)
		require.Equal(t, "111-11-1111", row["ssn"],
			"the row under test must carry decrypted plaintext, or this is not a Secure Lens's retraction")
	}
	subject.awaitVerdict(t)

	// The removal: a soft tombstone on the org→location link. No event in this
	// step names a provider, and neither endpoint of the link is one.
	linkKey := "lnk.org." + org1 + ".locatedIn.location." + loc1
	raw, err := json.Marshal(map[string]any{
		"key": linkKey, "isDeleted": true,
		"createdAt": "2026-08-01T00:00:00Z", "lastModifiedAt": "2026-08-02T00:00:00Z",
	})
	require.NoError(t, err)
	_, err = env.coreKV.Put(context.Background(), linkKey, raw)
	require.NoError(t, err)

	pollUntil(t, 30*time.Second, func() bool {
		_, present := subject.liveRow(t, pr1Key)
		return !present
	})

	// What landed is a seq-guarded soft tombstone, not a physical delete: the
	// watermark is what makes a lower-seq replay lose, and only the raw document
	// carries it.
	doc := subject.storedDoc(t, pr1Key)
	require.Equal(t, true, doc["isDeleted"], "the retraction is a tombstone: %v", doc)
	require.NotNil(t, doc["projectionSeq"], "and it carries the event's watermark: %v", doc)
	require.Nil(t, doc["ssn"], "a guarded delete writes a fresh {isDeleted, projectionSeq} body — the tombstone carries no row columns, secure or otherwise")

	// The fence for the control's negative: an event on the OTHER chain, which
	// both lenses reproject observably. Once its value has landed, the link
	// removal has been fully applied — so the control's surviving row is a
	// verdict, not a message still in flight.
	putNode(t, env.coreKV, loc2Key, map[string]any{
		"key": loc2Key, "class": "location", "data": map[string]any{"city": "Leeds Central"},
		"lastModifiedAt": "2026-08-03T00:00:00Z",
	})
	for _, l := range lenses {
		pollUntil(t, 30*time.Second, func() bool { return city(l, pr2Key) == "Leeds Central" })
	}

	require.Equal(t, "Bristol", city(control, pr1Key),
		"the unlicensed lens keeps the orphan — its rescan omits the provider and nothing turns that omission into a Delete")
	_, present := subject.liveRow(t, pr1Key)
	require.False(t, present,
		"the licensed Secure Lens retracts it: the derived anchor's own seeded evaluation is what makes the omission nameable")
	require.Greater(t, subject.pipeline.AnchorDerivationShadow().Acted, int64(0),
		"the subject's events must have been decided by the derived set")
}
