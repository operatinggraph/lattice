package processor

import (
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// LegacyDurable is the pre-per-lane single consumer name. A startup migration
// deletes it (substrate.Conn.DeleteStreamConsumer) so its un-acked messages
// redeliver to the per-lane durables (at-least-once + step-2 dedup make the
// one-time redelivery idempotent).
const LegacyDurable = "processor-main"

// laneOrder is the deterministic lane list. It mirrors Contract #2 §2.3
// (default / urgent / system / meta) and fixes iteration order for stable
// per-lane health output and spec construction.
var laneOrder = []string{"default", "urgent", "system", "meta"}

// laneDurable maps a lane name to its JetStream durable consumer name. Each
// lane gets its own durable bound to the `ops.<lane>` subject so per-lane
// backlog (Contract #5 §5.4 lane_lag) is separable and lanes drain
// independently (priority isolation — urgent never queues behind default).
var laneDurable = map[string]string{
	"default": "processor-default",
	"urgent":  "processor-urgent",
	"system":  "processor-system",
	"meta":    "processor-meta",
}

// LaneDurables returns a fresh lane→durable map for the four operation lanes,
// for wiring the health heartbeater's per-lane backlog reads. A copy is
// returned so callers cannot mutate the package's canonical mapping.
func LaneDurables() map[string]string {
	out := make(map[string]string, len(laneDurable))
	for lane, durable := range laneDurable {
		out[lane] = durable
	}
	return out
}

// LaneConsumerDefaults is the per-lane worker count when no override is set,
// mirroring the architecture config example (lattice-architecture.md): bulk
// `default` and priority `urgent` get the most pumps, `system` a pair, and
// `meta` exactly one (serial by contract). It is the canonical baseline
// LaneConsumers falls back to.
var LaneConsumerDefaults = map[string]int{
	"default": 2,
	"urgent":  4,
	"system":  2,
	"meta":    1,
}

// LaneConsumers resolves the per-lane pump-worker counts from the
// LATTICE_PROCESSOR_LANES_<LANE>_CONSUMERS override convention
// (lattice-architecture.md:568), falling back to LaneConsumerDefaults. getenv is
// the environment accessor (os.Getenv in production; a fake in tests). A value
// that is absent, empty, non-numeric, or below 1 keeps the default for that lane
// (a malformed override never silently disables a lane). The `meta` lane is
// always clamped to 1 regardless of any override — a meta fan-out would break the
// §2.3 serialization guarantee (fail-closed).
func LaneConsumers(getenv func(string) string) map[string]int {
	out := make(map[string]int, len(laneOrder))
	for _, lane := range laneOrder {
		n := LaneConsumerDefaults[lane]
		key := "LATTICE_PROCESSOR_LANES_" + strings.ToUpper(lane) + "_CONSUMERS"
		if v := strings.TrimSpace(getenv(key)); v != "" {
			if parsed, err := strconv.Atoi(v); err == nil && parsed >= 1 {
				n = parsed
			}
		}
		if lane == "meta" {
			n = 1 // fail-closed: meta is serial by contract (§2.3), never fanned out.
		}
		out[lane] = n
	}
	return out
}

// LaneSpecs builds the four per-lane ConsumerSupervisor specs for the Processor
// commit path, one durable per lane bound to its `ops.<lane>` subject. All four
// share the single supervised handler (the commit path is concurrency-correct —
// step-8 OCC + the RWMutex-guarded DDL cache; see the per-lane-consumers design
// §5.2). The `meta` lane is pinned to MaxAckPending=1 (Contract #2 §3.7) so DDL
// mutations are serialized server-side as well as by its single sequential pump.
//
// FilterSubject is the exact two-segment `ops.<lane>` subject every publisher
// emits (submit.go / candidates.go), matching the legacy processor-main filter
// list. consumers maps a lane to its pump-worker count (from LaneConsumers); a
// nil map or a missing/<1 entry means one pump for that lane (Fire-2 parity).
// The `meta` lane is forced to one worker regardless of the map — belt-and-braces
// with LaneConsumers' own clamp — so its serialization can never be widened.
func LaneSpecs(stream string, handler substrate.SupervisedHandler, ackWait time.Duration, consumers map[string]int, logger *slog.Logger) []substrate.ConsumerSpec {
	specs := make([]substrate.ConsumerSpec, 0, len(laneOrder))
	for _, lane := range laneOrder {
		spec := substrate.ConsumerSpec{
			Name:          laneDurable[lane],
			Stream:        stream,
			FilterSubject: "ops." + lane,
			DeliverPolicy: substrate.DeliverAll,
			AckWait:       ackWait,
			Workers:       consumers[lane],
			Handler:       handler,
			Logger:        logger,
		}
		if lane == "meta" {
			// Serial by contract (§2.3 / §3.7): one in-flight DDL mutation at a
			// time, so a meta-vertex commit + its synchronous DDL-cache
			// invalidation never races a second concurrent DDL mutation. Pin both
			// the worker count and MaxAckPending so neither a config override nor a
			// future caller can widen it.
			spec.Workers = 1
			spec.MaxAckPending = 1
		}
		specs = append(specs, spec)
	}
	return specs
}
