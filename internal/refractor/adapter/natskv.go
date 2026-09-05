package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/operatinggraph/lattice/internal/refractor/capabilityread"
	"github.com/operatinggraph/lattice/internal/refractor/failure"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// Compile-time check that NatsKVAdapter satisfies Adapter, Truncater and KeyLister.
var _ Adapter = (*NatsKVAdapter)(nil)
var _ Truncater = (*NatsKVAdapter)(nil)
var _ OutcomeTruncater = (*NatsKVAdapter)(nil)
var _ KeyLister = (*NatsKVAdapter)(nil)
var _ PrefixKeyLister = (*NatsKVAdapter)(nil)
var _ RowReader = (*NatsKVAdapter)(nil)
var _ SeqGuarded = (*NatsKVAdapter)(nil)
var _ OutcomeUpserter = (*NatsKVAdapter)(nil)
var _ OutcomeDeleter = (*NatsKVAdapter)(nil)
var _ GrantTransitionDeriver = (*NatsKVAdapter)(nil)

// guardVerdict reports how a guarded write ended when it did not error.
type guardVerdict uint8

const (
	// guardCommitted means the write landed (created or updated).
	guardCommitted guardVerdict = iota
	// guardDeclinedByWatermark means the stored projectionSeq equalled or
	// exceeded this call's token, so the write was dropped as an idempotent
	// no-op (Contract #6 §6.2).
	guardDeclinedByWatermark
	// guardDroppedNoToken means the caller supplied no ordering token, so the
	// write was dropped fail-closed before any stored watermark was consulted.
	guardDroppedNoToken
)

// guardOutcome is how one guardedWrite call ended when it did not error: the
// watermark verdict, and the liveness transition the write made to the key.
//
// The two are separate fields because they answer different questions and one
// is not derivable from the other — the verdict says whether the write landed,
// the transition says whether what the key HOLDS changed kind. Its zero value
// is the correct err-first classification (committed, no transition), so every
// error exit can return it unchanged.
type guardOutcome struct {
	verdict    guardVerdict
	transition GrantTransition
}

// guardCASMaxAttempts caps the conditional-write retry loop a guarded adapter
// runs when a concurrent writer (the retry-queue goroutine) collides on the
// same key. On exhaustion the write returns a plain error, which the pipeline's
// failure.Classify routes as CatTransient (re-enqueue, not a pump pause).
const guardCASMaxAttempts = 8

// projectionSeqField is the top-level body field carrying the monotonic
// ordering token on a guarded write (Contract #6 §6.2).
const projectionSeqField = "projectionSeq"

// kvStore is the subset of *substrate.KV's method set NatsKVAdapter depends
// on. *substrate.KV satisfies it implicitly (no call-site changes needed);
// tests substitute a scripted fake to trigger guardedWrite's
// revision-conflict-retry and CAS-exhaustion branches deterministically,
// which a real NATS-backed store can only reach via an actual race.
type kvStore interface {
	Get(ctx context.Context, key string) (*substrate.KVEntry, error)
	GetMultiNoSnapshot(ctx context.Context, keys []string) (map[string]*substrate.KVEntry, error)
	Create(ctx context.Context, key string, value []byte) (uint64, error)
	Update(ctx context.Context, key string, value []byte, expectedRevision uint64) (uint64, error)
	Put(ctx context.Context, key string, value []byte) (uint64, error)
	Delete(ctx context.Context, key string) error
	ListKeys(ctx context.Context) ([]string, error)
	ListKeysPrefix(ctx context.Context, prefix string) ([]string, error)
	Purge(ctx context.Context, key string) error
	Status(ctx context.Context) error
}

// NatsKVAdapter writes materialized rows to a NATS KV bucket.
type NatsKVAdapter struct {
	kv         kvStore
	keyOrder   []string   // ordered key field names; used for deterministic composite key construction
	deleteMode DeleteMode // hard (default): kv.Delete; soft: tombstone Put
	// guarded selects the monotonic projection-write guard (Contract #6 §6.2).
	// When true, Upsert/Delete write conditionally (CAS) so a lower-seq replay
	// is rejected, a Delete becomes a soft tombstone carrying the watermark, and
	// projectionSeq is stamped into the persisted body. Set per-lens via
	// SetGuarded, from the lens's own compiled projection plan.
	guarded bool
	// keyPrefix is the literal prefix every key this lens writes starts with
	// (OutputDescriptor.KeyPrefix), when the lens has one. It scopes Truncate to
	// the lens's own rows in a bucket several lenses share. Empty means the lens
	// owns the whole bucket, which is the dedicated-target case. Set per-lens via
	// SetKeyPrefix, from the same compiled projection plan SetGuarded reads.
	keyPrefix string
	// keyOwner reports whether a listed key is one this lens's own descriptor
	// built (OutputDescriptor.AnchorFromKey). It is the EXACT ownership test a
	// prefix cannot be, because one lens's prefix contains another's: `cap.`
	// covers `cap.roles.`, `cap.svc.`, `cap.ephemeral.` and
	// `cap.role-by-operation.`, so the kernel capability lens's prefix listing
	// returns four sibling lenses' rows. nil leaves the purge prefix-scoped,
	// which is right for a lens owning its target outright and for a descriptor
	// whose key inverse does not round-trip. Set per-lens via SetKeyOwner, from
	// the same compiled descriptor SetKeyPrefix reads.
	keyOwner func(key string) bool
	// readGrantWriter licenses this adapter to write the D1 read-grant
	// namespace. FALSE BY DEFAULT, which is the whole point: the D1 gate
	// discovers its rows by a wildcard listing over cap-read.*, so any key in
	// that namespace is read as a live grant whatever lens wrote it, and a lens
	// that cannot carry the grant-change edge would be minting grants no plane
	// ever hears withdrawn. Set true only by the installer, and only for a lens
	// projection.IsReadGrantProducer admits.
	//
	// It is enforced HERE rather than at the pipeline's write call sites
	// because this is where the key actually exists, and a policy applied at
	// the six-odd pipeline seams instead would be a coverage claim over a set
	// that grows.
	//
	// The write surface is three paths, and they are NOT all covered the same
	// way. upsert and deleteRow render their key through buildKey and are
	// refused there. truncate never calls buildKey at all — it LISTS keys and
	// Purges them, so a key it removes was rendered by some earlier write —
	// and is covered instead by truncateKeys, which drops every cap-read key
	// from an unlicensed adapter's purge set. Saying "every write goes through
	// buildKey" would be false and would hide exactly that third path.
	readGrantWriter bool
	// unsanctionedKeyReporter is called on EVERY refusal of a read-grant key,
	// so the refusal reaches the lens's own health entry rather than living in
	// a log line. Installed by the pipeline, which owns the health reporter;
	// nil in a harness.
	//
	// Deliberately not deduplicated here. An adapter is rebuilt on every
	// INTO-only hot reload, so a once on this type re-arms whenever a package
	// is reinstalled and "once per lens" would be false in exactly the
	// situation an operator is looking at the entry. The pipeline outlives its
	// adapters and owns the dedup.
	unsanctionedKeyReporter func(ctx context.Context, key string)
}

