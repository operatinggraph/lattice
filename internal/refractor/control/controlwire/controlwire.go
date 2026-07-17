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

	"github.com/asolgan/lattice/internal/refractor/health/healthwire"
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
	*healthwire.Entry                            // embedded; nil on non-health ops → fields absent in JSON
	Error              string                    `json:"error,omitempty"`
	Validate           *ValidateResult           `json:"validate,omitempty"`           // present only for "validate" op
	Rebuild            *RebuildResult            `json:"rebuild,omitempty"`            // present only for "rebuild" op
	Pause              *PauseResult              `json:"pause,omitempty"`              // present only for "pause" op
	Resume             *ResumeResult             `json:"resume,omitempty"`             // present only for "resume" op
	Delete             *DeleteResult             `json:"delete,omitempty"`             // present only for "delete" op
	PersonalRegister   *PersonalRegisterResult   `json:"personalRegister,omitempty"`   // present only for "register" op
	PersonalDeregister *PersonalDeregisterResult `json:"personalDeregister,omitempty"` // present only for "deregister" op
	PersonalHydrate    *PersonalHydrateResult    `json:"personalHydrate,omitempty"`    // present only for "hydrate" op
	PersonalSessionKey *PersonalSessionKeyResult `json:"personalSessionKey,omitempty"` // present only for "sessionkey" op
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
