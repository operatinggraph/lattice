package weaver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// gapColumnPrefix is the §10.2 gap-column naming convention: every gap column
// (and therefore every §10.8 gaps key and every mark key segment) is a
// missing_<gap> snake_case bool.
const gapColumnPrefix = "missing_"

// inflightColumnPrefix and maxretriesColumnPrefix name the two engine-recognized
// dispatch-SUPPRESSION inputs keyed off a gap column: for gap missing_<g> the
// Lens may project inflight_<g> (a remediation is legitimately in flight — a bool)
// and maxretries_<g> (the gap's retry cap — an integer the gate bounds its
// weaver-state dispatch-count against). They are §10.2 BodyColumns the engine
// reads to alter behavior — like freshUntil — NOT gaps keys: gapSuppressed skips a
// gap whose inflight_<g> is true OR whose dispatch-count has reached
// maxretries_<g>, while the gap itself stays violating, in both dispatch legs. The
// prefix swap from missing_ keeps them generic with zero playbook config, and
// because neither starts with missing_ the gap-column scans (openGapColumns,
// markCandidateColumns) never treat a companion as a gap or mark it. Documented in
// docs/components/weaver.md alongside freshUntil.
const (
	inflightColumnPrefix   = "inflight_"
	maxretriesColumnPrefix = "maxretries_"
)

// rowBodyColumn names the SYNTHETIC column the unparseable-row-body data error
// is keyed at (issueKeyDataEntity's `data:<targetId>.<entityId>.<column>`
// family). The fault is "this row's JSON body does not parse", which is about
// the whole body rather than any one column, so it needs a column segment that
// cannot collide with a projected one — otherwise a lens projecting a column
// called `body` would share one latch with the parse error, and either fault's
// clear would silently retire the other's.
//
// What makes it un-collidable is the READER WHITELIST, not the name. Refractor's
// output descriptor accepts any non-blank bodyColumn (projection.ParseOutputDescriptor),
// so no reservation is enforced at projection time; a lens may legally project a
// column called `__body`. But a column segment only reaches the `data:` family by
// being PASSED to a reader, and every such call site passes either an engine
// constant (`violating`, `entityKey`, freshUntilColumn, admissionPriorityColumn,
// inflightColumnPrefix+g, maxretriesColumnPrefix+g) or a gap column — and a gap
// column is either one of the row's own `missing_*` keys or a playbook gaps key,
// which validateTarget rejects unless it matches the same `missing_<gap>`
// convention. A lens-authored name therefore reaches this family only when it
// starts with `missing_`, which this constant does not.
//
// The `__` prefix is kept for readability, mirroring the `__count` / `__control`
// weaver-state key tails: it reads as engine-synthetic at a glance. It is a
// convention here, not the guarantee.
const rowBodyColumn = "__body"

// markTTLBackstopFactor sizes the mark's NATS per-key TTL relative to its
// lease: TTL = markTTLBackstopFactor × lease. The TTL must be STRICTLY longer
// than the lease — the reconciler sweep is the prompt reclaim and the TTL is
// only the backstop, and the sweep can re-attempt a gap only while the key
// still exists past leaseExpiresAt. Nothing watches weaver-state, so a raw TTL
// deletion unwedges the gap but cannot re-attempt it; a TTL equal to the lease
// would make the sweep's re-attempt leg unreachable. A constant, not a config
// knob.
const markTTLBackstopFactor = 2

// dispatchCountTTLBackstopFactor sizes the dispatch-count's NATS per-key TTL
// relative to the mark lease: TTL = dispatchCountTTLBackstopFactor × lease. The
// count is the per-(target, entity, gap) retry-budget accumulator (§E mechanism
// B): incremented on each actual dispatch, deleted on gap-close, and bounded
// against the row's maxretries_<g>. Unlike the mark (TTL ≈ one anti-storm window,
// re-armed on reclaim), the count is CHAIN-scoped — it must survive every
// mark-lease/TTL expiry across a multi-attempt chain so the budget accumulates,
// and only the gap-close reset (or this backstop) ever removes it. The factor is
// therefore much larger than markTTLBackstopFactor: it must outlast a full
// cap-length chain, which is paced by the bridge's CallDeadline — the give-up
// horizon at which each attempt's failed outcome lands. The sweep is suppressed
// while a call is in flight (inflight_<g>), so attempts are CallDeadline apart,
// NOT mark-lease apart: the worst-case chain is cap × CallDeadline ≈ 3 × 24h =
// 72h at the defaults, and 256 × a 30min lease ≈ 128h clears it with ~1.8×
// headroom — so the TTL never expires the count MID-chain and silently re-opens
// the budget. It exists
// ONLY to garbage-collect an orphaned count whose gap-close was never observed
// (the entity vanished without a closing row). A constant, not a config knob.
const dispatchCountTTLBackstopFactor = 256

// mark is the weaver-state anti-storm in-flight record (Contract #10 §10.3),
// keyed <targetId>.<entityId>.<gapColumn>. The CAS-create of this key is the
// dispatch OCC: concurrent evaluations of the same gap race the create, the
// loser drops, the winner dispatches. LeaseExpiresAt mirrors the lease the
// per-key TTL backstops (§10.3 visibility); HeldBy is the writing engine
// instance. ClaimID is the per-OPEN-EPISODE token (a fresh NanoID minted at the
// mark's CAS-create, PRESERVED verbatim across every reclaim-replace): it seeds
// the deterministic userTask identity (assignTask's taskId, triggerLoom's Loom
// instanceId) so re-dispatch of the same open gap collapses on the existing
// artifact instead of minting a duplicate (§10.3 consumer-enforced idempotency).
// A legitimate close→reopen mints a new mark ⇒ new ClaimID ⇒ a fresh artifact.
//
// EscalatedFrom carries the plan LEG this episode displaced: an Augur
// escalation replaces the gap's leg mark with its own, whose Action is the
// reasoning op's dispatch class rather than a catalog Ref, and without this
// field the leg the gap was on would be unrecoverable — every level test on the
// pin (releaseCompletedLeg's effects-hold release) would go dark for as long as
// the escalation stood. It is empty on an ordinary leg mark, and on any mark
// that does not carry it — for which legOf falls to the count document.
//
// Escalation is the trigger the episode was escalated on ("unplannable" |
// "exhausted"), and empty for an ordinary episode. It is what makes the mark
// declare its own class: an escalation's Action is the reasoning op's dispatch
// class, which a gap whose catalog ref was merely removed looks exactly like, so
// a reader that inferred the class from "this action no longer resolves" would
// take a removed ref under an open leg for a standing escalation. The document
// declares its class; the key only addresses.
type mark struct {
	TargetID       string `json:"targetId"`
	EntityKey      string `json:"entityKey"`
	Gap            string `json:"gap"`
	Action         string `json:"action"`
	EscalatedFrom  string `json:"escalatedFrom,omitempty"`
	Escalation     string `json:"escalation,omitempty"`
	ClaimID        string `json:"claimId,omitempty"`
	ClaimedAt      string `json:"claimedAt"`
	LeaseExpiresAt string `json:"leaseExpiresAt,omitempty"`
	HeldBy         string `json:"heldBy,omitempty"`
}

