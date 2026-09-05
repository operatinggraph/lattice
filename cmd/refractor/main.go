// refractor is the Lattice projection engine. It consumes Core KV CDC and
// sources lens definitions from `vtx.meta.>` (filtered by envelope class
// `meta.lens` per data-contracts.md §1.2 line 70).
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"slices"
	"strconv"
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
	"github.com/operatinggraph/lattice/internal/refractor/classkeyshredded"
	"github.com/operatinggraph/lattice/internal/refractor/consumer"
	"github.com/operatinggraph/lattice/internal/refractor/control"
	"github.com/operatinggraph/lattice/internal/refractor/failure"
	"github.com/operatinggraph/lattice/internal/refractor/grantchange"
	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/keyshredded"
	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/projection"
	"github.com/operatinggraph/lattice/internal/refractor/rebuildgate"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/refractor/subjects"
	"github.com/operatinggraph/lattice/internal/refractor/taxonomy"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/vault"
)

const (
	coreKVBucket             = "core-kv"
	healthKVBucket           = "health-kv"
	adjacencyKVBucket        = "refractor-adjacency"
	personalInterestKVBucket = "personal-lens-interest"
	defaultHeartbeatEvery    = 10 * time.Second
	// lensAckWait bounds how long JetStream waits for a lens pipeline to ack a
	// delivered CDC event before redelivering it. The JetStream default (30s)
	// is shorter than a heavy fan-out evaluation under load, and a redelivery
	// issued mid-evaluation repeats the work while the first attempt is still
	// running — under sustained pressure the consumer live-locks: every
	// in-flight message is a redelivery and the ack floor stops advancing
	// (measured on a swap-bound host: floors advanced ~16 messages/hour while
	// the process burned full CPU). Redelivery exists to recover from a DEAD
	// consumer, not a slow one, so the window is sized far above any sane
	// evaluation; a crashed process's messages redeliver to the restarted
	// pipeline after this wait, which projection freshness tolerates (the
	// sweeps backstop staleness).
	lensAckWait = 5 * time.Minute
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
	// personal is the DECLARED half of "this lens publishes to devices"
	// (projection.IsPersonalLens over the activated rule), read alongside the
	// running pipeline's adapter by publishesToDevices. The declaration is what
	// makes the answer safe where the pipeline is absent: on the class-key
	// erasure path a "no" ADMITS the target, so an entry that has not yet been
	// activated must still answer for what its spec says it is.
	personal bool
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
	// delete key and sweep plan were installed from. Neither swap re-runs that
	// installation, so an edit here re-activates the lens (reload.go's
	// reloader.reactivate).
	output *lens.OutputDescriptorSpec
	// projectionKind is the aspect activation's install switch dispatched on
	// (projection.IsActorAggregate). It is recorded beside output because it
	// decides whether the descriptor above was installed AT ALL: a lens edited
	// into or out of the actor-aggregate kind needs the same re-installation an
	// Output edit does, with a byte-identical descriptor on both sides, and
	// nothing else on the reload path examines it.
	projectionKind string
	// secureColumns is the Secure-Lens column set the RUNNING pipeline's
	// decryptor was built from. Hot-reload guards compare an incoming spec
	// against this — not against the last-seen spec — so a refused update
	// cannot poison the baseline and wedge the lens.
	secureColumns []lens.SecureColumn

	// rule is the *lens.Rule the RUNNING pipeline was activated with — set
	// at activation (newPipelineEntry) and replaced only by a successful
	// MATCH hot-reload (reload.go's MatchChange arm), never by an INTO-only
	// one (which swaps the adapter, not the compiled rule the pipeline
	// evaluates). A taxonomy re-derivation (reloader.rederiveEntry) needs
	// the actual CompiledRule/CompiledBranches to call
	// UseFullEngineBranches again — there is no cheaper thing to compare
	// against, because the compiled rule IS the state a re-derivation
	// replaces (dynamic-type-taxonomy-design.md §14 Fire A item 4, §17.6).
	rule *lens.Rule

	// taxExpansionLabels is the union of ExpansionLabels() across rule's
	// CompiledRule + CompiledBranches (unionExpansionLabels), computed once
	// whenever rule is set rather than on every taxonomy event. A rule with
	// no `*` anywhere has an empty, non-nil union — what both
	// newPipelineEntry and reloader.rederiveEntry use to skip a lens the
	// taxonomy can never affect.
	taxExpansionLabels map[string]struct{}

	// taxMu guards taxExpansion/taxExpansionStatus/taxExpansionResolved/
	// taxGen/taxRebuild* below.
	// Both CoreKVSource's single dispatch goroutine (reloader.rederiveEntry's
	// compare-and-commit) and the rebuild goroutine it starts touch these
	// fields, so every read and every write goes through taxMu.
	taxMu sync.Mutex
	// taxExpansion and taxExpansionStatus are the resolver ANSWER that
	// produced the client gate the running pipeline currently publishes — the
	// baseline reloader.rederiveEntry diffs a fresh Expand call against to
	// tell a real taxonomy change from a no-op.
	//
	// "The answer that produced it", not "the set it matches against": on the
	// degrade path the two differ by construction. A StatusUnknown answer is
	// (nil, StatusUnknown) here, while the gate published from it matches
	// against the pipeline's last known good expansion
	// (Pipeline.carriedLabelExpansion). That is the right thing to diff
	// against anyway — change detection asks whether the RESOLVER moved — and
	// it is what makes the return-to-resolvable republish: taxonomyExpansionEqual
	// compares Status first, so (nil, StatusUnknown) is unequal to every
	// resolvable answer, degraded gate included.
	//
	// Whether the server-side consumer filter has caught up is a separate
	// question with its own field (taxRebuildPending). Folding the two into
	// one latch would make a failed rebuild leave a baseline describing a gate
	// that was never in effect, and then swallow the very event that would
	// repair it.
	//
	// The assignment and the publication it describes are NOT one atomic step:
	// they are separate critical sections under two different mutexes (the
	// pipeline's ruleMu, then this taxMu). What orders them is that every
	// writer — reloader.rederiveEntry, reloader.update, and the taxonomy
	// callbacks that drive them — runs on CoreKVSource's single dispatch
	// goroutine, so no second publication can interleave between a publish and
	// the baseline that records it. taxMu is still taken on every access
	// because the rebuild goroutine reads these fields off that goroutine. A
	// writer added anywhere else needs an ordering argument of its own; the
	// lock alone will not supply one.
	//
	// Seeded at activation from the resolver (newPipelineEntry), not left at
	// a conservative zero value — see that function's doc for why an
	// inaccurate baseline only ever costs one redundant re-derivation, never
	// a missed one.
	taxExpansion       map[string]map[string]struct{}
	taxExpansionStatus taxonomy.Status
	// taxExpansionResolved is the last RESOLVED (non-StatusUnknown) expansion
	// answer — the set the running gate is actually matching against, which is
	// exactly what taxExpansion cannot tell you: taxExpansion records the
	// ANSWER, and on the §6.5 degrade path that answer is (nil, StatusUnknown)
	// while the gate keeps matching against the last known good set.
	//
	// It exists so reloader.rederiveEntry can tell a SHRINK from a grow: a
	// shrink must truncate the target before the replay, or the dropped
	// subtype's already-projected rows orphan forever (no event for a type the
	// narrowed filter no longer admits will ever arrive to retract them — on a
	// grant-producing lens an orphaned row is a live grant).
	//
	// Lifetime: seeded at activation alongside taxExpansion (newPipelineEntry)
	// when the first answer is resolved; replaced on every later resolved
	// answer, whether from a taxonomy re-derivation or a MATCH hot-reload;
	// NEVER cleared by a StatusUnknown answer, because an unresolved expansion
	// changes nothing about what the gate is matching against; and untouched by
	// a failed rebuild, so the shrink it describes is still detectable on the
	// retry.
	taxExpansionResolved map[string]map[string]struct{}
	// taxGen counts publications of this entry's client gate — a taxonomy
	// re-derivation or a MATCH hot-reload. A rebuild goroutine captures it
	// before it starts and compares on the way out, so the answer of a
	// rebuild that a newer publication has already superseded is discarded
	// instead of clearing a flag that describes a gate it never saw.
	taxGen uint64
	// taxRebuildPending reports that the gate taxExpansion describes has no
	// SUCCESSFUL Rebuild behind it — the consumer's registered filter may
	// still carry an older label set. It is what keeps rederiveEntry's
	// unchanged fast path (which exists to stop a rebuild storm) from
	// swallowing a taxonomy that moved away and came back: the sequence
	// E0 → E1 (gate published, rebuild failed) → E0 compares equal against a
	// baseline that IS in effect, and the outstanding rebuild is what still
	// needs driving.
	taxRebuildPending bool
	// taxRebuildTruncate makes the owed rebuild clear the target store before
	// it replays — set when the set the gate publishes SHRANK, either because
	// the taxonomy dropped a subtype (rederiveEntry) or because a MATCH edit
	// narrowed the label set (reloader.update). The narrowed filter admits no
	// event for what was dropped, so nothing will ever be delivered that could
	// retract those rows one by one.
	//
	// Sticky until a rebuild SUCCEEDS: a shrink whose rebuild failed must retry
	// WITH the truncate, so it is cleared in the same taxMu critical section
	// that clears taxRebuildPending and never on its own — the two can then not
	// disagree about what the outstanding rebuild owes. A grow landing while a
	// shrink's rebuild is still owed leaves the flag set, which merely makes
	// that rebuild a full re-derivation from empty: strictly the safe
	// direction, since the replay re-projects everything the current gate
	// admits.
	//
	// The flag asks for a truncate; whether the shrink actually retracts is a
	// property of the lens's TARGET, and there are three cases.
	//
	//   - The lens scopes its own rows in a shared bucket. A NATS-KV
	//     actor-aggregate whose Output declares a literal key prefix (the
	//     capability-kv `cap.svc.{actorSuffix}` shape) gets that prefix bound
	//     onto its adapter by projection.ApplyTruncateScope, so the purge covers
	//     its rows and no sibling producer's. This is the case the flag is
	//     built for, and the one an unscoped purge would turn into a
	//     platform-wide authorization wipe.
	//   - The lens owns its target outright — no key prefix, or a Postgres
	//     table of its own. Truncate clears the whole target, which is exactly
	//     the from-empty state a §6.2 replay converges to.
	//   - The lens declares DiffRetraction. The shared grant table implements
	//     no adapter.Truncater at all, so the truncate is declined with a log
	//     line and the retraction rides the transport the lens already declares
	//     (§6.4): a diffRetraction lens never seeds its evaluation, so every
	//     replayed event recomputes the lens's complete row set, and
	//     Pipeline.applyDiffRetraction deletes every key the target still
	//     carries that the fresh set no longer produces — scoped to the lens's
	//     own grant_source. It needs one delivered event to run on: a rebuild
	//     whose narrowed filter admits no live subject retracts nothing until
	//     the next event the lens reacts to.
	//
	// Two shapes retract NOTHING on a shrink, and the flag is deliberately NOT
	// set for either — a truncate that cannot be confined does more damage than
	// the orphaned rows it would remove.
	//
	//   - An UNSCOPED shared target: a NATS-KV lens sharing its bucket with
	//     other producers but declaring no output key prefix
	//     (rbac-domain's capabilityRoleIndex on capability-kv; one-bill's four
	//     lenses on one history bucket). NatsKVAdapter.Truncate would purge the
	//     whole bucket — every sibling producer's rows, healed only at sweep
	//     pace. reloader.truncateIsSafe refuses it, through
	//     Pipeline.RebuildTruncateIsScoped, and says so on the lens's log.
	//   - An un-truncatable target with no DiffRetraction: a GrantTable lens
	//     that retracts through anchor tombstones alone.
	//
	// For both, the dropped rows persist until an anchor tombstone, a sweep or
	// an operator reaches them. A lens of either shape must not be authored with
	// a `*` label, or edited to narrow its labels, until it either scopes its
	// target or declares a retraction transport.
	taxRebuildTruncate bool
	// taxRebuildRunning reports that a goroutine is currently driving the
	// pending rebuild. At most one runs per entry: it re-reads taxGen after
	// each attempt and loops when a newer gate landed underneath it, so
	// re-derivations settle in publication order rather than racing.
	taxRebuildRunning bool
}