// ErrUnsanctionedReadGrantKey is returned when a lens that is not an installed
// read-grant producer tries to write a key in the D1 cap-read namespace.
//
// It is TERMINAL, never transient: the lens's own declaration is what makes the
// key unsanctioned, so redelivering the same event renders the same key
// forever, and a category that retries would spin against a permanent
// misconfiguration. The write never lands, which is the direction that matters.
//
// What the caller does with that is the caller's rule, and it is not uniform:
// pipeline.writeResults treats a terminal write error as a per-result failure
// and acks the message — EXCEPT that it checks FailClosed first, so a
// perEntry retraction carrying that flag would Nak for redelivery instead.
// writeResults therefore recognizes this sentinel ahead of its FailClosed arm;
// see the comment there for why a permanent misconfiguration must not be
// redelivered.
var ErrUnsanctionedReadGrantKey = errors.New("adapter: lens is not an installed read-grant producer, so it may not write the " + capabilityread.KeyPrefix + " namespace")

// New creates a NatsKVAdapter that writes to kv.
// keyOrder must match the rule's into.key field list and determines the order
// in which key values are concatenated to form the KV key
// (e.g. ["account_id","agreement_id"] → "acct-001.abc123").
// deleteMode selects hard (kv.Delete) vs soft (tombstone Put) delete projection;
// it is fixed for the life of the adapter.
//
// The adapter is built unguarded; SetGuarded enables the projection-write guard
// for the lenses that require it. That decision is derived from the lens's
// compiled projection plan (projection.RequiresGuard), never from a
// canonical-name list, so this constructor carries no lens knowledge.
func New(kv *substrate.KV, keyOrder []string, deleteMode DeleteMode) (*NatsKVAdapter, error) {
	if len(keyOrder) == 0 {
		return nil, errors.New("natskv: keyOrder must not be empty")
	}
	return &NatsKVAdapter{kv: kv, keyOrder: keyOrder, deleteMode: deleteMode}, nil
}

// SetGuarded enables or disables the monotonic projection-write guard for this
// adapter. It must be called at construction time, before the pipeline starts
// writing — the flag is not safe to flip concurrently with writes.
func (a *NatsKVAdapter) SetGuarded(guarded bool) { a.guarded = guarded }

// SetKeyPrefix scopes this adapter's Truncate to the keys the lens itself
// writes. Like SetGuarded it must be called at construction time, before the
// pipeline starts writing.
//
// prefix is OutputDescriptor.KeyPrefix — the same literal AnchorFromKey matches
// first, already validated as a usable NATS subject filter (non-empty, ending
// on a "." boundary, no wildcard token). Passing "" leaves the adapter
// truncating the whole bucket, which is correct for a lens that owns its target
// outright.
func (a *NatsKVAdapter) SetKeyPrefix(prefix string) { a.keyPrefix = prefix }

// KeyPrefix reports the prefix Truncate is scoped to, or "" when this adapter
// truncates its whole bucket.
func (a *NatsKVAdapter) KeyPrefix() string { return a.keyPrefix }

// SetKeyOwner binds the exact ownership test Truncate applies to the keys its
// prefix listing hands back. Like SetGuarded and SetKeyPrefix it must be called
// at construction time, before the pipeline starts writing.
//
// owns is the lens's own key inverse (projection.OutputDescriptor.AnchorFromKey,
// bound by ApplyTruncateScope). Passing nil leaves the purge scoped by prefix
// alone, which is what a lens owning its target outright needs.
func (a *NatsKVAdapter) SetKeyOwner(owns func(key string) bool) { a.keyOwner = owns }

// OwnsKeysExactly reports whether an exact ownership test is bound, so a caller
// can tell a purge confined to the keys this lens actually wrote from one
// confined only by a prefix a sibling producer may share.
func (a *NatsKVAdapter) OwnsKeysExactly() bool { return a.keyOwner != nil }

// Guarded reports whether the projection-write guard is enabled. The pipeline
// consults it — e.g. Rebuild forces a truncate before rescanning a guarded
// target, since its monotonic watermark would otherwise reject a lower-seq
// historical replay — because a guarded watermark may only be advanced or
// cleared by a stream-sequenced write.
func (a *NatsKVAdapter) Guarded() bool { return a.guarded }