// markStore is the weaver-state accessor for in-flight marks. The in-flight
// check is always a KV read — never an in-memory map: durable dispatch state
// lives in the bucket so any replica resolves it. lease sizes each mark's
// leaseExpiresAt (and, scaled by markTTLBackstopFactor, its per-key TTL);
// instance is the heldBy holder tag.
type markStore struct {
	conn     *substrate.Conn
	bucket   string
	lease    time.Duration
	instance string
}

func newMarkStore(conn *substrate.Conn, bucket string, lease time.Duration, instance string) *markStore {
	return &markStore{conn: conn, bucket: bucket, lease: lease, instance: instance}
}

// markKey builds the §10.3 mark key. Entity is keyed by NanoID, never the
// dotted vertex key (the full key rides the mark's entityKey field —
// document-is-truth).
func markKey(targetID, entityID, gapColumn string) string {
	return targetID + "." + entityID + "." + gapColumn
}

// create CAS-creates the mark (KV create-on-absent — the dispatch OCC) and
// returns its create revision (the per-dispatch-episode tag the deterministic
// requestId derives from) AND the freshly-minted per-open-episode claimId (the
// stable token the userTask identity derives from, §10.3). exists=true means the
// create lost the race: another dispatch of this gap is in flight (claimId is
// empty — the winner's claimId lives on the existing mark and the loser does not
// dispatch). The mark carries the §10.3 lease (leaseExpiresAt = now + lease,
// heldBy = this instance) and a NATS per-key TTL of markTTLBackstopFactor ×
// lease — the backstop that bounds the mark's life even if no reconciler ever
// sweeps it.
//
// escalatedFrom is the plan leg this episode displaces and escalation the
// trigger it was escalated on — both set only by an Augur escalation, empty for
// every ordinary dispatch.
func (m *markStore) create(ctx context.Context, targetID, entityID, gapColumn, entityKey, action, escalatedFrom, escalation string) (revision uint64, claimID string, exists bool, err error) {
	claimID, err = substrate.NewNanoID()
	if err != nil {
		return 0, "", false, fmt.Errorf("weaver: mint mark claimId: %w", err)
	}
	now := time.Now()
	rec := mark{
		TargetID:       targetID,
		EntityKey:      entityKey,
		Gap:            gapColumn,
		Action:         action,
		EscalatedFrom:  escalatedFrom,
		Escalation:     escalation,
		ClaimID:        claimID,
		ClaimedAt:      substrate.FormatTimestamp(now),
		LeaseExpiresAt: substrate.FormatTimestamp(now.Add(m.lease)),
		HeldBy:         m.instance,
	}
	body, err := json.Marshal(rec)
	if err != nil {
		return 0, "", false, fmt.Errorf("weaver: marshal mark: %w", err)
	}
	rev, err := m.conn.KVCreateWithTTL(ctx, m.bucket, markKey(targetID, entityID, gapColumn), body,
		markTTLBackstopFactor*m.lease)
	if err != nil {
		if errors.Is(err, substrate.ErrRevisionConflict) {
			return 0, "", true, nil
		}
		return 0, "", false, err
	}
	return rev, claimID, false, nil
}

// get reads the mark for one gap, returning its current revision. Lane-1 only
// ever CAS-creates and deletes marks, and the sweep's reclaim replaces the
// whole value under a revision condition — so the current revision always
// identifies the episode currently holding the gap (the episode tag).
// found=false means no dispatch is in flight.
func (m *markStore) get(ctx context.Context, targetID, entityID, gapColumn string) (rec *mark, revision uint64, found bool, err error) {
	entry, err := m.conn.KVGet(ctx, m.bucket, markKey(targetID, entityID, gapColumn))
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return nil, 0, false, nil
		}
		return nil, 0, false, err
	}
	var rc mark
	if err := json.Unmarshal(entry.Value, &rc); err != nil {
		return nil, 0, false, fmt.Errorf("weaver: unmarshal mark %s: %w", entry.Key, err)
	}
	return &rc, entry.Revision, true, nil
}

// replace re-arms an expired mark in place — the reconciler's reclaim claim.
// The write is revision-conditioned on expectedRevision (the revision the
// sweep read this pass) and produces a fresh §10.3 value (new claimedAt and
// leaseExpiresAt, heldBy = this instance) with a re-armed per-key TTL, so the
// key is never absent across a reclaim: a crash at any point leaves either
// the old expired mark (re-swept next pass) or the fresh mark (its lease
// bounds the retry). The returned revision is the fresh dispatch-episode tag.
// conflict=true means the mark changed since the read (a fresh episode
// CAS-created it, or its TTL marker landed) — the caller must skip.
//
// claimID is the existing mark's per-open-episode token, PRESERVED verbatim: a
// reclaim is the SAME open episode (only the lease/claimedAt/heldBy refresh), so
// the userTask identity it seeds stays stable and the re-dispatch collapses on
// the existing task/instance rather than duplicating it (§10.3).
//
// ttl is the re-armed per-key TTL — the backstop that bounds the mark's life if
// no reconciler ever sweeps it. The caller sizes it: the default backstop
// (markTTLBackstopFactor × lease) for a normal reclaim, or wider for a
// collapse-only userTask reclaim that the sweep will deliberately pace with a
// backoff longer than the default backstop (so the mark survives until the next
// scheduled reclaim instead of TTL-expiring into a markless open gap).
//
// escalatedFrom is the displaced plan leg the mark carries and escalation the
// class it declares (the mark type's doc). The whole value is rewritten here, so
// a re-arm that did not thread the pair through would drop the leg an escalation
// is standing over — taking every level test on that pin dark — and leave the
// re-armed mark unable to say it is an escalation at all. The caller passes what
// the re-armed episode still is, which for an ordinary re-arm is what the mark
// already carried.
func (m *markStore) replace(ctx context.Context, targetID, entityID, gapColumn, entityKey, action, escalatedFrom, escalation, claimID string,
	expectedRevision uint64, ttl time.Duration) (revision uint64, conflict bool, err error) {

	now := time.Now()
	rec := mark{
		TargetID:       targetID,
		EntityKey:      entityKey,
		Gap:            gapColumn,
		Action:         action,
		EscalatedFrom:  escalatedFrom,
		Escalation:     escalation,
		ClaimID:        claimID,
		ClaimedAt:      substrate.FormatTimestamp(now),
		LeaseExpiresAt: substrate.FormatTimestamp(now.Add(m.lease)),
		HeldBy:         m.instance,
	}
	body, err := json.Marshal(rec)
	if err != nil {
		return 0, false, fmt.Errorf("weaver: marshal mark: %w", err)
	}
	rev, err := m.conn.KVUpdateWithTTL(ctx, m.bucket, markKey(targetID, entityID, gapColumn), body,
		expectedRevision, ttl)
	if err != nil {
		if errors.Is(err, substrate.ErrRevisionConflict) {
			return 0, true, nil
		}
		return 0, false, err
	}
	return rev, false, nil
}

