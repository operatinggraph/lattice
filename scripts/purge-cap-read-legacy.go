//go:build ignore

// purge-cap-read-legacy.go — cap-read-per-anchor-grant-keys-design.md §6
// point 4, Fire 3's one-shot legacy-shape purge.
//
// Every cap-read.* NATS-KV producer now emits per-anchor keys (Fire 1 base
// lens + Fire 2 package producers); the legacy aggregate-document shapes
// (`cap-read.<actorType>.<actorID>` and `cap-read.<domain>.<actorType>.
// <actorID>`) drain as the auth-plane sweep re-evaluates each actor and
// guard-tombstones its legacy parent doc (§4.2). This tool closes the tail
// no sweep direction can claim: a legacy doc for an actor who departed
// *before* the flip, and never gets a post-flip evaluation. It bounded-
// prefix-enumerates every "cap-read." key, classifies it by shape (the same
// disjointness test §3.1 argues — a legacy shape's actor-type token position
// holds a vertex-type name, never a NanoID), and guard-tombstones any live
// legacy-shape entry found, at the terminal watermark
// (math.MaxInt64, mirroring keyshredded's admin-write sentinel — an
// authoritative, un-overwritable write outside the pipeline's own seq
// stream). The write path itself is reimplemented here (not called through
// NatsKVAdapter.guardedWrite): that method lives on a pipeline-internal
// adapter instance no standalone script can construct.
//
// Exit 0: the scan found zero LIVE legacy-shape entries left after this run
// (already-tombstoned or freshly tombstoned both count as drained) — the
// design's stated Fire 3 gate. Exit 1: a live legacy entry survived a
// tombstone attempt, or a "cap-read." key matched none of the four known
// shapes (fail loud rather than silently skip an unrecognized layout).
//
// Run via: make purge-cap-read-legacy (== go run ./scripts/purge-cap-read-legacy.go)
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"github.com/operatinggraph/lattice/cmd/lattice/output"
	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// capReadActorType mirrors internal/pkgmgr/anchorwalk.go's unexported
// capReadActorType — the only vertex-type token any cap-read key ever
// carries structurally (every producer hardcodes "identity" as the
// actorSuffix's type). validateGrantDomainName additionally refuses any
// domain literally named "identity" (Fire 2), so the two never collide.
const capReadActorType = "identity"

const maxCASAttempts = 5

// terminalSeq is the admin-write watermark: high enough that no ordinary
// pipeline write (a real JetStream stream sequence) can ever exceed it and
// resurrect a purged legacy doc. Mirrors keyshredded.Manager's math.MaxInt64
// sentinel (internal/refractor/keyshredded/manager.go).
const terminalSeq = uint64(math.MaxInt64)

type shape int

const (
	shapeUnknown shape = iota
	shapeNewBase
	shapeNewDomain
	shapeLegacyBase
	shapeLegacyDomain
)

func (s shape) legacy() bool { return s == shapeLegacyBase || s == shapeLegacyDomain }

// classify parses one "cap-read."-prefixed key into its shape, per §3.1's
// disjointness test: a legacy shape's type-position token is a vertex type
// name (never a NanoID); a new-shape key's actor-id/anchor-id tokens are
// always NanoIDs.
func classify(key string) shape {
	rest, ok := strings.CutPrefix(key, "cap-read.")
	if !ok {
		return shapeUnknown
	}
	parts := strings.Split(rest, ".")
	switch len(parts) {
	case 2: // cap-read.<actorType>.<actorID>
		if parts[0] == capReadActorType && substrate.IsValidNanoID(parts[1]) {
			return shapeLegacyBase
		}
	case 3:
		// cap-read.<actorType>.<actorID>.<anchorID> (new base) vs
		// cap-read.<domain>.<actorType>.<actorID> (legacy domain).
		if parts[0] == capReadActorType && substrate.IsValidNanoID(parts[1]) && substrate.IsValidNanoID(parts[2]) {
			return shapeNewBase
		}
		if parts[1] == capReadActorType && substrate.IsValidNanoID(parts[2]) {
			return shapeLegacyDomain
		}
	case 4: // cap-read.<domain>.<actorType>.<actorID>.<anchorID>
		if parts[1] == capReadActorType && substrate.IsValidNanoID(parts[2]) && substrate.IsValidNanoID(parts[3]) {
			return shapeNewDomain
		}
	}
	return shapeUnknown
}

func isDeleted(value []byte) bool {
	var body struct {
		IsDeleted bool `json:"isDeleted"`
	}
	if err := json.Unmarshal(value, &body); err != nil {
		return false
	}
	return body.IsDeleted
}

// tombstoneIfLive guard-tombstones key at the terminal watermark if it is
// currently live. Reports whether it acted.
func tombstoneIfLive(ctx context.Context, kv *substrate.KV, key string) (bool, error) {
	for attempt := 0; attempt < maxCASAttempts; attempt++ {
		entry, err := kv.Get(ctx, key)
		if err != nil {
			if errors.Is(err, substrate.ErrKeyNotFound) {
				return false, nil
			}
			return false, fmt.Errorf("get %s: %w", key, err)
		}
		if isDeleted(entry.Value) {
			return false, nil
		}
		body := map[string]any{
			"isDeleted":     true,
			"projectedAt":   time.Now().UTC().Format(time.RFC3339),
			"projectionSeq": terminalSeq,
		}
		data, err := json.Marshal(body)
		if err != nil {
			return false, fmt.Errorf("marshal tombstone for %s: %w", key, err)
		}
		if _, err := kv.Update(ctx, key, data, entry.Revision); err != nil {
			if errors.Is(err, substrate.ErrRevisionConflict) {
				continue
			}
			return false, fmt.Errorf("update %s: %w", key, err)
		}
		return true, nil
	}
	return false, fmt.Errorf("%s: revision conflict not resolved after %d attempts", key, maxCASAttempts)
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	natsURL := envOrDefault("NATS_URL", "nats://localhost:4222")

	conn, err := output.Connect(ctx, natsURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: connect to NATS: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	kv, err := conn.OpenKV(ctx, bootstrap.CapabilityKVBucket)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: open %s: %v\n", bootstrap.CapabilityKVBucket, err)
		os.Exit(1)
	}

	keys, err := kv.ListKeysPrefix(ctx, "cap-read.")
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: list cap-read.* keys: %v\n", err)
		os.Exit(1)
	}

	var (
		newCount, tombstonedNow, alreadyClean, unknownCount, failCount int
	)
	for _, key := range keys {
		s := classify(key)
		switch {
		case s == shapeUnknown:
			unknownCount++
			fmt.Printf("UNKNOWN shape, left untouched: %s\n", key)
			continue
		case !s.legacy():
			newCount++
			continue
		}

		acted, err := tombstoneIfLive(ctx, kv, key)
		if err != nil {
			failCount++
			fmt.Printf("FAIL   %s -> %v\n", key, err)
			continue
		}
		if acted {
			tombstonedNow++
			fmt.Printf("PURGED %s\n", key)
		} else {
			alreadyClean++
		}
	}

	fmt.Printf("\nscanned=%d new-shape=%d legacy-already-tombstoned=%d legacy-purged-now=%d unknown=%d errors=%d\n",
		len(keys), newCount, alreadyClean, tombstonedNow, unknownCount, failCount)

	if unknownCount > 0 || failCount > 0 {
		fmt.Println("FAIL: bucket scan did not come back clean — see above")
		os.Exit(1)
	}
	fmt.Println("OK: bucket scan clean — zero live legacy-shape cap-read entries")
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
