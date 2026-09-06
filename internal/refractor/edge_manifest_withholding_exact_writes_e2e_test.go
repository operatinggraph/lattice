// Package refractor_test — T9 of
// perentry-unchanged-entry-withholding-design.md §10: the mechanism's payoff
// measured on the REAL generated producer, in the harm's own shape.
//
// The harm (§1.2) is a link create that fans out to many actors and rewrites
// every entry of every one of them. This test builds exactly that — one
// `providedTo` create whose enumeration reaches four actors holding seven
// entries between them — and pins the counts the design claims: the event
// writes EXACTLY the entries it created, the audit subject gains exactly that
// many, every untouched entry's guard watermark stands still, and a second
// identical event writes nothing at all.
//
// DOMAIN. §10's T9 names `edgeManifestStaff`. That domain is not reachable by
// a `providedTo` create: the staff walks are the held-role and worksAt spines
// (packages/edge-manifest/lenses.go — `chainHeldRoles`, `worksAt`), and
// `providedTo` appears in exactly one walk, edgeInstances', which declares
// `domainBase`. So the producer under test is `edgeManifestReadGrants`
// (cap-read.edgeManifest), which is the one that owns the relation T9 names
// and the one the fan-out shape is real for. The design's pairing of the two
// was a slip; the mechanism, the walk and the counts are otherwise T9's.
//
// FAN-OUT. The enumeration is an undirected adjacency BFS that stops at the
// first actor on each path (pipeline.ActorEnumerator), so a `providedTo`
// create on a service instance that already serves several identities reaches
// every one of them — the widening this test needs, and the same widening the
// live measurement recorded. Seeding two instances against three identities
// and then providing one of those instances to a fourth is what turns one link
// create into four actor evaluations over seven entries.
package refractor_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/consumer"
	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/projection"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/refractor/subjects"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	edgemanifest "github.com/operatinggraph/lattice/packages/edge-manifest"
)

// storedEntry is one per-anchor grant key as the assertions read it: the KV
// revision (which moves on any landed write, including one the guard declines
// to commit) and the guard's own watermark (which moves only when a write
// actually stores a body).
type storedEntry struct {
	revision      uint64
	projectionSeq uint64
	live          bool
}

