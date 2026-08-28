package processor

// nfrS6Operations is the operation set whose rejections NFR-S6 requires to be
// indistinguishable on the wire.
//
// Membership means: EVERY rejection of the operation reached after
// authorization answers ErrCodeClaimKeyInvalid with nil details and one fixed
// message, whatever actually failed. Contract #9
// (docs/contracts/09-identity-claim-flow.md §9.3) states it without
// qualification: "All failure modes collapse to the generic ClaimKeyInvalid
// reply code (NFR-S6 anti-enumeration); specific outcomes surface only via
// Health KV." The real code, message and details go to the Processor log and
// the Health-KV claim-attempts counter — never to the caller.
//
// The set is keyed on operationType and deliberately NOT on the error code the
// failure happened to produce. Keying on the code left two holes. A step-4
// hydrate or decrypt fault on the operation's own declared keys returns a bare
// fmt.Errorf (step4_hydrate.go) and classifies as ErrCodeInternalError, so it
// escaped the collapse entirely — leaving a sealed-but-unclaimed identity
// distinguishable from a non-existent one by wire code. And it made
// CompleteCredentialLink's coverage accidental: that operation qualifies today
// only because its script reuses ClaimIdentity's "ClaimKeyInvalid: " fail prefix
// (identity-domain/ddls.go's fail_link), so narrowing a code-keyed predicate to
// ClaimIdentity alone would have silently uncovered a real enumeration oracle
// without failing a single test.
//
// Membership also closes the operation's declared read set
// (refuseUndeclaredContextHint, descriptor_floor.go): the causes of these
// operations' rejections are equalized over the keys the DESCRIPTOR names, and
// a submitter free to add keys of its own would price work the equalization has
// no subject for.
var nfrS6Operations = map[string]struct{}{
	"ClaimIdentity":          {}, // op-name: (policy) member of the equalized rejection set, which must cover every op the Gateway submits under a raw credential pin=TestRawCredentialCarveOutIsNFRS6Equalized
	"CompleteCredentialLink": {}, // op-name: (policy) member of the equalized rejection set, which must cover every op the Gateway submits under a raw credential pin=TestRawCredentialCarveOutIsNFRS6Equalized
}

// isNFRS6Operation reports whether this operationType's rejections must be
// collapsed to the generic wire shape. See nfrS6Operations for what membership
// means.
func isNFRS6Operation(operationType string) bool {
	_, ok := nfrS6Operations[operationType]
	return ok
}

// IsNFRS6Operation reports whether operationType's rejections are equalized to
// the generic NFR-S6 wire shape.
//
// It is the predicate another package asserts containment against. Membership
// means every rejection of the operation reached after authorization answers
// ErrCodeClaimKeyInvalid with nil details and one fixed message, whatever
// actually failed — so nothing a caller can observe distinguishes one cause
// from another. A component that submits an operation under a RAW credential,
// where the script hashes that credential into an index key, therefore depends
// on this being true of the operation: without the collapse, its rejections
// separate a bound credential from an unbound one, and the pair becomes an
// enumeration oracle.
func IsNFRS6Operation(operationType string) bool {
	return isNFRS6Operation(operationType)
}

// claimRejectionMessage is the single message every NFR-S6 rejection carries.
//
// It names no step, no key and no underlying error, because each of those is
// itself an oracle: the step name alone separates a script refusal from a
// hydrate fault, and a hydrate fault's text quotes the very key the caller was
// probing (step4_hydrate.go wraps as "step4: decrypt <key>: ...").
const claimRejectionMessage = "claim key invalid"

// claimOutcomeInternalFault is the Health-KV claim-attempts outcome recorded
// when an NFR-S6 operation is rejected by a fault the script never reached — a
// hydrate, decrypt, validate or encrypt failure. The caller cannot tell it from
// an ordinary refusal, by construction, so this counter is the only place it
// becomes visible as something an operator should look at.
const claimOutcomeInternalFault = "internal-fault"
