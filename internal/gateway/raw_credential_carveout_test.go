package gateway

import (
	"sort"
	"testing"

	"github.com/operatinggraph/lattice/internal/processor"
)

// TestRawCredentialCarveOutIsNFRS6Equalized pins the containment invariant
// between two sets that live in two packages and are maintained independently:
//
//	rawCredentialCarveOut ⊆ processor's NFR-S6 equalized set
//
// The carve-out is the set of operations the Gateway submits under the RAW
// authenticated actor rather than the resolved business identity, because their
// scripts hash op.actor to derive a credentialindex key. Hashing the raw
// credential is exactly what makes a rejection informative: the outcome differs
// according to whether that credential is already bound. So every carve-out
// member must also be one whose rejections the Processor collapses to a single
// wire shape.
//
// Containment, not equality, is the sound direction: an operation may be
// equalized without being in the carve-out, and the reverse inclusion is not
// required. That is not the same as equalization being free — membership in
// nfrS6Operations also closes the operation's declared read set at step 4, so
// widening that set without a matching Dispatch descriptor refuses the
// operation outright. See the failure message for what equalizing actually costs.
//
// SCOPE: this pins the Gateway's carve-out, which is where every production
// submitter of these operations goes (cmd/facet, the vertical apps and the edge
// browser agent all reach the Processor through /v1/operations). It does not
// see a binary that submits to core-operations directly — cmd/lattice's admin
// identity tooling does exactly that, and satisfies the invariant only because
// the operations it names happen to be equalized. A new raw-credential ceremony
// added outside the Gateway is not covered here.
func TestRawCredentialCarveOutIsNFRS6Equalized(t *testing.T) {
	var unequalized []string
	for op := range rawCredentialCarveOut {
		if !processor.IsNFRS6Operation(op) {
			unequalized = append(unequalized, op)
		}
	}
	if len(unequalized) == 0 {
		return
	}
	sort.Strings(unequalized)
	for _, op := range unequalized {
		t.Errorf("%q is in the Gateway's rawCredentialCarveOut but is NOT NFR-S6 equalized "+
			"(internal/processor's nfrS6Operations).\n"+
			"That pairing is a live enumeration oracle: the carve-out submits %[1]q under the raw "+
			"authenticated actor, whose script hashes op.actor into a credentialindex key, so the "+
			"operation's rejections depend on whether that credential is already bound — and without "+
			"the NFR-S6 collapse those rejections reach the caller distinguishable, which is a probe "+
			"for who holds an account.\n"+
			"There are two ways out, and the cheap-looking one is not cheap. Taking %[1]q OUT of "+
			"the carve-out, letting resolveActor resolve its actor, is the simple fix wherever it "+
			"applies.\n"+
			"Adding %[1]q to internal/processor's nfrS6Operations is NOT a free conservative "+
			"widening: the same predicate closes the operation's declared read set at step 4 "+
			"(refuseUndeclaredContextHint), and an operation with no Dispatch descriptor admits "+
			"nothing — so it would then refuse EVERY declared key, rejecting 100%% of its "+
			"submissions behind the one reply shape engineered to tell nobody why. Equalizing "+
			"means all three together: add it here, give it an OpDispatchSpec whose read templates "+
			"cover its whole legitimate declared set, and move every dispatcher of it onto exactly "+
			"that set.", op)
	}
}

// TestRawCredentialCarveOutIsNonEmpty keeps the containment assertion above
// honest. Containment over an empty set is vacuously true, so a carve-out
// emptied by a refactor would leave that test green while proving nothing.
func TestRawCredentialCarveOutIsNonEmpty(t *testing.T) {
	if len(rawCredentialCarveOut) == 0 {
		t.Fatal("rawCredentialCarveOut is empty — the containment invariant above then holds " +
			"vacuously and pins nothing; if the carve-out is genuinely gone, retire both tests " +
			"deliberately rather than leaving a test that can no longer fail")
	}
}
