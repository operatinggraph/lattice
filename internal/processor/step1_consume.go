package processor

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// ConsumerConfig bundles the JetStream consumer wiring options.
type ConsumerConfig struct {
	// StreamName is the JetStream stream to consume from. Bootstrap
	// provisions this as `core-operations`.
	StreamName string
	// Durable name. Defaults to `processor-main`.
	Durable string
	// FilterSubjects restricts which subjects this consumer receives.
	// Bootstrap provisions the stream as `ops.>` covering all lanes.
	// Defaults to ops.default, ops.urgent, ops.system, and ops.meta —
	// matching the two-segment form all publishers use. ops.meta covers
	// DDL-mutation operations (CreateMetaVertex, TombstoneMetaVertex,
	// UpdateMetaVertex) alongside standard lane messages.
	FilterSubjects []string
	// MaxAckPending caps in-flight messages. 0 → JetStream default.
	MaxAckPending int
	// AckWait bounds how long the server waits for ack before
	// redelivery. Pulled from the consumer config, defaulting to 30s.
	AckWait time.Duration
}

// applyDefaults fills empty fields with sensible defaults.
func (cc *ConsumerConfig) applyDefaults() {
	if cc.StreamName == "" {
		cc.StreamName = "core-operations"
	}
	if cc.Durable == "" {
		cc.Durable = "processor-main"
	}
	if len(cc.FilterSubjects) == 0 {
		// Two-segment subjects match production publishers exactly:
		// `ops.<lane>` is what submit.go, candidates.go, and all other
		// publishers use. ops.meta carries DDL-mutation operations
		// (CreateMetaVertex, TombstoneMetaVertex, UpdateMetaVertex) and
		// must be included alongside the standard lanes.
		cc.FilterSubjects = []string{"ops.default", "ops.urgent", "ops.system", "ops.meta"}
	}
	if cc.AckWait == 0 {
		cc.AckWait = 30 * time.Second
	}
}

// EnsureConsumer creates (or updates) the durable JetStream consumer used
// by the Processor. Returns the Consumer handle which the commit path
// uses to drive Consume.
func EnsureConsumer(ctx context.Context, js jetstream.JetStream, cc ConsumerConfig, logger *slog.Logger) (jetstream.Consumer, error) {
	cc.applyDefaults()
	cfg := jetstream.ConsumerConfig{
		Durable:        cc.Durable,
		Name:           cc.Durable,
		AckPolicy:      jetstream.AckExplicitPolicy,
		FilterSubjects: cc.FilterSubjects,
		AckWait:        cc.AckWait,
		MaxAckPending:  cc.MaxAckPending,
	}
	cons, err := js.CreateOrUpdateConsumer(ctx, cc.StreamName, cfg)
	if err != nil {
		return nil, fmt.Errorf("processor: create/update consumer %q on stream %q: %w",
			cc.Durable, cc.StreamName, err)
	}
	logger.Info("jetstream consumer ready",
		"stream", cc.StreamName,
		"durable", cc.Durable,
		"filter", cc.FilterSubjects)
	return cons, nil
}

// parseEnvelopeFromMessage runs step 1 on a delivered JetStream message.
// Returns the parsed envelope on success or an error describing why the
// message is malformed (step-1 reject path).
func parseEnvelopeFromMessage(m jetstream.Msg) (*OperationEnvelope, error) {
	if m == nil || len(m.Data()) == 0 {
		return nil, fmt.Errorf("step 1: empty message body")
	}
	return ParseEnvelope(m.Data())
}

// extractRequestIDBestEffort tries to pull a requestId out of a payload
// that failed full envelope validation, for purposes of emitting a
// MalformedOperation health marker keyed by requestId. Falls back to ""
// when no recoverable id is present.
func extractRequestIDBestEffort(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	type onlyID struct {
		RequestID string `json:"requestId"`
	}
	var o onlyID
	if err := jsonUnmarshalLenient(data, &o); err != nil {
		return ""
	}
	return o.RequestID
}