// DerivesGrantTransition reports whether this adapter's outcome-returning
// writes carry a real liveness transition (adapter.GrantTransitionDeriver).
//
// It follows the guard, because the guard is what reads the stored body: only
// guardedWrite holds the stored entry (fetched for the CAS precondition) and
// the outgoing body at the same moment, which is the only place the comparison
// is derivable. The unguarded arms of upsert/deleteRow put or remove the key
// without ever reading its liveness, and report TransitionNone — which means
// "we looked and nothing changed", a claim they have no evidence for.
//
// A caller wanting per-key grant announcements must therefore consult this
// rather than the OutcomeDeleter/OutcomeUpserter interfaces, which say only
// that an outcome is reported at all.
func (a *NatsKVAdapter) DerivesGrantTransition() bool { return a.guarded }

// SetReadGrantWriter licenses this adapter to write the D1 read-grant
// namespace. Like SetGuarded and SetKeyPrefix it must be called at construction
// time, before the pipeline starts writing.
//
// The caller is projection.InstallActorAggregate, from the same compiled
// plan/descriptor data every other installation decision reads — never a
// canonical-name list, and never "this lens has a grant-change sink": a
// producer whose host wired no reprojector still writes real grants that its
// standing healer converges, and refusing its writes would take the read-auth
// plane down over a latency posture.
func (a *NatsKVAdapter) SetReadGrantWriter(licensed bool) { a.readGrantWriter = licensed }

// ReadGrantWriter reports whether this adapter carries the D1 read-grant
// namespace licence. Its reader is the reload path's own pin: an adapter is the
// only honest source for what a REPLACEMENT actually acquired, exactly as
// Guarded is for the §6.2 guard, and a rule-level re-derivation in a test would
// be asserting the predicate against itself rather than against the binding.
func (a *NatsKVAdapter) ReadGrantWriter() bool { return a.readGrantWriter }

// SetUnsanctionedGrantKeyReporter installs the callback this adapter invokes
// the first time it refuses a read-grant key, so the refusal lands on the
// lens's own health entry. Construction-time, like every other Set* here.
func (a *NatsKVAdapter) SetUnsanctionedGrantKeyReporter(fn func(ctx context.Context, key string)) {
	a.unsanctionedKeyReporter = fn
}

// HasUnsanctionedGrantKeyReporter reports whether a refusal on this adapter can
// reach a health entry at all, the same way Pipeline.HasGrantChangeSink reports
// its own wiring: whether a replacement adapter acquired it is a property only
// the adapter can be asked about, and a refusal that lands in a log line with
// no fault behind it is invisible on exactly the lens an operator is looking at.
func (a *NatsKVAdapter) HasUnsanctionedGrantKeyReporter() bool {
	return a.unsanctionedKeyReporter != nil
}

// refuseUnsanctionedGrantKey fails a write whose rendered key claims the D1
// read-grant namespace on a lens that is not an installed read-grant producer.
//
// This is the runtime half of the producer closure. The authoring gate and the
// registration refusal both read a lens's DECLARED output key space, which is
// the §6.13 descriptor — and a plain lens has none: it renders its key from
// RETURN columns, so a cypher returning the literal 'cap-read.billing' into the
// first key column mints a live five-token grant that both declaration-level
// checks are structurally blind to. The key is only knowable here.
//
// Refusing on the RENDERED key rather than on any declaration is what makes
// that hole closed rather than narrowed.
//
// It is BUCKET-BLIND, and that is a deliberate simplification rather than an
// implied claim: only a key in the capability bucket is ever read by
// capabilityread's listing, so an unlicensed lens writing cap-read.* into a
// business bucket harms nobody. Refusing it anyway keeps this guard answering
// the same question the authoring gate answers — "is this lens sanctioned to
// use the namespace" — with one rule instead of two, and the namespace is
// reserved regardless of where a lens tries to spend it.
func (a *NatsKVAdapter) refuseUnsanctionedGrantKey(ctx context.Context, key string) error {
	if a.readGrantWriter || !strings.HasPrefix(key, capabilityread.KeyPrefix) {
		return nil
	}
	if a.unsanctionedKeyReporter != nil {
		a.unsanctionedKeyReporter(ctx, key)
	}
	return failure.Terminal(fmt.Errorf("%w: key %q", ErrUnsanctionedReadGrantKey, key))
}

// buildKey concatenates key field values in keyOrder order, joined with ".".
// Lattice key shape convention (Contract #1) uses "." as the segment
// separator throughout — vtx.<type>.<id>.<aspect>, lnk.<…>, cap.identity.<id>.
// Returns an error if any key field is absent from keys.
func (a *NatsKVAdapter) buildKey(keys map[string]any) (string, error) {
	parts := make([]string, len(a.keyOrder))
	for i, field := range a.keyOrder {
		val, ok := keys[field]
		if !ok {
			return "", fmt.Errorf("natskv: key field %q absent from keys map", field)
		}
		parts[i] = fmt.Sprintf("%v", val)
	}
	return strings.Join(parts, "."), nil
}

// Upsert serializes row to JSON and writes it to the KV bucket under the
// constructed key. An unguarded adapter writes conditionally on row content
// (see upsert). A guarded adapter writes conditionally on the projectionSeq
// watermark instead: it drops the write as an idempotent no-op when a write
// with an equal-or-higher projectionSeq already landed, and otherwise stamps
// projectionSeq into the persisted body and commits via a CAS loop so a
// lower-seq replay can never overwrite a newer projection (Contract #6 §6.2).
func (a *NatsKVAdapter) Upsert(ctx context.Context, keys map[string]any, row map[string]any, projectionSeq uint64) error {
	_, err := a.upsert(ctx, keys, row, projectionSeq)
	return err
}

// UpsertWithOutcome is Upsert plus a report of what the call did to the target
// (adapter.OutcomeUpserter). The pipeline's write-audit step (writeResults)
// type-asserts for this and audits only UpsertOutcome.Committed, which excludes
// all three of this adapter's ways of storing nothing: an unguarded row skipped
// as byte-identical, a guarded write the watermark declined, and a guarded write
// dropped for want of an ordering token. None of them is a new audit fact.
func (a *NatsKVAdapter) UpsertWithOutcome(ctx context.Context, keys map[string]any, row map[string]any, projectionSeq uint64) (UpsertOutcome, error) {
	return a.upsert(ctx, keys, row, projectionSeq)
}

