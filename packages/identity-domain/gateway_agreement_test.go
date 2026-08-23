// Agreement tests between internal/gateway's Go index-key derivations and this
// package's Starlark ones (client-ceremony-op-descriptors-design.md §19).
//
// The gateway derives two keys this package also derives: the identityindex key
// for a verified email (GET /v1/actor?probe=1's existence hint) and the
// credentialindex key for an actor (the whoami response's credentialIndexKey).
// Both are genuine second implementations — the package's version is Starlark
// source text, so there is no Go helper for the gateway to call — and the G2
// derived-key annotations on those two sites name these tests as what keeps the
// two in step.
//
// Both tests drive a REAL operation through the REAL pipeline and then look for
// the vertex at the key the GATEWAY computed. That is the whole design: the
// Starlark side is never recomputed in Go here, it is executed, and the raw
// input goes in unnormalized so the assertion depends on normalize_email
// actually trimming and lowercasing. A table that hashed an already-normalized
// string on both sides would prove only that two Go functions agree about a
// string neither one normalized, and would still pass with every Starlark
// normalizer replaced by `return raw`.
package identitydomain_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/gateway"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// TestEmailIdentityIndexKeyAgreesWithIdentityDomain pins
// gateway.EmailIdentityIndexKey against `identity_index_key("email",
// normalize_email(raw))`.
//
// Each vector's RAW email is submitted on a real CreateUnclaimedIdentity; the
// script normalizes it and writes its index vertex; the assertion then reads
// that vertex at the key the gateway derives from the SAME raw string. If
// either side's trimming, lowercasing or "email:" framing changes, the two keys
// part and the read misses.
func TestEmailIdentityIndexKeyAgreesWithIdentityDomain(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"already normalized", "plain@example.com"},
		{"mixed case", "MiXeD.Case@Example.COM"},
		{"leading and trailing whitespace", "  padded@example.com\t"},
		{"whitespace and case together", "\t Both.Sides@EXAMPLE.com  "},
		{"unicode local part", "  Zoë.Ünicode@Example.com "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, conn := setupTestEnv(t)
			durable := "gwagree-" + strings.NewReplacer(" ", "-", "\t", "-").Replace(tc.name)
			cp, cons := newCreatePipeline(t, ctx, conn, durable)

			// The gateway's answer, computed from the raw claim exactly as
			// probeExistingIdentityHint computes it from a verified token.
			wantKey, ok := gateway.EmailIdentityIndexKey(tc.raw)
			if !ok {
				t.Fatalf("gateway.EmailIdentityIndexKey(%q) reported no key", tc.raw)
			}

			payload, err := json.Marshal(map[string]string{
				"name":         "Agreement " + tc.name,
				"email":        tc.raw,
				"claimKeyHash": strings.Repeat("c", 64),
			})
			if err != nil {
				t.Fatalf("marshal payload: %v", err)
			}
			testutil.PublishOp(t, conn, &processor.OperationEnvelope{
				RequestID:     testutil.GenReqID("GwAgreeEmail"),
				Lane:          processor.LaneDefault,
				OperationType: "CreateUnclaimedIdentity",
				Actor:         staffActorKey,
				SubmittedAt:   "2026-08-23T09:00:00Z",
				Class:         "identity",
				Payload:       payload,
			})
			testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

			if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, wantKey); err != nil {
				t.Fatalf("no identityindex vertex at the gateway-derived key %s for raw %q — "+
					"the gateway's Go normalization and identity-domain's normalize_email have diverged: %v",
					wantKey, tc.raw, err)
			}
		})
	}
}

// TestCredentialIndexKeyAgreesWithIdentityDomain pins
// gateway.CredentialIndexKey against `credential_index_key(op.actor)`, in both
// directions:
//
//   - AGREEMENT: a real ClaimIdentity writes the credentialindex vertex for the
//     claiming actor, and it must sit at the key the gateway would report on GET
//     /v1/actor.
//   - NON-NORMALIZATION: neither side may normalize the actor key on the way in.
//     An actor key is already a Contract #1 key, so lowercasing or trimming it
//     is silent corruption, not a cleanup — the opposite of the rule that
//     governs a contact string. Agreement alone does not pin this: two sides
//     that BOTH started lowercasing would still agree with each other while
//     indexing every actor under the wrong key. So the vertex must be absent
//     from the normalized variants of the same key.
func TestCredentialIndexKeyAgreesWithIdentityDomain(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newClaimPipeline(t, ctx, conn, "gwagree-cred")

	identityKey, claimKeyPlaintext := createIdentityAndGetKeys(
		t, ctx, conn, cp, cons, testutil.GenReqID("GwAgreeCredCreate"))

	wantKey := gateway.CredentialIndexKey(consumerActorKey)

	testutil.PublishOp(t, conn, &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("GwAgreeCredClaim"),
		Lane:          processor.LaneDefault,
		OperationType: "ClaimIdentity",
		Actor:         consumerActorKey,
		SubmittedAt:   "2026-08-23T09:01:00Z",
		Class:         "identity",
		Payload: json.RawMessage(`{"claimKey":"` + claimKeyPlaintext +
			`","targetIdentityKey":"` + identityKey + `"}`),
		AuthContext: &processor.AuthContext{Target: consumerActorKey},
		// The target identity's own aspects, which the ceremony reads and no
		// derivation supplies. The credentialindex key under test is
		// deliberately NOT declared: it is exactly what derive_reads
		// contributes, so declaring it would hide the derivation this test
		// exists to check.
		ContextHint: &processor.ContextHint{Reads: []string{
			identityKey,
			identityKey + ".state",
			identityKey + ".claimKey",
		}},
	})
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, wantKey); err != nil {
		t.Fatalf("no credentialindex vertex at the gateway-derived key %s for actor %s — "+
			"gateway.CredentialIndexKey and identity-domain's credential_index_key have diverged: %v",
			wantKey, consumerActorKey, err)
	}

	// The non-normalization half rests entirely on the actor key differing from
	// its own normalized forms. A NanoID from the Contract #1 alphabet
	// (internal/substrate/nanoid.go) carries upper-case characters, but an
	// all-lower-case one is representable — and against such a key every
	// absence assertion below would be asserting the absence of the key that is
	// provably PRESENT, i.e. it would silently invert into a tautology. Fail
	// loudly instead: the fixture actor is the thing to change.
	if strings.ToLower(consumerActorKey) == consumerActorKey {
		t.Fatalf("fixture actor key %s is already all lower-case, so the normalized variants "+
			"below are the same key as the one just asserted PRESENT and prove nothing — "+
			"give the fixture an actor id carrying an upper-case character", consumerActorKey)
	}

	for _, variant := range []struct {
		name  string
		actor string
	}{
		{"lower-cased", strings.ToLower(consumerActorKey)},
		{"trimmed and lower-cased", strings.ToLower(strings.TrimSpace(consumerActorKey))},
	} {
		normalizedKey := gateway.CredentialIndexKey(variant.actor)
		if normalizedKey == wantKey {
			t.Fatalf("%s actor key derives the SAME credentialindex key %s — the derivation "+
				"is not sensitive to the normalization this test exists to forbid",
				variant.name, normalizedKey)
		}
		if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, normalizedKey); err == nil {
			t.Fatalf("a credentialindex vertex exists at %s, derived from the %s actor key %q — "+
				"an actor key is already a Contract #1 key and must be hashed as-is; something "+
				"in the ceremony started normalizing it",
				normalizedKey, variant.name, variant.actor)
		}
	}
}