// TestEdgeManifest_E2E_LinkCreateWritesExactlyTheEntriesItChanged is T9.
//
// Three phases, each with the control that makes the next one's silence mean
// something:
//
//	(a) POPULATE — three identities, two service instances, six grant entries.
//	    The entries appearing at all is the control for every "nothing was
//	    written" assertion below: this producer demonstrably writes.
//	(b) ONE LINK CREATE — a fourth identity is provided one of the instances.
//	    The enumeration reaches all four actors and re-derives all seven
//	    entries, of which exactly ONE is new. Exactly one entry is committed,
//	    the audit subject gains exactly one, the six pre-existing entries keep
//	    their revisions AND their watermarks, and the withheld tally rises by
//	    exactly six.
//	(c) THE SAME EVENT AGAIN — the identical link body re-put. The same four
//	    actors are re-evaluated and every one of the seven entries is now
//	    unchanged: zero committed, zero new audit entries, and the withheld
//	    tally rises by exactly seven, which is the control proving the pass
//	    ran rather than the event being dropped.
//
// REVERT-PROOF: disable markUnchangedEntries and phase (c)'s barrier — the
// withheld tally reaching its expected total — never fires, so the test reds
// naming the count it was waiting for.
func TestEdgeManifest_E2E_LinkCreateWritesExactlyTheEntriesItChanged(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping edge-manifest withholding exact-write e2e in -short mode")
	}

	s := natsfixture.StartServer(t)
	nc := natsfixture.Connect(t, s.ClientURL())
	defer nc.Close()

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	js := conn.JetStream()

	testutil.EnsurePrimordials(t)
	seeder, err := bootstrap.NewSeeder(nc, logger)
	require.NoError(t, err)
	require.NoError(t, seeder.ProvisionBuckets(ctx))
	require.NoError(t, seeder.SeedPrimordial(ctx))

	coreKV, err := conn.OpenKV(ctx, bootstrap.CoreKVBucket)
	require.NoError(t, err)
	adjKV, err := conn.OpenKV(ctx, bootstrap.RefractorAdjacencyKV)
	require.NoError(t, err)
	capKV, err := conn.OpenKV(ctx, bootstrap.CapabilityKVBucket)
	require.NoError(t, err)

	boots := consumer.NewBootstrapper(conn, bootstrap.CoreKVBucket, adjKV)
	go func() { _ = boots.Run(ctx) }()
	select {
	case <-boots.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("adjacency bootstrapper did not reach Ready within 10s")
	}

	// --- install the real edge-manifest package through the real meta lane,
	// exactly as the Fire 2 producer-flip e2e does. ---
	metaCP, _, err := processor.MakeStubPipeline(conn, bootstrap.CoreKVBucket, bootstrap.HealthKVBucket, processor.AuthModeStub, logger, "em-t9-meta")
	require.NoError(t, err)
	metaCons, err := processor.EnsureConsumer(ctx, js, processor.ConsumerConfig{
		StreamName: "core-operations", Durable: "em-t9-meta",
		FilterSubjects: []string{"ops.meta"}, AckWait: 5 * time.Second,
	}, logger)
	require.NoError(t, err)
	metaCtx, metaCancel := context.WithCancel(ctx)
	defer metaCancel()
	metaCC, err := metaCons.Consume(func(m jetstream.Msg) { metaCP.HandleMessage(metaCtx, m) })
	require.NoError(t, err)
	defer metaCC.Stop()

	installer := testutil.NewInstaller(conn, bootstrap.BootstrapIdentityKey)
	installer.RoleIDs = testutil.StandardRoleIDs()
	_, err = installer.Install(ctx, edgemanifest.Package)
	require.NoError(t, err, "installing edge-manifest must succeed")

	// --- activate the generated edgeManifestReadGrants producer through
	// projection.InstallActorAggregate, which is what wires the §6.2 guard,
	// the perEntry envelope and the prefix-diff retraction. ---
	fullEngine := full.New()
	projectionRevision := func(k string) uint64 {
		entry, gErr := coreKV.Get(ctx, k)
		if gErr != nil || entry == nil {
			return 0
		}
		return entry.Revision
	}

	src := lens.NewCoreKVSource(conn, bootstrap.CoreKVBucket, "test", logger)
	loaded := make(chan *lens.Rule, 32)
	src.SetLoadCallback(func(r *lens.Rule) {
		select {
		case loaded <- r:
		default:
		}
	})
	src.SetUpdateCallback(func(_, _ *lens.Rule, _ lens.UpdateKind) {})
	require.NoError(t, src.Start(ctx))

	var producerRule *lens.Rule
	deadline := time.Now().Add(20 * time.Second)
	for producerRule == nil {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatal("did not activate the edgeManifestReadGrants producer lens within deadline")
		}
		select {
		case r := <-loaded:
			if r.CanonicalName == "edgeManifestReadGrants" {
				producerRule = r
			}
		case <-time.After(remaining):
		}
	}
	require.Equal(t, "anchorId", producerRule.Output.EntryKeyColumn,
		"the producer under test must be the perEntry shape, or there is nothing to withhold")

	adpt, err := adapter.New(capKV, producerRule.Into.Key, adapter.DeleteModeHard)
	require.NoError(t, err)
	p, err := pipeline.New(producerRule.ID, "nats_kv", bootstrap.CoreKVBucket, adjKV, coreKV, adpt, nil)
	require.NoError(t, err)
	p.UseFullEngine(fullEngine, producerRule.CompiledRule)
	require.True(t, projection.InstallActorAggregate(p, adpt, producerRule, projectionRevision, adjKV, coreKV, logger))

	// The audit trail is half of what T9 counts, so this pipeline writes one.
	require.NoError(t, health.EnsureAuditStream(ctx, conn))
	p.SetAuditWriter(health.NewAuditWriter(conn, producerRule.ID))
	auditSubject := subjects.Audit(producerRule.ID)
	auditEntries := func() uint64 {
		stream, sErr := js.Stream(ctx, health.AuditStreamName)
		require.NoError(t, sErr)
		info, iErr := stream.Info(ctx, jetstream.WithSubjectFilter(auditSubject))
		require.NoError(t, iErr)
		return info.State.Subjects[auditSubject]
	}

	p.RunOn(conn, e2eSpec(producerRule.ID, bootstrap.CoreKVBucket))
	pipelineCtx, pipelineCancel := context.WithCancel(ctx)
	pipelineDone := make(chan struct{})
	go func() { defer close(pipelineDone); p.Run(pipelineCtx) }()
	t.Cleanup(func() { pipelineCancel(); <-pipelineDone })

	// --- (a) POPULATE: two service instances, three identities, six grants. ---
	instanceIDs := []string{pl2NanoID("t9-instance-one"), pl2NanoID("t9-instance-two")}
	residentIDs := []string{pl2NanoID("t9-resident-a"), pl2NanoID("t9-resident-b"), pl2NanoID("t9-resident-c")}

	grantKey := func(identityID, instanceID string) string {
		return "cap-read.edgeManifest.identity." + identityID + "." + instanceID
	}
	readEntry := func(key string) storedEntry {
		entry, gErr := capKV.Get(ctx, key)
		if gErr != nil || entry == nil || len(entry.Value) == 0 {
			return storedEntry{}
		}
		var env map[string]any
		require.NoError(t, json.Unmarshal(entry.Value, &env), "value at %q must be JSON", key)
		deleted, _ := env["isDeleted"].(bool)
		seq, _ := env["projectionSeq"].(float64)
		return storedEntry{revision: entry.Revision, projectionSeq: uint64(seq), live: !deleted}
	}

	for _, instID := range instanceIDs {
		emWriteVertex(t, ctx, coreKV, substrate.VertexKey("service", instID), "service.backgroundCheck.instance", map[string]any{})
	}
	for _, idID := range residentIDs {
		emWriteVertex(t, ctx, coreKV, substrate.VertexKey("identity", idID), "identity", map[string]any{"name": "T9 " + idID})
	}
	// The fourth identity exists from the start, AHEAD of the seed links in
	// stream order, so its own create event is processed — and found to hold no
	// grant — before the population barrier below can pass. Phase (b) is then
	// exactly one event. Written beside the link create instead, the vertex
	// event's evaluation races the link put: the rule consumer is one pump
	// reading LIVE Core KV, so a vertex event picked up after the link landed
	// derives the newcomer's entry from the vertex event, and the link event
	// that follows finds all seven unchanged — 7 withheld, not 6, and the
	// "exactly one write" the test pins lands under the wrong event.
	newcomerID := pl2NanoID("t9-resident-d")
	emWriteVertex(t, ctx, coreKV, substrate.VertexKey("identity", newcomerID), "identity", map[string]any{"name": "T9 newcomer"})
	for _, instID := range instanceIDs {
		for _, idID := range residentIDs {
			emWriteLink(t, ctx, coreKV, "service", instID, "providedTo", "identity", idID)
		}
	}

	var populated []string
	for _, idID := range residentIDs {
		for _, instID := range instanceIDs {
			populated = append(populated, grantKey(idID, instID))
		}
	}
	require.Len(t, populated, 6)

	// Barrier on the EFFECT: every seeded grant key is live in the target.
	require.Eventually(t, func() bool {
		for _, key := range populated {
			if !readEntry(key).live {
				return false
			}
		}
		return true
	}, 60*time.Second, 100*time.Millisecond,
		"the six seeded grant entries must all land before the measurement starts — "+
			"this is also the control that this producer writes at all")

	// The population is quiescent only once the last event has been processed.
	// Barrier on the effect again: the actors' entries stop moving. A stable
	// pair of reads a poll apart is what says the fan-out has drained, and it
	// is what makes the deltas below attributable to the single event that
	// follows rather than to a straggler from the seeding.
	beforeEvent := map[string]storedEntry{}
	require.Eventually(t, func() bool {
		snapshot := map[string]storedEntry{}
		for _, key := range populated {
			snapshot[key] = readEntry(key)
		}
		stable := len(beforeEvent) == len(snapshot)
		if stable {
			for key, was := range beforeEvent {
				if snapshot[key] != was {
					stable = false
					break
				}
			}
		}
		beforeEvent = snapshot
		return stable
	}, 60*time.Second, 250*time.Millisecond, "the seeding fan-out must drain before the measurement")

	auditBefore := auditEntries()
	withheldBefore := p.EntriesWithheld()
	writesBefore := p.ProjectionWrites()
	require.Positive(t, auditBefore, "the control: the seeding really did commit entries and audit them")

	// --- (b) ONE LINK CREATE, fanning out to four actors over seven entries. ---
	newKey := grantKey(newcomerID, instanceIDs[0])

	emWriteLink(t, ctx, coreKV, "service", instanceIDs[0], "providedTo", "identity", newcomerID)

	// Barrier on the EFFECT: the one entry this event creates is live, AND the
	// six it did not change have all been re-evaluated and withheld. Waiting
	// for the withheld tally rather than for a timeout is what makes the
	// zero-write assertions below assertions about a pass that RAN.
	require.Eventually(t, func() bool {
		return readEntry(newKey).live && p.EntriesWithheld() >= withheldBefore+6
	}, 60*time.Second, 100*time.Millisecond,
		"the link create must write the newcomer's entry and withhold the six unchanged ones; "+
			"withheld so far: %d, expected at least %d", p.EntriesWithheld()-withheldBefore, 6)

	require.Equal(t, withheldBefore+6, p.EntriesWithheld(),
		"EXACTLY the six pre-existing entries are withheld — one per entry of each of the three "+
			"actors the enumeration reached besides the newcomer")
	require.Equal(t, auditBefore+1, auditEntries(),
		"the audit subject gains exactly one entry: the one the event created")
	require.Equal(t, writesBefore+1, p.ProjectionWrites(),
		"exactly one adapter write is attempted — a withheld entry never reaches the adapter")

	for _, key := range populated {
		now := readEntry(key)
		require.True(t, now.live, "%s must still be granted", key)
		require.Equal(t, beforeEvent[key].revision, now.revision,
			"%s was not changed by this event, so its stored revision must not move", key)
		require.Equal(t, beforeEvent[key].projectionSeq, now.projectionSeq,
			"%s keeps the watermark of the write that last CHANGED it — that is the fact "+
				"that makes withholding its rewrite safe", key)
	}

	// --- (c) THE SAME EVENT AGAIN: zero writes, zero audit entries. ---
	afterCreate := map[string]storedEntry{}
	for _, key := range append(append([]string{}, populated...), newKey) {
		afterCreate[key] = readEntry(key)
	}
	require.Len(t, afterCreate, 7)

	auditSteady := auditEntries()
	withheldSteady := p.EntriesWithheld()
	writesSteady := p.ProjectionWrites()

	emWriteLink(t, ctx, coreKV, "service", instanceIDs[0], "providedTo", "identity", newcomerID)

	// The same barrier, on the same effect: the pass is done when every one of
	// the seven entries has been re-derived and withheld. If withholding were
	// off, this never reaches seven and the failure names the count.
	require.Eventually(t, func() bool {
		return p.EntriesWithheld() >= withheldSteady+7
	}, 60*time.Second, 100*time.Millisecond,
		"a second identical event must withhold all seven entries; withheld so far: %d of 7",
		p.EntriesWithheld()-withheldSteady)

	require.Equal(t, withheldSteady+7, p.EntriesWithheld(),
		"EXACTLY seven — the four actors the enumeration reaches hold seven entries between them, "+
			"and not one of them changed")
	require.Equal(t, auditSteady, auditEntries(),
		"a second identical event commits nothing, so the audit subject gains nothing")
	require.Equal(t, writesSteady, p.ProjectionWrites(),
		"a second identical event reaches the adapter zero times")
	for key, was := range afterCreate {
		now := readEntry(key)
		require.True(t, now.live, "%s must still be granted", key)
		require.Equal(t, was.revision, now.revision, "%s must not be rewritten", key)
		require.Equal(t, was.projectionSeq, now.projectionSeq, "%s's watermark must not move", key)
	}
}