// upsert is the shared implementation behind Upsert and UpsertWithOutcome.
func (a *NatsKVAdapter) upsert(ctx context.Context, keys map[string]any, row map[string]any, projectionSeq uint64) (UpsertOutcome, error) {
	key, err := a.buildKey(keys)
	if err != nil {
		return UpsertOutcome{}, fmt.Errorf("natskv upsert: %w", err)
	}
	if err := a.refuseUnsanctionedGrantKey(ctx, key); err != nil {
		return UpsertOutcome{}, err
	}
	if a.guarded {
		// Always reports Wrote:true, even through guardedWrite's own internal
		// no-op branches (a stale-or-equal stored seq, or the sequence-less
		// fail-closed drop): advancing — or deliberately holding — the
		// projectionSeq watermark is this path's job on every call regardless
		// of row content, so it must never gain a row-content skip the way the
		// unguarded path below has. An identical-row skip that also skipped
		// the watermark write would leave the stored seq behind, and a later
		// stale replay of a DIFFERENT row at a lower (but still-unseen-by-this-
		// key) seq could then pass the monotonic guard that watermark exists
		// to block.
		out, err := a.guardedWrite(ctx, key, row, projectionSeq, false)
		if err != nil {
			return UpsertOutcome{}, err
		}
		// Committed is read off the verdict positively, so both of this path's
		// ways of storing nothing — a stale-or-equal watermark, and the
		// sequence-less fail-closed drop — are excluded by construction rather
		// than by a caller subtracting each of them from Wrote and having to
		// know how many there are.
		return UpsertOutcome{
			Wrote:               true,
			Committed:           out.verdict == guardCommitted,
			DeclinedByWatermark: out.verdict == guardDeclinedByWatermark,
			Key:                 key,
			Transition:          out.transition,
		}, nil
	}
	data, err := json.Marshal(row)
	if err != nil {
		return UpsertOutcome{}, fmt.Errorf("natskv upsert: marshal row: %w", err)
	}
	// Read-before-write: one extra KV read on every unguarded upsert, spent to
	// possibly avoid a Put — which also skips that Put's CDC fan-out (the
	// target bucket's watchers, e.g. Weaver, re-notifying) and, via
	// UpsertOutcome, the pipeline's audit publish. This pays off exactly when
	// an unanchored lens rewrites its full row set on a trigger that left this
	// particular row's content unchanged. json.Marshal sorts map keys
	// (guaranteed since Go 1.12), so two calls carrying logically-equal rows
	// always marshal to the same bytes. A failed or absent read
	// (ErrKeyNotFound, or any other error) can't prove identity either way, so
	// it falls through to the unconditional Put — the same behavior this
	// method had before it compared anything.
	if current, getErr := a.kv.Get(ctx, key); getErr == nil && bytes.Equal(current.Value, data) {
		return UpsertOutcome{Wrote: false, Committed: false, Key: key}, nil
	}
	if _, err := a.kv.Put(ctx, key, data); err != nil {
		return UpsertOutcome{}, fmt.Errorf("natskv upsert: put %s: %w", key, err)
	}
	return UpsertOutcome{Wrote: true, Committed: true, Key: key}, nil
}

// Delete projects a Core KV deletion into the target KV bucket. The behavior is
// fixed at construction time by the adapter's deleteMode:
//
//   - DeleteModeHard (default): physically removes the key via kv.Delete. Lineage
//     already lives in Core KV, so the derived view reflects deletions as
//     removals. Deleting a never-existed key is idempotent — the absent-key
//     ErrKeyNotFound is swallowed and nil returned.
//   - DeleteModeSoft: writes a tombstone document {isDeleted:true, projectedAt:…}
//     for audit/forensic targets that opt in. Overwriting a never-existed key is
//     naturally idempotent (Put creates).
//
// Both absence (hard) and tombstone (soft) are treated as denial by the
// capability authorizer (step3_auth_capability): an absent key resolves to
// NoCapabilityEntry and an isDeleted doc to a denied entry. The freshness-ceiling
// comparison that originally motivated soft-delete on the capability plane was
// removed in Story 1.5.4, so absence and tombstone are now equivalent for auth.
func (a *NatsKVAdapter) Delete(ctx context.Context, keys map[string]any, projectionSeq uint64) error {
	_, err := a.deleteRow(ctx, keys, projectionSeq)
	return err
}

// DeleteWithOutcome is Delete plus a report of whether the retraction actually
// landed (adapter.OutcomeDeleter). A reconciler uses it to tell a committed
// revocation apart from one the watermark guard declined — the direction where
// a silent drop leaves the pre-edit permission set live.
func (a *NatsKVAdapter) DeleteWithOutcome(ctx context.Context, keys map[string]any, projectionSeq uint64) (DeleteOutcome, error) {
	return a.deleteRow(ctx, keys, projectionSeq)
}

