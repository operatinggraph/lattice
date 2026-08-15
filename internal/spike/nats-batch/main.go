// Package main is the Story 1.1 spike harness for validating NATS JetStream
// atomic batch behavior at the operation patterns Lattice's Processor commit path requires.
//
// Usage:
//
//	go run ./internal/spike/nats-batch/
//
// See README.md for the written findings and Go/No-Go recommendation.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	natsserver "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

func main() {
	// Start embedded NATS server with JetStream enabled.
	opts := natsserver.DefaultTestOptions
	opts.Port = -1
	opts.JetStream = true

	s := natsserver.RunServer(&opts)
	defer func() {
		if jsCfg := s.JetStreamConfig(); jsCfg != nil {
			defer os.RemoveAll(jsCfg.StoreDir)
		}
		s.Shutdown()
	}()

	// Connect to the embedded server.
	nc, err := nats.Connect(s.ClientURL())
	if err != nil {
		log.Fatalf("FATAL: failed to connect to embedded NATS: %v", err)
	}
	defer nc.Close()

	js, err := jetstream.New(nc)
	if err != nil {
		log.Fatalf("FATAL: jetstream.New: %v", err)
	}

	fmt.Println("=== Story 1.1 — NATS Atomic Batch Spike ===")
	fmt.Printf("NATS server version: %s\n", server.VERSION)
	fmt.Println()

	results := []struct{ name, outcome string }{}

	runTest := func(name string, fn func() string) {
		outcome := fn()
		results = append(results, struct{ name, outcome string }{name, outcome})
		fmt.Printf("[%s] %s\n\n", outcome, name)
	}

	runTest("Test 1: TTL-in-batch", func() string { return testTTLInBatch(nc, js) })
	runTest("Test 2: Revision condition atomicity", func() string { return testRevisionConditionAtomicity(nc, js) })
	runTest("Test 3: Multi-subject batch (single KV bucket)", func() string { return testMultiSubjectBatch(nc, js) })
	runTest("Test 4: TTL marker delivery to KV watcher", func() string { return testTTLMarkerDelivery(nc, js) })

	// Print final summary.
	fmt.Println("=== SUMMARY ===")
	allPass := true
	for _, r := range results {
		prefix := "PASS"
		if !strings.HasPrefix(r.outcome, "PASS") {
			allPass = false
			prefix = "FAIL"
		}
		fmt.Printf("  [%s] %s\n", prefix, r.name)
	}
	fmt.Println()

	if !allPass {
		fmt.Println("One or more tests FAILED. See output above.")
		os.Exit(1)
	}
	fmt.Println("All tests PASSED.")
}

// kvBucketSubject returns the NATS publish subject for a KV key.
// KV publish subjects follow the pattern: $KV.<bucket>.<key>
func kvBucketSubject(bucket, key string) string {
	return fmt.Sprintf("$KV.%s.%s", bucket, key)
}

// pubAckResponse matches the NATS PubAck JSON envelope returned by the server.
type pubAckResponse struct {
	Stream    string  `json:"stream"`
	Sequence  uint64  `json:"seq"`
	Duplicate bool    `json:"duplicate"`
	BatchID   string  `json:"batch,omitempty"`
	BatchSize uint64  `json:"count,omitempty"`
	Error     *apiErr `json:"error,omitempty"`
}

type apiErr struct {
	Code        int    `json:"code"`
	ErrCode     uint16 `json:"err_code"`
	Description string `json:"description"`
}

// publishAtomicBatch publishes a set of messages as a NATS atomic batch.
//
// All messages except the last are published fire-and-forget. The last message
// includes the "Nats-Batch-Commit: 1" header and uses RequestMsg to wait for
// the server's all-or-nothing ack.
//
// The batchID must be unique per batch attempt. Messages must already have their
// Subject set and optional headers (e.g. Nats-Expected-Last-Subject-Sequence,
// Nats-TTL) pre-set before calling this function.
func publishAtomicBatch(nc *nats.Conn, batchID string, messages []*nats.Msg, timeout time.Duration) (*pubAckResponse, error) {
	if len(messages) == 0 {
		return nil, fmt.Errorf("empty batch")
	}

	for i, m := range messages {
		if m.Header == nil {
			m.Header = nats.Header{}
		}
		seq := uint64(i + 1)
		m.Header.Set("Nats-Batch-Id", batchID)
		m.Header.Set("Nats-Batch-Sequence", strconv.FormatUint(seq, 10))

		if i < len(messages)-1 {
			// Not the last message — fire and forget.
			if err := nc.PublishMsg(m); err != nil {
				return nil, fmt.Errorf("publish msg %d: %w", seq, err)
			}
		} else {
			// Last message — set commit flag and wait for the batch ack.
			m.Header.Set("Nats-Batch-Commit", "1")
			resp, err := nc.RequestMsg(m, timeout)
			if err != nil {
				return nil, fmt.Errorf("request commit msg: %w", err)
			}
			var ack pubAckResponse
			if err := json.Unmarshal(resp.Data, &ack); err != nil {
				return nil, fmt.Errorf("unmarshal ack: %w (raw: %s)", err, string(resp.Data))
			}
			return &ack, nil
		}
	}
	return nil, fmt.Errorf("unreachable")
}

// createCoreKVBucket creates a KV bucket configured for Lattice Core KV:
//   - LimitMarkerTTL enabled (required for per-key TTL and watcher expiry events)
//   - AllowAtomicPublish enabled on the underlying stream
func createCoreKVBucket(ctx context.Context, js jetstream.JetStream, bucket string) (jetstream.KeyValue, error) {
	kv, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket: bucket,
		// LimitMarkerTTL enables AllowMsgTTL on the stream and lets the KV watcher
		// see expiry events. Contract #4 §4.3 requires this on Core KV.
		LimitMarkerTTL: 5 * time.Minute,
	})
	if err != nil {
		return nil, fmt.Errorf("create KV bucket %q: %w", bucket, err)
	}

	// Enable atomic publish on the underlying KV stream.
	streamName := fmt.Sprintf("KV_%s", bucket)
	stream, err := js.Stream(ctx, streamName)
	if err != nil {
		return nil, fmt.Errorf("get stream %q: %w", streamName, err)
	}
	cfg := stream.CachedInfo().Config
	cfg.AllowAtomicPublish = true
	if _, err := js.UpdateStream(ctx, cfg); err != nil {
		return nil, fmt.Errorf("update stream %q to enable atomic publish: %w", streamName, err)
	}

	return kv, nil
}

// newMsg creates a NATS message for a KV bucket key with optional headers.
func newMsg(bucket, key string, payload []byte) *nats.Msg {
	m := nats.NewMsg(kvBucketSubject(bucket, key))
	m.Data = payload
	return m
}

// setRevisionCondition sets the Nats-Expected-Last-Subject-Sequence header.
// revision=0 means "key must not exist" (create-if-absent).
func setRevisionCondition(m *nats.Msg, revision uint64) {
	if m.Header == nil {
		m.Header = nats.Header{}
	}
	m.Header.Set("Nats-Expected-Last-Subject-Sequence", strconv.FormatUint(revision, 10))
}

// setTTL sets the Nats-TTL header for per-key TTL on a message.
func setTTL(m *nats.Msg, ttl time.Duration) {
	if m.Header == nil {
		m.Header = nats.Header{}
	}
	m.Header.Set("Nats-TTL", ttl.String())
}
