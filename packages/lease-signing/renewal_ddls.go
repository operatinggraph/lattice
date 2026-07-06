package leasesigning

import (
	"encoding/json"

	"github.com/asolgan/lattice/internal/pkgmgr"
)

// RenewalDDLs returns the package's renewal-vertex DDL declaration: the
// `renewal` vertex type (design
// loftspace-lease-renewal-goal-authored-target-design.md §4.1/§4.4). One
// `renewal` vertex models one renewal cycle for one leaseapp: root data
// {cycleEnd, status} (status is the sanctioned lifecycle scalar; cycleEnd is a
// second root scalar carrying the leaseEnd this cycle renews, for the lens's
// cycle equality-match — flagged in the design's For-Andrew block, ratified as
// staged). The renewal LINKS to its leaseapp (`renews`); tenant/unit/landlord
// are reached by walking the leaseapp's OWN existing links in the lens, never
// duplicated as renewal-side links.
//
// Five ops share this one DDL (Contract #1's one-vertex-type-one-DDL
// convention, mirroring leaseapp's five ops on one DDL):
//
//   - OpenRenewal — Weaver's service actor (directOp, the SetListingStatus
//     cross-package precedent). CreateOnly: mints the renewal vertex (id =
//     crypto.sha256NanoID("renewal:"+leaseappId+":"+cycleEnd), the
//     identityindex deterministic-id precedent) + the renews link.
//   - SetRenewalTerms — landlord task-grant (+ operator). Writes .terms.
//   - VerifyGuarantor — landlord task-grant (+ operator). Writes
//     .guarantorVerification.
//   - SignRenewal — tenant task-grant. Writes .renewalSignature, flips status to
//     complete, and extends the leaseapp's .tenancy (a second, cross-vertex
//     mutation in the same batch — the CreateAppointment multi-vertex-write
//     precedent).
//   - CancelRenewal — landlord task-less grant (+ operator). Flips status to
//     cancelled.
func RenewalDDLs() []pkgmgr.DDLSpec {
	return []pkgmgr.DDLSpec{renewalDDL()}
}

func renewalDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "renewal",
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"OpenRenewal", "SetRenewalTerms", "VerifyGuarantor", "SignRenewal", "CancelRenewal"},
		Description: "Lease-renewal-cycle DDL. Vertex shape: vtx.renewal.<id>, class=renewal, root data = " +
			"{cycleEnd (the .tenancy leaseEnd this cycle renews, copied at open), status ∈ open|complete|cancelled} " +
			"(D5 — minimal; status is the sanctioned lifecycle scalar, cycleEnd a second root scalar for the lens's " +
			"cycle equality-match). id = crypto.sha256NanoID(\"renewal:\"+leaseappId+\":\"+cycleEnd) — deterministic " +
			"AND grammar-valid (the identityindex precedent), so OpenRenewal is CreateOnly-idempotent per " +
			"(leaseapp, cycle). Link: lnk.renewal.<id>.renews.leaseapp.<leaseappId> (\"renewal renews leaseapp\"; " +
			"the later-arriving renewal is the source, Contract #1 §1.1). Tenant/unit/landlord are reached by " +
			"walking the leaseapp's OWN applicationFor/appliesToUnit/manages links in the lens — the renewal " +
			"carries no links of its own to them. Aspects: .terms {rentAmount, termMonths, setAt} " +
			"(SetRenewalTerms), .guarantorVerification {verifiedAt, method} (VerifyGuarantor), .renewalSignature " +
			"{signedAt} (SignRenewal — named distinctly from leaseapp's .signature aspect so the two ops' " +
			"guard-grammar effect paths never collide, Fire-7 oscillation-ring fix). " +
			"OpenRenewal{leaseApp} is Weaver's service-actor directOp (the SetListingStatus cross-package " +
			"precedent): reads the leaseapp + its .tenancy aspect, derives cycleEnd = tenancy.leaseEnd, computes " +
			"the deterministic renewal id, and CreateOnly-commits the vertex + the renews link — a duplicate fire " +
			"(re-dispatch, redelivery) collides on the vertex create and converges. Rejects UnknownLeaseApplication " +
			"(dead/absent leaseapp) or NoTenancy (no .tenancy aspect yet — the leaseExpiry gap should never have " +
			"opened without one). " +
			"SetRenewalTerms{renewalKey, rentAmount, termMonths} is the landlord's rent-adjustment leg: validates " +
			"rentAmount > 0, termMonths is a whole number (InvalidTermMonths otherwise — a fractional value would " +
			"otherwise be silently truncated by SignRenewal's later add_months call, which casts months to int " +
			"internally), and " +
			"termMonths >= ceil(renewalWindow in months) (a term shorter than the renewal " +
			"window would open the NEXT cycle the instant this one signs — monthly rollover is out of scope), " +
			"then CREATE-OR-UPDATE-writes .terms {rentAmount, termMonths, setAt}. Rejects TermsLocked once " +
			".renewalSignature is present or status != open — terms can never drift under a recorded signature; revision " +
			"before signature rides the operator/trusted-tool model (no in-chain task-revision path — the §10.6 " +
			"task auto-complete consumes the ephemeral grant on first submit). " +
			"VerifyGuarantor{renewalKey, leaseApp, applicant, method?} is the landlord's guarantor-recheck leg: " +
			"verifies leaseApp against the renewal's OWN renews link, verifies applicant against that leaseapp's " +
			"OWN applicationFor link, then reads the verified applicant's .profile aspect. Rejects " +
			"LeaseAppMismatch/ApplicantMismatch on either link-verification failure, and NoGuarantorToVerify " +
			"when the profile's hasGuarantor is not true (nothing to verify). Writes .guarantorVerification " +
			"{verifiedAt, method}. " +
			"SignRenewal{renewalKey, leaseApp, applicant} is the tenant's completion leg — the write-path mirror " +
			"of the planner's signRenewal terminal-leg pre (write-path honesty: the op must not rely on the " +
			"planner for write safety). Rejects RenewalNotOpen unless the renewal's status is still open (guards " +
			"against re-signing an already-complete or cancelled cycle, which would otherwise re-read and " +
			"re-extend an already-extended .tenancy.leaseEnd — the double-extension bug); rejects " +
			"LeaseAppMismatch/ApplicantMismatch on either link-verification failure; rejects NotReadyToSign " +
			"unless .terms is present; rejects GuarantorNotVerified when the " +
			"verified applicant profile says hasGuarantor=true and .guarantorVerification is absent. On success it " +
			"writes .renewalSignature {signedAt}, flips the renewal root status to complete, and — in the SAME batch — " +
			"extends the LEASEAPP's .tenancy: leaseEnd += terms.termMonths (calendar months), renewalOpensAt " +
			"recomputed as leaseEnd - renewalWindow (the CreateAppointment multi-vertex-write precedent). The " +
			"leaseapp key is taken from the LIVE renews link (never trusted from a payload field) — a link-" +
			"verified cross-vertex write, the Withdraw precedent. " +
			"CancelRenewal{renewalKey, reason?} is the landlord's terminal decline: rejects when .renewalSignature is " +
			"present (TermsLocked-style — a signed cycle cannot be cancelled), else flips status to cancelled " +
			"(+ the optional reason). A cancelled cycle counts as \"this cycle's renewal\" in the leaseExpiry " +
			"lens (a recorded decline must not be reopened by the sweep) and is terminal in v1 (no revive op).",
		Script: renewalDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"leaseApp":{"type":"string","description":"vtx.leaseapp.<NanoID> the renewal cycle is for. Required on OpenRenewal (validated alive, must carry a .tenancy aspect); also required on VerifyGuarantor/SignRenewal, where it is verified against the renewal's OWN renews link (LeaseAppMismatch on mismatch) before its .profile is read or its .tenancy is extended."},` +
			`"renewalKey":{"type":"string","description":"vtx.renewal.<NanoID> of the renewal cycle to act on (SetRenewalTerms/VerifyGuarantor/SignRenewal/CancelRenewal; required, validated alive)."},` +
			`"applicant":{"type":"string","description":"vtx.identity.<NanoID> of the leaseApp's applicant. Required on VerifyGuarantor/SignRenewal; verified against the leaseApp's OWN applicationFor link (ApplicantMismatch on mismatch) before the applicant's .profile (hasGuarantor) is trusted — a tampered payload cannot borrow a different applicant's guarantor state."},` +
			`"rentAmount":{"type":"number","description":"The adjusted monthly rent for the renewed term (SetRenewalTerms; required, > 0)."},` +
			`"termMonths":{"type":"integer","description":"The renewed lease term in months (SetRenewalTerms; required, whole number, >= ceil(renewalWindow in months))."},` +
			`"method":{"type":"string","description":"Free-text method/note for how the guarantor was re-verified, e.g. \"phone\" or \"updated pay stub\" (VerifyGuarantor; optional)."},` +
			`"reason":{"type":"string","description":"Optional free-text rationale for a CancelRenewal decline (CancelRenewal; optional)."}},` +
			`"required":[]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.renewal.<NanoID> of the created or acted-on renewal cycle (the operation's principal key)."}}}`,
		FieldDescription: map[string]string{
			"leaseApp":   "Full vtx.leaseapp.<NanoID> key of the application whose renewal cycle is opening (OpenRenewal) — validated alive and required to carry a .tenancy aspect (NoTenancy otherwise). Also required on VerifyGuarantor/SignRenewal, where it is verified against the renewal's OWN renews link (LeaseAppMismatch on mismatch) rather than trusted bare from the payload. The caller lists it, and its .tenancy aspect, in ContextHint.Reads.",
			"renewalKey": "Full vtx.renewal.<NanoID> key of the renewal cycle. SetRenewalTerms/VerifyGuarantor/SignRenewal/CancelRenewal all validate it is alive. The caller lists it (and, for VerifyGuarantor/SignRenewal, its renews link target) in ContextHint.Reads.",
			"applicant":  "Full vtx.identity.<NanoID> key of the leaseApp's applicant. Required on VerifyGuarantor/SignRenewal — verified against the leaseApp's OWN applicationFor link (ApplicantMismatch on mismatch) before that applicant's .profile aspect (hasGuarantor) is read, so a caller cannot substitute a different applicant to spoof or dodge the guarantor check.",
			"rentAmount": "The adjusted monthly rent for the renewed term (SetRenewalTerms; required, > 0). Stored on the .terms aspect.",
			"termMonths": "The renewed lease term in months (SetRenewalTerms; required, whole number >= ceil(renewalWindow in months) — a shorter term would open the next renewal cycle immediately upon signing; a fractional value is rejected rather than silently truncated). Stored on the .terms aspect; SignRenewal later adds this value to the leaseapp's .tenancy.leaseEnd.",
			"method":     "Free-text note on how the guarantor was re-verified (VerifyGuarantor; optional), e.g. a phone call or an updated pay stub. Stored on the .guarantorVerification aspect for the audit trail; not read by any gap predicate.",
			"reason":     "Optional free-text rationale for a landlord's CancelRenewal decline. Stored on the renewal root alongside status=cancelled.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "OpenRenewal — Weaver opens a renewal cycle as a leaseapp nears its renewalOpensAt horizon",
				Payload: map[string]any{"leaseApp": "vtx.leaseapp.<NanoID>"},
				ExpectedOutcome: "Validates the leaseapp is alive and carries a .tenancy aspect. Derives cycleEnd = " +
					"tenancy.leaseEnd and the deterministic id = crypto.sha256NanoID(\"renewal:\"+leaseappId+\":\"+cycleEnd). " +
					"CreateOnly-commits vtx.renewal.<id> (root data {cycleEnd, status: open}) + the renews link " +
					"(renewal→leaseapp). Returns primaryKey. A duplicate fire for the same (leaseapp, cycle) collides on " +
					"the vertex create and converges (no duplicate cycle). Rejects UnknownLeaseApplication or NoTenancy.",
			},
			{
				Name:    "SetRenewalTerms — landlord sets the renewed rent + term",
				Payload: map[string]any{"renewalKey": "vtx.renewal.<NanoID>", "rentAmount": 2500, "termMonths": 12},
				ExpectedOutcome: "Validates the renewal is alive, rentAmount > 0, termMonths is a whole number, and " +
					"termMonths meets the renewalWindow floor. Writes/updates .terms {rentAmount, termMonths, " +
					"setAt: <op.submittedAt, canonical UTC>}. Returns primaryKey. Rejects TermsLocked once the cycle is " +
					"signed or not open, InvalidArgument on a non-positive rentAmount or a too-short termMonths, and " +
					"InvalidTermMonths on a fractional termMonths (e.g. 2.5) rather than silently truncating it.",
			},
			{
				Name: "VerifyGuarantor — landlord re-verifies the tenant's guarantor",
				Payload: map[string]any{"renewalKey": "vtx.renewal.<NanoID>", "leaseApp": "vtx.leaseapp.<NanoID>",
					"applicant": "vtx.identity.<NanoID>", "method": "updated pay stub"},
				ExpectedOutcome: "Verifies leaseApp against the renewal's OWN renews link and applicant against that " +
					"leaseapp's OWN applicationFor link (both link-verified, never trusted bare from the payload), then " +
					"reads the verified applicant's .profile aspect. Rejects NoGuarantorToVerify if the profile's " +
					"hasGuarantor is not true. Writes .guarantorVerification {verifiedAt, method}. Returns primaryKey.",
			},
			{
				Name: "SignRenewal — tenant signs, completing the cycle and extending the lease",
				Payload: map[string]any{"renewalKey": "vtx.renewal.<NanoID>", "leaseApp": "vtx.leaseapp.<NanoID>",
					"applicant": "vtx.identity.<NanoID>"},
				ExpectedOutcome: "Rejects RenewalNotOpen if the cycle is not open (already signed, completed, or " +
					"cancelled); rejects NotReadyToSign unless .terms is present; rejects GuarantorNotVerified when the " +
					"verified profile says hasGuarantor=true and .guarantorVerification is absent. On success, writes " +
					".renewalSignature {signedAt}, sets the renewal root status=complete, and — verifying leaseApp against " +
					"the LIVE renews link and applicant against that leaseapp's LIVE applicationFor link — extends that " +
					"leaseapp's .tenancy: leaseEnd += terms.termMonths (calendar months), renewalOpensAt recomputed. Both " +
					"writes commit in the SAME batch. Returns primaryKey.",
			},
			{
				Name:    "CancelRenewal — landlord declines to renew",
				Payload: map[string]any{"renewalKey": "vtx.renewal.<NanoID>", "reason": "Selling the property."},
				ExpectedOutcome: "Rejects TermsLocked-style if the cycle is already signed. Otherwise sets the renewal " +
					"root status=cancelled (+ reason if supplied). Returns primaryKey. A cancelled cycle is terminal in " +
					"v1 (no revive op) and counts as this cycle's disposition in the leaseExpiry lens, so the sweep does " +
					"not reopen it.",
			},
		},
		Effects: map[string][]json.RawMessage{
			// SignRenewal unconditionally writes the .renewalSignature aspect
			// on commit — the fact the renewalComplete goal's terminal-leg
			// action declares as its effect (renewal_targets.go). The aspect
			// is named .renewalSignature (not .signature) specifically so its
			// guard-grammar path is structurally distinct from leaseapp's
			// SignLease op, which declares "signature.data.signedAt" as ITS
			// effect (ddls.go) — the oscillation ring
			// (internal/weaver/oscillation.go) keys purely on the bare
			// {Aspect, Field} path with no vertex-type/operationType
			// component, so two different aspect names are what keeps the two
			// structurally-unrelated targets (leaseApplicationComplete /
			// renewalComplete) from ever being cross-attributed into a false
			// alternating pattern under ordinary concurrent traffic.
			"SignRenewal": {json.RawMessage(`{"present":"subject.renewalSignature.data.signedAt"}`)},
		},
	}
}