// deleteRow is the shared implementation behind Delete and DeleteWithOutcome.
func (a *NatsKVAdapter) deleteRow(ctx context.Context, keys map[string]any, projectionSeq uint64) (DeleteOutcome, error) {
	key, err := a.buildKey(keys)
	if err != nil {
		return DeleteOutcome{}, fmt.Errorf("natskv delete: %w", err)
	}
	if err := a.refuseUnsanctionedGrantKey(ctx, key); err != nil {
		return DeleteOutcome{}, err
	}
	if a.guarded {
		// A guarded delete is always a soft tombstone carrying the watermark,
		// regardless of the lens's deleteMode: the high-water mark must survive
		// physical absence so a lower-seq replay still loses. Absence and an
		// isDeleted tombstone are equivalent for authorization (Contract #6 §6.8).
		out, gerr := a.guardedWrite(ctx, key, nil, projectionSeq, true)
		if gerr != nil {
			return DeleteOutcome{}, gerr
		}
		// Wrote is claimed only for a retraction that COMMITTED. A
		// sequence-less drop retracted nothing either, so it must not be
		// reported as a write just because it was not a watermark conflict.
		return DeleteOutcome{
			Wrote:               out.verdict == guardCommitted,
			DeclinedByWatermark: out.verdict == guardDeclinedByWatermark,
			Key:                 key,
			Transition:          out.transition,
		}, nil
	}
	if a.deleteMode == DeleteModeSoft {
		tombstone := map[string]any{
			"isDeleted":   true,
			"projectedAt": time.Now().UTC().Format(time.RFC3339),
		}
		data, err := json.Marshal(tombstone)
		if err != nil {
			return DeleteOutcome{}, fmt.Errorf("natskv delete: marshal tombstone: %w", err)
		}
		if _, err := a.kv.Put(ctx, key, data); err != nil {
			return DeleteOutcome{}, fmt.Errorf("natskv delete: put tombstone %s: %w", key, err)
		}
		return DeleteOutcome{Wrote: true, Key: key}, nil
	}
	// Hard delete: physically remove the key. Deleting an absent key is a no-op.
	if err := a.kv.Delete(ctx, key); err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			// Deleting an absent key is idempotent and not an error, but it
			// retracted nothing — reporting a write here would let a caller
			// book a repair for a row that was never there.
			return DeleteOutcome{Key: key}, nil
		}
		return DeleteOutcome{}, fmt.Errorf("natskv delete: delete %s: %w", key, err)
	}
	return DeleteOutcome{Wrote: true, Key: key}, nil
}

// guardedWrite performs a monotonic, conditional write under the projection
// guard (Contract #6 §6.2). delete selects a soft tombstone body
// {isDeleted:true, projectionSeq} over a live upsert body (row + injected
// projectionSeq). It reads the current entry, drops the write as an idempotent
// no-op when the stored projectionSeq is greater than or equal to the incoming
// one, and otherwise commits with the entry's revision as the CAS precondition.
// A revision conflict (a concurrent writer landed first) triggers a re-read and
// re-compare, bounded by guardCASMaxAttempts; on exhaustion it returns a plain
// error (routed transient) after a warn naming the key.
//
// A guarded write always carries a real JetStream stream sequence (≥ 1); a
// caller supplying incomingSeq == 0 has no real ordering token behind the
// write. Such a write carries no ordering and is dropped as a fail-closed no-op
// so it can neither create a clobberable seq-0 key nor no-op a real update.
//
// The returned guardVerdict distinguishes the three ways this call can end
// without an error, because a caller holding evidence that the row diverges
// needs to tell a repair that COMMITTED apart from one that provably did not
// land — and, among the latter, a watermark conflict apart from a missing
// ordering token. Folding the two drops together would report an absent token
// as a conflict with a stored one, which is a different fault with a different
// fix.
//
// The returned GrantTransition is the second, orthogonal half: whether the key
// changed LIVENESS, i.e. came to hold a live row or stopped holding one. This
// is the only place in the platform where both bodies are in hand at once — the
// stored entry is already read for the CAS precondition, and the outgoing body
// is built here — so it is the only place the distinction is derivable. A
// watcher on the target bucket cannot make it: the guarded path deliberately
// rewrites an unchanged body on every evaluation to advance the watermark, so
// every write looks alike from the outside.
func (a *NatsKVAdapter) guardedWrite(ctx context.Context, key string, row map[string]any, incomingSeq uint64, delete bool) (guardOutcome, error) {
	if incomingSeq == 0 {
		slog.Warn("natskv guarded write: dropping sequence-less write (no ordering token)",
			"key", key, "delete", delete)
		// Unknown, not None: this returns before any Get, so no stored body was
		// ever read and no comparison was ever made. Reporting "no transition"
		// would be a claim this branch has no evidence for.
		return guardOutcome{verdict: guardDroppedNoToken, transition: TransitionUnknown}, nil
	}
	body := a.guardedBody(row, incomingSeq, delete)
	data, err := json.Marshal(body)
	if err != nil {
		return guardOutcome{}, fmt.Errorf("natskv guarded write: marshal %s: %w", key, err)
	}

	for attempt := 0; attempt < guardCASMaxAttempts; attempt++ {
		entry, getErr := a.kv.Get(ctx, key)
		if getErr != nil {
			if !errors.Is(getErr, substrate.ErrKeyNotFound) {
				return guardOutcome{}, fmt.Errorf("natskv guarded write: get %s: %w", key, getErr)
			}
			// Key absent: create it. A concurrent create wins the revision and
			// we re-read on the next iteration.
			if _, createErr := a.kv.Create(ctx, key, data); createErr != nil {
				if errors.Is(createErr, substrate.ErrRevisionConflict) {
					continue
				}
				return guardOutcome{}, fmt.Errorf("natskv guarded write: create %s: %w", key, createErr)
			}
			return guardOutcome{transition: transitionFrom(nil, false, data)}, nil
		}

		if storedSeq, ok := storedProjectionSeq(entry.Value); ok && storedSeq >= incomingSeq {
			// A write with an equal-or-higher watermark already landed; this is
			// an idempotent no-op (a stale lower-seq replay loses). No write
			// happened, so nothing transitioned.
			return guardOutcome{verdict: guardDeclinedByWatermark}, nil
		}

		if _, updErr := a.kv.Update(ctx, key, data, entry.Revision); updErr != nil {
			if errors.Is(updErr, substrate.ErrRevisionConflict) {
				continue
			}
			return guardOutcome{}, fmt.Errorf("natskv guarded write: update %s: %w", key, updErr)
		}
		return guardOutcome{transition: transitionFrom(entry.Value, true, data)}, nil
	}

	slog.Warn("natskv guarded write: CAS loop exhausted under contention",
		"key", key, "attempts", guardCASMaxAttempts, "projectionSeq", incomingSeq)
	return guardOutcome{}, fmt.Errorf("natskv guarded write: %s: revision conflict not resolved after %d attempts", key, guardCASMaxAttempts)
}