// isPerEntryCapReadOutput reports whether output describes a cap-read.*
// PerEntry producer — EntryKeyColumn set and OutputKeyPattern prefixed
// "cap-read." — the same two declared descriptor fields sweepEnrolment and
// ApplyTruncateScope already key structural per-lens decisions off, not a
// parse of the lens's compiled cypher. Factored out of capReadShredTargets so
// lens activation (validatePerEntryCapReadAdapter) applies the identical test
// before a misconfigured lens ever reaches the registry.
func isPerEntryCapReadOutput(output *lens.OutputDescriptorSpec) bool {
	return output != nil && output.EntryKeyColumn != "" && strings.HasPrefix(output.OutputKeyPattern, "cap-read.")
}

// capReadShredTargets returns a keyshredded.NullifyTarget for every currently
// running lens whose Output descriptor is a cap-read.* NATS-KV PerEntry
// producer — the base capabilityRead lens and every package-generated
// AnchorWalk producer alike (cap-read-per-anchor-grant-keys-design.md's Fire
// 2 follow-on). Pure over its input so it is testable without a live registry
// mutex; the caller holds the lock.
//
// A hand-authored Postgres GrantTable cap-read producer (e.g.
// packages/clinic-domain's grant_source lenses) carries no Output descriptor,
// so it is never returned here and its rows are not nullified by this sweep.
// That is an open gap in the erasure, not an exclusion: the descriptor is what
// names the per-entry key a nullify has to reach, and a producer without one
// gives this function nothing to aim at.
func capReadShredTargets(registry map[string]*pipelineEntry) []keyshredded.NullifyTarget {
	var targets []keyshredded.NullifyTarget
	for id, entry := range registry {
		if !isPerEntryCapReadOutput(entry.output) {
			continue
		}
		targets = append(targets, keyshredded.NullifyTarget{RuleID: id, PerEntry: true})
	}
	return targets
}

// holderTypeRebuildTargets returns a RebuildTarget for every running lens with a
// secure column that declares holderType — the enumeration a retention-class key
// destruction is delivered through (retention-class-key-custody-design.md §6.3
// step 2).
//
// It reads the DECLARED SecureColumn.HolderTypes the running pipeline's
// decryptor was built from, never an inference over compiled cypher: which
// holders a column will open is a statement its author made, and deriving it
// instead would make an erasure's completeness depend on a parser agreeing with
// a declaration. Pure over its input so it is testable without a live registry
// mutex; the caller holds the lock, exactly as capReadShredTargets is used.
//
// A lens that publishes to devices is enumerated and LABELLED, never omitted:
// the consumer refuses such a target and withholds the attestation, where
// dropping it here would leave the destruction reading as complete over
// plaintext its devices still hold.
func holderTypeRebuildTargets(registry map[string]*pipelineEntry, holderType string) []classkeyshredded.RebuildTarget {
	var targets []classkeyshredded.RebuildTarget
	for id, entry := range registry {
		for _, col := range entry.secureColumns {
			if slices.Contains(col.HolderTypes, holderType) {
				targets = append(targets, classkeyshredded.RebuildTarget{
					RuleID:   id,
					Personal: entry.publishesToDevices(),
				})
				break
			}
		}
	}
	return targets
}

// publishesToDevices reports whether this entry writes to a per-actor subject
// stream devices subscribe to — either because the lens was activated as one
// (projection.IsPersonalLens over its rule) or because the adapter it is writing
// through right now is a per-actor publisher.
//
// Both, and the union is deliberate. The RUNNING adapter has to be asked because
// an INTO-only reload can move a lens onto a personal target the spec this entry
// was built from never named. The DECLARED fact has to be asked because the
// running one is unavailable on a nil pipeline — the unit fixtures that build
// entries out of the declared fields, and any entry not yet activated.
//
// Which way an absent answer resolves is the whole point. This label reaches the
// class-key erasure attestation, where FALSE is the ADMITTING answer: a target
// labelled non-personal is rebuilt and attested on, while a personal one is
// refused because its plaintext also sits on devices no rebuild reaches. So an
// unavailable running answer must fall back to what the lens declares itself to
// be, never to the admitting default.
func (e *pipelineEntry) publishesToDevices() bool {
	return e.personal || (e.pipeline != nil && e.pipeline.PublishesToDevices())
}