// delete clears one gap's mark (gap closed — level-reconciled clearing). A
// missing key is success: the level reconcile deletes by candidate column, not
// by observed presence.
func (m *markStore) delete(ctx context.Context, targetID, entityID, gapColumn string) error {
	err := m.conn.KVDelete(ctx, m.bucket, markKey(targetID, entityID, gapColumn))
	if err != nil && !errors.Is(err, substrate.ErrKeyNotFound) {
		return err
	}
	return nil
}

// deleteRevision clears one gap's mark, but ONLY if it is still at revision —
// the revision-conditioned counterpart to delete, for a caller that read the
// mark at a known revision and must not blindly clear a DIFFERENT episode
// that raced in since (releaseCompletedLeg's reconciler call site: the sweep
// reads an expired mark, but a concurrent CDC delivery may have already
// released that SAME leg and CAS-created a fresh mark for the next one before
// the sweep's delete lands — a blind KVDelete would then wipe the fresh
// episode's mark instead of skipping it, exactly the race `replace` and
// `deleteMark` already guard against for every other sweep-path mutation).
// conflict=true (mirroring replace's own conflict semantics) means the mark
// changed or is already gone — the caller must not proceed as if ITS release
// won.
func (m *markStore) deleteRevision(ctx context.Context, targetID, entityID, gapColumn string, revision uint64) (conflict bool, err error) {
	if err := m.conn.KVDeleteRevision(ctx, m.bucket, markKey(targetID, entityID, gapColumn), revision); err != nil {
		if errors.Is(err, substrate.ErrRevisionConflict) || errors.Is(err, substrate.ErrKeyNotFound) {
			return true, nil
		}
		return false, err
	}
	return false, nil
}

// countKeySuffix names the reserved dispatch-count key tail:
// `<targetId>.<entityId>.<gapColumn>.__count`. The count is matched by suffix
// wherever weaver-state is enumerated — the marksInFlight gauge skips it, and
// the reconciler sweep routes it to its own count leg — because it is NOT a
// §10.3 mark: it has a 4th segment, so splitMarkKey would reject it as corrupt.
// The "__count" tail can never be a
// gapColumn (singleTokenPattern forbids the dot) nor a NanoID entityId
// (substrate.Alphabet has no underscore), so the count, mark, and `__control`
// key shapes are mutually disjoint.
const countKeySuffix = ".__count"

// dispatchCount is the JSON body of a `<targetId>.<entityId>.<gapColumn>.__count`
// key. It carries two independent tallies for the two populations that read it,
// because one integer cannot serve both:
//
//   - Count is the ATTEMPTS mounted in the current chain — a dispatch that
//     actually mounts something against the vendor, the task or the op. It is
//     the exhaustion gate's input (`maxretries_<g>`).
//   - Reclaims is how many times the sweep (or lane 1's stale-external re-arm,
//     or an escalation re-fire) has RE-ARMED the same open episode. It is the
//     reclaim backoff's exponent. A re-arm that collapses onto an artifact the
//     open episode already created mounts no attempt, so it advances Reclaims
//     alone — the same predicate the `__effect` window books on.
//
// Leg is the actionRef Count is charged to, so the plan leg a gap was on
// survives the mark that pinned it (the count leg reaches a gap with no mark at
// all). EscalatedAt is the last Augur escalation fire, which paces the next one
// on the Reclaims series.
//
// A document may carry Count alone; each other field then reads as its zero
// value, which is the same disposition a first reclaim and a first escalation
// have anyway.
type dispatchCount struct {
	Count       int    `json:"count"`
	Reclaims    int    `json:"reclaims,omitempty"`
	Leg         string `json:"leg,omitempty"`
	EscalatedAt string `json:"escalatedAt,omitempty"`
}

// countKey builds the §E dispatch-count key. Entity is keyed by NanoID (the same
// segment shape as the mark key), with the reserved __count tail.
func countKey(targetID, entityID, gapColumn string) string {
	return targetID + "." + entityID + "." + gapColumn + countKeySuffix
}

// splitCountKey splits a §E dispatch-count key
// `<targetId>.<entityId>.<gapColumn>.__count`. The reserved tail is stripped
// first, which is what makes the shape decidable: what remains is exactly the
// positional `<targetId>.<entityId>.<gapColumn>` mark shape, whose segments are
// an install-validated dot-free token, a bare NanoID and another dot-free token
// (splitMarkKey). A key without the reserved tail — a mark, a `__control`
// marker, an `__effect` window — is not a count key, and anything that does not
// parse is corrupt.
func splitCountKey(key string) (targetID, entityID, gapColumn string, ok bool) {
	head, found := strings.CutSuffix(key, countKeySuffix)
	if !found {
		return "", "", "", false
	}
	return splitMarkKey(head)
}

// dispatchCountCASRetries bounds the read-modify-write retry loop on the count
// (no atomic-increment primitive exists). A handful of attempts absorbs the rare
// concurrent increment (lane-1 vs the sweep's reclaim both firing the same gap);
// beyond that the loser surfaces the conflict to its caller, which already treats
// a count failure as the safe side (do not over-suppress).
const dispatchCountCASRetries = 5

// getDispatchCount reads the whole dispatch-count document for one gap (the
// zero value when absent — no dispatch has happened yet, or the count was reset
// on a gap-close). The read is the gate's authority: the budget is spent iff
// Count has reached the row's maxretries_<g>. Callers that pace, resolve a leg
// or re-fire an escalation read the document's other fields from the SAME read,
// so a gap costs one round trip per pass however many of its tallies are
// consulted.
//
// The KV revision comes back with the document, and revision 0 means there was
// no document to read. It is what a caller conditions a write on to prove it is
// acting on the value it looked at — releaseCompletedLeg's markless branch takes
// its whole mutual exclusion from it, having no mark to condition on.
func (m *markStore) getDispatchCount(ctx context.Context, targetID, entityID, gapColumn string) (dispatchCount, uint64, error) {
	entry, err := m.conn.KVGet(ctx, m.bucket, countKey(targetID, entityID, gapColumn))
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return dispatchCount{}, 0, nil
		}
		return dispatchCount{}, 0, err
	}
	var dc dispatchCount
	if err := json.Unmarshal(entry.Value, &dc); err != nil {
		return dispatchCount{}, 0, fmt.Errorf("weaver: unmarshal dispatch-count %s: %w", entry.Key, err)
	}
	return dc, entry.Revision, nil
}