// transitionFrom classifies the liveness change between what a key held and
// what a guarded write just put there. storedFound distinguishes an absent key
// from one holding an empty body — they classify differently in the one
// direction that matters, since a tombstone created over an absent key revoked
// nothing (nothing was ever granted) while a tombstone written over an empty
// body cannot be proven not to have.
//
// A stored body that does not parse is treated as a transition in whichever
// direction the incoming body points, rather than as "no change". The choice is
// deliberate and one-sided: an extra signal costs one re-evaluation, a missed
// one leaves a revoked grant honoured until the standing healer next runs, and
// this classifier is read by a security filter's change edge.
func transitionFrom(stored []byte, storedFound bool, outgoing []byte) GrantTransition {
	incomingLive, _ := bodyIsLive(outgoing)
	if !storedFound {
		if incomingLive {
			return TransitionGranted
		}
		// A tombstone body created over an absent key: reachable, because
		// deleteRow routes every retraction through guardedWrite regardless of
		// whether the key exists. Nothing was granted, so nothing was revoked.
		return TransitionNone
	}
	storedLive, known := bodyIsLive(stored)
	if !known {
		if incomingLive {
			return TransitionGranted
		}
		return TransitionRevoked
	}
	switch {
	case storedLive == incomingLive:
		return TransitionNone
	case incomingLive:
		return TransitionGranted
	default:
		return TransitionRevoked
	}
}

