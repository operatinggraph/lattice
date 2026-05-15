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

func Rules(team, ruleID string) string {
	validateToken("team", team)
	validateToken("ruleID", ruleID)
	return fmt.Sprintf("materializer.rules.%s.%s", team, ruleID)
}

func Health(ruleID string) string {
	validateToken("ruleID", ruleID)
	return fmt.Sprintf("materializer.health.%s", ruleID)
}

func DLQ(team, ruleID string) string {
	validateToken("team", team)
	validateToken("ruleID", ruleID)
	return fmt.Sprintf("materializer.dlq.%s.%s", team, ruleID)
}

func Metrics(ruleID string) string {
	validateToken("ruleID", ruleID)
	return fmt.Sprintf("materializer.metrics.%s", ruleID)
}

func Audit(ruleID string) string {
	validateToken("ruleID", ruleID)
	return fmt.Sprintf("materializer.audit.%s", ruleID)
}

func AdjKey(nodeID string) string {
	validateToken("nodeID", nodeID)
	return fmt.Sprintf("adj.%s", nodeID)
}

// Control returns the NATS subject for the Materializer control API service.
// All control operations (validate, rebuild, pause, resume, delete) are sent to this subject.
// The full NATS request-reply service is implemented in Epic 5.
func Control() string {
	return "materializer.control"
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