// incrementDispatchCount books one dispatch against the gap's count document
// (creating it on absence) and returns the document it wrote. It is the
// read-modify-write analogue of an atomic increment: a CAS-create on absence,
// else a revision-conditioned update, retried a bounded number of times so a
// concurrent increment (lane-1 vs the sweep's reclaim) does not lose a booking.
// Every write arms the long TTL backstop (dispatchCountTTLBackstopFactor ×
// lease) — the count is chain-scoped and the gap-close reset is its prompt
// removal; the TTL only GCs an orphan.
//
// The caller states what the dispatch WAS, on two independent axes, because the
// two tallies answer different questions (dispatchCount's doc):
//
//   - attempt: this dispatch mounts a genuinely new attempt, so Count advances
//     toward maxretries_<g>. actionRef names the leg the attempts are charged
//     to, and a document with no leg recorded takes it as its own.
//   - reclaim: this dispatch re-armed an already-open episode, so Reclaims
//     advances and the next re-arm waits one step longer.
//   - legScoped: this gap's budget is PER LEG, so a stored leg different from
//     actionRef is a leg boundary and restarts the bookkeeping under the new
//     ref (bookDispatch states the rule and why only a goal gap gets it).
//
// A collapse-only re-dispatch is a reclaim and not an attempt; a fresh episode
// is an attempt and not a reclaim; an external or directOp re-arm is both.
func (m *markStore) incrementDispatchCount(ctx context.Context, targetID, entityID, gapColumn, actionRef string,
	attempt, reclaim, legScoped bool) (dispatchCount, error) {

	key := countKey(targetID, entityID, gapColumn)
	ttl := dispatchCountTTLBackstopFactor * m.lease
	for try := 0; try < dispatchCountCASRetries; try++ {
		entry, err := m.conn.KVGet(ctx, m.bucket, key)
		if err != nil {
			if !errors.Is(err, substrate.ErrKeyNotFound) {
				return dispatchCount{}, err
			}
			if !attempt {
				// A re-arm with no document to re-arm against. Creating one
				// would persist {count:0}, and a count key that exists and
				// reads 0 is the ONE state the sweep's count leg treats as an
				// operator's un-park — it would fire a fresh markless episode
				// for a gap nobody re-armed. The re-arm tally has nothing to
				// pace here either: the first booking creates the document at
				// the first attempt.
				return dispatchCount{}, nil
			}
			next := bookDispatch(dispatchCount{}, actionRef, attempt, reclaim, legScoped)
			body, mErr := json.Marshal(next)
			if mErr != nil {
				return dispatchCount{}, fmt.Errorf("weaver: marshal dispatch-count: %w", mErr)
			}
			if _, cErr := m.conn.KVCreateWithTTL(ctx, m.bucket, key, body, ttl); cErr != nil {
				if errors.Is(cErr, substrate.ErrRevisionConflict) {
					continue // someone created it first — re-read and update.
				}
				return dispatchCount{}, cErr
			}
			return next, nil
		}
		var dc dispatchCount
		if uErr := json.Unmarshal(entry.Value, &dc); uErr != nil {
			return dispatchCount{}, fmt.Errorf("weaver: unmarshal dispatch-count %s: %w", entry.Key, uErr)
		}
		next := bookDispatch(dc, actionRef, attempt, reclaim, legScoped)
		body, mErr := json.Marshal(next)
		if mErr != nil {
			return dispatchCount{}, fmt.Errorf("weaver: marshal dispatch-count: %w", mErr)
		}
		if _, uErr := m.conn.KVUpdateWithTTL(ctx, m.bucket, key, body, entry.Revision, ttl); uErr != nil {
			if errors.Is(uErr, substrate.ErrRevisionConflict) {
				continue // lost the race — re-read and retry.
			}
			return dispatchCount{}, uErr
		}
		return next, nil
	}
	return dispatchCount{}, fmt.Errorf("weaver: dispatch-count %s contended past %d retries", key, dispatchCountCASRetries)
}

// bookDispatch applies one dispatch's booking to a count document — the pure
// half of incrementDispatchCount's read-modify-write, so the rule is testable
// without a KV and identical on the create and update legs.
//
// actionDirectOp is never taken as a leg. It is a dispatch CLASS, and the one
// dispatch that carries it as an "actionRef" is the Augur escalation, whose
// episode is booked nowhere at all; a booking that let it through would rewrite
// Leg to a name no catalog holds and restart the budget of the chain the
// escalation is standing over — which is exactly the leg the escalation must
// preserve. Every leg a goal catalog can declare is a Ref, so nothing legitimate
// is lost.
//
// legScoped is what makes a leg change a BOUNDARY rather than merely a different
// pick, and only a goal gap has it. A goal chain advances leg by leg, each
// boundary witnessed by releaseCompletedLeg (which deletes the whole document),
// so a change seen here is a boundary nothing witnessed and the previous leg's
// attempts no longer bound this one. Every other shape re-decides its action per
// EPISODE from live inputs — a candidates gap re-ranks over the `__effect`
// close-rate windows, which move under it — so two candidates alternating would
// restart the budget on every booking, the gate would never see it reach the
// cap, and the gap would sit in the silent park §10.8 forbids instead of
// escalating. There the differing ref records the current pick and nothing more.
func bookDispatch(dc dispatchCount, actionRef string, attempt, reclaim, legScoped bool) dispatchCount {
	next := dc
	leg := actionRef
	if leg == actionDirectOp {
		leg = ""
	}
	if attempt {
		switch {
		case legScoped && leg != "" && next.Leg != "" && next.Leg != leg:
			// A leg change IS a new chain: the attempts charged to the previous
			// leg bound that leg, not this one. It restarts the whole episode's
			// bookkeeping, not just the tally: the re-arm history and the last
			// escalation belong to the leg they were spent on, and carrying them
			// would make the new leg's first reclaim wait the old leg's
			// exponential.
			next.Count = 1
			next.Reclaims = 0
			next.EscalatedAt = ""
		case next.Count == 0 && next.Leg == "" && next.EscalatedAt != "":
			// An ESCALATION-ONLY document: no attempt has ever been charged to
			// it and it names no leg, so the only thing it records is an
			// escalation's pacing (bookEscalation creates exactly this shape for
			// the `unplannable` trigger). The attempt booked over it belongs to a
			// chain that can act again, and the re-arm history and last-fire
			// instant it would otherwise inherit are the escalation's — carried
			// forward, the fresh chain's first reclaim would wait out the dead
			// episode's exponential. The same restart the leg change performs, for
			// the same reason.
			//
			// An operator's un-park is the other document that reads 0, and it is
			// told apart by its stored leg: resetDispatchCount rewrites only
			// Count, so an un-parked chain still names the leg it was on and keeps
			// inheriting its pacing.
			next.Count = 1
			next.Reclaims = 0
			next.EscalatedAt = ""
		default:
			next.Count++
		}
		if leg != "" {
			next.Leg = leg
		}
	}
	if reclaim {
		next.Reclaims++
	}
	return next
}

