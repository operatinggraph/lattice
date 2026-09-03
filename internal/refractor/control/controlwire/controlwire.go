// Package controlwire holds the Refractor control-plane wire format — the
// request/response payloads of the `lattice.ctrl.refractor.<lensId>.<op>`
// endpoints and the subject that addresses them. It depends on nothing but the
// standard library and internal/refractor/health/healthwire.
//
// It exists because internal/refractor/control bundles the NATS micro service
// that answers these endpoints beside their payloads, so importing a
// ControlRequest links a NATS client. The Edge node is a control-plane *client*
// — it calls register/hydrate/sessionkey and decodes the replies — and a
// browser-hosted engine must not carry a NATS client at all
// (edge-browser-node-design.md §3.2): the transport it is allowed is the
// Gateway door. internal/refractor/control re-exports every name here, so
// server-side call sites read as control vocabulary and do not import this
// package directly.
//
// Client and server share these definitions rather than each declaring their
// own: a re-declared struct is exactly the drift hazard the Edge round-trip
// test (edge-lattice-full-design.md §8.1 RR-4) exists to catch.
package controlwire

import (
	"time"

	"github.com/operatinggraph/lattice/internal/refractor/health/healthwire"
)

// SubjectPrefix is the root of every Refractor control subject.
const SubjectPrefix = "lattice.ctrl.refractor"

// ControlSubject returns the canonical request subject for the given lens ID
// + op.
func ControlSubject(lensID, op string) string {
	return SubjectPrefix + "." + lensID + "." + op
}

// ControlRequest is the JSON payload sent to control endpoints. Op and RuleID
// are now expressed in the request subject (lattice.ctrl.refractor.<lensId>.<op>),
// so on the wire only the operation-specific fields (Truncate) carry
// meaning. The Op and RuleID fields are retained for backwards compatibility
// with tooling that still constructs the legacy single-subject payload — when
// the subject path provides values the subject path wins.
type ControlRequest struct {
	Op       string `json:"op,omitempty"`       // legacy; subject path is authoritative
	RuleID   string `json:"ruleId,omitempty"`   // legacy; subject path is authoritative
	Truncate bool   `json:"truncate,omitempty"` // used by "rebuild" op; default false

	// IdentityID, DeviceID, Types, Anchors are used by the "register"/
	// "deregister" ops (personal-secure-lens-design.md §3.3, Fire PL.2): a
	// Personal Lens device Interest Set registration. Sent on
	// lattice.ctrl.refractor.personal.{register,deregister} — the "personal"
	// subject segment is a fixed pseudo-lensId, not a real lens.
	IdentityID string   `json:"identityId,omitempty"`
	DeviceID   string   `json:"deviceId,omitempty"`
	Types      []string `json:"types,omitempty"`
	Anchors    []string `json:"anchors,omitempty"`

	// AspectScope and TTLSeconds are used by the "sessionkey" op
	// (edge-lattice-full-design.md §3.6, EDGE.4), sent on
	// lattice.ctrl.refractor.personal.sessionkey. AspectScope is carried
	// through to vault.IssueSessionKey for audit/API-shape only (there is one
	// DEK per identity, not one per aspect). TTLSeconds <= 0 lets the Vault
	// backend pick its own default/ceiling.
	AspectScope string `json:"aspectScope,omitempty"`
	TTLSeconds  int64  `json:"ttlSeconds,omitempty"`

	// Cursor is used by the "syncgap" op (edge-syncgap-control-rpc-design.md
	// §3.1): the last SYNC stream sequence the requesting device applied.
	// Serialized without omitempty — 0 (no deltas ever applied) is a
	// legitimate, maximally-conservative value that must reach the server, so
	// the handler answers gapped=true and the device re-hydrates.
	Cursor uint64 `json:"cursor"`

	// ActorKey is used by the "reproject" op
	// (capability-projection-reconciliation-design.md §3.1): the Contract #1
	// vertex key of the actor whose row is reconciled against the graph.
	ActorKey string `json:"actorKey,omitempty"`
}