// validatePerEntryCapReadAdapter refuses a PerEntry cap-read.* lens whose
// adapter cannot enumerate keys by prefix. keyshredded's PerEntry
// nullification path (pipeline.DeleteAllForActor) requires
// adapter.PrefixKeyLister, which only NatsKVAdapter implements today
// (anchorwalk.go hardcodes "nats-kv"); capReadShredTargets discovers PerEntry
// targets by descriptor alone, so a future non-NATS-KV producer (e.g.
// Postgres) would otherwise reach the registry undetected and only fail at
// the first live shred event — pausing the whole auth-plane lens
// (privacy-critical, no retry) as a side effect of an unrelated identity's
// shred, an outage the removed static-Targets operator gate previously made
// structurally impossible. Catching the same gap here, at activation, refuses
// only the misconfigured lens itself
// (cap-read-per-anchor-grant-keys-design.md's Fire 4 residual).
func validatePerEntryCapReadAdapter(r *lens.Rule, adpt adapter.Adapter) error {
	if !isPerEntryCapReadOutput(r.Output) {
		return nil
	}
	if _, ok := adpt.(adapter.PrefixKeyLister); !ok {
		return fmt.Errorf("lens %q: perEntry cap-read.* lens targets adapter %T, which cannot enumerate keys by prefix (required for keyshredded nullification)", r.ID, adpt)
	}
	return nil
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

	instance := newInstanceToken()
	logger.Info("refractor starting", "instance", instance, "natsURL", *natsURL)

	// The primordial identifier table, loaded before anything in this process
	// names a primordial entity. Two consumers depend on it, and an empty table
	// silently degrades both rather than failing:
	//
	//   - wireControlChecker's bootstrap.SystemActorKeys matches holdsRole links
	//     against the roleOperator NanoID, so an empty one routes every actor —
	//     the primordial admin and the kernel service actors included — as
	//     ordinary at the control-plane capability checker.
	//   - The KeyShredded nullification listener's target is
	//     {RuleID: bootstrap.CapabilityReadLensID} (see the keyshredded.New call
	//     below). An empty RuleID matches no registered rule, so every
	//     ShredIdentityKey's cap-read nullification would nak to the redelivery
	//     cap and give up, leaving the shredded identity's read-grant
	//     projections in place — privacy residue, reported only as a warning.
	//
	// Same env/default as every other daemon (cmd/processor/main.go:71).
	bootstrapJSONPath := envOr("BOOTSTRAP_JSON_PATH", "./lattice.bootstrap.json")
	if err := bootstrap.Load(bootstrapJSONPath); err != nil {
		logger.Error("load bootstrap JSON", "path", bootstrapJSONPath, "err", err)
		os.Exit(1)
	}

	// Live introspection (heap/goroutine/CPU profiles) on a loopback listener,
	// enabled only when REFRACTOR_PPROF_ADDR is set — a runaway process can be
	// asked what it is holding (`go tool pprof http://<addr>/debug/pprof/heap`)
	// instead of being killed blind. The operator sets a loopback address;
	// nothing binds by default.
	if pprofAddr := os.Getenv("REFRACTOR_PPROF_ADDR"); pprofAddr != "" {
		go func() {
			logger.Info("pprof listener", "addr", pprofAddr)
			if err := http.ListenAndServe(pprofAddr, nil); err != nil {
				logger.Error("pprof listener exited", "addr", pprofAddr, "err", err)
			}
		}()
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Substrate is the integration boundary. Refractor is a long-running
	// daemon: MaxReconnects: -1 keeps nats.go retrying forever, because a
	// closed *nats.Conn never recovers on its own — without it, an outage
	// long enough to exhaust nats.go's own default budget (60 attempts, ~2s
	// apart) leaves the process alive but permanently disconnected, driving
	// the lens engine while touching no NATS subject ever again.
	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{
		URL: *natsURL,
		// Name is what identifies this process in `nats connz` during an
		// incident; without it the engine holding the most subscriptions on
		// the server is the one nobody can put a name to.
		Name:          "lattice-refractor:" + instance,
		MaxReconnects: -1,
		ReconnectWait: 1 * time.Second,
		NKeySeedFile:  envOr("NATS_NKEY", ""),
		CredsFile:     envOr("NATS_CREDS", ""),
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

	// Retires the superseded per-lens "AUDIT_<ruleID>" stream layout —
	// idempotent (an already-cleaned deployment no-ops) and self-healing, so
	// no environment needs a manual sweep. Must run BEFORE EnsureAuditStream
	// below: each legacy stream's subject (lattice.refractor.audit.<ruleID>)
	// is a literal subset of the consolidated stream's wildcard
	// (lattice.refractor.audit.>), and JetStream permanently refuses to
	// create a stream whose subjects overlap an existing one's
	// (JSStreamSubjectOverlapErr) — so on any deployment that still has
	// legacy streams, creating REFRACTOR_AUDIT first would fail every boot
	// until they were removed by hand. Best-effort: a failure here leaves
	// stray legacy streams for the next boot to retry — EnsureAuditStream's
	// own overlap error is the fail-closed backstop if one survives.
	var legacyAuditStreamsDeleted int
	cleanupErr := retryTransientBoot(ctx, func() error {
		n, err := health.CleanupLegacyAuditStreams(ctx, conn)
		legacyAuditStreamsDeleted = n
		return err
	})
	if cleanupErr != nil {
		logger.Error("cleanup legacy audit streams", "err", cleanupErr)
	} else if legacyAuditStreamsDeleted > 0 {
		logger.Info("retired legacy per-lens audit streams", "count", legacyAuditStreamsDeleted)
	}

	// The consolidated Refractor audit trail (docs/components/refractor.md's
	// Audit row): one JetStream stream shared by every lens, ensured once
	// here rather than per lens in startPipeline — every lens's AuditWriter
	// below publishes into it under its own subject.
	if err := retryTransientBoot(ctx, func() error { return health.EnsureAuditStream(ctx, conn) }); err != nil {
		logger.Error("ensure audit stream", "err", err)
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

	// ONE gate for every path that starts a lens rebuild — the taxonomy reload
	// scheduler (rl.taxRebuild, below) and the operator "rebuild" control op.
	// Each rebuild is a durable JetStream delete-recreate, and the server pays
	// the same for it whichever path asked; a bound held per path would leave
	// the sum unbounded, which is the burst taxonomyRebuildConcurrency exists to
	// prevent. Sharing the instance also serializes the two paths per lens, so
	// an operator rebuild and a taxonomy rebuild of one lens cannot interleave.
	rebuildGate := rebuildgate.New(taxonomyRebuildConcurrency)

	controlSvc := control.NewService()
	controlSvc.SetRebuildGate(rebuildGate)
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
	// bootstrapper.Ready()/src.Start below) correctly hits ErrRuleNotRegistered
	// → NakWithDelay and retries, instead of an empty
	// target list vacuously Acking + recording the identity as clean with
	// nothing actually checked (a regression this must not reintroduce).
	// Hand-authored Postgres GrantTable cap-read producers
	// (packages/clinic-domain's four) carry no Output descriptor and
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

	// taxResolver is the single dynamic-type-taxonomy resolver every
	// full-engine pipeline's `*` expansion reads (dynamic-type-taxonomy-
	// design.md §14 Fire A item 4) — installed on every pipeline at
	// activation via SetTaxonomyResolver (startPipeline, below) and fed by
	// the lens source's taxonomy callback (wired near src.Start, below).
	taxResolver := taxonomy.New()

	// rl is predeclared empty here: startPipeline (defined next) must be
	// able to record a taxonomy activation refusal on it, and startPipeline
	// is itself one of rl's dependencies (rl.activateForTaxonomy) — so rl
	// must exist before startPipeline's closure captures it. The remaining
	// fields are filled in further down, once everything they close over
	// exists; recordRefusedForTaxonomy/clearRefusedForTaxonomy only ever
	// touch rl.refused/rl.refusedMu, which need no other field to be safe
	// to call against this zero-value *reloader.
	rl := &reloader{}

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

	// The retention-class half. A class holder is not the ciphertext's host, so
	// no CDC event reaches the lenses whose rows it opens and no sweep enrols
	// them — the destruction is delivered by rebuilding each declaring lens
	// instead (retention-class-key-custody-design.md §6.3). Like the identity
	// half's listers, the target set is re-derived from the live registry per
	// event so a lens installed after this line is still reached.
	classKeyShredded := classkeyshredded.New(classkeyshredded.Config{
		Conn:         conn,
		EventsStream: bootstrap.CoreEventsStreamName,
		Control:      controlSvc,
		Logger:       logger,
		ActorKey:     privacyActorKey,
	})
	classKeyShredded.SetTargetLister(func(holderType string) []classkeyshredded.RebuildTarget {
		mu.Lock()
		defer mu.Unlock()
		return holderTypeRebuildTargets(registry, holderType)
	})
	go func() {
		if err := classKeyShredded.Run(ctx); err != nil && ctx.Err() == nil {
			logger.Error("classkeyshredded listener exited with error", "err", err)
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
				copyCapabilitySweepStatus(&snap, sw.Status(), sw.Interval())
			}
			// The divergence audit's verdicts, read from the in-process auditor
			// on exactly the terms the sweep's are: before the reporter, because
			// an unreadable health entry is an observation fault and not an
			// audit one. A REFUSED lens carries a non-nil auditor too — its
			// refusal is a published verdict, and this is the field that
			// publishes it.
			if au := entry.pipeline.Auditor(); au != nil {
				copyCapabilityAuditStatus(&snap, au.Status(), au.Interval())
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
			// Everything st already carries is transferred BEFORE the pending
			// read, which can fail on its own. A pause reason and a last error
			// are facts in hand about the lens; losing them to a fault observing
			// something else leaves the lens looking merely unobserved, and
			// Loupe's fault conjunct keys off a live LastError.
			if st.PauseReason != nil {
				snap.PauseReason = *st.PauseReason
			}
			if st.LastError != nil {
				snap.LastError = *st.LastError
			}
			// A structural pause this lens cleared under its own probe. Every
			// grant-table lens is auth-plane (projection.IsAuthPlane) AND is
			// opted into structural self-heal (verifiesReadPathPosture), so this
			// path carries the recovery for the lenses feeding actor_read_grants
			// — precisely the ones whose silent self-heal would be worst. An
			// unparseable stamp leaves the zero time, which the heartbeater reads
			// as "never self-healed": a malformed value must not manufacture a
			// recovery that did not happen.
			if st.StructuralAutoRecoveredAt != "" {
				if at, perr := time.Parse(time.RFC3339, st.StructuralAutoRecoveredAt); perr == nil {
					snap.StructuralAutoRecoveredAt = at
				}
			}
			snap.StructuralAutoRecoveredCause = st.StructuralAutoRecoveredCause
			snap.StructuralAutoRecoveryAttempts = st.StructuralAutoRecoveryAttempts
			pending, err := entry.pipeline.Pending(context.Background())
			if err != nil {
				snap.Status = "unknown"
				snap.Unreadable = "consumer pending count: " + err.Error()
				out = append(out, snap)
				continue
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
				copyLensSweepStatus(&snap, sw.Status(), sw.Interval())
			}
			// The divergence audit's verdicts — for a plain lens this is the
			// ONLY per-row correctness signal there is, so it is read on the
			// same terms as the sweep's and for a sharper version of the same
			// reason: the audit writes nothing, so a verdict lost to a fault
			// observing something else is a divergence nothing else will find.
			// A REFUSED lens carries a non-nil auditor too, because its refusal
			// is a published verdict rather than an absence.
			if au := entry.pipeline.Auditor(); au != nil {
				copyLensAuditStatus(&snap, au.Status(), au.Interval())
			}
			// The plain arm's neighbour-anchor derivation, read from the same
			// live pipeline and ahead of the reporter, for the reason the two
			// above are read there: the tally is per-run state no health entry
			// carries, and a retraction transport that is falling back must not
			// be lost to a fault observing something else. A lens whose
			// derivation is not armed copies a zero status, which publishes
			// nothing rather than a zero that would read as a verdict.
			copyLensDerivationStatus(&snap, entry.pipeline.PlainDerivationStatus())
			// What carries a retraction when a NEIGHBOUR event drops one of
			// this lens's rows, re-derived per beat off the same rule snapshot
			// the licence reads — so a MATCH hot-reload that changes the shape
			// (and with it the closure verdict, the scan-root graph, or the
			// neighbour dependency itself) cannot leave a stale posture
			// published. entry.authPlane is this branch's own precondition,
			// carried in rather than re-derived so the field and the activation
			// gate answer about the same plane.
			copyLensRetractionTransport(&snap, entry.pipeline.PlainRetractionTransport(entry.authPlane))
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
			// Everything st already carries is transferred BEFORE the pending
			// read, which can fail on its own. Same doctrine as the sweep
			// verdicts above — an observation fault about one input must not
			// erase a fact already in hand about another. The redaction count is
			// the sharpest case (a NATS blip would silently downgrade the
			// highest-ranked alert in the system to a warning), but a pause
			// reason and a last error are facts too, and Loupe's fault conjunct
			// keys off a live LastError.
			snap.SecureRedactions = st.SecureRedactions
			if st.PauseReason != nil {
				snap.PauseReason = *st.PauseReason
			}
			if st.LastError != nil {
				snap.LastError = *st.LastError
			}
			// A structural pause this lens cleared under its own probe. It is a
			// fact about a lens that is FINE now, which makes it the easiest one
			// to lose to a fault observing something else — and the one whose
			// loss is silent, since the recovered entry is otherwise identical to
			// a lens that never faulted. An unparseable stamp leaves the zero
			// time, which the heartbeater reads as "never self-healed": a
			// malformed value must not manufacture a recovery that did not
			// happen.
			if st.StructuralAutoRecoveredAt != "" {
				if at, perr := time.Parse(time.RFC3339, st.StructuralAutoRecoveredAt); perr == nil {
					snap.StructuralAutoRecoveredAt = at
				}
			}
			snap.StructuralAutoRecoveredCause = st.StructuralAutoRecoveredCause
			snap.StructuralAutoRecoveryAttempts = st.StructuralAutoRecoveryAttempts
			pending, err := entry.pipeline.Pending(context.Background())
			if err != nil {
				snap.Status = "unknown"
				snap.Unreadable = "consumer pending count: " + err.Error()
				out = append(out, snap)
				continue
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
	// REFRACTOR_MAX_BINDINGS raises or disables (<=0) the per-evaluation
	// binding-set cap without a rebuild, for the case where a legitimate query
	// outgrows the default backstop.
	if v := os.Getenv("REFRACTOR_MAX_BINDINGS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			logger.Error("invalid REFRACTOR_MAX_BINDINGS; keeping the default", "value", v, "err", err)
		} else {
			fullEngine = fullEngine.WithMaxBindings(n)
			logger.Info("binding-set cap overridden", "cap", n)
		}
	}

	// The divergence audit's deployment levers (lens-projection-divergence-
	// audit-design.md §6.3). Read here, once, rather than per lens: two places
	// build pipelines and one of them could be missed — the same reason the
	// derivation knobs below are read here.
	//
	// REFRACTOR_AUDIT_ENABLED is the kill switch, and it routes through
	// enrolment rather than skipping installation, so a disarmed deployment
	// publishes `auditEnrolled: false` with reason "disabled by deployment" on
	// every lens instead of looking like a corpus that audits clean.
	auditOpts := pipeline.AuditOptions{}
	if v := os.Getenv("REFRACTOR_AUDIT_ENABLED"); v != "" {
		enabled, err := strconv.ParseBool(v)
		if err != nil {
			logger.Error("invalid REFRACTOR_AUDIT_ENABLED; keeping the default", "value", v, "err", err)
		} else {
			pipeline.SetAuditEnabled(enabled)
			logger.Info("divergence audit arming overridden", "enabled", enabled)
		}
	}
	// A zero/negative interval or batch is REJECTED rather than accepted,
	// because AuditPlan resolves either back to its default — accepting one
	// would log an override that did not happen, and (for the batch) offer a
	// kill switch that disables nothing. The switch is the flag above.
	if v := os.Getenv("REFRACTOR_AUDIT_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		switch {
		case err != nil:
			logger.Error("invalid REFRACTOR_AUDIT_INTERVAL; keeping the default", "value", v, "err", err)
		case d <= 0:
			logger.Error("invalid REFRACTOR_AUDIT_INTERVAL; must be a positive duration, keeping the default", "value", v)
		default:
			auditOpts.Interval = d
			logger.Info("divergence audit interval overridden", "interval", d)
		}
	}
	if v := os.Getenv("REFRACTOR_AUDIT_BATCH"); v != "" {
		n, err := strconv.Atoi(v)
		switch {
		case err != nil:
			logger.Error("invalid REFRACTOR_AUDIT_BATCH; keeping the default", "value", v, "err", err)
		case n <= 0:
			logger.Error("invalid REFRACTOR_AUDIT_BATCH; must be a positive integer (use REFRACTOR_AUDIT_ENABLED=false to disable), keeping the default", "value", v)
		default:
			auditOpts.Batch = n
			logger.Info("divergence audit batch overridden", "batch", n)
		}
	}

	// REFRACTOR_ANCHOR_DERIVATION selects how the pattern-directed
	// affected-anchor derivation participates in an actor-aware fan-out:
	// `act` (the default) lets it decide which anchors reproject, `shadow`
	// counts it against the enumerator's answer without acting, `off` runs
	// neither. It is set here rather than per-lens because the two places
	// pipelines are built — the static rule loader below and
	// projection.InstallActorAggregate — would otherwise each need it, and one
	// of them could be missed.
	//
	// Turning it down bounds further damage from a derivation shortfall on the
	// next event; it does NOT heal a row already left stale, which is Rebuild's
	// job or the sweep's (auth-plane-projection-latency-design.md §17.5).
	anchorDerivation := pipeline.DefaultAnchorDerivationMode()
	if v := os.Getenv("REFRACTOR_ANCHOR_DERIVATION"); v != "" {
		m, err := pipeline.ParseDerivationMode(v)
		if err != nil {
			logger.Error("invalid REFRACTOR_ANCHOR_DERIVATION; keeping the default", "value", v, "err", err)
		} else {
			pipeline.SetDefaultAnchorDerivationMode(m)
			anchorDerivation = m
		}
	}

	// REFRACTOR_PLAIN_DERIVED_ANCHOR_CAP overrides
	// pipeline.DefaultPlainDerivedAnchorCap (64) — the plain arm's own
	// derivation-mode fallback trigger (plain-lens-neighbour-anchor-
	// derivation-design.md §4.2): a derived set larger than the cap falls
	// back to today's unseeded evaluation rather than paying for that many
	// seeded ones. Read here rather than per-lens for the same reason the
	// mode above is: two places build pipelines and one of them could be
	// missed.
	if v := os.Getenv("REFRACTOR_PLAIN_DERIVED_ANCHOR_CAP"); v != "" {
		n, err := strconv.Atoi(v)
		switch {
		case err != nil:
			logger.Error("invalid REFRACTOR_PLAIN_DERIVED_ANCHOR_CAP; keeping the default", "value", v, "err", err)
		case n <= 0:
			// Rejected rather than silently accepted: SetDefaultPlainDerivedAnchorCap
			// treats n <= 0 as "unset" (restores the built-in default), so logging
			// "cap overridden" for a non-positive value would claim an override that
			// did not happen — mirrors ParseDerivationMode's own convention of
			// rejecting an unusable value rather than silently keeping the default
			// under a misleading log line.
			logger.Error("invalid REFRACTOR_PLAIN_DERIVED_ANCHOR_CAP; must be a positive integer, keeping the default", "value", v)
		default:
			pipeline.SetDefaultPlainDerivedAnchorCap(n)
			logger.Info("plain-lens derived-anchor cap overridden", "cap", n)
		}
	}
	// Logged unconditionally, not only when overridden. Which arm decides a
	// reprojection is the single most load-bearing thing about this process's
	// auth-plane behaviour, and an operator reading a tally line needs to know
	// which mode produced it without inferring it from the line's own name.
	logger.Info("anchor-derivation mode", "mode", anchorDerivation.String())

	// REFRACTOR_ACTOR_PEER_ANCHORS is the way back from §18.1's widening: `on`
	// (the default) lets an event on a vertex of a lens's own actor type reach
	// the anchors whose pattern binds that vertex at a non-anchor position, `off`
	// answers with the changed vertex alone. Set here for the same reason the
	// mode above is — two places build pipelines and one of them could be missed.
	//
	// It is a SEPARATE switch from the mode above, and deliberately so: that
	// one's `off` routes to the ActorEnumerator, which is the arm that walks, so
	// it cannot turn this off. `off` here reinstates a known under-approximation
	// (a grant outliving its source), and like the mode it bounds the next event
	// rather than healing a row already stale — that is Rebuild's job or the
	// sweep's.
	peerAnchors := pipeline.DefaultActorPeerAnchorMode()
	if v := os.Getenv("REFRACTOR_ACTOR_PEER_ANCHORS"); v != "" {
		m, err := pipeline.ParsePeerAnchorMode(v)
		if err != nil {
			logger.Error("invalid REFRACTOR_ACTOR_PEER_ANCHORS; keeping the default", "value", v, "err", err)
		} else {
			pipeline.SetDefaultActorPeerAnchorMode(m)
			peerAnchors = m
		}
	}
	logger.Info("actor peer-anchor mode", "mode", peerAnchors.String())

	// REFRACTOR_WALK_SCOPE is the way back from the pattern-scoped actor walk
	// (refractor-hub-walk-and-periodic-load-design.md §5.1): `on` (the default)
	// lets the walk follow only the relations of pattern hops incident to a
	// position admitting the type it is standing on, `off` puts every lens back
	// on the relation-blind walk. Set here for the same reason the two modes
	// above are — two places build pipelines and one of them could be missed.
	//
	// It is a THIRD switch, separate from both: REFRACTOR_ANCHOR_DERIVATION's
	// `off` routes to the ActorEnumerator, which is the arm the scope narrows,
	// and REFRACTOR_ACTOR_PEER_ANCHORS decides only whether an actor-type event
	// reaches peers at all. Neither reaches this. `off` restores the descriptor-
	// hub expansion the scope exists to end, so it is a containment lever for an
	// operator who believes a lens is missing anchors, not a posture to deploy
	// in — and like the others it bounds the next event rather than healing a
	// row already stale, which is `lattice lens rebuild`'s job or the sweep's.
	walkScope := pipeline.DefaultWalkScopeMode()
	if v := os.Getenv("REFRACTOR_WALK_SCOPE"); v != "" {
		m, err := pipeline.ParseWalkScopeMode(v)
		if err != nil {
			logger.Error("invalid REFRACTOR_WALK_SCOPE; keeping the default", "value", v, "err", err)
		} else {
			pipeline.SetDefaultWalkScopeMode(m)
			walkScope = m
		}
	}
	logger.Info("actor walk-scope mode", "mode", walkScope.String())

	// REFRACTOR_HUB_READ_SCOPE is the way back from the full engine's
	// relation-scoped marked-hub reads
	// (refractor-hub-walk-and-periodic-load-design.md §9.1): `on` (the default)
	// lets a typed relationship hop read an overflow-marked node at the hop's
	// own relation, `off` puts every typed hop back on the whole-node read —
	// the hub's entire Core KV link keyspace drained once per evaluation that
	// crosses it, however few of its relations the pattern follows. Read here
	// for the same reason the modes above are: engines are constructed wherever
	// a pipeline is built and one of those sites could be missed.
	//
	// It is separate from REFRACTOR_WALK_SCOPE, which narrows which relations
	// the actor fan-out's ENUMERATION follows before any evaluation runs; this
	// one narrows what an evaluation's own traversal reads. `off` restores the
	// per-evaluation hub expansion the scope exists to end, so it is a
	// containment lever for an operator who believes a lens is missing edges,
	// not a posture to deploy in — and like the others it bounds the next event
	// rather than healing a row already stale, which is `lattice lens rebuild`'s
	// job or the sweep's.
	hubReadScope := full.DefaultHubReadScopeMode()
	if v := os.Getenv("REFRACTOR_HUB_READ_SCOPE"); v != "" {
		m, err := full.ParseHubReadScopeMode(v)
		if err != nil {
			logger.Error("invalid REFRACTOR_HUB_READ_SCOPE; keeping the default", "value", v, "err", err)
		} else {
			full.SetDefaultHubReadScopeMode(m)
			hubReadScope = m
		}
	}
	logger.Info("hub read-scope mode", "mode", hubReadScope.String())

	// REFRACTOR_ENGINE_PREFETCH is the way back from the full engine's batched
	// node/aspect/adjacency prefetch (prefetchAspects/prefetchNodes/
	// prefetchEdges in internal/refractor/ruleengine/full): `on` (the default)
	// batches the reads a stage's expressions are about to make into few round
	// trips, `off` puts every evaluation back on the point-read path — one Core
	// KV or Adjacency KV round trip per key, exactly as before the batching
	// landed. Read here for the same reason the modes above are: engines are
	// constructed wherever a pipeline is built and one of those sites could be
	// missed.
	enginePrefetch := full.DefaultPrefetchMode()
	if v := os.Getenv("REFRACTOR_ENGINE_PREFETCH"); v != "" {
		m, err := full.ParsePrefetchMode(v)
		if err != nil {
			logger.Error("invalid REFRACTOR_ENGINE_PREFETCH; keeping the default", "value", v, "err", err)
		} else {
			full.SetDefaultPrefetchMode(m)
			enginePrefetch = m
		}
	}
	logger.Info("engine prefetch mode", "mode", enginePrefetch.String())

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

	// syncStreams is this process's record of which SYNC streams its Personal
	// Lenses target. It answers two questions from one piece of state: whether
	// a given stream still needs its one-time consumer-lifetime adoption (the
	// backfill of consumers predating ConsumerLimits.InactiveThreshold, plus
	// the reconciler that then reads those now-expiring durables), and whether
	// more than one stream has been seen at all — which makes every
	// registration in the single, stream-less Interest Set bucket
	// unattributable and stops the reconciler deleting anything.
	//
	// LIFETIME: per process; see SyncStreamWitness's own note. The adoption
	// side is safe to forget at exit because after Inc 1 nothing in the tree
	// can create a zero-threshold consumer on SYNC, so a second pass over the
	// same stream is a provable no-op, a transient failure simply retries at
	// the next boot, and a hot reload that introduces a NEW stream name still
	// gets its own first pass. The population the backfill drains is fixed and
	// shrinking, which is why there is no ticker behind it.
	//
	// It has to exist because the stream name is only knowable inside
	// startPipeline (it is the personal lens rule's own Into.Stream), and
	// startPipeline runs once per personal-lens rule — edge-manifest alone
	// ships ten — and again for every one of them on every hot reload.
	syncStreams := health.NewSyncStreamWitness()

	// The divergence audit's enrolment tally, filled one lens at a time by
	// startPipeline below and reported once after boot settles.
	census := newAuditCensus()

	// The D1 read-grant change edge (personal-lens-grant-change-trigger-
	// design.md §4.2). One per process: the read-grant producers announce a
	// grant landing or being withdrawn, and this re-drives that one actor
	// across every personal lens — the only thing that re-asks the security
	// filter every personal row is gated on.
	//
	// It is offered to every actor-aggregate installation and the installer's
	// own classification decides which lenses it is actually wired onto. The
	// consumer side registers below, beside the control-plane hydrator, which
	// is where the concrete personal pipeline is in hand.
	// Its drain worker starts later, once the registry-completeness gate it
	// waits on exists — see SetRegistryReady below. The object has to exist
	// here because startPipeline both offers it to every actor-aggregate
	// install and registers every personal lens on it.
	grantReprojector := grantchange.New()

	// The SECOND out-of-pattern input's change edge
	// (personal-lens-derivation-licence-design.md §4.2). A personal row is
	// decided against the D1 read gate AND the Interest Set, both read live at
	// evaluation time. The grant edge above covers the first; this covers the
	// second. Without it a device that narrows its interest keeps receiving the
	// excluded keys until the convergence sweep comes round.
	//
	// Both writers of the Interest Set take a bare closure rather than a shared
	// sink type: control imports health, so no interface either package
	// declares is usable by both. The closure enqueues onto the grant edge's own
	// coalescing dirty set, which already owns the bound, the drop accounting
	// and the registry-ready hold — the reconciler's is installed at its
	// construction site below.
	controlSvc.SetInterestChangeSink(grantReprojector.InterestChanged)

	// The standing healer behind that edge (§4.3). The edge is in-process and
	// best-effort by construction — a crash between the producer's write and
	// the drain loses the signal, the coalescing set is bounded, and a lens
	// that registers after a transition never hears about it — so the personal
	// plane gets the convergence sweep every other actor-aggregate plane
	// already has. One sweeper for all fifteen personal lenses, not one each:
	// they share a single identity population, and fifteen tickers would walk
	// it fifteen times. It rides the reprojector's own registry, so there is no
	// second registration site to keep in step. Started beside the drain below.
	// healthKVHandle is threaded so the sweep can count live Refractor instances
	// once per pass. The grant-change edge is an in-process function call
	// (grantchange.GrantChangeEdgeSpansDeployment), so a second instance stops it
	// spanning the deployment while every wiring conjunct a personal lens could
	// test stays true — the personal derivation licence refuses on the count, and
	// a nil handle here would make that count unreadable and refuse everything.
	personalSweeper := grantchange.NewPersonalSweeper(grantReprojector, coreKV, healthKVHandle)

	startPipeline := func(r *lens.Rule) {
		// Computed once, at function scope, so both the taxonomy-refusal
		// recording (below, on a UseFullEngineBranches failure) and the
		// taxonomy-refusal CLEARING (at the registry insert, far below —
		// not right after UseFullEngineBranches succeeds, since several
		// more return paths sit between that and a lens actually being
		// live) read the identical value. Safe to compute unconditionally
		// for every lens, full-engine or not: unionExpansionLabels' own
		// type assertion answers empty for a non-full-engine rule.
		expansionLabels := unionExpansionLabels(r)

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

		if err := validatePerEntryCapReadAdapter(r, adpt); err != nil {
			logger.Error("perEntry cap-read lens activation refused", "lensId", r.ID, "err", err)
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
		// Installed before UseFullEngineBranches below, like SetSecureDecryptor
		// — SetTaxonomyResolver's own doc requires it be set before Run, and
		// useFullEngineBranches reads it unguarded (dynamic-type-taxonomy-
		// design.md §14 Fire A item 4).
		p.SetTaxonomyResolver(taxResolver)

		// The adjacency index's progress cursor, handed to every lens: the
		// executor's edge reads are served from that index, and Reproject's
		// retraction arm refuses to conclude "this anchor is gone" from a view
		// the index has not brought up to the token it writes under. One
		// process-wide bootstrapper, so every pipeline reads the same cursor.
		p.SetAdjacencyAppliedFn(bootstrapper.AppliedSeq)

		// Wire full engine when selected.
		if r.ResolvedEngine == ruleengine.EngineFull {
			if r.CompiledRule == nil {
				logger.Error("full engine selected but CompiledRule is nil", "lensId", r.ID)
				return
			}
			// Thread the output key columns so the engine builds the complete
			// multi-column projection key (a composite-key lens — e.g. a
			// GrantTable lens — needs every key column the adapter requires) —
			// BEFORE UseFullEngineBranches, not after. hotReloadKeyColumns
			// (shared with the MATCH hot-reload path) makes the THREADING
			// decision identically for a plain lens and a Personal lens, so
			// activation and a later cypher edit can never disagree about
			// WHICH columns get threaded; threadsKeyColumns names which
			// lenses are exempt and why. The alias-VALIDATION gate just below
			// is a separate question and is NOT shared the same way: it runs
			// only under threadsKeyColumns(r), so a Personal lens is exempt
			// from it here exactly as reload.go's own `if threadsKeyColumns`
			// arm is (reload.go's ValidateReturnAliases call for a Personal
			// lens's MATCH update sits inside its own `if threaded` block,
			// one level broader — a pre-existing, narrower divergence
			// left untouched here).
			//
			// The ordering matters because KeyColumns is the one CompiledRule
			// field ever mutated after construction, and
			// UseFullEngineBranches' publish is copy-on-write for any
			// `*`-carrying rule (full.WithLabelExpansion returns a shallow
			// COPY, dynamic-type-taxonomy-design.md §4.3): threading after
			// that publish would set KeyColumns on the ORIGINAL
			// r.CompiledRule, not the copy the pipeline actually evaluates —
			// a `*`-carrying Personal lens would then run with KeyColumns
			// permanently nil and the adapter would reject every write.
			// InstallPersonalLens threads again further below (via the same
			// projection.ThreadKeyColumns), which is a harmless no-op once
			// this has already run — it stays independently correct for
			// callers that invoke it without this pre-threading.
			if cr, ok := r.CompiledRule.(*full.CompiledRule); ok {
				if keyCols, thread := hotReloadKeyColumns(r); thread {
					if err := projection.ThreadKeyColumns(cr, r.CompiledBranches, keyCols); err != nil {
						logger.Error("full engine key-column validation", "lensId", r.ID, "err", err)
						return
					}
					if threadsKeyColumns(r) {
						// A Secure Lens's secure + identity-key columns must be
						// RETURN aliases — a typo would otherwise project silent
						// nulls (secure column) or Terminal-DLQ every row
						// (identity-key column) with nothing pointing at the
						// misdeclared spec. A Personal Lens's reserved "__actor"
						// key has no such shape, so it is exempt: threadsKeyColumns
						// is false for it.
						if err := cr.ValidateReturnAliases(secureAliasNames(r.Into.SecureColumns)...); err != nil {
							logger.Error("secure-column RETURN-alias validation", "lensId", r.ID, "err", err)
							return
						}
					}
				}
			}
			if err := p.UseFullEngineBranches(fullEngine, r.CompiledRule, r.CompiledBranches); err != nil {
				logger.Error("full engine activation", "lensId", r.ID, "err", err)
				// A `*` lens refused specifically because its taxonomy
				// expansion is unknown (taxonomy.StatusUnknown — no resolver
				// snapshot yet, an unresolvable label, or a cycle/depth
				// fault) is not permanently broken: it is dark until the
				// taxonomy supplies what it needs, which the taxonomy
				// callback's rl.taxonomyChanged() retries on every live
				// event (§14 Fire A item 4, §17.6's "a boot refusal is
				// never retried" precondition). Recorded unconditionally
				// whenever this lens HAS expansion labels — not only when
				// the error looks taxonomy-shaped — because any other
				// UseFullEngineBranches failure for a `*` lens (a cycle, an
				// unresolvable label) is exactly as retry-worthy once the
				// taxonomy changes; a lens with NO expansion labels never
				// reaches this branch at all, since its UseFullEngineBranches
				// never calls the resolver and so never fails for a
				// taxonomy reason.
				if len(expansionLabels) > 0 {
					rl.recordRefusedForTaxonomy(r)
				}
				return
			}
			// NOT cleared here — clearRefusedForTaxonomy runs only once this
			// lens actually reaches the registry, far below: several more
			// return paths sit between this line and that one
			// (SetDiffRetraction, ValidateUnanchoredForDiffRetraction,
			// ValidateNoFilteringWhereForConvergence, InstallActorAggregate,
			// InstallPersonalLens, NewSecureDecryptor,
			// ParseISO8601Duration, ...), and a lens that fails on one of
			// them belongs in neither the registry nor refused — dark for
			// the process's life, with nothing left to ever retry it.
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

		// Both activation-time retraction guards
		// (secure-plain-lens-retraction-and-audit-design.md §3.3, §4.4): the
		// shared-target scoping a target diff on a shared NATS-KV bucket needs, and
		// the business plane's rule that a lens a neighbour can orphan must carry a
		// transport that retracts it. Both are decided from the registry, which is
		// why the sibling set is read here and the rule is applied there. A refusal
		// is already on the lens's health entry by the time this returns false, and
		// the disposition is the DiffRetraction guard's above: the lens does not
		// activate.
		if !admitRetractionTransport(ctx, logger, reporter, r, p, registeredSiblingsOnTarget(&mu, registry, r.ID, r.Into.Target, r.Into.Bucket)) {
			return
		}

		// Convergence-lens no-filtering-WHERE activation guard
		// (negative-filter-retraction-projection-design.md;
		// docs/components/refractor.md's authoring invariant). A plain
		// (non-actorAggregate) lens projecting into the shared weaver-targets
		// bucket must carry no filtering WHERE — the plain path's presence-check
		// retraction would emit a Delete on a WHERE-dropped anchor, which Weaver
		// reads as "entity gone," not "stopped violating." actorAggregate lenses
		// (e.g. unroutedTasks, orphanedTaskGrants) are exempt from this guard:
		// their retraction never runs through the plain path's presence check,
		// so a filtering WHERE there is always safe, regardless of which of the
		// following actually retracts it — a row-producing lens (an OPTIONAL
		// MATCH secondary pattern, RealnessFilter set) through the envelope's
		// per-row realness-filter callback (projection/driver.go's EnvelopeFn);
		// a lens whose filtering WHERE sits on the anchor match itself, so the
		// cypher returns no row at all once it stops matching, through the
		// zero-row-retraction transport (pipeline.Pipeline.zeroRowRetraction,
		// armed by projection.InstallActorAggregate); a perEntry lens through
		// its own prefix-diff (multiEntryRetractions); or the actor's own
		// vertex tombstone through the anchor-tombstone shortcut. Data-driven,
		// not canonical-name-keyed — a brand-new convergence lens is checked
		// for free. Simple-engine lenses have no CompiledRule of this shape and
		// are silently out of scope (they express matching differently).
		if r.Into.Target == "nats_kv" && r.Into.Bucket == bootstrap.WeaverTargetsBucket && !projection.IsActorAggregate(r) {
			if cr, ok := r.CompiledRule.(*full.CompiledRule); ok {
				if err := cr.ValidateNoFilteringWhereForConvergence(); err != nil {
					logger.Error("convergence-lens filtering-WHERE validation", "lensId", r.ID, "err", err)
					return
				}
			}
		}

		// The producer-closure refusal, applied to EVERY lens rather than only
		// the actor-aggregate arm below (personal-lens-derivation-licence-
		// design.md §4.3b). The D1 read gate finds cap-read.* keys by a wildcard
		// listing, so any lens writing that namespace is read as a grant
		// producer whatever its projectionKind — and a lens that cannot carry
		// the change edge writes grants nothing hears withdrawn. Refusing here
		// closes the set that the reader's wildcard leaves open; the same
		// predicate runs inside InstallActorAggregate, so the two arms cannot
		// disagree about which shapes are sanctioned.
		if refusal := projection.CapReadWriterRefusal(r); refusal != "" {
			logger.Error("read-grant producer closure REFUSED activation: "+refusal,
				"lensId", r.ID, "canonicalName", r.CanonicalName)
			return
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
			if !projection.InstallActorAggregate(p, adpt, r, projectionRevision, adjKV, coreKV, logger,
				projection.WithGrantChangeSink(grantReprojector)) {
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
			// The grant-change edge's consumer registry. Its own list, not the
			// hydrator one above: control.Hydrator is a one-method, unexported
			// registry behind a deliberate boundary that keeps internal/control
			// from importing the pipeline package at all, and there is no
			// iterator over it. This list is Refractor-internal and crosses
			// nothing.
			registerPersonalHealer(grantReprojector, personalSweeper, controlSvc, r.ID, p,
				pipeline.PersonalDerivationWiring{
					// This activation arm IS the personal-lens arm of the
					// install switch, so the class conjunct is asserted from the
					// dispatch rather than re-derived from the envelope.
					PersonalLens: true,
					// The two handles InstallPersonalLens was threaded, read
					// from the same variables that call passes: requireReadGate
					// is true above, so a nil capabilityKV would have refused
					// the registration outright and never reached here — the
					// conjunct is asserted from the handle anyway, because a
					// licence that inferred it from an unrelated refusal would
					// silently become vacuous if that posture ever changed.
					ReadGateWired:           capabilityKV != nil,
					InterestFilterInstalled: personalInterestKV != nil,
					// The two edges Increment 1 built. Each is asserted from
					// the object that would carry it, never from the presence of
					// one standing in for both: "a reprojector exists" and "the
					// Interest Set's writers reach it" are different claims, and
					// only the second is what conjunct 2 rests on.
					GrantReprojectorWired: grantReprojector != nil,
					// All FOUR Interest Set writers, read LIVE. Three live on
					// the control service — register, deregister, and hydrate's
					// registration-creating arm. The fourth is the
					// InterestReconciler's orphan reap, which cmd/refractor
					// constructs a few statements below this one and only for a
					// deployment that has a SYNC stream, so it cannot be sampled
					// here at all: a reconciler built after this line with no
					// sink would leave a value captured now saying "armed" while
					// a fourth writer reaps silently. The process census counts
					// every reconciler from CONSTRUCTION, so this closure sees
					// one the moment it exists; a deployment that builds none
					// owes no fourth announcement and the census is legitimately
					// empty.
					InterestEdgeArmed: func() bool {
						return controlSvc.InterestChangeSinkInstalled() &&
							health.InterestReconcilersWithoutSink() == 0
					},
				})
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

			// The consumer-lifetime adoption for this SYNC stream, once per
			// process (see syncStreams' lifetime note). Observing the stream
			// here is also what lets the reconciler notice a SECOND stream and
			// stop deleting. It sits here,
			// rather than beside the DurableJanitor in the boot sequence,
			// because this is the first and only place the stream's name is
			// known: it is the lens rule's own Into.Stream, and a "SYNC"
			// literal would sweep the wrong stream — or none — in a
			// deployment whose personal lens targets a differently-named one.
			//
			// Ordering: buildRuleAdapter ran at the top of startPipeline, and
			// NewNatsSubjectAdapter's own constructor calls ensureSyncStream
			// before it returns an adapter, so by the time control reaches
			// here the stream carries its ConsumerLimits and the write-backs
			// below cannot exceed a ceiling that does not exist yet.
			if syncStreams.Observe(syncStream) {
				// Off the activation path: the sweep costs two round trips per
				// consumer on the stream, and the population it exists to drain
				// is by definition large (74 orphans were swept by hand once
				// already), so running it inline would hold up every lens
				// behind this one for no correctness gain.
				go func() {
					// The consumers that predate the policy never inherit it —
					// a stream-limits change only rejects consumers that EXCEED
					// the new value, and one sitting at zero exceeds nothing.
					// An orphan by definition never re-attaches, so this is the
					// only thing that reaches the very population the policy
					// exists for. Failure is logged and the process continues:
					// a best-effort sweep is never a reason to fail activation,
					// and the next boot retries.
					backfilled, err := conn.BackfillConsumerInactiveThreshold(ctx, syncStream,
						adapter.SyncConsumerInactiveThreshold, logger)
					if err != nil {
						logger.Warn("backfill SYNC consumer inactive threshold", "stream", syncStream, "err", err)
					} else if len(backfilled) > 0 {
						logger.Info("backfilled SYNC consumers onto the inactive-threshold policy",
							"stream", syncStream, "count", len(backfilled))
					}
					// The registration's own backstop: a device's Interest Set
					// row has no expiry of its own, so it follows the durable
					// that now does. Started after the backfill rather than
					// beside it so the two read the same world, though the
					// reconciler's own boot grace window would cover the gap
					// anyway.
					reconciler := health.NewInterestReconciler(conn, personalInterestKV, syncStream, syncStreams, logger)
					// Reaping an orphaned registration WIDENS what
					// personalinterest.IsRelevant admits for that identity, and
					// a personal lens reads that answer live — so the reap
					// announces on the same Interest Set change edge the
					// control-plane register/deregister ops use.
					reconciler.SetInterestChangeSink(grantReprojector.InterestChanged)
					reconciler.Run(ctx)
				}()
			}

			controlSvc.SetSyncFirstSeq(func(ctx context.Context) (uint64, error) {
				st, err := conn.JetStream().Stream(ctx, syncStream)
				if err != nil {
					return 0, fmt.Errorf("syncgap: look up stream %q: %w", syncStream, err)
				}
				return st.CachedInfo().State.FirstSeq, nil
			})
			// The hydrate delivery-position read (edge-cold-signin-delivery-
			// position-design.md §3.2): the "personal.hydrate" control RPC
			// returns the requesting identity's own last sequence on its
			// personal SYNC subject, at the moment it read it, so a cold or
			// gapped Edge node can start its consumer at that position
			// instead of replaying the stream's full retained history. This
			// is scoped to the caller's own subject, never the stream-wide
			// LastSeq — the SYNC stream carries every identity's subject,
			// and this RPC answers an untrusted, per-identity Edge caller
			// directly, so a stream-wide answer would leak platform-wide
			// sync volume across tenants. subjects.PersonalSync is the same
			// construction the personal lens's own publisher uses, so the
			// two can never drift. A fresh Stream lookup per call, same
			// posture as the syncgap read above — never a long-lived
			// handle's stale cache. An identity with no frames yet on its
			// subject is the normal cold case, not an error: it degrades to
			// 0, which means DeliverAll over an empty subject.
			subjectPrefix := r.Into.SubjectPrefix
			controlSvc.SetSyncLastSeq(func(ctx context.Context, identityID string) (uint64, error) {
				st, err := conn.JetStream().Stream(ctx, syncStream)
				if err != nil {
					return 0, fmt.Errorf("hydrate: look up stream %q: %w", syncStream, err)
				}
				subject := subjects.PersonalSync(subjectPrefix, identityID)
				msg, err := st.GetLastMsgForSubject(ctx, subject)
				if err != nil {
					if errors.Is(err, jetstream.ErrMsgNotFound) {
						return 0, nil
					}
					return 0, fmt.Errorf("hydrate: get last msg for subject %q: %w", subject, err)
				}
				return msg.Sequence, nil
			})
		}

		// A Secure Lens (Contract #3 §3.10): install the decrypt-at-projection
		// transform (the Vault-present check ran before the adapter was built).
		// translateSpec already guaranteed protected-postgres posture, so the
		// RLS verify-and-pause below applies.
		if len(r.Into.SecureColumns) > 0 {
			cols := make([]pipeline.SecureColumn, len(r.Into.SecureColumns))
			for i, sc := range r.Into.SecureColumns {
				cols[i] = pipeline.SecureColumn{Column: sc.Column, HolderTypes: slices.Clone(sc.HolderTypes), Field: sc.Field}
			}
			dec, err := pipeline.NewSecureDecryptor(vaultBackend, coreKV, cols, &vaultCalls)
			if err != nil {
				logger.Error("build secure decryptor", "lensId", r.ID, "err", err)
				return
			}
			p.SetSecureDecryptor(dec)
			logger.Info("secure lens decryptor installed", "lensId", r.ID, "columns", len(cols))
		}

		// The lens's authorization plane, recorded on the pipeline for EVERY
		// lens kind. It sits below the switch above for the same ordering
		// reason the audit does, and it is a no-op re-assertion for an
		// actor-aggregate lens, whose installer already set the identical value
		// (projection.InstallActorAggregate → plan.AuthPlane, itself
		// projection.IsAuthPlane). What it closes is every OTHER kind: a
		// plain-kind lens declaring nats_kv into the capability bucket, or a
		// Postgres grant table, projects an authorization surface with no
		// actor-aggregate installer to say so, and the plain arm's narrowing
		// licence (pipeline.Pipeline's authPlane field doc) has to be able to
		// refuse it.
		installLensPlane(p, r)

		// The plain-lens divergence audit (lens-projection-divergence-audit-
		// design.md §4.3/§4.4). It gives a plain lens its first per-row
		// correctness verdict — a background recompute-and-compare that NEVER
		// writes to the target — and it must sit here, below every stage that
		// installs a conjunct it reads: the envelope/enumerator switch above,
		// SetDiffRetraction, and SetSecureDecryptor immediately above. A
		// conjunct evaluated against a half-built pipeline reads as satisfied
		// for a lens that must never audit, which is the same ordering hazard
		// ConsumerFilter's own placement note spells out below.
		//
		// A refusal is logged at Info, not Warn: refusing is the designed
		// outcome for most of the corpus (an actor-aggregate lens is the
		// sweep's, a Postgres target cannot read a row back), and it is
		// published per lens as auditEnrolled/auditRefusal — the log line is
		// the operator's local copy, not the record.
		// The plane is passed in rather than read back off the pipeline so
		// enrolment never depends on whether an earlier stage happened to
		// record it — projection.IsAuthPlane is the one canonical derivation
		// (nats_kv into the capability bucket, or a Postgres grant table), and
		// both this call and installLensPlane above take it from there.
		if enrolled, refusal := p.InstallAudit(pipeline.AuditOptions{
			AuthPlane: projection.IsAuthPlane(r),
			Interval:  auditOpts.Interval,
			Batch:     auditOpts.Batch,
		}); enrolled {
			census.record(r.ID, true, "")
			logger.Info("divergence audit enrolled", "lensId", r.ID,
				"anchorLabel", p.Auditor().AnchorLabel(), "interval", p.Auditor().Interval())
		} else {
			census.record(r.ID, false, refusal)
			logger.Info("lens gets no divergence audit", "lensId", r.ID, "reason", refusal)
		}

		// D1 (refractor-footprint-reduction-design.md): a full-engine lens
		// with an exhaustive referenced-label set gets a narrowed,
		// server-side FilterSubjects consumer instead of the broad
		// $KV.<bucket>.> filter — ConsumerFilter is the single derivation
		// both this activation call and Pipeline.Rebuild (on a later
		// MATCH hot-reload) share, so eligibility and the label set are
		// never computed two different ways.
		//
		// This call must stay AFTER every stage that installs a conjunct of the
		// actor-aware eligibility predicate — UseFullEngineBranches above, the
		// InstallActorAggregate switch above (enumerator, pattern-closure, sweep
		// plan), and SetSecureDecryptor above — because a consumer filter is
		// fixed at registration and so snapshots a predicate that is otherwise
		// evaluated per event (see ConsumerFilter's doc).
		//
		// Moving it up, or adding a new install stage below it, costs
		// CORRECTNESS — not just narrowing. A pipeline whose enumerator is not
		// installed yet is indistinguishable from a plain one, so it takes the
		// plain eligibility branch, whose conditions UseFullEngineBranches has
		// already met: narrowing is granted with none of §4.2's conjuncts
		// evaluated, relation-narrowed as well. Early is the MOST aggressive
		// filter, and no revert widens a registered one back.
		// filterDecision is the derivation's account of the choice these two
		// values encode (dynamic-type-taxonomy-design.md §10.3's footprint
		// triple). It is carried down to the registry insert rather than
		// reported here: a health write is what CREATES this lens's entry on a
		// first-ever activation, and the return paths between here and there
		// would leave one behind describing a lens that never ran. Purely
		// observational either way — the spec below is built from the two
		// filter values alone, exactly as before.
		filterSubjects, filterSubject, filterDecision := p.ConsumerFilter()
		p.RunOn(conn, lensConsumerSpec(r, coreKVBucket, filterSubjects, filterSubject))

		// Per-lens lag metrics: read pending from the supervised consumer by
		// durable name, so the poller tracks the live consumer across a rebuild
		// reset with no handle re-binding.
		lp := health.NewLagPoller(conn, p.Pending, reporter, r.ID)
		lp.SetProgressFunc(func() time.Time { return p.Progress().LastProjectedAt })
		lp.SetAckStatsFunc(p.AckStats)
		lp.SetPeakRowsFunc(p.PeakBindingRows)
		// The per-entry writes this lens avoided, and the read-backs that
		// failed and so avoided none. Wired for every lens, but the pipeline
		// answers ok == false for one that cannot withhold at all — the poller
		// then leaves both fields absent rather than publishing a zero that
		// would read as a mechanism installed and saving nothing.
		lp.SetWithholdCountsFunc(p.WithholdCounts)
		// The one writer that can report the personal healer's SILENCE. The
		// sweeper publishes its verdict at the end of every pass, so a sweeper
		// that stops passing leaves `clean` standing on every personal lens's
		// entry — the field reading healthy through the exact condition it
		// exists to report. This poller runs on its own clock and escalates the
		// stored token to `stale`, and only ever to `stale`, which is the
		// ownership rule that makes a second writer of one field safe.
		lp.SetPersonalHealerPassFunc(func() (time.Time, time.Duration, int, bool) {
			if personalSweeper == nil {
				return time.Time{}, 0, 0, false
			}
			v := personalSweeper.Verdict()
			return v.CompletedAt, v.Interval, pipeline.PersonalHealerStaleCycles, true
		})
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
		// The backing stream (health.AuditStreamName, shared by every lens) is
		// ensured once at process startup, not here.
		p.SetAuditWriter(health.NewAuditWriter(conn, r.ID))

		lensCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})

		entry := newPipelineEntry(r, adpt, p, reporter, cancel, done, taxResolver)

		mu.Lock()
		registry[r.ID] = entry
		mu.Unlock()
		// The lens is genuinely live only from this point on — not from the
		// earlier UseFullEngineBranches success above, which several more
		// return paths still separate from ever reaching the registry (a
		// lens failing on one of them belongs in neither the registry nor
		// refused, or it is dark for the process's life with nothing to
		// retry it). len(expansionLabels) > 0 gates this the same way
		// recordRefusedForTaxonomy's call does, so a lens that was never
		// queued (no `*`) costs a harmless no-op map delete.
		if len(expansionLabels) > 0 {
			rl.clearRefusedForTaxonomy(r.ID)
		}

		// The consumer footprint ConsumerFilter derived above, published now
		// that the lens is live — the first write to this lens's health entry on
		// a first-ever activation, which is why it waits for the registry rather
		// than sitting beside the derivation. It must also stay ABOVE Run: a
		// narrowed registration Run refuses falls back to the broad filter and
		// overwrites this triple with registration-failed, and a write landing
		// after that would put the refused derivation back. Never fatal — a lens
		// that cannot describe its footprint is still a lens that must run.
		p.RecordFilterDecision(ctx, filterDecision)

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
		// Every lens, regardless of kind, has a pipeline + durable consumer +
		// health entry to tear down — the operator "delete" control RPC
		// (control.Service.deleteRule) dispatches to this. remover.remove
		// (below) drives the SAME pipelineDeleter automatically on a Core KV
		// tombstone, so the two triggers share one removal mechanism.
		controlSvc.RegisterDeleter(r.ID, pipelineDeleter{ruleID: r.ID, entry: entry, clearRefused: rl.clearRefusedForTaxonomy, dropGrantConsumer: grantReprojector.DeregisterPersonal})
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

		// The divergence audit runs beside the sweep on the same lens context,
		// so it stops with the lens. RunAudit returns immediately unless the
		// enrolment above granted this lens a plan.
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.RunAudit(lensCtx)
		}()

		logger.Info("lens pipeline started", "lensId", r.ID, "target", r.Into.Target, "table", r.Into.Table, "bucket", r.Into.Bucket)
	}

	lookupEntry := func(lensID string) (*pipelineEntry, bool) {
		mu.Lock()
		defer mu.Unlock()
		entry, ok := registry[lensID]
		return entry, ok
	}

	// takeEntry looks up and removes a registry entry in the same locked
	// step (single-flight against a redelivered or double tombstone) — the
	// remover's counterpart to lookupEntry's read-only lookup.
	takeEntry := func(lensID string) (*pipelineEntry, bool) {
		mu.Lock()
		defer mu.Unlock()
		entry, ok := registry[lensID]
		if ok {
			delete(registry, lensID)
		}
		return entry, ok
	}

	// activateIfNotRegistered is THE entry point for "load (or reload) this
	// lens ID if it is not already running" — src.SetLoadCallback (a first
	// CDC sighting) and rl.activateForTaxonomy (retryRefused's retry) must
	// go through the identical existence check, not each duplicate it or,
	// worse, one of them call the bare startPipeline directly: two callers
	// racing startPipeline for the same ID with no guard between them could
	// register two pipelines for one lens.
	activateIfNotRegistered := func(r *lens.Rule) {
		mu.Lock()
		_, exists := registry[r.ID]
		mu.Unlock()
		if !exists {
			startPipeline(r)
		}
	}

	// Fill in the remaining fields of the rl predeclared above — every
	// dependency below is now in scope.
	rl.ctx = ctx
	rl.logger = logger
	// The same gate controlSvc runs its "rebuild" op on, so the two paths share
	// one ceiling on concurrent durable delete-recreates. Installed before the
	// first lens activates, which is the only point at which an enqueue could
	// otherwise install a private one of its own.
	rl.taxRebuild.setGate(rebuildGate)
	rl.lookup = lookupEntry
	rl.buildAdapter = buildRuleAdapter
	rl.fullEngine = fullEngine
	rl.resolver = taxResolver
	rl.liveEntries = func() []*pipelineEntry {
		mu.Lock()
		defer mu.Unlock()
		out := make([]*pipelineEntry, 0, len(registry))
		for _, entry := range registry {
			out = append(out, entry)
		}
		return out
	}
	rl.activateForTaxonomy = activateIfNotRegistered

	rm := &remover{
		logger:     logger,
		take:       takeEntry,
		unregister: controlSvc.Unregister,
		// clearRefused evicts a tombstoned lens from rl.refused
		// unconditionally, whether or not it ever reached the pipeline
		// registry — a refused (never-activated) lens's tombstone still
		// reaches remove (CoreKVSource.known is set for every dispatched
		// spec regardless of whether loadCB's activation attempt
		// succeeded), and take's registry lookup alone would miss exactly
		// that lens, leaving it queued for retryRefused to resurrect.
		clearRefused: rl.clearRefusedForTaxonomy,
		// dropGrantConsumer is unconditional for the same reason: a personal
		// lens that registered as a grant-change reprojection consumer must
		// stop being re-driven the moment its definition is gone.
		dropGrantConsumer: grantReprojector.DeregisterPersonal,
	}
	// The removal half of an Output edit's re-activation, driven through the SAME
	// remover a tombstone goes through: one removal mechanism, so a re-activation
	// cannot tear a lens down any less completely than a delete does. It differs
	// from the tombstone callback in one way only — it hands the teardown's
	// success back, because reactivate must not start a replacement over a
	// pipeline that failed to stop. Its counterpart, rl.activateForTaxonomy, is
	// the existence-checked activation entry point set above.
	rl.deactivate = func(old *lens.Rule) error { return rm.stop(old, "reactivation") }

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
	src.SetLoadCallback(activateIfNotRegistered)
	// ruleKnown answers "does the source still have this rule loaded" —
	// retryRefused's belt to remover.remove/pipelineDeleter.Delete's
	// eviction braces: a rule tombstoned while queued in refused is dropped
	// rather than retried. src.Get is set here, after src exists, rather
	// than where rl's other fields are filled in above.
	rl.ruleKnown = func(ruleID string) bool {
		_, ok := src.Get(ruleID)
		return ok
	}
	src.SetUpdateCallback(rl.update)
	src.SetRemoveCallback(rm.remove)
	// The taxonomy trigger, on this SAME meta watch (dynamic-type-taxonomy-
	// design.md §6.1, amendment A1; §14 Fire A item 4). The snapshot and dead
	// callbacks fire on src's single dispatch goroutine — the same one
	// SetLoadCallback/SetUpdateCallback/SetRemoveCallback above already run
	// on — so nothing here needs a lock beyond what taxResolver and rl
	// already take internally, and no goroutine wraps the synchronous half
	// (only rl.taxonomyChanged's per-entry Rebuild call is async, mirroring
	// reloader.update's MatchChange arm).
	//
	// The liveness pair PRESERVES that confinement rather than breaking it,
	// which is why it is a pair. Armed is a resolver flag flip, so it runs
	// wherever the transition was observed — including substrate's
	// connection-state goroutine — and the fail-closed direction never waits
	// on whatever event this dispatch goroutine happens to be inside. Changed
	// is rl.taxonomyChanged, and it runs HERE, because that sweep is exactly
	// the second writer the ordering arguments above rule out: two goroutines
	// re-deriving the live corpus interleave one rederiveEntry's publish with
	// another's baseline commit (published outside entry.taxMu, recorded
	// inside it), and race activateIfNotRegistered's check-then-register into
	// two pipelines for one lens.
	//
	// The GRAPH and the CURRENCY of it are separate signals on purpose
	// (taxonomy.Resolver.SetArmed's doc): a snapshot is installed the moment
	// one is rebuilt, including the partial ones the boot replay emits, while
	// arming waits until the source can show its consumer has drained. Arming
	// here on every snapshot instead — which is what the presence of a
	// snapshot tempts — would report StatusArmed over a taxonomy the replay
	// is still halfway through, and a `*` lens activating in that window
	// narrows on a downward closure whose leaves have not been read yet.
	src.SetTaxonomyCallback(func(snap []taxonomy.TypeSnapshot) {
		taxResolver.InstallSnapshot(snap)
		rl.taxonomyChanged()
	})
	src.SetTaxonomyLivenessCallbacks(lens.TaxonomyLiveness{
		Armed: func(armed bool) { taxResolver.SetArmed(armed) },
		// Both directions re-derive. Arming lets every live `*` lens narrow
		// from the broad filter it took while the taxonomy was unproven;
		// disarming widens it back before the blind window can be served
		// through a narrow client gate that acks-and-drops.
		Changed: rl.taxonomyChanged,
	})
	src.SetTaxonomyDeadCallback(func() {
		taxResolver.SetArmed(false)
		rl.taxonomyChanged()
	})
	controlSvc.SetRuleGetter(src)
	if err := src.Start(ctx); err != nil {
		logger.Error("start core kv lens source", "err", err)
		os.Exit(1)
	}
	logger.Info("core kv lens source started", "watchPrefixes", []string{"vtx.meta.>", "lnk.meta.*.subtypeOf.>"}, "classFilter", "meta.lens")

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

	// The grant-change drain's readiness gate, and only now its worker.
	//
	// A grant-change signal is consumed exactly once: the drain takes an actor
	// off its dirty set and reprojects it across whatever personal lenses are
	// registered at that moment. Lenses register one at a time as their rules
	// activate, while the read-grant producers are already running, so a drain
	// that ran during boot would reproject an actor against a SHORT registry and
	// the frames its unregistered lenses owed would be gone — no log, no Health
	// issue, and no healer until the convergence sweep lands.
	//
	// That is the same hazard the retention-class consumer's readiness gate
	// above answers — over a registry still loading, a short answer is
	// indistinguishable from a complete one, and Core KV, the persistent lens
	// registry, is what makes the two separable. The reprojector holds its
	// signals (bounded and coalescing) until this reports complete, then drains
	// them against a full registry.
	//
	// It is deliberately the CORPUS-GLOBAL ReconcileNow, not the narrowed
	// ReconcileNowForHolderType that gate uses. The narrowing exists so one
	// permanently-unactivatable lens elsewhere cannot withhold every
	// attestation forever, and the probe offers no equivalent narrowing for
	// "the lenses a personal reprojection needs" — so this inherits that
	// failure, and grantchange.RegistryHoldMax is what bounds it. See
	// SetRegistryReady for what that costs on a deployment carrying such a lens.
	grantReprojector.SetRegistryReady(func(ctx context.Context) error {
		missing, err := registryProbe.ReconcileNow(ctx)
		if err != nil {
			return fmt.Errorf("reconcile declared lenses against the registry: %w", err)
		}
		if len(missing) > 0 {
			return fmt.Errorf("%d lens(es) are in Core KV but not yet registered: %v", len(missing), missing)
		}
		return nil
	})
	go grantReprojector.Run(ctx)
	// The sweep waits on no such gate. Its whole job is to converge whatever the
	// fast path missed, and an empty registry costs it one identity listing per
	// cycle and nothing else — while a lens that registers late is precisely one
	// of the cases it exists to cover.
	go personalSweeper.Run(ctx)

	// Taxonomy currency on this instance's own entry. Without it the only
	// trace of a resolver that never arms is a per-lens filterBroadReason,
	// and that reason ranks LAST — so the one shipped `*` lens, which is
	// non-exhaustive for its own reasons, reports that instead and an
	// indefinitely-unarmed platform looks perfectly healthy.
	hb.TaxonomyLivenessProvider = func() health.TaxonomyLivenessSnapshot {
		st := src.TaxonomyLivenessStatus()
		return health.TaxonomyLivenessSnapshot{
			Armed:         st.Armed,
			Dead:          st.Dead,
			UnarmedSince:  st.UnarmedSince,
			ProbeFailures: st.ProbeFailures,
		}
	}

	// The same reconciliation, on demand, is the retention-class destruction
	// consumer's readiness gate. It enumerates live lenses by declared holder
	// type to decide which read models a destroyed key still reaches, and that
	// enumeration is only as trustworthy as the registry underneath it: over a
	// registry still loading, a SHORT target set is indistinguishable from a
	// complete one, and "nothing declares this holder type" — the legitimately
	// clean case that must attest — is indistinguishable from "nothing has
	// registered yet". Core KV is the platform's persistent lens registry, so
	// asking it, rather than the in-process map whose incompleteness is the
	// hazard, is what makes the two separable. Scoped to the lenses that DECLARE
	// the destroyed holder type, because the corpus-global form would let one
	// permanently-unactivatable lens anywhere withhold every attestation forever.
	//
	// Wired HERE rather than at construction on purpose: until this line runs
	// the consumer has no readiness check and treats every event as not-ready,
	// which covers exactly the cold-boot window its DeliverAll durable replays
	// the whole subject history through.
	classKeyShredded.SetRegistryReady(func(ctx context.Context, holderType string) error {
		missing, err := registryProbe.ReconcileNowForHolderType(ctx, holderType)
		if err != nil {
			return fmt.Errorf("reconcile declared lenses against the registry: %w", err)
		}
		if len(missing) > 0 {
			return fmt.Errorf("%d lens(es) declaring holder type %q are in Core KV but not registered: %v",
				len(missing), holderType, missing)
		}
		return nil
	})

	// The inverse reconciliation: a per-lens durable whose lens no longer
	// exists in Core KV. The tombstone-triggered reap (remover, above) needs
	// a live pipeline and a delivered tombstone event, so a lens removed
	// while this process was down — or one whose delete call failed — leaves
	// its durable holding an ack floor forever. See health.DurableJanitor for
	// why deleting on this keep-set is safe.
	go health.NewDurableJanitor(conn, coreKVBucket, registeredLensIDs,
		[]string{lens.BootstrapLensID}, logger).Run(ctx)

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

	// The enrolment census (§4.4): the increment's first act is a REPORTED
	// number of audited lenses rather than a predicted one, so a refusal reason
	// that turns out to dominate is a grounded follow-on instead of a guess made
	// at design time.
	go census.Report(ctx, logger)

	logger.Info("refractor ready", "instance", instance)
	<-ctx.Done()
	logger.Info("refractor shutting down")
	wg.Wait()
	poolManager.Close()
}

// lensConsumerSpec builds the supervised-runtime spec for one lens: durable name
// refractor-<ruleID>, queue group = the same name (NFR12), DeliverLastPerSubject
// (ADR-15), the Core KV stream, and whichever consumer filter the caller's
// derivation produced. ruleID must not be "adjacency" (it would collide with the
// bootstrapper's refractor-adjacency consumer). The supervisor creates the
// durable idempotently when Run registers it.
//
// The two pause fields are derived here, from one predicate, rather than at the
// call site: a lens whose Probe verifies its read-path posture starts
// infra-paused so that verification runs BEFORE its first projection —
// fail-closed, Contract #6 §6.14, verify-and-pause — and the same property is
// what makes its structural pause self-adjudicating. One predicate licenses
// both, so they are the same set by construction and cannot drift apart. Every
// other lens drains immediately and waits for an operator.
//
// It is a named function rather than a literal inside the activation closure so
// the derivation is testable: inlined, flipping StructuralProbe to false shipped
// a dead feature past a fully green suite, because the predicate was tested in
// isolation and the e2e set the flag by hand.
func lensConsumerSpec(r *lens.Rule, coreKVBucket string, filterSubjects []string, filterSubject string) substrate.ConsumerSpec {
	var initialPause substrate.PauseReason
	if verifiesReadPathPosture(r.Into) {
		initialPause = substrate.PauseInfra
	}
	return substrate.ConsumerSpec{
		Name:            subjects.LensDurable(r.ID),
		Stream:          subjects.CoreKVStream(coreKVBucket),
		FilterSubjects:  filterSubjects,
		FilterSubject:   filterSubject,
		DeliverPolicy:   substrate.DeliverLastPerSubject,
		DeliverGroup:    subjects.LensDurable(r.ID),
		AckWait:         lensAckWait,
		InitialPause:    initialPause,
		StructuralProbe: verifiesReadPathPosture(r.Into),
	}
}

// verifiesReadPathPosture reports whether a lens's Probe is a full read-path
// POSTURE VERIFICATION — VerifyProtectedTable / VerifyGrantTable, which check
// the table, its columns, its RLS state and the unique constraint its
// ON CONFLICT needs — rather than a liveness ping.
//
// It is the single condition behind two decisions that must never diverge: the
// fail-closed InitialPause gate (verify before the first projection) and the
// StructuralProbe opt-in (probe your own way out of a structural pause). Both
// are licensed by the same property — a probe that adjudicates the condition —
// so they are one predicate rather than two copies that a future widening could
// desynchronize silently.
//
// The set is narrower than "every lens that has a Probe", and that is not an
// oversight. A plain PostgresAdapter's Probe is pool.Ping and nothing more, so
// it passes while the structural condition still holds — opting it in buys
// resume → re-pause → resume churn, with the entry reading active for a share of
// it, which is strictly worse than an honest structural pause. Completing that
// probe is not cheap either: a plain lens declares no body columns at all
// (Into.Columns is protected-only — the plain base adapter is returned unwrapped
// when !Protected, and only the protected path is handed r.Into.Columns), so a
// verification would be key-columns-only and would still pass through the
// missing-column fault it is supposed to catch. The NATS-KV and NATS-subject
// lenses are left out for a different reason: their structural class (bucket or
// stream absent) is real but has never been observed live, and the machinery
// waits for a consumer rather than shipping ahead of one.
func verifiesReadPathPosture(into lens.IntoConfig) bool {
	return into.Protected || into.GrantTable
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

// installLensPlane records on the pipeline which plane its lens projects onto,
// from projection.IsAuthPlane — the one canonical derivation, covering BOTH of
// its arms (nats_kv into the capability bucket, and a Postgres grant table).
// Every mechanism that must treat an authorization surface differently from a
// business read model reads that one flag, so a lens kind whose installer never
// declared it would be judged as an ordinary business lens by all of them.
//
// It is a function rather than an inline call so the activation path's own
// derivation is reachable from a test: the flag is otherwise only observable
// after a whole refractor has booted.
func installLensPlane(p *pipeline.Pipeline, r *lens.Rule) {
	p.SetAuthPlane(projection.IsAuthPlane(r))
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
// identical (order-sensitive — the spec is authored, not computed; that holds
// for the holder-type list within an entry for the same reason).
func secureColumnsEqual(a, b []lens.SecureColumn) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Column != b[i].Column || a[i].Field != b[i].Field {
			return false
		}
		if !slices.Equal(a[i].HolderTypes, b[i].HolderTypes) {
			return false
		}
	}
	return true
}

// secureAliasNames collects every RETURN alias a secure-column declaration
// consumes. That is the ciphertext column alone: custody comes from the
// ciphertext's own keyId, so no second column is read to decrypt one.
func secureAliasNames(cols []lens.SecureColumn) []string {
	if len(cols) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(cols))
	out := make([]string, 0, len(cols))
	for _, sc := range cols {
		if _, dup := seen[sc.Column]; dup {
			continue
		}
		seen[sc.Column] = struct{}{}
		out = append(out, sc.Column)
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

// newInstanceToken mints this process's Health-KV instance token and refuses to
// return one that would escape the census built on it.
//
// The heartbeat lands at health.refractor.<instance>, and the personal
// derivation licence counts live Refractors with the single-token filter
// health.refractor.* . A token containing a `.` writes a key that filter does
// NOT match, so the instance would be invisible to the census — and the
// direction is the bad one: a second such Refractor UNDER-counts, and every
// personal lens on the first one keeps narrowing on a grant-change edge that no
// longer spans the deployment. A dot is also how the per-lens sub-keys under an
// instance are formed, so a dotted token could collide with one.
//
// hex.EncodeToString cannot produce a dot, so this can only fire if the token's
// construction changes. It panics rather than sanitising because a silently
// rewritten instance id is a second identity for one process, which is worse
// than not starting: this runs at the top of main, before anything is wired.
func newInstanceToken() string {
	token := "rfx-" + randHex(6)
	if strings.ContainsAny(token, ".*>") {
		panic("refractor: instance token " + strconv.Quote(token) +
			" contains a NATS subject metacharacter — its heartbeat key would escape the health.refractor.* instance census, and an uncounted instance is the fail-OPEN direction for the personal derivation licence")
	}
	return token
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
		Verdict:       res.Verdict.String(),
		VerdictReason: res.VerdictReason,
		ProjectionSeq: res.ProjectionSeq,
	}, nil
}
