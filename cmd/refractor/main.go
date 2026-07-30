// refractor is the Lattice projection engine. It consumes Core KV CDC and
// sources lens definitions from `vtx.meta.>` (filtered by envelope class
// `meta.lens` per data-contracts.md §1.2 line 70).
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/controlauth"
	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/capabilityenv"
	"github.com/operatinggraph/lattice/internal/refractor/consumer"
	"github.com/operatinggraph/lattice/internal/refractor/control"
	"github.com/operatinggraph/lattice/internal/refractor/failure"
	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/keyshredded"
	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/projection"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/refractor/subjects"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/vault"
)

const (
	coreKVBucket             = "core-kv"
	healthKVBucket           = "health-kv"
	adjacencyKVBucket        = "refractor-adjacency"
	personalInterestKVBucket = "personal-lens-interest"
	defaultHeartbeatEvery    = 10 * time.Second
)

// reservedActivationBuckets mirrors internal/pkgmgr's reservedBucketNames
// (bucketguard.go) as a fail-closed backstop at Refractor lens activation —
// the platform-private buckets a lens must never target, since the nats-kv
// adapter auto-creates/truncates whatever Bucket a lens declares verbatim.
// Derived from bootstrap's platform-bucket registry, like pkgmgr's guard, so
// the two mirrors cannot drift apart again (credential-bindings was missing
// from this map before the registry existed — a second live instance of the
// same hand-copied-list bug bucketguard.go had).
var reservedActivationBuckets = bootstrap.ReservedBuckets()

type pipelineEntry struct {
	cancel        context.CancelFunc
	done          chan struct{}
	pipeline      *pipeline.Pipeline
	reporter      *health.Reporter
	canonicalName string // keyed under lensLatency in heartbeats.
	authPlane     bool   // projects the capability-kv authorization surface (projection.IsAuthPlane).
	// guarded reports whether the adapter the RUNNING pipeline was activated
	// with enforces the §6.2 projection-write guard, and the fields under it
	// are the surface the lens writes as its spec declares it. A guarded lens
	// is pinned to that surface: the guard orders writes against what is stored
	// there, so moving the target would strand every key the lens already wrote
	// with no way to retract it. (A grant lens is the one shape whose declared
	// table is not the table it writes — the grant writer always addresses
	// actor_read_grants — so pinning it there only ever refuses a cosmetic
	// edit.)
	guarded     bool
	target      string
	bucket      string
	table       string
	dsn         string
	grantSource string
	// protected marks the RLS-locked read-model posture, which is what forces
	// the §6.2 guard on a Postgres adapter. Retiring it on a running lens would
	// drop that guard with nothing underneath to refuse the swap.
	protected bool
	// grantTable reports whether the RUNNING pipeline projects the shared
	// actor_read_grants table — the lens's identity, and not something an
	// adapter swap can change (the rows it already wrote would become
	// unaddressable).
	grantTable bool
	// output is the §6.13 Output descriptor the RUNNING pipeline's envelope,
	// delete key and sweep plan were installed from. An INTO-only update
	// re-runs none of that installation, so an edit here is refused.
	output *lens.OutputDescriptorSpec
	// secureColumns is the Secure-Lens column set the RUNNING pipeline's
	// decryptor was built from. Hot-reload guards compare an incoming spec
	// against this — not against the last-seen spec — so a refused update
	// cannot poison the baseline and wedge the lens.
	secureColumns []lens.SecureColumn
}

// capReadShredTargets returns a keyshredded.NullifyTarget for every currently
// running lens whose Output descriptor is a cap-read.* NATS-KV PerEntry
// producer — the base capabilityRead lens and every package-generated
// AnchorWalk producer alike (cap-read-per-anchor-grant-keys-design.md's Fire
// 2 follow-on). The two fields checked, EntryKeyColumn and OutputKeyPattern,
// are the same declared descriptor fields sweepEnrolment and
// ApplyTruncateScope already key structural per-lens decisions off, not a
// parse of the lens's compiled cypher. Pure over its input so it is testable
// without a live registry mutex; the caller holds the lock.
//
// A hand-authored Postgres GrantTable cap-read producer (e.g.
// packages/clinic-domain's grant_source lenses) carries no Output descriptor
// and so is never returned here — a distinct gap this fire does not close.
func capReadShredTargets(registry map[string]*pipelineEntry) []keyshredded.NullifyTarget {
	var targets []keyshredded.NullifyTarget
	for id, entry := range registry {
		if entry.output == nil || entry.output.EntryKeyColumn == "" {
			continue
		}
		if !strings.HasPrefix(entry.output.OutputKeyPattern, "cap-read.") {
			continue
		}
		targets = append(targets, keyshredded.NullifyTarget{RuleID: id, PerEntry: true})
	}
	return targets
}

// grantTableShredRevokers returns one keyshredded.GrantTableRevoker per
// distinct Postgres DSN among currently-running grant-table lenses
// (packages/clinic-domain's four grant_source producers today, any future
// package's tomorrow) — deduped, since every grant-table lens across every
// package writes the SAME shared actor_read_grants table, so a shred needs
// only one revoke call per distinct database, not one per lens
// (cap-read-per-anchor-grant-keys-design.md's Postgres GrantTable residual).
// poolManager.Acquire caches by DSN, so this opens no connection beyond what
// the grant lens's own activation already did. Pure over its input (besides
// the cached pool lookup) so it is testable without a live registry mutex;
// the caller holds the lock.
func grantTableShredRevokers(ctx context.Context, poolManager *adapter.PoolManager, registry map[string]*pipelineEntry) []keyshredded.GrantTableRevoker {
	var out []keyshredded.GrantTableRevoker
	seen := make(map[string]bool)
	for _, entry := range registry {
		if !entry.grantTable || entry.dsn == "" || seen[entry.dsn] {
			continue
		}
		seen[entry.dsn] = true
		pool, err := poolManager.Acquire(ctx, entry.dsn)
		if err != nil {
			// The lens's own activation already surfaces a DSN failure loudly
			// (infra-pause probe loop); nothing more useful to do here than skip it.
			continue
		}
		gw, err := adapter.NewPostgresGrantWriter(pool, 0)
		if err != nil {
			continue
		}
		out = append(out, gw)
	}
	return out
}