// ControlResponse is the JSON payload returned by the control service.
// On success (health op): Entry fields are present (promoted from embedded *healthwire.Entry).
// On success (validate op): Validate field is present; Entry fields are absent.
// On success (rebuild op): Rebuild field is present; Entry fields are absent.
// On success (pause op): Pause field is present; Entry fields are absent.
// On success (resume op): Resume field is present; Entry fields are absent.
// On success (delete op): Delete field is present; Entry fields are absent.
// On error: only "error" field is present.
type ControlResponse struct {
	*healthwire.Entry                                        // embedded; nil on non-health ops → fields absent in JSON
	Error                    string                          `json:"error,omitempty"`
	Validate                 *ValidateResult                 `json:"validate,omitempty"`                 // present only for "validate" op
	Rebuild                  *RebuildResult                  `json:"rebuild,omitempty"`                  // present only for "rebuild" op
	Pause                    *PauseResult                    `json:"pause,omitempty"`                    // present only for "pause" op
	Resume                   *ResumeResult                   `json:"resume,omitempty"`                   // present only for "resume" op
	Delete                   *DeleteResult                   `json:"delete,omitempty"`                   // present only for "delete" op
	PersonalRegister         *PersonalRegisterResult         `json:"personalRegister,omitempty"`         // present only for "register" op
	PersonalDeregister       *PersonalDeregisterResult       `json:"personalDeregister,omitempty"`       // present only for "deregister" op
	PersonalHydrate          *PersonalHydrateResult          `json:"personalHydrate,omitempty"`          // present only for "hydrate" op
	PersonalSessionKey       *PersonalSessionKeyResult       `json:"personalSessionKey,omitempty"`       // present only for "sessionkey" op
	PersonalSyncGap          *PersonalSyncGapResult          `json:"personalSyncGap,omitempty"`          // present only for "syncgap" op
	PersonalRequestHydration *PersonalRequestHydrationResult `json:"personalRequestHydration,omitempty"` // present only for "requesthydration" op
	Reproject                *ReprojectResult                `json:"reproject,omitempty"`                // present only for "reproject" op
	PersonalDerivation       *PersonalDerivationStatus       `json:"personalDerivation,omitempty"`       // present on "health" for a Personal Lens
}

// PersonalDerivationStatus is one Personal Lens's narrowing-licence verdict,
// answered live on the "health" op
// (personal-lens-derivation-licence-design.md §4.4).
//
// It rides the control RPC rather than the health KV Entry because the two
// answer different questions and only one of them is per-lens. The entry's
// personalSweepVerdict is PLANE-WIDE: one shared sweeper fans one pass verdict
// onto every personal lens, so it says what the healer achieved and nothing
// about which conjunct is refusing THIS lens. Conjuncts 0-2 are properties of
// the host's wiring, and conjunct 3's "a pass begun after this lens registered"
// clause is per-lens by construction — none of them is visible on a shared
// verdict. An operator who sees a lens on the enumerator needs the conjunct, and
// this is where it is.
//
// It is derived at request time, never stored: every input is either live
// process wiring or the healer's current verdict, so a persisted copy would be
// a snapshot of a question whose whole point is that its answer moves.
type PersonalDerivationStatus struct {
	// Licensed reports whether this lens is currently acting on a derived
	// anchor set rather than on the ActorEnumerator's walk.
	Licensed bool `json:"licensed"`
	// Refusal names the conjunct refusing it, "" when licensed. It is the same
	// string the `anchor derivation cannot act on this lens` log line carries,
	// so an operator reading either finds the same sentence.
	Refusal string `json:"refusal,omitempty"`
}

// ReprojectResult is the synchronous acknowledgement returned by the
// "reproject" op (capability-projection-reconciliation-design.md §3.1).
// Converged reports that the stored row already matched the recomputed one,
// so nothing was written; Wrote reports that a divergence was healed.
// ProjectionSeq is the ordering token the write carried — the pipeline's
// last-applied stream sequence captured before re-evaluation.
type ReprojectResult struct {
	Actor     string `json:"actor"`
	Converged bool   `json:"converged"`
	Deleted   bool   `json:"deleted,omitempty"`
	Wrote     bool   `json:"wrote"`
	// Verdict is the reconciliation's conclusion: "converged", "healed",
	// "blocked" or "unverified". It is carried alongside the booleans rather
	// than derived from them because two of its four values are invisible in
	// that encoding — a blocked and an unverified actor both render as
	// {converged:false, wrote:false}, which reads as "nothing to do".
	Verdict string `json:"verdict"`
	// VerdictReason names the cause behind a blocked or unverified verdict.
	VerdictReason string `json:"verdictReason,omitempty"`
	ProjectionSeq uint64 `json:"projectionSeq"`
}

// RebuildResult is the async acknowledgement returned by the "rebuild" op.
// Started is always true when the op is accepted; the rebuild runs asynchronously.
type RebuildResult struct {
	Started bool `json:"started"`
}

// PauseResult is the synchronous acknowledgement returned by the "pause" op.
// Paused is always true when the op is accepted.
type PauseResult struct {
	Paused bool `json:"paused"`
}

// ResumeResult is the synchronous acknowledgement returned by the "resume" op.
// Resumed is always true when the op is accepted.
type ResumeResult struct {
	Resumed bool `json:"resumed"`
}

// DeleteResult is the synchronous acknowledgement returned by the "delete" op.
// Deleted is always true when the op is accepted.
type DeleteResult struct {
	Deleted bool `json:"deleted"`
}

// PersonalRegisterResult is the synchronous acknowledgement returned by the
// "register" op (personal-secure-lens-design.md §3.3, Fire PL.2).
type PersonalRegisterResult struct {
	Registered bool `json:"registered"`
}

// PersonalDeregisterResult is the synchronous acknowledgement returned by the
// "deregister" op.
type PersonalDeregisterResult struct {
	Deregistered bool `json:"deregistered"`
}

