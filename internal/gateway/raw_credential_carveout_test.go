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
// Containment, not equality, is the sound direction. Equalizing an operation
// that never sees a raw credential is merely conservative and costs nothing, so
// the reverse inclusion is not required.
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
			"Add %[1]q to internal/processor's nfrS6Operations (equalizing an op costs nothing), or "+
			"take it out of the carve-out and let resolveActor resolve its actor.", op)
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
