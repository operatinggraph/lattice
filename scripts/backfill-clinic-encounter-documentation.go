//go:build ignore

// backfill-clinic-encounter-documentation.go — one-time repair for
// verticals.md "7 clinical notes are stored as plaintext PHI their own
// provider cannot read back": these appointments predate the sensitivity
// split RecordEncounter now writes on every call (packages/clinic-domain
// ddls.go's RecordEncounter branch) — their `.encounter` aspect holds
// {documentedAt, followUpRequested, summary} in the clear, with no sibling
// `.documentation` aspect. clinicEncountersRead gates on
// `documentation.data.documentedAt <> null`, so every one of these rows
// drops out of every provider's read entirely.
//
// The fix is to re-submit RecordEncounter with the SAME content already on
// each row: the op is an unconditioned upsert (packages/clinic-domain/
// ddls.go, RecordEncounter branch comment) that always writes .encounter
// SENSITIVE-encrypted per the package's current DDL and always writes the
// non-PHI .documentation sibling — no new op needed, mirroring
// seed-showcase.go's own admin-submitted RecordEncounter call. This gap
// cannot recur (every RecordEncounter since the split writes both aspects
// atomically in one batch), so this script, like BackfillPatientRegistration,
// is a one-time manual repair, not a standing auto-remediation loop.
//
// Run via: go run ./scripts/backfill-clinic-encounter-documentation.go
// (needs NATS_URL / NATS_NKEY / BOOTSTRAP_JSON_PATH, same as the seed-*
// scripts — see `make seed-classic-demo`'s recipe in the Makefile).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/operatinggraph/lattice/cmd/lattice/output"
	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/scripts/pkgverify"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	natsURL := pkgverify.EnvOrDefault("NATS_URL", "nats://localhost:4222")
	bootstrapPath := pkgverify.EnvOrDefault("BOOTSTRAP_JSON_PATH", "./lattice.bootstrap.json")

	must(bootstrap.Load(bootstrapPath), "load bootstrap JSON")

	conn, err := output.Connect(ctx, natsURL)
	must(err, "connect to NATS")
	defer conn.Close()

	adminKey := bootstrap.BootstrapIdentityKey

	keys, err := conn.KVListKeysPrefix(ctx, bootstrap.CoreKVBucket, "vtx.appointment.")
	must(err, "list vtx.appointment. keys")
	sort.Strings(keys)

	fixed := 0
	for _, key := range keys {
		if !strings.HasSuffix(key, ".encounter") {
			continue
		}
		apptKey := strings.TrimSuffix(key, ".encounter")
		if !alive(ctx, conn, apptKey) || alive(ctx, conn, apptKey+".documentation") {
			continue
		}

		entry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, key)
		must(err, "get "+key)
		var aspect struct {
			IsDeleted bool                       `json:"isDeleted"`
			Data      map[string]json.RawMessage `json:"data"`
		}
		must(json.Unmarshal(entry.Value, &aspect), "unmarshal "+key)
		if aspect.IsDeleted || len(aspect.Data["ct"]) > 0 {
			// Already encrypted (the split already applied) with no
			// .documentation sibling would be a different, unexpected shape
			// this script isn't built to repair — skip rather than guess.
			continue
		}

		// RecordEncounter's .encounter upsert is UNCONDITIONED (ddls.go): it
		// always rewrites summary/assessment/plan, so silently omitting a
		// field this legacy row actually carried would overwrite it with "".
		// Every key the pre-split write path could ever have set is decoded
		// explicitly; anything else is a shape this script wasn't built to
		// repair, so it refuses rather than flatten unknown PHI.
		known := map[string]bool{"documentedAt": true, "followUpRequested": true,
			"summary": true, "assessment": true, "plan": true, "followUpDate": true}
		for field := range aspect.Data {
			if !known[field] {
				must(fmt.Errorf("unrecognized field %q", field), "unexpected .encounter shape on "+apptKey)
			}
		}
		var summary string
		must(json.Unmarshal(aspect.Data["summary"], &summary), "decode summary on "+apptKey)
		var followUpRequested bool
		if raw, ok := aspect.Data["followUpRequested"]; ok {
			must(json.Unmarshal(raw, &followUpRequested), "decode followUpRequested on "+apptKey)
		}
		payload := map[string]any{
			"appointmentKey":    apptKey,
			"summary":           summary,
			"followUpRequested": followUpRequested,
		}
		for _, field := range []string{"assessment", "plan", "followUpDate"} {
			raw, ok := aspect.Data[field]
			if !ok {
				continue
			}
			var value string
			must(json.Unmarshal(raw, &value), "decode "+field+" on "+apptKey)
			if value != "" {
				payload[field] = value
			}
		}

		submitOp(ctx, conn, adminKey, "RecordEncounter", "appointment", payload,
			&processor.ContextHint{Reads: []string{apptKey}})
		fmt.Printf("==> backfilled documentation: %s\n", apptKey)
		fixed++
	}
	fmt.Printf("==> done: %d appointment(s) backfilled.\n", fixed)
}

func alive(ctx context.Context, conn *substrate.Conn, key string) bool {
	entry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, key)
	if err != nil {
		return false
	}
	var doc struct {
		IsDeleted bool `json:"isDeleted"`
	}
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		return false
	}
	return !doc.IsDeleted
}

func submitOp(ctx context.Context, conn *substrate.Conn, actorKey, operationType, class string, payload map[string]any, hint *processor.ContextHint) *processor.OperationReply {
	reqID, err := substrate.NewNanoID()
	must(err, "generate requestId")
	payloadBytes, err := json.Marshal(payload)
	must(err, "marshal payload")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: operationType,
		Actor:         actorKey,
		Class:         class,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Payload:       payloadBytes,
		ContextHint:   hint,
	}
	reply, err := output.SubmitOp(ctx, conn, env)
	must(err, "submit "+operationType)
	mustAccepted(reply, operationType)
	return reply
}

func mustAccepted(reply *processor.OperationReply, context string) {
	if reply.Status == processor.ReplyStatusAccepted {
		return
	}
	if reply.Error != nil {
		fmt.Fprintf(os.Stderr, "FATAL %s: rejected code=%s message=%s\n", context, reply.Error.Code, reply.Error.Message)
	} else {
		fmt.Fprintf(os.Stderr, "FATAL %s: status=%s (no error detail)\n", context, reply.Status)
	}
	os.Exit(1)
}

func must(err error, context string) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL %s: %v\n", context, err)
		os.Exit(1)
	}
}
