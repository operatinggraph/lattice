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
// What this package does NOT do is enforce anything. Contract #2 §2.5 makes
// the read disposition a client declaration: the Gateway copies contextHint
// from the request body verbatim (internal/gateway/gateway.go), step 3 never
// inspects it, mergeDerivedReads lets the envelope's disposition stand over a
// derived one (internal/processor/derive_reads.go), and step 4 honours what it
// is handed. ClaimIdentity and CompleteCredentialLink are both granted to
// every consumer, so any holder of an ordinary consumer credential can submit
// a hand-rolled envelope that re-opens the oracle for itself. These builders
// close the shipped clients; the envelope surface stays open until the
// Processor can pin a declared key's disposition against a client override,
// which is a Contract #2 §2.5 amendment rather than a code change here.
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