// bookEscalation records that an Augur escalation fired for this gap just now:
// EscalatedAt carries the fire instant the next re-fire is paced against, and
// Reclaims advances so that pacing lengthens exactly like every other re-arm of
// an open episode. Same bounded CAS read-modify-write loop as
// incrementDispatchCount; booked=false means nothing was written.
//
// An ABSENT document is created for the `unplannable` trigger and only for it.
// That trigger is reached with no document by construction — the gap books
// nothing, and one that has no plan has mounted no attempt — so refusing the
// create would leave the two doors it serves with nothing to pace on at all, and
// every re-fire unpaced forever. `exhausted` keeps the refusal: it is reached
// only past a spent budget, so absence is impossible there, and a document
// created at Count 0 for it would read to the reconciler's count leg as an
// operator's un-park — the one state that arm fires a fresh markless episode on
// — handing the gap a dispatch nobody asked for. The created shape is
// {count:0, reclaims:1, escalatedAt:now}: no attempt is charged to it, and the
// leg it names is empty, which is what tells it apart from an un-park (whose
// stored leg survives the reset) at every reader of a zero.
func (m *markStore) bookEscalation(ctx context.Context, targetID, entityID, gapColumn, trigger string, now time.Time) (booked bool, err error) {
	key := countKey(targetID, entityID, gapColumn)
	ttl := dispatchCountTTLBackstopFactor * m.lease
	for try := 0; try < dispatchCountCASRetries; try++ {
		entry, gErr := m.conn.KVGet(ctx, m.bucket, key)
		if gErr != nil {
			if !errors.Is(gErr, substrate.ErrKeyNotFound) {
				return false, gErr
			}
			if trigger != escalateUnplannable {
				return false, nil
			}
			fresh := dispatchCount{Reclaims: 1, EscalatedAt: substrate.FormatTimestamp(now)}
			body, mErr := json.Marshal(fresh)
			if mErr != nil {
				return false, fmt.Errorf("weaver: marshal dispatch-count: %w", mErr)
			}
			if _, cErr := m.conn.KVCreateWithTTL(ctx, m.bucket, key, body, ttl); cErr != nil {
				if errors.Is(cErr, substrate.ErrRevisionConflict) {
					continue // someone created it first — re-read and update.
				}
				return false, cErr
			}
			return true, nil
		}
		var dc dispatchCount
		if uErr := json.Unmarshal(entry.Value, &dc); uErr != nil {
			return false, fmt.Errorf("weaver: unmarshal dispatch-count %s: %w", entry.Key, uErr)
		}
		dc.EscalatedAt = substrate.FormatTimestamp(now)
		dc.Reclaims++
		body, mErr := json.Marshal(dc)
		if mErr != nil {
			return false, fmt.Errorf("weaver: marshal dispatch-count: %w", mErr)
		}
		if _, uErr := m.conn.KVUpdateWithTTL(ctx, m.bucket, key, body, entry.Revision, ttl); uErr != nil {
			if errors.Is(uErr, substrate.ErrRevisionConflict) {
				continue // lost the race — re-read and retry.
			}
			if errors.Is(uErr, substrate.ErrKeyNotFound) {
				return false, nil
			}
			return false, uErr
		}
		return true, nil
	}
	return false, fmt.Errorf("weaver: dispatch-count %s contended past %d retries", key, dispatchCountCASRetries)
}

// retryBudgetStore is the two-operation view of weaver-state the un-park verb
// needs: read the budget WITH the revision it was read at, then write it back
// to 0 conditioned on that revision. Naming the pair as an interface is what
// makes the verb's refusal path reachable in a test — the conflict it exists to
// report is a lost race against a concurrent dispatch, and racing a live KV to
// produce one is the kind of proof that passes when it feels like it. markStore
// is the only production implementation.
type retryBudgetStore interface {
	dispatchCountEntry(ctx context.Context, targetID, entityID, gapColumn string) (doc dispatchCount, revision uint64, found bool, err error)
	resetDispatchCount(ctx context.Context, targetID, entityID, gapColumn string, prior dispatchCount, expectedRevision uint64) (conflict bool, err error)
}

// dispatchCountEntry reads one gap's dispatch-count document together with the
// KV revision it was read at — the revision a conditioned write must name to
// prove it is replacing the value it looked at. found=false means no chain has
// ever dispatched this gap. An unreadable body reports the zero document with
// its real revision: the value is unknowable, but the key is still the one to
// condition on, and a reset is exactly the repair for it.
//
// The whole document, not just the count: an un-park replaces the budget and
// keeps everything else the document says about the episode (its pacing history
// and the leg it is on), which the writer can only do if the reader hands it
// over.
func (m *markStore) dispatchCountEntry(ctx context.Context, targetID, entityID, gapColumn string) (doc dispatchCount, revision uint64, found bool, err error) {
	entry, err := m.conn.KVGet(ctx, m.bucket, countKey(targetID, entityID, gapColumn))
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return dispatchCount{}, 0, false, nil
		}
		return dispatchCount{}, 0, false, err
	}
	var dc dispatchCount
	if uErr := json.Unmarshal(entry.Value, &dc); uErr != nil {
		return dispatchCount{}, entry.Revision, true, nil
	}
	return dc, entry.Revision, true, nil
}

// resetDispatchCount re-arms one gap's §E retry budget by writing its
// dispatch-count back to 0 at expectedRevision — the operator un-park
// (Engine.ResetRetryBudget), conditioned on the revision its caller read so it
// can only ever replace the value the operator was shown.
//
// It writes 0; it does NOT delete the key, and the difference is the whole
// point. The reconciler's count leg is COUNT-ANCHORED: it enumerates
// `…__count` keys, so a gap whose count key is gone is a gap that leg cannot
// see. Deleting here would leave a parked gap with no count, no mark and no
// delivery coming — un-suppressed and still un-dispatched, a quieter park than
// the one the operator was trying to end. A 0 is indistinguishable from an
// absent key to every reader (getDispatchCount returns 0 for both), so the
// budget is genuinely fresh, while the key survives for the next pass to
// enumerate and dispatch from. The write re-arms the same long TTL every other
// count write uses.
//
// conflict=true means the count moved between that read and this write — a
// dispatch landed first — so nothing is written: the chain is going again on
// its own, and forcing it back to 0 would hand it a second full budget nobody
// asked for. A key that vanished mid-flight reports the same way; both are
// "the state you decided on is gone", and re-reading is the remedy.
//
// It re-arms the BUDGET and nothing else: prior is the document the caller read
// at expectedRevision, and every field but Count is written back verbatim. An
// operator's un-park says "let this gap try again"; it says nothing about how
// often the sweep has re-armed the episode, which leg the chain is on, or when
// the escalation last fired — and zeroing those would restart the pacing of an
// episode that may still be open, re-firing it at the base interval.
func (m *markStore) resetDispatchCount(ctx context.Context, targetID, entityID, gapColumn string,
	prior dispatchCount, expectedRevision uint64) (conflict bool, err error) {

	prior.Count = 0
	body, mErr := json.Marshal(prior)
	if mErr != nil {
		return false, fmt.Errorf("weaver: marshal dispatch-count: %w", mErr)
	}
	if _, uErr := m.conn.KVUpdateWithTTL(ctx, m.bucket, countKey(targetID, entityID, gapColumn), body,
		expectedRevision, dispatchCountTTLBackstopFactor*m.lease); uErr != nil {
		if errors.Is(uErr, substrate.ErrRevisionConflict) || errors.Is(uErr, substrate.ErrKeyNotFound) {
			return true, nil
		}
		return false, uErr
	}
	return false, nil
}

