// Package identityceremony holds the declared read sets the identity claim
// and credential-link ceremonies submit, shared by every client that
// dispatches them: Facet, the LoftSpace app, the `lattice identity` CLI and
// the verify-claim-ceremony harness.
//
// It exists because the declaration is a SECURITY property of these
// ceremonies, not a per-client convenience. Both are NFR-S6 paths: every
// rejection cause — wrong secret, spent secret, no such identity, already
// claimed, credential already bound — must render one indistinguishable
// answer, and the read disposition is what decides whether it does. A client
// that declares the target under `reads` instead of `optionalReads`, or that
// declares a malformed key at all, gets a DIFFERENT wire code back for the
// "no such identity" case, which is an enumeration oracle over the identity
// keyspace. Keeping the rule in one place is what stops four dispatchers from
// each having their own answer.
//
// What this package does NOT do is enforce anything itself, and for the two
// ceremonies in the NFR-S6 set it does not need to. Contract #2 §2.5 makes the
// read disposition a client declaration — the Gateway copies contextHint from
// the request body verbatim (internal/gateway/gateway.go), step 3 never
// inspects it, and mergeDerivedReads lets the envelope's disposition stand over
// a derived one (internal/processor/derive_reads.go) — but for ClaimIdentity
// and CompleteCredentialLink the Processor holds a CLOSED declared set at the
// head of step 4: an envelope naming a key the operation's own op-meta
// descriptor does not name, or naming any egressReads key or any enumeration,
// is refused before hydration
// (internal/processor/descriptor_floor.go, refuseUndeclaredContextHint). A
// hand-rolled envelope from an ordinary consumer credential therefore cannot
// re-open the oracle for itself, and cannot pad the work inside the rejection
// quantum either.
//
// What the builders below owe that mechanism is the other half: each emits
// EXACTLY the template set its op's descriptor declares
// (packages/identity-domain/opmetas.go), which is what keeps the shipped
// clients on the admitted side of a rule that refuses everything else. A
// builder that grew a key the descriptor does not carry would fail every
// submission through it, so that correspondence is asserted key for key in this
// package's own tests.
//
// InitiateCredentialLink is outside the NFR-S6 set and carries no descriptor,
// so its envelope surface is the ordinary open one and its builder is the only
// thing holding its disposition.
package identityceremony

import (
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// identityVertexType is the Contract #1 vertex type both ceremonies' targets
// must be.
const identityVertexType = "identity"

// ClaimContextHint builds ClaimIdentity's declared read set for a
// caller-supplied target, mirroring the op's own Dispatch spec
// (packages/identity-domain/opmetas.go). Returns nil when targetKey is not a
// well-formed identity vertex key.
//
// optionalReads, never reads: a target that does not exist is one of the
// claim's own adjudicated outcomes (`fail_claim("no-target")`). Declared as a
// required read, its absence is recorded required-absent and the script's
// first touch of it faults HydrationMiss, which reaches the caller as
// HydrationFailed carrying the probed key in details.missingKey — a different
// code AND the key itself, one guess at a time.
//
// A target that fails the Contract #1 grammar is declared NOT AT ALL, because
// the Processor rejects a malformed declared key with an InvalidReadKey
// hydration fault raised before the script runs
// (internal/processor/step4_hydrate.go). Undeclared, the script reaches its
// own `no-target` branch and the SERVER renders the ordinary generic
// rejection — the same code path every other cause takes, rather than a
// client-side imitation of it. This mirrors what the DDL's own derive_reads
// already does with `is_identity_vertex_key` for the keys it derives.
func ClaimContextHint(targetKey string) *processor.ContextHint {
	if !substrate.IsVertexKeyOfType(targetKey, identityVertexType) {
		return nil
	}
	return &processor.ContextHint{
		OptionalReads: []string{
			targetKey,
			targetKey + ".state",
			targetKey + ".claimKey",
		},
	}
}

// CompleteCredentialLinkContextHint builds CompleteCredentialLink's declared
// read set for a caller-supplied target. Returns nil when targetKey is not a
// well-formed identity vertex key.
//
// The same dispositions as ClaimContextHint, for the same reason and with more
// force. CompleteCredentialLink's `fail_link` reuses the "ClaimKeyInvalid: "
// prefix verbatim (packages/identity-domain/ddls.go) precisely so NFR-S6's
// generic rejection covers this ceremony too, and its scope=self gate binds
// authContext.target to the RAW NEW CREDENTIAL, not to payload.targetIdentityKey
// — so unlike the claim, nothing upstream of the script constrains which
// identity a submitter names.
//
// The credentialindex dedup probe is deliberately absent: it is a class-(g)
// key the DDL's own derive_reads computes from the actor (Contract #2 §2.5),
// and no submitter can hash it.
func CompleteCredentialLinkContextHint(targetKey string) *processor.ContextHint {
	if !substrate.IsVertexKeyOfType(targetKey, identityVertexType) {
		return nil
	}
	return &processor.ContextHint{
		OptionalReads: []string{
			targetKey,
			targetKey + ".state",
			targetKey + ".linkKey",
			targetKey + ".credentialBinding",
		},
	}
}

// InitiateCredentialLinkContextHint builds InitiateCredentialLink's declared
// read set. Returns nil when identityKey is not a well-formed identity vertex
// key.
//
// This ceremony's subject is op.actor itself — there is no payload target and
// step 3's scope=self gate has already established that the caller IS that
// identity — so no cross-identity probe exists here and this is not the
// oracle-bearing leg. optionalReads all the same: the script adjudicates an
// absent or malformed subject with a named outcome of its own
// (`IdentityNotFound`), and a required read faults before that outcome can
// render, replacing the ceremony's answer with a hydration wire code.
func InitiateCredentialLinkContextHint(identityKey string) *processor.ContextHint {
	if !substrate.IsVertexKeyOfType(identityKey, identityVertexType) {
		return nil
	}
	return &processor.ContextHint{
		OptionalReads: []string{identityKey, identityKey + ".state"},
	}
}
