package loom

import (
	"context"
	"testing"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// TestFingerprintOf_DiffersOnRecreateRelevantFields proves the reconcile diff
// comparator (specFingerprint, fed by fingerprintOf) detects every field whose
// change requires a durable recreate (Reset): Stream, FilterSubject,
// DeliverPolicy, DeliverGroup. Two specs identical in these fields fingerprint
// equal even if their hooks (Handler/Logger) differ — hooks are refreshed via
// UpdateSpec without a recreate.
func TestFingerprintOf_DiffersOnRecreateRelevantFields(t *testing.T) {
	t.Parallel()
	base := substrate.ConsumerSpec{
		Name:          "loom-widget",
		Stream:        "core-events",
		FilterSubject: "events.widget.>",
		DeliverPolicy: substrate.DeliverAll,
		DeliverGroup:  "",
	}

	t.Run("identical specs fingerprint equal", func(t *testing.T) {
		other := base
		if fingerprintOf(base) != fingerprintOf(other) {
			t.Fatalf("identical specs produced different fingerprints: %+v vs %+v", fingerprintOf(base), fingerprintOf(other))
		}
	})

	t.Run("differing FilterSubject fingerprints differently", func(t *testing.T) {
		other := base
		other.FilterSubject = "events.gadget.>"
		if fingerprintOf(base) == fingerprintOf(other) {
			t.Fatalf("specs differing in FilterSubject produced equal fingerprints")
		}
	})

	t.Run("differing Stream fingerprints differently", func(t *testing.T) {
		other := base
		other.Stream = "KV_loom-state"
		if fingerprintOf(base) == fingerprintOf(other) {
			t.Fatalf("specs differing in Stream produced equal fingerprints")
		}
	})

	t.Run("differing DeliverPolicy fingerprints differently", func(t *testing.T) {
		other := base
		other.DeliverPolicy = substrate.DeliverLastPerSubject
		if fingerprintOf(base) == fingerprintOf(other) {
			t.Fatalf("specs differing in DeliverPolicy produced equal fingerprints")
		}
	})

	t.Run("differing DeliverGroup fingerprints differently", func(t *testing.T) {
		other := base
		other.DeliverGroup = "loom-replicas"
		if fingerprintOf(base) == fingerprintOf(other) {
			t.Fatalf("specs differing in DeliverGroup produced equal fingerprints")
		}
	})

	t.Run("differing hooks alone fingerprint equal", func(t *testing.T) {
		other := base
		other.Handler = supervisedHandler(func(_ context.Context, _ substrate.Message) substrate.Decision { return substrate.Ack })
		if fingerprintOf(base) != fingerprintOf(other) {
			t.Fatalf("specs differing only in Handler produced different fingerprints")
		}
	})
}