func main() {
	natsURL := flag.String("nats-url", envOr("NATS_URL", nats.DefaultURL), "NATS server URL")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	instance := "rfx-" + randHex(6)
	logger.Info("refractor starting", "instance", instance, "natsURL", *natsURL)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Substrate is the integration boundary.
	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{
		URL:          *natsURL,
		NKeySeedFile: envOr("NATS_NKEY", ""),
		CredsFile:    envOr("NATS_CREDS", ""),
	})
	if err != nil {
		logger.Error("nats connect", "err", err)
		os.Exit(1)
	}
	defer conn.Close()
	nc := conn.NATS()
	js := conn.JetStream()

	// Start the heartbeater early so health.refractor.<instance> shows
	// up in Health KV within 10s of process start (AC #6 + AC #9).
	hb := health.NewLatticeHeartbeater(conn, healthKVBucket, instance, defaultHeartbeatEvery, logger)
	go hb.Run(ctx)

	// The deferred-retry queue for Transient write failures (the failure-tier
	// "deferred retry queue" route, docs/components/refractor-failure-tiers.md).
	// Shared across every pipeline instance — one Run loop for the process;
	// RetryQueue enforces the single-caller invariant itself.
	retryQueue := failure.NewRetryQueue()
	go retryQueue.Run(ctx)

	// Open Core KV and the (pre-provisioned) refractor-adjacency bucket as
	// substrate handles — the read path threads *substrate.KV, not raw jetstream.
	coreKV, err := conn.OpenKV(ctx, coreKVBucket)
	if err != nil {
		logger.Error("open core KV", "bucket", coreKVBucket, "err", err)
		os.Exit(1)
	}
	adjKV, err := conn.OpenKV(ctx, adjacencyKVBucket)
	if err != nil {
		logger.Error("open refractor adjacency KV", "bucket", adjacencyKVBucket, "err", err)
		os.Exit(1)
	}
	healthKVHandle, err := conn.OpenKV(ctx, healthKVBucket)
	if err != nil {
		logger.Error("open health KV", "bucket", healthKVBucket, "err", err)
		os.Exit(1)
	}
	// The Personal Lens's per-device Interest Set registry (Fire PL.2):
	// backs the control plane's "register"/"deregister" ops and the fan-out
	// pipeline's relevance filter (personal-secure-lens-design.md §3.3).
	personalInterestKV, err := conn.OpenKV(ctx, personalInterestKVBucket)
	if err != nil {
		logger.Error("open personal-lens-interest KV", "bucket", personalInterestKVBucket, "err", err)
		os.Exit(1)
	}
	// D1's read-path Capability KV (Contract #6 §6.14) — the Personal Lens's
	// security gate (personal-secure-lens-design.md §3.4, Fire PL.3).
	capabilityKV, err := conn.OpenKV(ctx, bootstrap.CapabilityKVBucket)
	if err != nil {
		logger.Error("open capability KV", "bucket", bootstrap.CapabilityKVBucket, "err", err)
		os.Exit(1)
	}

	bootstrapper := consumer.NewBootstrapper(conn, coreKVBucket, adjKV)
	go func() {
		if err := bootstrapper.Run(ctx); err != nil && ctx.Err() == nil {
			logger.Error("adjacency bootstrap failed — no lenses will start", "err", err)
			stop() // cancel the root context so main exits and the process can restart
		}
	}()

	poolManager := adapter.NewPoolManager()
	controlSvc := control.NewService()
	controlSvc.SetPersonalInterestKV(personalInterestKV)
	controlSvc.SetCoreKV(coreKV)
	checker, err := wireControlChecker(ctx, conn, "refractor", controlauth.RefractorOps, logger)
	if err != nil {
		logger.Error("wire control-plane capability checker", "err", err)
		os.Exit(1)
	}
	controlSvc.SetCapabilityChecker(checker)
	actorVerifier, err := controlauth.WireActorVerifierFromEnv(ctx, conn, logger)
	if err != nil {
		logger.Error("wire control-plane actor verifier", "err", err)
		os.Exit(1)
	}
	controlSvc.SetActorVerifier(actorVerifier)

	// The KeyShredded nullification listener (vault-crypto-shredding-design.md
	// §2.4, Fire 4a) — the Refractor half of crypto-shredding's async
	// finalization; internal/privacyworker (in cmd/processor) is the other,
	// independent consumer of the same event. Every cap-read.* NATS-KV
	// PerEntry producer — the bootstrap capabilityRead base lens and every
	// package-generated AnchorWalk producer alike — is a perEntry target
	// (cap-read-per-anchor-grant-keys-design.md §6, Fire 2 follow-on): a
	// shredded identity's own per-anchor grant children
	// (cap-read.<domain>.identity.<id>.<anchorId>) must be enumerated and
	// nullified, not just a retired legacy parent document (which the
	// producer's own per-actor evaluation already tombstones, paired
	// separately). SetTargetLister below discovers package-generated
	// producers dynamically once the lens registry exists. The base lens
	// stays ALSO listed statically here — deliberately redundant with what
	// the lister would eventually find — as a floor: it guarantees
	// effectiveTargets() is never empty at cold boot, so a shred event
	// redelivered before the lens registry has loaded (Run starts before
	// bootstrapper.Ready()/src.Start below) correctly hits
	// ErrRuleNotRegistered → NakWithDelay and retries, instead of an empty
	// target list vacuously Acking + recording the identity as clean with
	// nothing actually checked (an adversarial-review-caught regression this
	// fire must not reintroduce). Hand-authored Postgres GrantTable cap-read
	// producers (packages/clinic-domain's four) carry no Output descriptor and
	// so are reached by neither this static entry nor the lister — closed by a
	// separate, parallel mechanism below (SetGrantRevokerLister): the shared
	// actor_read_grants table is revoked by DSN, not by per-lens RuleID, since
	// no single lens owns it. The privacy service actor (Fire 4b finalization
	// recording) is graph-discovered — absent on a pre-v15 kernel, which
	// disables recording without disabling nullification.
	privacyCtx, privacyCancel := context.WithTimeout(context.Background(), 10*time.Second)
	privacyActorKey, paErr := bootstrap.PrivacyActorKey(privacyCtx, conn)
	privacyCancel()
	if paErr != nil {
		logger.Error("discover privacy service actor", "err", paErr)
		os.Exit(1)
	}
	keyShredded := keyshredded.New(keyshredded.Config{
		Conn:         conn,
		EventsStream: bootstrap.CoreEventsStreamName,
		Control:      controlSvc,
		Logger:       logger,
		ActorKey:     privacyActorKey,
		Targets: []keyshredded.NullifyTarget{
			{RuleID: bootstrap.CapabilityReadLensID, PerEntry: true},
		},
	})
	// The Vault backend for Secure-Lens decrypt-at-projection (Contract #3
	// §3.10; vault-crypto-shredding-design.md §2.3 Phase B). Optional: a
	// deployment with no Secure Lens needs no KEK; a Secure Lens activating
	// with no Vault fails closed at startPipeline. A configured-but-invalid
	// KEK is a hard startup failure — silently proceeding would strand every
	// secure lens with a confusing per-lens activation error.
	vaultBackend, vaultErr := loadVault(logger)
	if vaultErr != nil {
		logger.Error("load vault backend", "err", vaultErr)
		os.Exit(1)
	}
	// The "sessionkey" control op (edge-lattice-full-design.md §3.6, EDGE.4)
	// needs the same vaultBackend Secure Lenses use. Guard on the concrete
	// pointer, not the vault.Vault interface SetVault takes — a nil
	// *vault.LocalBackend wrapped in a non-nil interface would defeat
	// personalSessionKey's `s.vault == nil` fail-closed check.
	if vaultBackend != nil {
		controlSvc.SetVault(vaultBackend)
	}
	// vaultCalls counts Vault.Decrypt invocations across every Secure Lens for
	// the Contract #5 §5.4 vault_calls_total heartbeat metric. Reports 0 while
	// no secure lens is active (Refractor then makes no Vault calls).
	var vaultCalls atomic.Uint64

	hb.KeyShreddedHandledTotalProvider = keyShredded.HandledTotal
	hb.VaultCallsTotalProvider = vaultCalls.Load

	var (
		mu       sync.Mutex
		registry = make(map[string]*pipelineEntry)
		wg       sync.WaitGroup
	)

	// capReadShredTargets is re-derived from the live registry on every shred
	// event (not computed once here) so a package installed after this line
	// runs still gets its cap-read.* producer nullified — see its doc.
	keyShredded.SetTargetLister(func() []keyshredded.NullifyTarget {
		mu.Lock()
		defer mu.Unlock()
		return capReadShredTargets(registry)
	})
	// grantTableShredRevokers is likewise re-derived from the live registry on
	// every shred event, so a Postgres GrantTable producer (clinic-domain's
	// four grant_source lenses today) installed after this line still gets
	// its actor's rows revoked on shred — see its doc.
	keyShredded.SetGrantRevokerLister(func() []keyshredded.GrantTableRevoker {
		mu.Lock()
		defer mu.Unlock()
		return grantTableShredRevokers(ctx, poolManager, registry)
	})

	go func() {
		if err := keyShredded.Run(ctx); err != nil && ctx.Err() == nil {
			logger.Error("keyshredded listener exited with error", "err", err)
		}
	}()

	// Registry size for the heartbeater's metrics.lensesRegistered
	// (lens-registry-restart-integrity-design.md §4 Fire B step 1) — the
	// exact started-pipeline set every other registry-scoped provider below
	// already reads.
	hb.LensCountProvider = func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(registry)
	}

	// Per-Lens latency stats provider for the heartbeater.
	// Falls back to a no-op when no pipeline has a latency buffer.
	hb.LensLatencyProvider = func() map[string]health.LensLatencySnapshot {
		mu.Lock()
		defer mu.Unlock()
		out := make(map[string]health.LensLatencySnapshot, len(registry))
		for _, entry := range registry {
			if entry.pipeline == nil || entry.canonicalName == "" {
				continue
			}
			buf := entry.pipeline.LatencyBuffer()
			if buf == nil {
				continue
			}
			snap := buf.Snapshot()
			if snap.Count == 0 {
				continue
			}
			out[entry.canonicalName] = health.LensLatencySnapshot{
				Count: snap.Count,
				Mean:  snap.Mean,
				P95:   snap.P95,
				P99:   snap.P99,
			}
		}
		return out
	}

	// LagProvider for the heartbeater — read pending count per lens from each
	// pipeline's supervised consumer (by durable name, via the supervisor).
	hb.LagProvider = func() map[string]uint64 {
		mu.Lock()
		defer mu.Unlock()
		out := make(map[string]uint64, len(registry))
		for lensID, entry := range registry {
			if entry.pipeline == nil {
				continue
			}
			pending, err := entry.pipeline.Pending(context.Background())
			if err != nil {
				continue
			}
			out[lensID] = pending
		}
		return out
	}

	// CapabilityLensProvider for the heartbeater — liveness of the auth-plane
	// (capability-kv) lenses for the §5.5 capability backstop. Read-only: status
	// from the lens reporter, lag from the supervised consumer; no authz path,
	// Core KV, or projection is touched. A read error yields an "unknown" lens,
	// never a missing one.
	type capLensEntry struct {
		lensID string
		entry  *pipelineEntry
	}
	hb.CapabilityLensProvider = func() []health.CapabilityLensStatus {
		mu.Lock()
		entries := make([]capLensEntry, 0, len(registry))
		for lensID, entry := range registry {
			if entry.authPlane && entry.pipeline != nil && entry.reporter != nil {
				entries = append(entries, capLensEntry{lensID: lensID, entry: entry})
			}
		}
		mu.Unlock()

		out := make([]health.CapabilityLensStatus, 0, len(entries))
		for _, ent := range entries {
			entry := ent.entry
			// Both names come from the registry, not from the health entry, so
			// the lens is identifiable in the metric map even on the branch below
			// where nothing about it could be read — a canonical name is optional
			// in a lens spec, and an unnamed lens must not land under the empty
			// key and collide with the next one.
			snap := health.CapabilityLensStatus{
				CanonicalName: entry.canonicalName,
				RuleID:        ent.lensID,
			}
			// The sweep's coverage and repair verdicts, read from the in-process
			// sweeper rather than the persisted entry: the streaks are what
			// escalate the issues, and they are per-run state the health entry
			// deliberately does not carry (a restart starts a fresh escalation
			// window, and re-discovers a live repair failure on its first pass).
			// Read before the reporter, because they stay valid even when it does
			// not: an unreadable health entry is an observation fault, not a
			// sweep one, and a live repair failure must not be lost to it.
			if sw := entry.pipeline.Sweeper(); sw != nil {
				status := sw.Status()
				snap.SweepReconciled = status.Reconciled
				snap.SweepDivergentStreak = status.DivergentStreak
				snap.SweepFailingActors = status.FailingActors
				snap.SweepFailedStreak = status.FailedStreak
				snap.SweepLastFailure = status.LastFailure
				snap.SweepLastPassAt = status.LastPassAt
				snap.SweepSuppression = status.Suppression
				snap.SweepSuppressionAt = status.SuppressionAt
				snap.SweepInterval = sw.Interval()
			}
			// A rebuild suppresses the sweep, so the stall detector cannot judge
			// the sweep's silence while one runs. These let it judge the REBUILD
			// instead — one that is draining against one that is wedged.
			snap.RebuildOutstanding, snap.RebuildProgressAt = entry.pipeline.RebuildProgress()
			// A lens whose liveness inputs cannot be read is reported as unknown,
			// never omitted: dropping it removes the lens from
			// metrics.capabilityLens entirely, which is indistinguishable from a
			// lens that was never installed — the auth-plane read model going
			// unobserved would read as nothing being wrong.
			st, err := entry.reporter.GetStatus(context.Background())
			if err != nil {
				snap.Status = "unknown"
				snap.Unreadable = "lens health entry: " + err.Error()
				out = append(out, snap)
				continue
			}
			pending, err := entry.pipeline.Pending(context.Background())
			if err != nil {
				snap.Status = "unknown"
				snap.Unreadable = "consumer pending count: " + err.Error()
				out = append(out, snap)
				continue
			}
			if st.PauseReason != nil {
				snap.PauseReason = *st.PauseReason
			}
			snap.Status = st.Status
			snap.ConsumerLag = pending
			out = append(out, snap)
		}
		return out
	}

	// LensProvider for the heartbeater — the generalized (non-auth-plane)
	// projection-liveness backstop (lens-projection-liveness-design.md §3.3).
	// Sibling of CapabilityLensProvider above: same read-only shape, scoped to
	// business lenses so the auth-plane path stays untouched (§5.1). Reads the
	// in-process Progress() live every beat (independent of the LagPoller's 5s
	// cycle), so the backstop alert survives a LagPoller stall (design §5.5).
	hb.LensProvider = func() []health.LensLivenessStatus {
		mu.Lock()
		entries := make([]capLensEntry, 0, len(registry))
		for lensID, entry := range registry {
			if !entry.authPlane && entry.pipeline != nil && entry.reporter != nil {
				entries = append(entries, capLensEntry{lensID: lensID, entry: entry})
			}
		}
		mu.Unlock()

		out := make([]health.LensLivenessStatus, 0, len(entries))
		for _, ent := range entries {
			entry := ent.entry
			// The rule ID comes from the registry, not the health entry, so an
			// unreadable lens is still identifiable below — and an unnamed lens
			// never lands under the empty key and collides with the next one.
			snap := health.LensLivenessStatus{
				CanonicalName: entry.canonicalName,
				RuleID:        ent.lensID,
			}
			// The convergence sweep's verdicts, read from the in-process sweeper
			// and before the reporter, for the reasons the cap path reads them
			// there: the streaks are per-run state the health entry does not
			// carry, and a live repair failure must not be lost to an unreadable
			// health entry — that is an observation fault, not a sweep one. Nil
			// for a lens the install gate did not enrol.
			if sw := entry.pipeline.Sweeper(); sw != nil {
				status := sw.Status()
				snap.SweepReconciled = status.Reconciled
				snap.SweepDivergentStreak = status.DivergentStreak
				snap.SweepFailingActors = status.FailingActors
				snap.SweepFailedStreak = status.FailedStreak
				snap.SweepLastFailure = status.LastFailure
				snap.SweepLastPassAt = status.LastPassAt
				snap.SweepSuppression = status.Suppression
				snap.SweepSuppressionAt = status.SuppressionAt
				snap.SweepInterval = sw.Interval()
			}
			// A lens whose liveness inputs cannot be read is reported as
			// unknown, never omitted: dropping it removes the lens from
			// metrics.lensLiveness entirely, which is indistinguishable from a
			// lens that was never installed — the read model going unobserved
			// would read as nothing being wrong.
			st, err := entry.reporter.GetStatus(context.Background())
			if err != nil {
				snap.Status = "unknown"
				snap.Unreadable = "lens health entry: " + err.Error()
				out = append(out, snap)
				continue
			}
			pending, err := entry.pipeline.Pending(context.Background())
			if err != nil {
				snap.Status = "unknown"
				snap.Unreadable = "consumer pending count: " + err.Error()
				out = append(out, snap)
				continue
			}
			if st.PauseReason != nil {
				snap.PauseReason = *st.PauseReason
			}
			snap.RuleID = st.RuleID
			snap.Status = st.Status
			snap.ProjectionLag = pending
			snap.LastProjectedAt = entry.pipeline.Progress().LastProjectedAt
			out = append(out, snap)
		}
		return out
	}

	buildTargetAdapter := func(r *lens.Rule) (adapter.Adapter, error) {
		// DeleteMode is defaulted to "hard" and validated upstream (Parse /
		// translateSpec); re-parse here to obtain the typed value for the adapter.
		deleteMode, err := adapter.ParseDeleteMode(r.Into.DeleteMode)
		if err != nil {
			return nil, fmt.Errorf("lens %q: delete_mode: %w", r.ID, err)
		}
		switch r.Into.Target {
		case "nats_kv":
			// Fail-closed mirror of pkgmgr's install-time reserved-bucket guard
			// (internal/pkgmgr/bucketguard.go): a platform-private bucket must
			// never be auto-created/truncated as a lens target, even if a lens
			// spec reached activation by a path that skipped pkgmgr's check
			// (hand-authored spec, direct install).
			if _, reserved := reservedActivationBuckets[r.Into.Bucket]; reserved {
				return nil, fmt.Errorf("lens %q: Bucket %q is a platform-private bucket, never a lens target — refusing to open/create it", r.ID, r.Into.Bucket)
			}
			// Open the target bucket as a substrate handle; create it first if it
			// does not exist (pre-provisioned buckets like capability-kv are reused).
			targetKV, err := conn.OpenKV(ctx, r.Into.Bucket)
			if err != nil {
				if _, cerr := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: r.Into.Bucket}); cerr != nil {
					return nil, cerr
				}
				targetKV, err = conn.OpenKV(ctx, r.Into.Bucket)
				if err != nil {
					return nil, err
				}
			}
			return adapter.New(targetKV, r.Into.Key, deleteMode)
		case "postgres":
			pool, err := poolManager.Acquire(ctx, r.Into.DSN)
			if err != nil {
				return nil, err
			}
			// A grant lens projects to the shared actor_read_grants table through
			// the seq-guarded grant writer (Contract #6 §6.14). The table is
			// provisioned out-of-band; the adapter's Probe verifies its posture and
			// the lens starts infra-paused until the verify passes (verify-and-
			// pause). Refractor issues no runtime DDL.
			if r.Into.GrantTable {
				gw, err := adapter.NewPostgresGrantWriter(pool, r.Into.QueryTimeout)
				if err != nil {
					return nil, err
				}
				// The declared grant_source confines this lens to its own rows in the
				// shared table — it guards every write and scopes the key enumeration
				// diff retraction reads back.
				return adapter.NewGrantWriterAdapter(gw, r.Into.GrantSource)
			}
			// A protected read model (read-path authorization, D1.3): the RLS-locked
			// business table (FORCE ROW LEVEL SECURITY + the §6.14 set-membership
			// policy) and the actor_read_grants table its policy references are both
			// provisioned out-of-band. The adapter's Probe verifies the posture and
			// the lens starts infra-paused until it passes — Refractor projects
			// nothing into a table that is not locked down, and issues no DDL. A
			// non-protected table is also provisioned out-of-band.
			base, err := adapter.NewPostgresAdapter(pool, r.Into.Table, r.Into.Key, r.Into.QueryTimeout, deleteMode)
			if err != nil {
				return nil, err
			}
			if !r.Into.Protected {
				return base, nil
			}
			return adapter.NewProtectedAdapter(base, r.Into.ArrayColumns, r.Into.Columns)
		case "nats_subject":
			// The Personal Lens transport (personal-secure-lens-design.md Fire 1):
			// a fire-and-forget per-actor delta publish, not a KV write — no
			// bucket/table to open, just the backing SYNC-style stream, which the
			// adapter ensures (JIT, mirroring the nats_kv bucket-create-if-absent
			// above).
			return adapter.NewNatsSubjectAdapter(ctx, conn, r.ID, r.Into.SubjectPrefix, r.Into.Stream, r.Into.Key)
		default:
			return nil, fmt.Errorf("unknown adapter target %q", r.Into.Target)
		}
	}

	buildRuleAdapter := func(r *lens.Rule) (adapter.Adapter, error) {
		return buildAdapter(r, buildTargetAdapter)
	}

	// Share a single full.Engine across all full-engine lenses — the engine
	// is stateless; per-rule state lives in the CompiledRule passed to UseFullEngine.
	fullEngine := full.New()

	// projectionRevision reads the current Core KV revision for an arbitrary
	// key. The actor-aggregate envelope uses it to populate
	// `projectedFromRevisions`. Errors and absent keys collapse to 0, which the
	// envelope drops (partial coverage is acceptable).
	projectionRevision := func(k string) uint64 {
		entry, err := coreKV.Get(context.Background(), k)
		if err != nil || entry == nil {
			return 0
		}
		return entry.Revision
	}

	startPipeline := func(r *lens.Rule) {
		// A Secure Lens needs the Vault before anything else is built —
		// refusing here leaves no half-constructed state behind.
		if len(r.Into.SecureColumns) > 0 && vaultBackend == nil {
			logger.Error("secure lens requires a Vault backend — set LATTICE_VAULT_MASTER_KEK(_FILE); lens not activated",
				"lensId", r.ID, "canonicalName", r.CanonicalName)
			return
		}

		// A transient NATS RTT (context deadline exceeded, connection blip)
		// during a boot/hot-reload burst must not permanently strand the
		// lens — buildRuleAdapter's own error path has no way to tell a
		// blip apart from a real config error, so the retry sits here.
		var adpt adapter.Adapter
		err := retryTransientBoot(ctx, func() error {
			var buildErr error
			adpt, buildErr = buildRuleAdapter(r)
			return buildErr
		})
		if err != nil {
			logger.Error("build adapter", "lensId", r.ID, "err", err)
			return
		}

		reporter := health.New(healthKVHandle, r.ID)
		reporter.SetRuleSequence(r.Sequence)
		reporter.SetRuleEngine(r.ResolvedEngine)

		p, err := pipeline.New(r.ID, r.Into.Target, coreKVBucket, adjKV, coreKV, adpt, reporter)
		if err != nil {
			logger.Error("create pipeline", "lensId", r.ID, "err", err)
			return
		}

		// Wire full engine when selected.
		if r.ResolvedEngine == ruleengine.EngineFull {
			if r.CompiledRule == nil {
				logger.Error("full engine selected but CompiledRule is nil", "lensId", r.ID)
				return
			}
			// Thread the output key columns so the engine builds the complete
			// multi-column projection key (a composite-key lens — e.g. a
			// GrantTable lens — needs every key column the adapter requires).
			// Fail closed if a key column is not a RETURN alias; see
			// threadsKeyColumns for which lenses are exempt and why.
			if cr, ok := r.CompiledRule.(*full.CompiledRule); ok && threadsKeyColumns(r) {
				cr.KeyColumns = []string(r.Into.Key)
				if err := cr.ValidateKeyColumns(); err != nil {
					logger.Error("full engine key-column validation", "lensId", r.ID, "err", err)
					return
				}
				// A Secure Lens's secure + identity-key columns must be RETURN
				// aliases — a typo would otherwise project silent nulls (secure
				// column) or Terminal-DLQ every row (identity-key column) with
				// nothing pointing at the misdeclared spec.
				if err := cr.ValidateReturnAliases(secureAliasNames(r.Into.SecureColumns)...); err != nil {
					logger.Error("secure-column RETURN-alias validation", "lensId", r.ID, "err", err)
					return
				}
			}
			p.UseFullEngineBranches(fullEngine, r.CompiledRule, r.CompiledBranches)
		}

		// Fire 3 (negative-filter-retraction-projection-design.md §2.4): a plain
		// lens whose composite output key isn't derivable read-free from its own
		// anchor opts into target-diff retraction via the lens-definition flag —
		// data-driven, not canonical-name-keyed, same as every other per-lens
		// component below. Fail closed if the query isn't genuinely unanchored:
		// the diff compares the target's FULL live key set against the
		// re-execute's FULL freshly-computed row set, which is only exact when
		// that row set is already the complete global truth — an
		// $actorKey-scoped query would instead retract every OTHER live
		// anchor's rows on its first event.
		if r.Into.DiffRetraction {
			cr, ok := r.CompiledRule.(*full.CompiledRule)
			if !ok {
				logger.Error("diff retraction requires the full engine", "lensId", r.ID)
				return
			}
			if err := cr.ValidateUnanchoredForDiffRetraction(); err != nil {
				logger.Error("diff retraction validation", "lensId", r.ID, "err", err)
				return
			}
			// And fail closed if the target cannot enumerate its keys: without
			// that the lens would retract nothing while presenting as a lens
			// that retracts (for a grant producer, as a working revocation
			// path). A dark lens is the safe end of that trade.
			if err := p.SetDiffRetraction(true); err != nil {
				logger.Error("diff retraction adapter support", "lensId", r.ID, "err", err)
				return
			}
			logger.Info("diff retraction installed", "lensId", r.ID)
		}

		// Convergence-lens no-filtering-WHERE activation guard
		// (negative-filter-retraction-projection-design.md's review carry-out;
		// docs/components/refractor.md's authoring invariant). A plain
		// (non-actorAggregate) lens projecting into the shared weaver-targets
		// bucket must carry no filtering WHERE — Fire 2's presence-check
		// retraction would emit a Delete on a WHERE-dropped anchor, which
		// Weaver reads as "entity gone," not "stopped violating." actorAggregate
		// lenses (e.g. unroutedTasks) are exempt: their retraction runs through
		// the envelope's EmptyBehavior, not this path, so a filtering WHERE
		// there is safe and already shipped. Data-driven, not
		// canonical-name-keyed — a brand-new convergence lens is checked for
		// free. Simple-engine lenses have no CompiledRule of this shape and are
		// silently out of scope (they express matching differently).
		if r.Into.Target == "nats_kv" && r.Into.Bucket == bootstrap.WeaverTargetsBucket && !projection.IsActorAggregate(r) {
			if cr, ok := r.CompiledRule.(*full.CompiledRule); ok {
				if err := cr.ValidateNoFilteringWhereForConvergence(); err != nil {
					logger.Error("convergence-lens filtering-WHERE validation", "lensId", r.ID, "err", err)
					return
				}
			}
		}

		// Install the per-lens projection components via data-driven paths keyed
		// off lens-definition aspects — never off the canonical name. An
		// actor-aggregate lens (projectionKind: actorAggregate) is driven by the
		// compiled ProjectionPlan: the §6.13 Output descriptor shapes the on-wire
		// envelope, the cross-vertex fan-out, the empty/delete-key behavior, and the
		// guard predicate. A brand-new package lens that opts in flows through the
		// same path with zero edits here. The operation-aggregate role-index lens
		// (keyed by operationType) is driven by the generic null-key-skip envelope.
		switch {
		case projection.IsActorAggregate(r):
			if !projection.InstallActorAggregate(p, adpt, r, projectionRevision, adjKV, coreKV, logger) {
				return
			}
		case isOperationRoleIndexLens(r):
			// Operation-aggregate lens (the role-by-operation index): keyed by
			// operationType and targeting the capability-kv bucket, it rewrites
			// each row into the Contract #6 §6.1 `cap.role-by-operation.<op>`
			// shape and skips rows whose operationType is null/empty (a collect
			// over zero MATCH bindings). It is keyed by operationType, not by
			// actor — no per-actor revoke→resurrect race — so it is NOT guarded
			// (Contract #6 §6.2/§6.3). Routed off the operationType key plus the
			// capability-kv bucket, not a canonical name.
			p.SetEnvelopeFn(capabilityenv.NewRoleIndexWrapper())
			p.SetLatencyBuffer(pipeline.NewLatencyRingBuffer(pipeline.DefaultLatencyBufferSize))
			logger.Info("operation-aggregate envelope installed", "lensId", r.ID, "key", r.Into.Key[0])
		case projection.IsPersonalLens(r):
			// Personal Lens fan-out (personal-secure-lens-design.md §3.3-3.4,
			// Fires PL.2-PL.3): installs the ActorEnumerator + the
			// "__actor"-injecting envelope, gated by D1's read-grant check
			// (capabilityKV) and filtered through the Interest Set registry.
			// requireReadGate=true: the production refractor fails closed if
			// capabilityKV is nil rather than installing a personal lens open
			// (edge-lattice-full-design.md §8.1 RR-3).
			if !projection.InstallPersonalLens(p, r, adjKV, coreKV, personalInterestKV, capabilityKV, true, logger) {
				return
			}
			// The Hydration Hook (personal-secure-lens-design.md §3.5, Fire
			// PL.4): the "personal.hydrate" control RPC fans out to every
			// registered Personal Lens pipeline for the requesting identity —
			// a deployment installs one per nats_subject rule (edge-manifest
			// alone ships ten), so this is a per-ruleID registry like every
			// other per-lens control hook, not a single overwritten handle.
			controlSvc.RegisterPersonalHydrator(r.ID, p)
			// The syncgap gap-detection read (edge-syncgap-control-rpc-
			// design.md §3.2): the "personal.syncgap" control RPC answers the
			// Edge node's warm-resume freshness check off the control host's
			// own full-grant STREAM.INFO, so the per-identity Edge grant never
			// needs the stream verb it deliberately denies. The stream name is
			// the lens rule's own Into.Stream — the authoritative target the
			// nats_subject adapter and hydrator are already wired from; a "SYNC"
			// literal here could gap-check the wrong stream in a deployment
			// whose personal lens targets a differently-named one, and a
			// wrong-stream FirstSeq can yield a false "not gapped". A fresh
			// Stream lookup per call reads the current FirstSeq (mirrors the
			// deleted natstransport.FirstSequence).
			syncStream := r.Into.Stream
			controlSvc.SetSyncFirstSeq(func(ctx context.Context) (uint64, error) {
				st, err := conn.JetStream().Stream(ctx, syncStream)
				if err != nil {
					return 0, fmt.Errorf("syncgap: look up stream %q: %w", syncStream, err)
				}
				return st.CachedInfo().State.FirstSeq, nil
			})
		}

		// A Secure Lens (Contract #3 §3.10): install the decrypt-at-projection
		// transform (the Vault-present check ran before the adapter was built).
		// translateSpec already guaranteed protected-postgres posture, so the
		// RLS verify-and-pause below applies.
		if len(r.Into.SecureColumns) > 0 {
			cols := make([]pipeline.SecureColumn, len(r.Into.SecureColumns))
			for i, sc := range r.Into.SecureColumns {
				cols[i] = pipeline.SecureColumn{Column: sc.Column, IdentityKeyColumn: sc.IdentityKeyColumn, Field: sc.Field}
			}
			dec, err := pipeline.NewSecureDecryptor(vaultBackend, coreKV, cols, &vaultCalls)
			if err != nil {
				logger.Error("build secure decryptor", "lensId", r.ID, "err", err)
				return
			}
			p.SetSecureDecryptor(dec)
			logger.Info("secure lens decryptor installed", "lensId", r.ID, "columns", len(cols))
		}

		// Configure the supervised runtime: durable name refractor-<ruleID>,
		// queue group = same name (NFR12), DeliverLastPerSubject (ADR-15), Core
		// KV stream + filter. The supervisor creates the durable idempotently when
		// Run registers it (CreateOrUpdateConsumer). ruleID must not be "adjacency"
		// (collides with the bootstrapper's refractor-adjacency consumer).
		// A protected/grant Postgres lens starts infra-paused so its Probe verifies
		// the out-of-band RLS posture BEFORE the first projection — fail-closed
		// (Contract #6 §6.14, verify-and-pause). Every other lens drains
		// immediately (zero-value InitialPause).
		var initialPause substrate.PauseReason
		if r.Into.Protected || r.Into.GrantTable {
			initialPause = substrate.PauseInfra
		}
		p.RunOn(conn, substrate.ConsumerSpec{
			Name:          "refractor-" + r.ID,
			Stream:        subjects.CoreKVStream(coreKVBucket),
			FilterSubject: subjects.CoreKVFilter(coreKVBucket),
			DeliverPolicy: substrate.DeliverLastPerSubject,
			DeliverGroup:  "refractor-" + r.ID,
			InitialPause:  initialPause,
		})

		// Per-lens lag metrics: read pending from the supervised consumer by
		// durable name, so the poller tracks the live consumer across a rebuild
		// reset with no handle re-binding.
		lp := health.NewLagPoller(conn, p.Pending, reporter, r.ID)
		lp.SetProgressFunc(func() time.Time { return p.Progress().LastProjectedAt })
		p.SetLagPoller(lp)

		// Transient write failures escalate to the shared retry queue (deferred
		// backoff, then DLQ on exhaustion) when the rule declares one; absent
		// `retry:`, a Transient failure keeps Naking for redelivery as before.
		if r.Retry.MaxAttempts > 0 {
			backoff, err := failure.ParseISO8601Duration(r.Retry.Backoff)
			if err != nil {
				logger.Error("parse retry backoff", "lensId", r.ID, "backoff", r.Retry.Backoff, "err", err)
				return
			}
			p.SetRetryQueue(retryQueue, conn, r.Retry.MaxAttempts, backoff)
		}

		// Per-rule audit trail: append an entry to lattice.refractor.audit.<lensId>
		// on every successful write (docs/components/refractor-failure-tiers.md).
		aw := health.NewAuditWriter(conn, r.ID)
		if err := retryTransientBoot(ctx, func() error { return aw.EnsureStream(ctx) }); err != nil {
			logger.Error("ensure audit stream", "lensId", r.ID, "err", err)
			return
		}
		p.SetAuditWriter(aw)

		lensCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})

		entry := newPipelineEntry(r, adpt, p, reporter, cancel, done)

		mu.Lock()
		registry[r.ID] = entry
		mu.Unlock()

		wg.Add(1)
		go func() {
			defer wg.Done()
			defer close(done)
			p.Run(lensCtx)
		}()

		controlSvc.Register(r.ID, p, reporter)
		controlSvc.RegisterPauser(r.ID, p)
		controlSvc.RegisterRebuilder(r.ID, p)
		controlSvc.RegisterRowNullifier(r.ID, p)
		controlSvc.RegisterRowSetNullifier(r.ID, p)
		// Per-actor reconciliation is defined only for actor-aggregate lenses
		// (capability-projection-reconciliation-design.md §3.1); the pipeline
		// refuses structurally besides, so the registry is the routing gate
		// rather than a second source of truth about lens kind.
		if projection.IsActorAggregate(r) {
			controlSvc.RegisterReprojector(r.ID, reprojectorFor(p))
		}

		// The convergence sweep (§3.2) runs beside the pump on the same lens
		// context, so it stops with the lens. RunSweep returns immediately
		// unless the driver installed a sweep plan, which it does for an
		// actor-aggregate lens able to scope a listing to its own keys.
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.RunSweep(lensCtx)
		}()

		logger.Info("lens pipeline started", "lensId", r.ID, "target", r.Into.Target, "table", r.Into.Table, "bucket", r.Into.Bucket)
	}

	lookupEntry := func(lensID string) (*pipelineEntry, bool) {
		mu.Lock()
		defer mu.Unlock()
		entry, ok := registry[lensID]
		return entry, ok
	}

	rl := &reloader{
		ctx:          ctx,
		logger:       logger,
		lookup:       lookupEntry,
		buildAdapter: buildRuleAdapter,
		fullEngine:   fullEngine,
	}

	// Wait for adjacency bootstrap before activating any lens.
	select {
	case <-bootstrapper.Ready():
		logger.Info("adjacency bootstrap complete")
	case <-ctx.Done():
		return
	}

	// Source 1: Core KV watch on `vtx.meta.>`, routed by envelope class
	// `meta.lens` (Decision #5; data-contracts.md §1.2 line 70).
	src := lens.NewCoreKVSource(conn, coreKVBucket, instance, logger)
	src.SetLoadCallback(func(r *lens.Rule) {
		mu.Lock()
		_, exists := registry[r.ID]
		mu.Unlock()
		if !exists {
			startPipeline(r)
		}
	})
	src.SetUpdateCallback(rl.update)
	controlSvc.SetRuleGetter(src)
	if err := src.Start(ctx); err != nil {
		logger.Error("start core kv lens source", "err", err)
		os.Exit(1)
	}
	logger.Info("core kv lens source started", "watchPrefix", "vtx.meta.>", "classFilter", "meta.lens")

	// Registry-reconciliation probe (lens-registry-restart-integrity-design.md
	// §4 Fire B step 2) — the detection half: after a boot grace window, and
	// then on a slow tick, diff Core KV's declared lens set against the
	// registry above and raise LensRegistryIncomplete when something is
	// missing, so a cold-registry incident is a red heartbeat issue instead
	// of an invisible one.
	registeredLensIDs := func() []string {
		mu.Lock()
		defer mu.Unlock()
		ids := make([]string, 0, len(registry))
		for id := range registry {
			ids = append(ids, id)
		}
		return ids
	}
	registryProbe := health.NewRegistryProbe(conn, coreKVBucket, registeredLensIDs, logger)
	go registryProbe.Run(ctx)
	hb.RegistryReconciliationProvider = registryProbe.Missing

	// Bootstrap lens (env-gated). Activates only if no meta-lens has loaded
	// after a short grace window AND the env var is set. Decision #7.
	if lens.BootstrapEnabled() {
		go func() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			mu.Lock()
			n := len(registry)
			mu.Unlock()
			if n == 0 {
				logger.Info("activating hardcoded bootstrap lens (REFRACTOR_BOOTSTRAP_LENS set, no meta-lens present)")
				startPipeline(lens.BootstrapLens())
			}
		}()
	}

	if err := controlSvc.StartNATSListener(ctx, nc); err != nil {
		logger.Error("start control NATS listener", "err", err)
		os.Exit(1)
	}
	logger.Info("control service started")

	logger.Info("refractor ready", "instance", instance)
	<-ctx.Done()
	logger.Info("refractor shutting down")
	wg.Wait()
	poolManager.Close()
}

