package failure

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/refractor/subjects"
)

// DLQMessage is the diagnostic payload written to a rule's DLQ stream.
// All field names are camelCase per architecture.md.
type DLQMessage struct {
	RuleID       string `json:"ruleId"`
	EntityID     string `json:"entityId"`
	FailedStage  string `json:"failedStage"`  // "traversal" | "projection" | "write"
	ErrorClass   string `json:"errorClass"`   // "TRANSIENT" | "TERMINAL"
	ErrorMessage string `json:"errorMessage"`
	RetryCount   int    `json:"retryCount"`
	RuleSequence string `json:"ruleSequence"` // NATS stream sequence of the active rule version; "" if reporter not configured
	Timestamp    string `json:"timestamp"`    // RFC3339 UTC
	RawPayload   string `json:"rawPayload"`   // original NATS message body as string
}

// Publish writes msg to the DLQ stream for the given team and ruleID.
// The stream is created (idempotent) if absent. Subject: subjects.DLQ(team, ruleID).
func Publish(ctx context.Context, js jetstream.JetStream, team, ruleID string, msg DLQMessage) error {
	subject := subjects.DLQ(team, ruleID)
	streamName := "MATERIALIZER_DLQ_" + strings.ToUpper(ruleID)
	_, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     streamName,
		Subjects: []string{subject},
		Storage:  jetstream.FileStorage,
	})
	if err != nil {
		return fmt.Errorf("failure: create DLQ stream: %w", err)
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failure: marshal DLQ message: %w", err)
	}
	_, err = js.Publish(ctx, subject, payload)
	return err
}