// deleteDispatchCount clears one gap's dispatch-count — the §E budget reset on
// gap-close, run from clearClosedMarks (the same level-reconciled path that
// deletes the mark) when a row delivery observes the close. It is not the only
// reset: the reconciler's count leg (sweeper.deleteCount) performs the same
// reset, revision-conditioned, for a gap whose row has gone quiet and whose
// close no delivery will ever announce. Both are level-reconciled reads of the
// same fact, so either observing it first is correct. A missing key is success
// (idempotent): a closed gap with no prior dispatch never had a count.
func (m *markStore) deleteDispatchCount(ctx context.Context, targetID, entityID, gapColumn string) error {
	err := m.conn.KVDelete(ctx, m.bucket, countKey(targetID, entityID, gapColumn))
	if err != nil && !errors.Is(err, substrate.ErrKeyNotFound) {
		return err
	}
	return nil
}

// deleteDispatchCountRevision clears one gap's dispatch-count, but ONLY if it is
// still at revision — the count's counterpart to deleteRevision, and the same
// semantics: conflict=true means the document changed or is already gone, and
// the caller must not proceed as if ITS delete won.
//
// It is what makes the count document a MUTEX. A release with no mark to
// condition on (releaseCompletedLeg's markless branch) has nothing else to
// serialize two concurrent derivations of the same leg boundary against, and the
// rest of the release is not idempotent: recordEffectClose flips one pending
// slot per call, so two blind releases would credit one leg twice. Winning this
// delete is the right to run the rest exactly once.
func (m *markStore) deleteDispatchCountRevision(ctx context.Context, targetID, entityID, gapColumn string,
	revision uint64) (conflict bool, err error) {

	if err := m.conn.KVDeleteRevision(ctx, m.bucket, countKey(targetID, entityID, gapColumn), revision); err != nil {
		if errors.Is(err, substrate.ErrRevisionConflict) || errors.Is(err, substrate.ErrKeyNotFound) {
			return true, nil
		}
		return false, err
	}
	return false, nil
}

// countInFlight reports how many in-flight marks exist in the bucket, scanned
// on the heartbeat cadence (never per-message). Reserved `<targetId>.__control`
// dispatch-skip markers, `…__count` dispatch-count keys, and `…__effect…`
// confidence windows are skipped — none is a §10.3 mark, told apart by the same
// reserved suffixes/marker the reconciler sweep splits its legs on, so the
// marksInFlight gauge counts only real in-flight dispatch.
func (m *markStore) countInFlight(ctx context.Context) (int, error) {
	keys, err := m.conn.KVListKeys(ctx, m.bucket)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, key := range keys {
		if strings.HasSuffix(key, controlKeySuffix) || strings.HasSuffix(key, countKeySuffix) ||
			strings.Contains(key, effectKeyMarker) {
			continue
		}
		n++
	}
	return n, nil
}

// effectKeyMarker names the reserved §10.3/§10.8 effect-bookkeeping shape:
// `<targetId>.__effect.<gapColumn>.<actionRef>` (Contract #10 §10.3, ratified
// 2026-07-04). Disjoint from marks/`__control`/`__count` by the same
// reserved-underscore-token argument: a real mark's segments never contain
// "__effect", so the marker can never collide.
const effectKeyMarker = ".__effect."

// effectWindowSize (K) sizes the sliding window of per-(target, gapColumn,
// actionRef) dispatch/close outcomes the planner's future close-rate ranking
// (Fire 5) reads. Config-tunable like MarkLease; a constant default here —
// Fire 5's brief may promote it to a config knob, the mechanism is fixed.
const effectWindowSize = 20

// effectStats is the JSON body of an `__effect` confidence-window key: a FIFO
// ring, oldest first, capped at effectWindowSize — one entry per dispatch
// episode of this (target, gapColumn, actionRef), true once that episode's
// gap has been observed to close, false while still open/pending. Eviction
// (the oldest entry dropped once len exceeds the cap, whatever its outcome)
// ages out old episodes on its own — the sliding window IS the decay, no
// clock sampling (design weaver-planner-mandate-design.md §3.2).
type effectStats struct {
	Window []bool `json:"window"`
}

// effectKey builds the reserved effect-bookkeeping key.
func effectKey(targetID, gapColumn, actionRef string) string {
	return targetID + effectKeyMarker + gapColumn + "." + actionRef
}

// splitEffectKey splits a `<targetId>.__effect.<gapColumn>.<actionRef>` key.
// targetId/gapColumn/actionRef are install-validated single dot-free tokens
// (singleTokenPattern), so the split is positional off the reserved marker.
func splitEffectKey(key string) (targetID, gapColumn, actionRef string, ok bool) {
	idx := strings.Index(key, effectKeyMarker)
	if idx <= 0 {
		return "", "", "", false
	}
	targetID = key[:idx]
	rest := key[idx+len(effectKeyMarker):]
	j := strings.IndexByte(rest, '.')
	if j <= 0 {
		return "", "", "", false
	}
	gapColumn, actionRef = rest[:j], rest[j+1:]
	if !singleTokenPattern.MatchString(targetID) || !singleTokenPattern.MatchString(gapColumn) ||
		!singleTokenPattern.MatchString(actionRef) {
		return "", "", "", false
	}
	return targetID, gapColumn, actionRef, true
}