// isOperationRoleIndexLens reports whether r is the operation-aggregate
// role-by-operation index: its sole output key is operationType AND it
// targets the capability-kv bucket (Contract #6 §6.1). Both conditions are
// required — a package lens keyed solely by operationType but targeting a
// different bucket does not match, and is left to its default envelope.
// Derived from the lens's Into descriptor, never from a canonical name.
func isOperationRoleIndexLens(r *lens.Rule) bool {
	return len(r.Into.Key) == 1 && r.Into.Key[0] == "operationType" && projection.IsAuthPlane(r)
}

// threadsKeyColumns reports whether r's Into.Key may be threaded onto its
// compiled rule as RETURN-alias-validated key columns. Only PLAIN projection
// lenses qualify: an envelope lens (actor-aggregate / operation-role-index)
// and a fan-out Personal Lens both derive their projection key from the
// envelope at write time, so their Into.Key (e.g. ["key"], or a Personal
// Lens's reserved "__actor") is deliberately not a RETURN alias — validating
// it would fail on a spec that is entirely correct.
//
// Both the activation path and the MATCH-update (hot-reload) path ask this
// question, and they must answer it identically: a lens that activates but
// whose later cypher edit is refused stays silently pinned to its old cypher
// until the process restarts.
func threadsKeyColumns(r *lens.Rule) bool {
	return !projection.IsActorAggregate(r) && !isOperationRoleIndexLens(r) && !projection.IsPersonalLens(r)
}

