package subjects

import (
	"fmt"
	"strings"
)

// validateToken panics if s is empty or contains NATS-reserved characters
// (`.`, `*`, `>`) or whitespace. Call at the top of every builder function.
func validateToken(name, s string) {
	if s == "" {
		panic(fmt.Sprintf("subjects: %s must not be empty", name))
	}
	if strings.ContainsAny(s, ".*> \t\n\r") {
		panic(fmt.Sprintf("subjects: %s %q contains invalid NATS token character", name, s))
	}
}

// DLQ returns the NATS subject for the Refractor DLQ for the given lensId.
// Team segment removed per Deviation 4 (team is vestigial in the post-morph code).
func DLQ(lensID string) string {
	validateToken("lensID", lensID)
	return fmt.Sprintf("lattice.refractor.dlq.%s", lensID)
}

// Metrics returns the NATS subject for Refractor per-lens consumer lag metrics.
func Metrics(lensID string) string {
	validateToken("lensID", lensID)
	return fmt.Sprintf("lattice.refractor.metrics.%s", lensID)
}

// Audit returns the NATS subject for the Refractor per-lens audit stream.
func Audit(lensID string) string {
	validateToken("lensID", lensID)
	return fmt.Sprintf("lattice.refractor.audit.%s", lensID)
}

func AdjKey(nodeID string) string {
	validateToken("nodeID", nodeID)
	return fmt.Sprintf("adj.%s", nodeID)
}

// PersonalSync returns the NATS subject for a Personal Lens's per-identity
// delta stream (personal-secure-lens-design.md Fire 1): prefix is the lens's
// configured subjectPrefix (a multi-segment convention, e.g.
// "lattice.sync.user" — not itself a single token) and actorID is the
// recipient identity, validated as a single subject token.
func PersonalSync(prefix, actorID string) string {
	if prefix == "" {
		panic("subjects: prefix must not be empty")
	}
	validateToken("actorID", actorID)
	return prefix + "." + actorID
}

// CoreKVStream returns the JetStream stream name for the given NATS KV bucket.
// NATS convention: KV bucket "foo" is backed by stream "KV_foo".
func CoreKVStream(bucket string) string {
	return "KV_" + bucket
}

// CoreKVFilter returns the JetStream filter subject that covers all entries in
// the given NATS KV bucket. Used when creating consumers on the Core KV stream.
func CoreKVFilter(bucket string) string {
	return "$KV." + bucket + ".>"
}