// bodyIsLive reports whether a persisted or outgoing guarded body represents a
// LIVE row rather than a soft tombstone, and whether that could be determined
// at all. A body carrying no isDeleted field is live — that is the ordinary
// projected row, and the perEntry envelope refuses an entry field named
// isDeleted precisely so a row can never impersonate a tombstone
// (projection/driver.go's EntryEnvelopeFn reserved-field check). An empty or
// unparseable body is unknown, never silently "live".
func bodyIsLive(data []byte) (live, known bool) {
	if len(data) == 0 {
		return false, false
	}
	var doc struct {
		IsDeleted bool `json:"isDeleted"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return false, false
	}
	return !doc.IsDeleted, true
}

// guardedBody builds the persisted document for a guarded write: a soft
// tombstone for a delete, or the projection row with projectionSeq injected as a
// top-level field for an upsert.
func (a *NatsKVAdapter) guardedBody(row map[string]any, incomingSeq uint64, delete bool) map[string]any {
	if delete {
		return map[string]any{
			"isDeleted":        true,
			"projectedAt":      time.Now().UTC().Format(time.RFC3339),
			projectionSeqField: incomingSeq,
		}
	}
	body := make(map[string]any, len(row)+1)
	for k, v := range row {
		body[k] = v
	}
	body[projectionSeqField] = incomingSeq
	return body
}

// storedProjectionSeq extracts the projectionSeq watermark from a persisted
// guarded body. Returns (0, false) when the body is empty, unparseable, or
// carries no projectionSeq (legacy doc written before the guard) — the caller
// then treats it as the lowest possible watermark and proceeds to write.
func storedProjectionSeq(data []byte) (uint64, bool) {
	if len(data) == 0 {
		return 0, false
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return 0, false
	}
	raw, ok := doc[projectionSeqField]
	if !ok {
		return 0, false
	}
	// json.Unmarshal into map[string]any always decodes a JSON number as
	// float64 (this function never uses a json.Decoder.UseNumber() reader,
	// so json.Number is unreachable here). A negative value is a malformed
	// watermark, not a real low seq — treated as absent rather than
	// converted, which would wrap to a bogus near-max uint64 and poison
	// every future guarded write to the key (permanent false "already
	// newer" no-op).
	v, ok := raw.(float64)
	if !ok || v < 0 {
		return 0, false
	}
	return uint64(v), true
}

// ListKeys returns every live key in the bucket, split back into its
// keyOrder field-name map (the inverse of buildKey). A single-field keyOrder
// (the common `IntoKey: ["key"]` capability-envelope shape) maps the whole
// key string verbatim — a Lattice key is itself "."-segmented
// (cap.identity.<id>), so splitting would misparse it. A composite keyOrder
// (2+ fields, e.g. app_id+landlord_id) splits on "." and requires the
// segment count to match exactly — safe because the platform's NanoID
// alphabet carries no dots, so no individual composite field value can
// introduce a spurious segment; a key that doesn't match is skipped (it
// belongs to a different lens sharing the bucket, or predates a keyOrder
// change) rather than surfacing a malformed partial map.
// A soft-delete-mode bucket's tombstone documents remain live NATS-KV keys
// (unlike a hard delete) and so are still listed here — acceptable because
// no live DiffRetraction lens targets a soft-delete NATS-KV bucket today.
func (a *NatsKVAdapter) ListKeys(ctx context.Context) ([]map[string]any, error) {
	keys, err := a.kv.ListKeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("natskv list keys: %w", err)
	}
	return a.mapKeys(keys), nil
}

// ListKeysPrefix returns the live keys under one prefix, in the same shape
// ListKeys returns them. The prefix becomes a JetStream subject filter, so the
// bucket is never streamed in full — which is what makes a per-lens listing
// affordable on a bucket several lenses share.
//
// An empty prefix is refused rather than silently answered with the whole
// bucket: this method's whole contract is that the caller receives a scoped
// listing, and a caller that asked to scope and was quietly given everything is
// the failure mode worth being loud about.
func (a *NatsKVAdapter) ListKeysPrefix(ctx context.Context, prefix string) ([]map[string]any, error) {
	if prefix == "" {
		return nil, fmt.Errorf("natskv list keys by prefix: prefix must not be empty")
	}
	keys, err := a.kv.ListKeysPrefix(ctx, prefix)
	if err != nil {
		return nil, fmt.Errorf("natskv list keys by prefix %q: %w", prefix, err)
	}
	return a.mapKeys(keys), nil
}

// mapKeys renders raw target keys as the keyOrder field-name maps ListKeys and
// ListKeysPrefix both return.
func (a *NatsKVAdapter) mapKeys(keys []string) []map[string]any {
	out := make([]map[string]any, 0, len(keys))
	if len(a.keyOrder) == 1 {
		field := a.keyOrder[0]
		for _, k := range keys {
			out = append(out, map[string]any{field: k})
		}
		return out
	}
	for _, k := range keys {
		parts := strings.Split(k, ".")
		if len(parts) != len(a.keyOrder) {
			slog.Warn("natskv list keys: skipping key with unexpected segment count",
				"key", k, "wantSegments", len(a.keyOrder), "gotSegments", len(parts))
			continue
		}
		m := make(map[string]any, len(parts))
		for i, field := range a.keyOrder {
			m[field] = parts[i]
		}
		out = append(out, m)
	}
	return out
}

// GetRow reads back the row previously written at keys, stripped of the
// guard's internal projectionSeq bookkeeping field (callers merge this into a
// freshly computed partial row — projectionSeq is re-added by the next
// Upsert's own guard, never carried by the caller). Returns (nil, false, nil)
// when the key does not exist or holds a soft-delete tombstone (isDeleted) —
// both read as "no row to carry forward from," the same posture Upsert's own
// absent-key branch takes.
func (a *NatsKVAdapter) GetRow(ctx context.Context, keys map[string]any) (map[string]any, bool, error) {
	key, err := a.buildKey(keys)
	if err != nil {
		return nil, false, fmt.Errorf("natskv get row: %w", err)
	}
	entry, err := a.kv.Get(ctx, key)
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("natskv get row: get %s: %w", key, err)
	}
	if len(entry.Value) == 0 {
		return nil, false, nil
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		return nil, false, fmt.Errorf("natskv get row: unmarshal %s: %w", key, err)
	}
	if isDeleted, _ := doc["isDeleted"].(bool); isDeleted {
		return nil, false, nil
	}
	delete(doc, projectionSeqField)
	return doc, true, nil
}

// GetRows reads back many stored rows at once, keyed by the exact key each was
// requested under, and shapes every member exactly as GetRow shapes its one:
// a soft-delete tombstone (isDeleted) is reported as absent, so is an empty or
// unparseable value, and the guard's internal projectionSeq bookkeeping field
// is stripped from every body returned. A key with no live, parseable row is
// therefore MISSING from the result rather than present-and-empty, which is
// what lets a caller read the map as "the bodies the target holds".
//
// A per-member problem never fails the batch (adapter.RowsReader's contract):
// an unparseable member is dropped like an absent one, because the caller's
// answer for both is the same — there is no stored body to compare against, so
// the row is written. An error return means the request set did not happen at
// all.
//
// BOUND. The read is substrate.KVGetMultiNoSnapshot over exact keys: ceil(N/1024)
// fast-path requests, each bounded by the client's directGetMultiDefaultTimeout,
// with no drain and so no round-starved ceiling. It runs on the caller's ctx —
// the pipeline's consumer context, which carries no deadline — so that
// per-request timeout is the whole bound, and N (the caller's own key set) is
// what sizes the request count.
//
// No snapshot is needed and none is taken: each key is compared independently
// against its own freshly computed body, so a result that blends instants
// answers every comparison correctly (KVGetMultiNoSnapshot's own
// independent-facts test).
func (a *NatsKVAdapter) GetRows(ctx context.Context, keys []string) (map[string]map[string]any, error) {
	if len(keys) == 0 {
		return map[string]map[string]any{}, nil
	}
	entries, err := a.kv.GetMultiNoSnapshot(ctx, keys)
	if err != nil {
		return nil, fmt.Errorf("natskv get rows: %d keys: %w", len(keys), err)
	}
	out := make(map[string]map[string]any, len(entries))
	for key, entry := range entries {
		if entry == nil || len(entry.Value) == 0 {
			continue
		}
		var doc map[string]any
		if uerr := json.Unmarshal(entry.Value, &doc); uerr != nil {
			continue
		}
		if isDeleted, _ := doc["isDeleted"].(bool); isDeleted {
			continue
		}
		delete(doc, projectionSeqField)
		out[key] = doc
	}
	return out, nil
}

// Truncate clears the lens's rows by purging them, so a rebuild's stream replay
// starts from an empty high-water state and the highest-seq write wins
// (Contract #6 §6.2). Purge removes each key's prior revisions and leaves a
// delete marker as the latest revision, so a subsequent Get returns
// ErrKeyNotFound: a guarded rebuild then takes the absent→Create path on the
// first replay and never reads a stale projectionSeq watermark, eliminating the
// rejected-write holes a lower-seq replay against a live watermark would leave.
//
// What "the lens's rows" means depends on whether the target is shared. With a
// key prefix set (SetKeyPrefix), only keys under it are purged: several lenses
// project into one bucket — the auth plane's `capability` is the live case — and
// an unscoped purge there is a platform-wide authorization wipe, every sibling
// lens's keys gone and healed only at sweep pace. The Postgres side has always
// worked this way for the same reason, from the other direction:
// GrantWriterAdapter implements no Truncater at all because actor_read_grants is
// shared (read_path_adapters.go). Without a prefix the whole bucket is purged,
// which is what a lens owning its target outright needs.
func (a *NatsKVAdapter) Truncate(ctx context.Context) error {
	_, err := a.truncate(ctx)
	return err
}

// TruncateWithKeys is Truncate plus the list of keys it removed
// (adapter.OutcomeTruncater). A purge writes through none of the per-key guard,
// so this list is the only account a caller reacting to retractions ever gets
// of what a truncating rebuild withdrew.
//
// The keys are returned even when the purge fails partway: everything before
// the failure is already gone, and a caller that reacts to retractions must
// hear about those rather than have them swallowed by the error.
func (a *NatsKVAdapter) TruncateWithKeys(ctx context.Context) ([]string, error) {
	return a.truncate(ctx)
}

// truncate is the shared implementation behind Truncate and TruncateWithKeys.
func (a *NatsKVAdapter) truncate(ctx context.Context) ([]string, error) {
	keys, err := a.truncateKeys(ctx)
	if err != nil {
		return nil, err
	}
	for i, key := range keys {
		// Purge is idempotent: a key deleted out from under us between the list
		// and the purge is not an error.
		if err := a.kv.Purge(ctx, key); err != nil {
			return keys[:i], fmt.Errorf("natskv truncate: purge %s: %w", key, err)
		}
	}
	return keys, nil
}

// truncateKeys returns the keys Truncate is allowed to purge: the lens's own
// rows when it declares a key prefix, the whole bucket when it does not, minus
// any D1 read-grant key an unlicensed lens has no business removing.
func (a *NatsKVAdapter) truncateKeys(ctx context.Context) ([]string, error) {
	var keys []string
	var err error
	if a.keyPrefix == "" {
		if keys, err = a.kv.ListKeys(ctx); err != nil {
			return nil, fmt.Errorf("natskv truncate: list keys: %w", err)
		}
	} else if keys, err = a.kv.ListKeysPrefix(ctx, a.keyPrefix); err != nil {
		return nil, fmt.Errorf("natskv truncate: list keys under %q: %w", a.keyPrefix, err)
	}
	return a.withoutUnsanctionedGrantKeys(ctx, a.ownedOnly(keys)), nil
}

// ownedOnly drops every listed key this lens's own descriptor did not build.
//
// The prefix Truncate lists under scopes the listing; it does not decide
// ownership, because one lens's prefix contains another's. The kernel
// `capability` lens writes `cap.{actorSuffix}`, and its prefix `cap.` also
// covers `cap.ephemeral.`, `cap.svc.`, `cap.roles.` and
// `cap.role-by-operation.` — four sibling producers of the same bucket. Purging
// those is not a rebuild of this lens: it is an authorization wipe healed only
// at sweep pace, of rows the replay that follows re-derives none of. And it runs
// unattended: a package upgrade's Output edit re-activates the lens, which forces
// this purge on a guarded target with no operator in the loop.
//
// No owner bound leaves the purge prefix-scoped — the lens owns its target
// outright, or its key inverse does not round-trip and filtering on it would
// skip the lens's OWN rows instead. One line per truncate, like the namespace
// refusal below.
func (a *NatsKVAdapter) ownedOnly(keys []string) []string {
	if a.keyOwner == nil {
		return keys
	}
	kept := make([]string, 0, len(keys))
	skipped := 0
	firstSkipped := ""
	for _, key := range keys {
		if a.keyOwner(key) {
			kept = append(kept, key)
			continue
		}
		if skipped == 0 {
			firstSkipped = key
		}
		skipped++
	}
	if skipped > 0 {
		slog.Info("natskv truncate: skipping keys under this lens's prefix that its own key inverse does not claim — a sibling producer shares the prefix",
			"skipped", skipped, "kept", len(kept), "firstKey", firstSkipped, "prefix", a.keyPrefix)
	}
	return kept
}

// withoutUnsanctionedGrantKeys drops every D1 read-grant key from an unlicensed
// adapter's purge set.
//
// Truncate is the write path buildKey cannot guard: it lists keys and Purges
// them, so the namespace refusal has to be applied to the LIST instead. And the
// exposure is not hypothetical — ApplyTruncateScope derives a key prefix only
// for an actor-aggregate lens, so a descriptor-less plain lens sharing the
// capability bucket has no prefix at all and its rebuild would otherwise purge
// the WHOLE bucket, every sanctioned producer's grants included.
//
// Skipping rather than erroring is deliberate: a rebuild is an operator action
// on a lens that may be perfectly well-formed apart from sharing a bucket, and
// failing it outright would leave that lens unrebuildable. Removing its own
// rows while leaving the namespace alone is the outcome an operator wants.
//
// It is also the one refusal that returns NOTHING to its caller — Truncate sees
// a shorter list, not an error — so unlike the write path it logs here rather
// than relying on the caller to. One line per truncate (a rebuild is rare and
// operator-initiated, so there is nothing to bury), naming the count and one
// example key; the health fault goes through the same reporter the write
// refusal uses, which the pipeline dedups to once per lens.
func (a *NatsKVAdapter) withoutUnsanctionedGrantKeys(ctx context.Context, keys []string) []string {
	if a.readGrantWriter {
		return keys
	}
	kept := make([]string, 0, len(keys))
	skipped := 0
	firstSkipped := ""
	for _, key := range keys {
		if strings.HasPrefix(key, capabilityread.KeyPrefix) {
			if skipped == 0 {
				firstSkipped = key
			}
			skipped++
			continue
		}
		kept = append(kept, key)
	}
	if skipped > 0 {
		slog.Error("natskv truncate: SKIPPING keys in the reserved D1 read-grant namespace — this lens is not an installed read-grant producer, so a rebuild of it must not purge another lens's grants",
			"skipped", skipped, "firstKey", firstSkipped, "namespace", capabilityread.KeyPrefix)
		if a.unsanctionedKeyReporter != nil {
			a.unsanctionedKeyReporter(ctx, firstSkipped)
		}
	}
	return kept
}

// Probe checks whether the NATS KV bucket is reachable by calling kv.Status.
// Returns nil if the bucket is accessible; returns an infrastructure or structural
// error that failure.Classify can route appropriately.
func (a *NatsKVAdapter) Probe(ctx context.Context) error {
	return a.kv.Status(ctx)
}

// Close is a no-op; the NATS KV handle lifecycle is managed by the caller.
func (a *NatsKVAdapter) Close() error {
	return nil
}