// recordEffectDispatch appends one fresh dispatch episode (pending, not yet
// closed) to the (targetID, gapColumn, actionRef) confidence window — the SAME
// two seams that advance the chain's dispatch-count (the CAS-create-won
// lane-1 path and the sweep's reclaim), never a redelivery re-fire. Read-
// modify-write retried like incrementDispatchCount (no atomic-append
// primitive exists); a persistent failure is the caller's to log and skip —
// the window is Fire 5's future ranking input, never a dispatch gate.
func (m *markStore) recordEffectDispatch(ctx context.Context, targetID, gapColumn, actionRef string) error {
	key := effectKey(targetID, gapColumn, actionRef)
	for attempt := 0; attempt < dispatchCountCASRetries; attempt++ {
		entry, err := m.conn.KVGet(ctx, m.bucket, key)
		var stats effectStats
		existed := false
		var rev uint64
		if err != nil {
			if !errors.Is(err, substrate.ErrKeyNotFound) {
				return err
			}
		} else {
			if uErr := json.Unmarshal(entry.Value, &stats); uErr != nil {
				return fmt.Errorf("weaver: unmarshal effect stats %s: %w", key, uErr)
			}
			existed = true
			rev = entry.Revision
		}
		stats.Window = append(stats.Window, false)
		if len(stats.Window) > effectWindowSize {
			stats.Window = stats.Window[len(stats.Window)-effectWindowSize:]
		}
		body, mErr := json.Marshal(stats)
		if mErr != nil {
			return fmt.Errorf("weaver: marshal effect stats: %w", mErr)
		}
		if !existed {
			if _, cErr := m.conn.KVCreate(ctx, m.bucket, key, body); cErr != nil {
				if errors.Is(cErr, substrate.ErrRevisionConflict) {
					continue // someone created it first — re-read and update.
				}
				return cErr
			}
			return nil
		}
		if _, uErr := m.conn.KVUpdate(ctx, m.bucket, key, body, rev); uErr != nil {
			if errors.Is(uErr, substrate.ErrRevisionConflict) {
				continue // lost the race — re-read and retry.
			}
			return uErr
		}
		return nil
	}
	return fmt.Errorf("weaver: effect stats %s contended past %d retries", key, dispatchCountCASRetries)
}

// recordEffectClose flips the OLDEST still-pending (not-yet-closed) episode in
// the (targetID, gapColumn, actionRef) confidence window to closed — run from
// the gap-close path (clearClosedMarks), the same level-reconciled seam that
// resets the dispatch-count. FIFO-oldest matching, not per-entity pairing: the
// window aggregates outcomes across every entity that dispatched this
// (target, gapColumn, actionRef), so an exact per-episode pairing is neither
// available nor needed — Fire 5's close-rate ranking only reads the
// aggregate. A missing key (nothing was ever dispatched for this pair) or a
// window with no pending slot (a stale/duplicate close, or every slot already
// closed) is a no-op, never an error.
func (m *markStore) recordEffectClose(ctx context.Context, targetID, gapColumn, actionRef string) error {
	key := effectKey(targetID, gapColumn, actionRef)
	for attempt := 0; attempt < dispatchCountCASRetries; attempt++ {
		entry, err := m.conn.KVGet(ctx, m.bucket, key)
		if err != nil {
			if errors.Is(err, substrate.ErrKeyNotFound) {
				return nil
			}
			return err
		}
		var stats effectStats
		if uErr := json.Unmarshal(entry.Value, &stats); uErr != nil {
			return fmt.Errorf("weaver: unmarshal effect stats %s: %w", key, uErr)
		}
		flipped := false
		for i := range stats.Window {
			if !stats.Window[i] {
				stats.Window[i] = true
				flipped = true
				break
			}
		}
		if !flipped {
			return nil
		}
		body, mErr := json.Marshal(stats)
		if mErr != nil {
			return fmt.Errorf("weaver: marshal effect stats: %w", mErr)
		}
		if _, uErr := m.conn.KVUpdate(ctx, m.bucket, key, body, entry.Revision); uErr != nil {
			if errors.Is(uErr, substrate.ErrRevisionConflict) {
				continue // lost the race — re-read and retry.
			}
			return uErr
		}
		return nil
	}
	return fmt.Errorf("weaver: effect stats %s contended past %d retries", key, dispatchCountCASRetries)
}

// effectCloseRate reads the (targetID, gapColumn, actionRef) confidence
// window and returns the fraction of its recorded episodes observed to close
// (closed / len(Window)), plus the sample size. ok=false means no window
// exists yet (nothing has ever dispatched this pair) — the Fire-4 shadow
// ranking treats that as "no data", never as a zero close-rate. A read
// failure other than key-not-found is returned so the caller can log it
// without silently ranking on stale data.
func (m *markStore) effectCloseRate(ctx context.Context, targetID, gapColumn, actionRef string) (rate float64, sampleSize int, ok bool, err error) {
	entry, gErr := m.conn.KVGet(ctx, m.bucket, effectKey(targetID, gapColumn, actionRef))
	if gErr != nil {
		if errors.Is(gErr, substrate.ErrKeyNotFound) {
			return 0, 0, false, nil
		}
		return 0, 0, false, gErr
	}
	var stats effectStats
	if uErr := json.Unmarshal(entry.Value, &stats); uErr != nil {
		return 0, 0, false, fmt.Errorf("weaver: unmarshal effect stats %s: %w", effectKey(targetID, gapColumn, actionRef), uErr)
	}
	if len(stats.Window) == 0 {
		return 0, 0, false, nil
	}
	closed := 0
	for _, w := range stats.Window {
		if w {
			closed++
		}
	}
	return float64(closed) / float64(len(stats.Window)), len(stats.Window), true, nil
}

// effectMismatch names one (target, gapColumn, actionRef) confidence window
// whose last effectWindowSize dispatch episodes recorded ZERO observed
// closes — the heartbeat-cadence signal for "dispatches commit but closes
// never arrive" (design §3.4): a package's declared remediation keeps firing
// but the lens gap it targets never flips, loudly a lens/effect mismatch (a
// stale/wrong guard, a lens projecting the wrong column, or a remediation that
// silently no-ops) rather than a normal in-progress retry chain (a window not
// yet full never alerts).
type effectMismatch struct {
	TargetID  string
	GapColumn string
	ActionRef string
}

// scanEffectMismatches enumerates every `__effect` confidence window in the
// bucket (heartbeat cadence, never per-message — mirrors countInFlight) and
// reports every one whose window has reached effectWindowSize dispatches with
// zero recorded closes. An unparseable key or value is skipped (the sweep's
// corrupt-key leg owns that cleanup); this scan is read-only.
func (m *markStore) scanEffectMismatches(ctx context.Context) ([]effectMismatch, error) {
	keys, err := m.conn.KVListKeys(ctx, m.bucket)
	if err != nil {
		return nil, err
	}
	var effectKeys []string
	for _, key := range keys {
		if _, _, _, ok := splitEffectKey(key); ok {
			effectKeys = append(effectKeys, key)
		}
	}
	entries, err := m.conn.KVGetMulti(ctx, m.bucket, effectKeys)
	if err != nil {
		return nil, err
	}
	var out []effectMismatch
	for _, key := range effectKeys {
		targetID, gapColumn, actionRef, ok := splitEffectKey(key)
		if !ok {
			continue
		}
		entry, present := entries[key]
		if !present {
			continue
		}
		var stats effectStats
		if err := json.Unmarshal(entry.Value, &stats); err != nil {
			continue
		}
		if len(stats.Window) < effectWindowSize {
			continue
		}
		closed := 0
		for _, w := range stats.Window {
			if w {
				closed++
			}
		}
		if closed == 0 {
			out = append(out, effectMismatch{TargetID: targetID, GapColumn: gapColumn, ActionRef: actionRef})
		}
	}
	return out, nil
}