// hotReloadKeyColumns returns the key columns to thread onto a rule's newly
// compiled form on the MATCH-update (hot-reload) path, and whether to thread
// (and therefore validate) at all.
//
// This must agree with what the ACTIVATION path installs, or a lens behaves
// differently after a cypher edit than it did at boot. A Personal Lens is the
// case that bites: it is exempt from threading Into.Key verbatim, because the
// reserved "__actor" field is envelope-injected and never a RETURN alias — but
// it still declares real business key columns, and InstallPersonalLens sets
// exactly those at activation. Threading nothing leaves KeyColumns empty,
// which drops the executor to its first-RETURN-item fallback: the keys map
// then carries only the first alias, the adapter rejects every write with
// "key field %q absent from keys map", and the pipeline retries that failure
// for as long as the process runs.
func hotReloadKeyColumns(r *lens.Rule) ([]string, bool) {
	switch {
	case threadsKeyColumns(r):
		return []string(r.Into.Key), true
	case projection.IsPersonalLens(r):
		return projection.PersonalBusinessKeys(r), true
	default:
		return nil, false
	}
}

// loadVault wires the optional local envelope-encryption Vault backend for
// Secure-Lens decrypt-at-projection. The master KEK is read from
// LATTICE_VAULT_MASTER_KEK (inline base64) if set, else from the file at
// LATTICE_VAULT_MASTER_KEK_FILE — the same sources cmd/processor uses, so one
// provisioned KEK (make provision-vault-kek) serves both processes. Unlike
// the Processor (which refuses to start without a KEK — it would otherwise
// commit plaintext), neither being set is fine here: (nil, nil), and any
// Secure Lens fails closed at activation instead.
func loadVault(logger *slog.Logger) (*vault.LocalBackend, error) {
	envVar, fileVar := "LATTICE_VAULT_MASTER_KEK", "LATTICE_VAULT_MASTER_KEK_FILE"
	var kek []byte
	var err error
	switch {
	case os.Getenv(envVar) != "":
		kek, err = vault.MasterKEKFromEnv(envVar)
	case os.Getenv(fileVar) != "":
		kek, err = vault.MasterKEKFromFile(os.Getenv(fileVar))
	default:
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load vault master KEK: %w", err)
	}
	v, err := vault.NewLocalBackend(kek, envOr("LATTICE_VAULT_KEK_VERSION", ""))
	if err != nil {
		return nil, fmt.Errorf("construct vault backend: %w", err)
	}
	logger.Info("vault wired for secure lenses", "backend", "local")
	return v, nil
}

