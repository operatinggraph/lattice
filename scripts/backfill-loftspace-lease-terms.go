//go:build ignore

// backfill-loftspace-lease-terms.go — one-time repair for verticals.md "A
// signed lease with no rent offer is never billed rent": these applications
// were approved before CreateLeaseApplication's unit-listing-rent fallback
// existed (lease-signing 0.31.14) — they carry no .terms aspect at all, so
// leaseRentSettlementSpec (packages/semantic-contracts/lenses.go), gated on
// requestedRent present, never projects a row and the lease never gets a
// ledger account or rent clause, live at 20 Riverside Walk and elsewhere.
//
// The fix is BackfillLeaseTerms (packages/lease-signing/ddls.go +
// scripts.go), an operator-only op that resolves the application's own
// appliesToUnit link and writes {requestedRent: unit.listing.rentAmount}
// onto .terms — this script finds every live, decided leaseapp missing a
// ledger account and submits it once each. This gap cannot recur (every
// CreateLeaseApplication since 0.31.14 already falls back to the unit's
// listed rent), so this is a one-time manual repair, like
// backfill-clinic-encounter-documentation.go.
//
// Run via: go run ./scripts/backfill-loftspace-lease-terms.go
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

	keys, err := conn.KVListKeysPrefix(ctx, bootstrap.CoreKVBucket, "vtx.leaseapp.")
	must(err, "list vtx.leaseapp. keys")
	sort.Strings(keys)

	fixed, skipped := 0, 0
	for _, key := range keys {
		if strings.Count(key, ".") != 2 {
			continue // an aspect/tombstone key, not the leaseapp vertex itself
		}
		if !alive(ctx, conn, key) || !alive(ctx, conn, key+".decision") {
			continue // only a decided (approved or declined) application ever needs a rent clause
		}
		if alive(ctx, conn, key+".ledgerAccount") {
			continue // already converged
		}
		terms, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, key+".terms")
		if err == nil {
			var aspect struct {
				IsDeleted bool                       `json:"isDeleted"`
				Data      map[string]json.RawMessage `json:"data"`
			}
			must(json.Unmarshal(terms.Value, &aspect), "unmarshal "+key+".terms")
			if !aspect.IsDeleted {
				if _, hasRent := aspect.Data["requestedRent"]; hasRent {
					continue // already carries a rent offer — nothing to backfill
				}
			}
		}

		reply, err := trySubmitOp(ctx, conn, adminKey, "BackfillLeaseTerms", "leaseapp",
			map[string]any{"leaseAppKey": key},
			&processor.ContextHint{Reads: []string{key}, OptionalReads: []string{key + ".terms"}})
		if err != nil {
			fmt.Fprintf(os.Stderr, "==> SKIP %s: %v\n", key, err)
			skipped++
			continue
		}
		_ = reply
		fmt.Printf("==> backfilled terms: %s\n", key)
		fixed++
	}
	fmt.Printf("==> done: %d lease application(s) backfilled, %d skipped (see stderr).\n", fixed, skipped)
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

// trySubmitOp submits an operation and reports a processor-level rejection
// (e.g. a domain guard like UnitNoLongerAvailable refusing one specific
// candidate) as an error rather than exiting — the caller decides whether
// that candidate is skippable. A transport/setup failure (can't even reach
// the Processor) still goes through must(), since that means the script
// itself can't proceed, not that this one candidate is bad.
func trySubmitOp(ctx context.Context, conn *substrate.Conn, actorKey, operationType, class string, payload map[string]any, hint *processor.ContextHint) (*processor.OperationReply, error) {
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
	if reply.Status != processor.ReplyStatusAccepted {
		if reply.Error != nil {
			return nil, fmt.Errorf("rejected code=%s message=%s", reply.Error.Code, reply.Error.Message)
		}
		return nil, fmt.Errorf("status=%s (no error detail)", reply.Status)
	}
	return reply, nil
}

func must(err error, context string) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL %s: %v\n", context, err)
		os.Exit(1)
	}
}