// controlKeySuffix names the reserved per-target dispatch-skip marker
// : `<targetId>.__control`. The marker is matched by suffix
// (seedDisabledTargets, the reconciler sweep), so the collision guard is the
// LAST key segment, not the entityId: a real mark's last segment is a
// `missing_*` gap column (validateTarget forces it), and "__control" does not
// start with "missing_". Combined with targetId being a single dot-free token,
// a 2-segment `<targetId>.__control` key can never equal a 3-segment
// `<targetId>.<entityId>.<gapColumn>` mark key.
const controlKeySuffix = ".__control"

// controlMark is the JSON body of the `<targetId>.__control` dispatch-skip
// marker.
type controlMark struct {
	Disabled   bool   `json:"disabled"`
	DisabledAt string `json:"disabledAt,omitempty"`
}

// controlKey builds the reserved per-target dispatch-skip marker key.
func controlKey(targetID string) string {
	return targetID + controlKeySuffix
}

// setDisabled writes or clears the `<targetId>.__control` dispatch-skip
// marker. disabled=true CAS-free-writes `{"disabled":true,
// "disabledAt":<now>}`; disabled=false deletes the key (missing-key-is-success,
// mirroring delete's missing-key posture — enable/resume on an already-enabled
// target is idempotent).
func (m *markStore) setDisabled(ctx context.Context, targetID string, disabled bool) error {
	if !disabled {
		err := m.conn.KVDelete(ctx, m.bucket, controlKey(targetID))
		if err != nil && !errors.Is(err, substrate.ErrKeyNotFound) {
			return err
		}
		return nil
	}
	body, err := json.Marshal(controlMark{Disabled: true, DisabledAt: substrate.FormatTimestamp(time.Now())})
	if err != nil {
		return fmt.Errorf("weaver: marshal control mark: %w", err)
	}
	if _, err := m.conn.KVPut(ctx, m.bucket, controlKey(targetID), body); err != nil {
		return err
	}
	return nil
}

// isDisabled reads the `<targetId>.__control` dispatch-skip marker. A
// missing key means active (not disabled) — never an error.
func (m *markStore) isDisabled(ctx context.Context, targetID string) (bool, error) {
	return m.isDisabledKey(ctx, controlKey(targetID))
}

// isDisabledKey reads the disabled flag from an already-known `__control` key
// (the key seedDisabledTargets already listed) — one KV read, no rebuild of a
// key it just parsed off the listing. A missing key means active (not
// disabled) — never an error.
func (m *markStore) isDisabledKey(ctx context.Context, key string) (bool, error) {
	entry, err := m.conn.KVGet(ctx, m.bucket, key)
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return false, nil
		}
		return false, err
	}
	var cm controlMark
	if err := json.Unmarshal(entry.Value, &cm); err != nil {
		return false, fmt.Errorf("weaver: unmarshal control mark %s: %w", entry.Key, err)
	}
	return cm.Disabled, nil
}

// deleteEffectWindows deletes every `<targetId>.__effect.<gapColumn>.<actionRef>`
// confidence window belonging to targetID and returns how many were removed.
// The reserved marker pins the boundary exactly — targetID is an
// install-validated dot-free token, so "t1.__effect." can never match a key
// under target "t10" — and nothing outside that shape is touched: marks,
// `…__count` retry budgets, and the `__control` marker all survive, which is
// what separates this from deleteByTargetPrefix.
//
// Each delete is conditioned on the revision read in this pass (mirroring the
// sweep's deleteEffect): a dispatch or close that lands between the read and
// the delete wins the conflict and survives as honest new history, so the
// count can under-report and the drain is never destructive to fresh state. A
// key that vanishes mid-scan (the sweep's orphan leg won the race) is already
// in the desired state and is skipped, not an error.
func (m *markStore) deleteEffectWindows(ctx context.Context, targetID string) (deleted int, err error) {
	keys, err := m.conn.KVListKeys(ctx, m.bucket)
	if err != nil {
		return 0, err
	}
	prefix := targetID + effectKeyMarker
	var windowKeys []string
	for _, key := range keys {
		if strings.HasPrefix(key, prefix) {
			windowKeys = append(windowKeys, key)
		}
	}
	entries, err := m.conn.KVGetMulti(ctx, m.bucket, windowKeys)
	if err != nil {
		return 0, fmt.Errorf("weaver: read effect windows for %s: %w", targetID, err)
	}
	for _, key := range windowKeys {
		entry, present := entries[key]
		if !present {
			continue
		}
		if delErr := m.conn.KVDeleteRevision(ctx, m.bucket, key, entry.Revision); delErr != nil {
			if errors.Is(delErr, substrate.ErrRevisionConflict) || errors.Is(delErr, substrate.ErrKeyNotFound) {
				continue
			}
			return deleted, fmt.Errorf("weaver: delete effect window %s: %w", key, delErr)
		}
		deleted++
	}
	return deleted, nil
}

// deleteByTargetPrefix deletes every weaver-state key with prefix
// "<targetID>." — every `<targetId>.<entityId>.<gapColumn>` in-flight mark,
// every `<targetId>.<entityId>.<gapColumn>.__count` retry-budget dispatch-count,
// the `<targetId>.__control` dispatch-skip marker, and every
// `<targetId>.__effect.<gapColumn>.<actionRef>` confidence window (all four
// share the prefix, so all four go). The trailing
// "." in the prefix means "t1." never matches a key under "t10." — no
// accidental cross-target overlap from a shared numeric prefix. Tolerates
// ErrKeyNotFound mid-scan (mirrors the reconciler sweep's scan-tolerance
// posture: a key deleted between the list and the delete is not an error).
func (m *markStore) deleteByTargetPrefix(ctx context.Context, targetID string) (deleted int, err error) {
	keys, err := m.conn.KVListKeys(ctx, m.bucket)
	if err != nil {
		return 0, err
	}
	prefix := targetID + "."
	for _, key := range keys {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		if delErr := m.conn.KVDelete(ctx, m.bucket, key); delErr != nil {
			if errors.Is(delErr, substrate.ErrKeyNotFound) {
				continue
			}
			return deleted, delErr
		}
		deleted++
	}
	return deleted, nil
}