// PersonalHydrateResult is the synchronous acknowledgement returned by the
// "hydrate" op (personal-secure-lens-design.md §3.5, Fire PL.4): the cold
// bulk projection has completed and every row has been published; Revision
// is the high-water mark the requesting device should resume incremental
// delivery from.
type PersonalHydrateResult struct {
	Hydrated bool   `json:"hydrated"`
	Revision uint64 `json:"revision"`
	// Lenses is the set of registered personal-hydrator rule IDs that ran
	// (personal-lens-retraction-design.md §3.4): the client drops any
	// stored key attribution whose lens is not in this set after a
	// completed hydrate, healing a decommissioned/re-minted lens's
	// otherwise-permanently-stranded keys (no emitter is left to retract
	// them any other way).
	Lenses []string `json:"lenses,omitempty"`
	// SyncStartSeq is the SYNC stream's last sequence at the moment the
	// hydrate burst began publishing (edge-cold-signin-delivery-position-
	// design.md §3.2): a cold or gapped Edge node resumes at
	// SyncStartSeq+1 instead of replaying the stream from its retained
	// beginning. Zero means the control host could not determine the
	// position (unset seam or a read error) — the requesting node falls
	// back to today's DeliverAll behaviour.
	SyncStartSeq uint64 `json:"syncStartSeq,omitempty"`
	// SyncEndSeq is the SYNC stream's last sequence on the identity's own
	// subject, read again after the hydrate fan-out has returned
	// (edge-first-paint-gate-identity-design.md §3.1): because
	// substrate.Conn.Publish waits for the JetStream store ack, every message
	// of every lens's burst — rows, keyset frames, markers — has already been
	// appended by the time this second read runs, so SyncEndSeq is always at
	// or above the burst's last sequence. A client uses it as the position
	// its first-paint gate waits for the delivery floor to reach. Zero means
	// the control host could not name a position (older control plane, unset
	// seam, or a read error) — the requesting node falls back to its
	// degraded gate. SyncEndSeq may exceed the burst's true last sequence
	// when unrelated traffic on the same subject races the read (another
	// device's burst, a live delta); that only moves a client's release
	// later, never earlier.
	SyncEndSeq uint64 `json:"syncEndSeq,omitempty"`
}

// PersonalSessionKeyResult is the synchronous acknowledgement returned by the
// "sessionkey" op (edge-lattice-full-design.md §3.6, EDGE.4): a transient
// session key for the requesting identity's own DEK, for the Edge node to
// decrypt ciphertext deltas locally and in-memory. ExpiresAt is a hygiene
// bound the caller enforces locally, not the security boundary — the Vault's
// ShredKey, checked fresh on every "sessionkey" call, is (vault.SessionKey).
type PersonalSessionKeyResult struct {
	Key       []byte    `json:"key"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// PersonalSyncGapResult is the synchronous answer returned by the "syncgap"
// op (edge-syncgap-control-rpc-design.md §3.1): whether SYNC retention has
// pruned messages past the requesting device's cursor
// (edge-lattice-full-design.md §3.2 — a gapped cursor means a durable resume
// would silently skip deltas, so the device must re-hydrate). Deliberately a
// boolean, not the stream's FirstSeq: the gap semantic (cursor < FirstSeq,
// and any future safety margin) stays with the retention owner and can change
// without a wire change; the watermark is extracted, not handed out.
type PersonalSyncGapResult struct {
	Gapped bool `json:"gapped"`
	// HydrationRequested reports an operator-initiated hydration request
	// pending for this device (loupe-flows-edge-depth-ux.md §3.2,
	// personalinterest.RequestHydration) — a bandwidth hint alongside Gapped,
	// not a correctness signal: the client's own gap decision stays
	// authoritative, this only asks it to also re-hydrate when otherwise it
	// would have stayed warm.
	HydrationRequested bool `json:"hydrationRequested,omitempty"`
}

// PersonalRequestHydrationResult is the synchronous acknowledgement returned
// by the "requesthydration" op (loupe-flows-edge-depth-ux.md §3.2): an
// operator has durably marked the target device for hydration on its next
// SYNC attach.
type PersonalRequestHydrationResult struct {
	Requested bool `json:"requested"`
}

// ValidateResult is returned by the "validate" op. It contains a best-effort
// field-presence report based on a sample of current Core KV entries.
type ValidateResult struct {
	SampleSize   int           `json:"sampleSize"`
	FieldReports []FieldReport `json:"fieldReports"`
	Warnings     []string      `json:"warnings,omitempty"` // fields absent from all sampled entries
}

// FieldReport describes the presence of one referenced field in the Core KV sample.
type FieldReport struct {
	Field   string `json:"field"`   // full expression, e.g. "a.id"
	FoundIn int    `json:"foundIn"` // number of sampled entries containing this property
	Present bool   `json:"present"` // true if foundIn > 0
}
