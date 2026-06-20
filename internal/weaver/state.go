package weaver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/asolgan/lattice/internal/substrate"
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
// instance. ClaimID is declared (omitempty) per the frozen §10.3 value shape;
// it is always empty on a written mark.
type mark struct {
	TargetID       string `json:"targetId"`
	EntityKey      string `json:"entityKey"`
	Gap            string `json:"gap"`
	Action         string `json:"action"`
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
// returns its create revision, the per-dispatch-episode tag the deterministic
// requestId derives from. exists=true means the create lost the race: another
// dispatch of this gap is in flight. The mark carries the §10.3 lease
// (leaseExpiresAt = now + lease, heldBy = this instance) and a NATS per-key
// TTL of markTTLBackstopFactor × lease — the backstop that bounds the mark's
// life even if no reconciler ever sweeps it.
func (m *markStore) create(ctx context.Context, targetID, entityID, gapColumn, entityKey, action string) (revision uint64, exists bool, err error) {
	now := time.Now()
	rec := mark{
		TargetID:       targetID,
		EntityKey:      entityKey,
		Gap:            gapColumn,
		Action:         action,
		ClaimedAt:      substrate.FormatTimestamp(now),
		LeaseExpiresAt: substrate.FormatTimestamp(now.Add(m.lease)),
		HeldBy:         m.instance,
	}
	body, err := json.Marshal(rec)
	if err != nil {
		return 0, false, fmt.Errorf("weaver: marshal mark: %w", err)
	}
	rev, err := m.conn.KVCreateWithTTL(ctx, m.bucket, markKey(targetID, entityID, gapColumn), body,
		markTTLBackstopFactor*m.lease)
	if err != nil {
		if errors.Is(err, substrate.ErrRevisionConflict) {
			return 0, true, nil
		}
		return 0, false, err
	}
	return rev, false, nil
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
func (m *markStore) replace(ctx context.Context, targetID, entityID, gapColumn, entityKey, action string,
	expectedRevision uint64) (revision uint64, conflict bool, err error) {

	now := time.Now()
	rec := mark{
		TargetID:       targetID,
		EntityKey:      entityKey,
		Gap:            gapColumn,
		Action:         action,
		ClaimedAt:      substrate.FormatTimestamp(now),
		LeaseExpiresAt: substrate.FormatTimestamp(now.Add(m.lease)),
		HeldBy:         m.instance,
	}
	body, err := json.Marshal(rec)
	if err != nil {
		return 0, false, fmt.Errorf("weaver: marshal mark: %w", err)
	}
	rev, err := m.conn.KVUpdateWithTTL(ctx, m.bucket, markKey(targetID, entityID, gapColumn), body,
		expectedRevision, markTTLBackstopFactor*m.lease)
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

// countKeySuffix names the reserved dispatch-count key tail:
// `<targetId>.<entityId>.<gapColumn>.__count`. The count is matched (and skipped)
// by suffix wherever marks are enumerated — the reconciler sweep and the
// marksInFlight gauge — because it is NOT a §10.3 mark: it has a 4th segment, so
// splitMarkKey would reject it as corrupt. The "__count" tail can never be a
// gapColumn (singleTokenPattern forbids the dot) nor a NanoID entityId
// (substrate.Alphabet has no underscore), so the count, mark, and `__control`
// key shapes are mutually disjoint.
const countKeySuffix = ".__count"

// dispatchCount is the JSON body of a `<targetId>.<entityId>.<gapColumn>.__count`
// key: the number of actual dispatches of that gap in the current chain.
type dispatchCount struct {
	Count int `json:"count"`
}

// countKey builds the §E dispatch-count key. Entity is keyed by NanoID (the same
// segment shape as the mark key), with the reserved __count tail.
func countKey(targetID, entityID, gapColumn string) string {
	return targetID + "." + entityID + "." + gapColumn + countKeySuffix
}

// dispatchCountCASRetries bounds the read-modify-write retry loop on the count
// (no atomic-increment primitive exists). A handful of attempts absorbs the rare
// concurrent increment (lane-1 vs the sweep's reclaim both firing the same gap);
// beyond that the loser surfaces the conflict to its caller, which already treats
// a count failure as the safe side (do not over-suppress).
const dispatchCountCASRetries = 5

// getDispatchCount reads the current dispatch-count for one gap (0 when absent —
// no dispatch has happened yet, or the count was reset on a gap-close). The read
// is the gate's authority: the budget is spent iff this count has reached the
// row's maxretries_<g>.
func (m *markStore) getDispatchCount(ctx context.Context, targetID, entityID, gapColumn string) (int, error) {
	entry, err := m.conn.KVGet(ctx, m.bucket, countKey(targetID, entityID, gapColumn))
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return 0, nil
		}
		return 0, err
	}
	var dc dispatchCount
	if err := json.Unmarshal(entry.Value, &dc); err != nil {
		return 0, fmt.Errorf("weaver: unmarshal dispatch-count %s: %w", entry.Key, err)
	}
	return dc.Count, nil
}

// incrementDispatchCount bumps the gap's dispatch-count by one (creating it at 1
// on absence) and returns the new value. It is the read-modify-write analogue of
// an atomic increment: a CAS-create on absence, else a revision-conditioned
// update, retried a bounded number of times so a concurrent increment (lane-1 vs
// the sweep's reclaim) does not lose a count. Every write arms the long TTL
// backstop (dispatchCountTTLBackstopFactor × lease) — the count is chain-scoped
// and the gap-close reset is its prompt removal; the TTL only GCs an orphan.
func (m *markStore) incrementDispatchCount(ctx context.Context, targetID, entityID, gapColumn string) (int, error) {
	key := countKey(targetID, entityID, gapColumn)
	ttl := dispatchCountTTLBackstopFactor * m.lease
	for attempt := 0; attempt < dispatchCountCASRetries; attempt++ {
		entry, err := m.conn.KVGet(ctx, m.bucket, key)
		if err != nil {
			if !errors.Is(err, substrate.ErrKeyNotFound) {
				return 0, err
			}
			body, mErr := json.Marshal(dispatchCount{Count: 1})
			if mErr != nil {
				return 0, fmt.Errorf("weaver: marshal dispatch-count: %w", mErr)
			}
			if _, cErr := m.conn.KVCreateWithTTL(ctx, m.bucket, key, body, ttl); cErr != nil {
				if errors.Is(cErr, substrate.ErrRevisionConflict) {
					continue // someone created it first — re-read and update.
				}
				return 0, cErr
			}
			return 1, nil
		}
		var dc dispatchCount
		if uErr := json.Unmarshal(entry.Value, &dc); uErr != nil {
			return 0, fmt.Errorf("weaver: unmarshal dispatch-count %s: %w", entry.Key, uErr)
		}
		next := dc.Count + 1
		body, mErr := json.Marshal(dispatchCount{Count: next})
		if mErr != nil {
			return 0, fmt.Errorf("weaver: marshal dispatch-count: %w", mErr)
		}
		if _, uErr := m.conn.KVUpdateWithTTL(ctx, m.bucket, key, body, entry.Revision, ttl); uErr != nil {
			if errors.Is(uErr, substrate.ErrRevisionConflict) {
				continue // lost the race — re-read and retry.
			}
			return 0, uErr
		}
		return next, nil
	}
	return 0, fmt.Errorf("weaver: dispatch-count %s contended past %d retries", key, dispatchCountCASRetries)
}

// deleteDispatchCount clears one gap's dispatch-count — the §E budget reset, run
// from clearClosedMarks on gap-close (the same level-reconciled path that deletes
// the mark). A missing key is success (idempotent): a closed gap with no prior
// dispatch never had a count.
func (m *markStore) deleteDispatchCount(ctx context.Context, targetID, entityID, gapColumn string) error {
	err := m.conn.KVDelete(ctx, m.bucket, countKey(targetID, entityID, gapColumn))
	if err != nil && !errors.Is(err, substrate.ErrKeyNotFound) {
		return err
	}
	return nil
}

// countInFlight reports how many in-flight marks exist in the bucket, scanned
// on the heartbeat cadence (never per-message). Reserved `<targetId>.__control`
// dispatch-skip markers and `…__count` dispatch-count keys are skipped — neither
// is a §10.3 mark (the same guard the reconciler sweep applies), so the
// marksInFlight gauge counts only real in-flight dispatch.
func (m *markStore) countInFlight(ctx context.Context) (int, error) {
	keys, err := m.conn.KVListKeys(ctx, m.bucket)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, key := range keys {
		if strings.HasSuffix(key, controlKeySuffix) || strings.HasSuffix(key, countKeySuffix) {
			continue
		}
		n++
	}
	return n, nil
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

// deleteByTargetPrefix deletes every weaver-state key with prefix
// "<targetID>." — every `<targetId>.<entityId>.<gapColumn>` in-flight mark AND
// the `<targetId>.__control` dispatch-skip marker. The trailing
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