// secureColumnsEqual reports whether two secure-column declarations are
// identical (order-sensitive — the spec is authored, not computed).
func secureColumnsEqual(a, b []lens.SecureColumn) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// secureAliasNames collects every RETURN alias a secure-column declaration
// consumes (the ciphertext column + its identity-key column, deduplicated).
func secureAliasNames(cols []lens.SecureColumn) []string {
	if len(cols) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(cols)*2)
	out := make([]string, 0, len(cols)*2)
	for _, sc := range cols {
		for _, n := range []string{sc.Column, sc.IdentityKeyColumn} {
			if _, dup := seen[n]; dup {
				continue
			}
			seen[n] = struct{}{}
			out = append(out, n)
		}
	}
	return out
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// wireControlChecker builds the control-plane capability checker
// (control-plane-capability-authz-design.md Fire 1b). Default LATTICE_AUTH_MODE
// is `capability` — mirrors cmd/processor's step-3 default; `stub` remains
// available for dev/test behind the same explicit env knob (one knob, no
// second CTRL-specific one, design §3.3). rbacRolesActive + systemActorKeys
// mirror the Processor's step-3 platform routing so the checker reads the
// same key the Processor would for a given actor. Preflight logs+alerts
// (never blocks startup) if the configured operator actor's grant is
// unresolvable.
func wireControlChecker(ctx context.Context, conn *substrate.Conn, component string, ops map[string]controlauth.OpMeta, logger *slog.Logger) (*controlauth.CapabilityKVChecker, error) {
	mode := controlauth.AuthMode(envOr("LATTICE_AUTH_MODE", string(controlauth.AuthModeCapability)))
	if mode == controlauth.AuthModeStub {
		return nil, fmt.Errorf("LATTICE_AUTH_MODE=stub is not permitted for a running component — stub (allow-all) control auth is retired as a deployable posture; use capability")
	}

	// Class-aware platform routing is unconditional (mirrors cmd/processor's
	// step-3 wiring): system actors read the cap.<actor> ∪ cap.roles.<actor>
	// union, every other actor reads cap.roles.<actor>. Correct whether or not
	// rbac-domain is installed (an absent cap.roles.<actor> is an empty skip in
	// capabilitykv.ReadAndMerge), so it is deliberately NOT gated on a boot-time
	// rbac-install probe — that probe latched the pre-install state for a
	// component booted before packages install and denied every package-granted
	// actor for the process lifetime. SystemActorKeys are primordial (stable
	// post-bootstrap), so a one-time discovery here is enough.
	discCtx, discCancel := context.WithTimeout(ctx, 10*time.Second)
	systemActorKeys, err := bootstrap.SystemActorKeys(discCtx, conn)
	discCancel()
	if err != nil {
		return nil, fmt.Errorf("discover system actor keys: %w", err)
	}

	alerts := controlauth.NewHealthAlertEmitter(conn, healthKVBucket, logger)
	checker := controlauth.NewCapabilityKVChecker(component, ops, conn, bootstrap.CapabilityKVBucket,
		systemActorKeys, true, mode, alerts, logger)

	operatorActor := os.Getenv("LATTICE_CONTROL_OPERATOR_ACTOR_KEY")
	preflightCtx, preflightCancel := context.WithTimeout(ctx, 10*time.Second)
	controlauth.Preflight(preflightCtx, checker, operatorActor, logger)
	preflightCancel()

	logger.Info("control-plane checker wired (class-aware, unconditional)",
		"component", component, "authMode", string(mode),
		"systemActors", len(systemActorKeys))
	return checker, nil
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// reprojectorFor bridges a pipeline to the control service's Reprojector.
// The two carry structurally identical results under different types so that
// internal/control never imports internal/pipeline; this composition root is
// the one place that knows both.
func reprojectorFor(p *pipeline.Pipeline) control.Reprojector {
	return pipelineReprojector{p: p}
}

type pipelineReprojector struct{ p *pipeline.Pipeline }

func (r pipelineReprojector) Reproject(ctx context.Context, actorKey string) (control.Reprojection, error) {
	res, err := r.p.Reproject(ctx, actorKey)
	if err != nil {
		return control.Reprojection{}, err
	}
	return control.Reprojection{
		Actor:         res.Actor,
		Converged:     res.Converged,
		Deleted:       res.Deleted,
		Wrote:         res.Wrote,
		ProjectionSeq: res.ProjectionSeq,
	}, nil
}
