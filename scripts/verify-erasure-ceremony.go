//go:build ignore

// verify-erasure-ceremony.go — assertion tool for `make verify-erasure-ceremony`.
//
// The live behavioural half of the erasure spine's verification, and the one
// thing privacy-base's tooling could not do before: `make
// verify-package-privacy-base` asserts the INSTALLED SHAPE of the spine (13
// DDLs, 4 lenses, the weaverTarget, the identityErasure pattern's four ordered
// steps, the grants) against Core KV, but nothing ever RAN the spine. Step 1
// ARMS an irreversible destruction — it writes a revocation flag on the
// envelope, and the privacy-worker's listener then calls Vault.ShredKey off the
// event that commit emits — so exercising it demands a subject minted to be
// destroyed, which is exactly why it had not been done.
//
// This harness mints that subject, gives it something real to erase, drives
// all four steps of the identityErasure spine against it in order as ordinary
// operations over `core-operations`, and asserts each step's observable
// post-state read back from Core KV plus the domain event it emits (correlated
// on the submission's own requestId, so a concurrent erasure on the same stack
// can never be mistaken for this one's).
//
// # The subject, and why it is minted here
//
// Everything the ceremony touches is minted by the run:
//
//	incumbent  — a second unclaimed identity, minted FIRST with the subject's
//	             email, so the subject arrives as a dedup duplicate and carries
//	             a real `duplicateOf` out-edge for step 4 to sweep.
//	subject    — the identity being erased. Fresh NanoID every run, so a second
//	             run needs no cleanup and never re-touches a prior subject.
//	credential — a device identity provisioned as a consumer, which then CLAIMS
//	             the subject. The claim is what gives step 3 real work: it
//	             writes the credentialindex vertex and the boundTo link that
//	             UnbindIdentityCredentials tombstones, and (because
//	             credentialBinding is a sensitive aspect) it mints the subject a
//	             REAL piiKey envelope, so step 1 shreds a live DEK rather than
//	             writing the never-had-a-key placeholder.
//
// The subject therefore reaches step 1 carrying one credential, one
// `indexes` edge (its own name index) and one `duplicateOf` edge — so steps 3
// and 4 are proven on rows they actually destroy, not on the zero-row path.
//
// # What it proves, step by step
//
//	step 1 — ShredIdentityKey: subject.piiKey.data.shredded == true with a
//	  shreddedAt stamp, and wrappedDEK byte-identical to what the setup
//	  captured; privacy.keyShredded emitted.
//	step 2 — SealIdentityForErasure: subject.erasureRequested written, class
//	  `erasureRequested`, requestedAt set, and shreddedAt equal to the
//	  envelope's stamp (the cycle discriminator §6 rests on);
//	  privacy.erasureRequested emitted.
//	step 3 — UnbindIdentityCredentials: the credential's
//	  vtx.credentialindex.<hash> vertex AND the boundTo link tombstoned; one
//	  identity.unbound naming the credential.
//	step 4 — PurgeIdentityDedupFootprint: driven to convergence (it sweeps ONE
//	  relation class per commit by design), asserting the name identityindex
//	  vertex, its `indexes` link and the `duplicateOf` link all tombstoned,
//	  and a final pass reporting purged=0.
//
// # Two boundaries this harness does not cross
//
// It NEVER mints itself a permission or a grant. ShredIdentityKey ships no
// grant from privacy-base on purpose (permissions.go: erasure is a subject's
// request and a deployment's consent decision); the sanctioned way to reach it
// is the separate consent package `packages/privacy-operator-grant`, which the
// Makefile target co-installs. A run against a stack without that package
// fails at step 1 with AuthDenied, which is the correct answer.
//
// And it never writes Core KV. Every state change goes through an operation
// (P2); Core KV is read-only here, which is sanctioned for platform tooling.
//
// # Where the ceremony stops
//
// The four synchronous commits are the whole scope. The erasure's ASYNC half —
// the privacy-worker's Vault.ShredKey and the Refractor's projection nullify,
// each landing a RecordShredFinalization progress record, and the completion
// attestation the identityErasureComplete target writes once both have — runs
// off the events this harness observes but is not asserted here: those are
// separate components on their own cadence, and a harness that waited on them
// would be measuring their liveness rather than the spine's four steps. The
// steps are also submitted directly rather than through the identityErasure
// Loom pattern, so the pattern's guards and step advance are equally untouched.
//
// # Polling
//
// Every wait polls to CONVERGENCE under one generous ceiling and reports the
// elapsed time on success. A fixed low deadline reads real unbounded latency
// as "never happened" — a slow-but-correct stack must pass.
//
// Exit 0: all assertions pass. Exit 1: one or more assertions failed.
//
// Run via: make verify-erasure-ceremony
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/operatinggraph/lattice/cmd/lattice/output"
	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/capabilitykv"
	"github.com/operatinggraph/lattice/internal/identityceremony"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// convergenceCeiling bounds every poll in this harness. It is deliberately far
// larger than any observed latency: what is being waited on (a Refractor lens
// re-projection, an outbox event publication) has no SLA, so a ceiling sized to
// a typical run turns ordinary slowness into a false failure. The ceiling
// exists only so a genuinely stuck stack stops rather than hangs; the elapsed
// time is reported on success, which is the number worth watching.
const convergenceCeiling = 3 * time.Minute

// pollInterval is the gap between condition checks. Nothing here is
// synchronised by elapsed time — every wait re-reads the real condition and
// returns the moment it holds.
const pollInterval = 150 * time.Millisecond

// sweepPasses bounds the PurgeIdentityDedupFootprint convergence loop. The op
// sweeps one relation class per commit (indexes, then duplicateOf out, then
// duplicateOf in), so a subject with a footprint on all three needs four passes
// to reach a purged=0 answer. The bound is generous against that and exists to
// turn a non-converging sweep into a failure instead of a spin.
const sweepPasses = 12

var (
	okCount   int
	failCount int
)

func ok(format string, args ...any) {
	okCount++
	fmt.Printf("OK   "+format+"\n", args...)
}

func fail(format string, args ...any) {
	failCount++
	fmt.Printf("FAIL "+format+"\n", args...)
}

func must(err error, context string) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL %s: %v\n", context, err)
		os.Exit(1)
	}
}

// mustAccepted fatals with the reply's error code/message (not a raw pointer
// dump — Go's fmt does not dereference nested struct-pointer fields) unless
// reply is accepted. Used for the SETUP submissions, whose failure means the
// ceremony never started rather than that the spine is broken.
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

// assertAccepted records a spine step's acceptance as an assertion rather than
// fataling: a rejected step IS the finding this harness exists to surface, and
// the later assertions' failures describe it more precisely than an early exit.
func assertAccepted(reply *processor.OperationReply, label string) bool {
	if reply.Status == processor.ReplyStatusAccepted {
		ok("%s: accepted", label)
		return true
	}
	if reply.Error != nil {
		fail("%s: want accepted, got %s code=%s message=%s", label, reply.Status, reply.Error.Code, reply.Error.Message)
	} else {
		fail("%s: want accepted, got %s (no error detail)", label, reply.Status)
	}
	return false
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	natsURL := envOrDefault("NATS_URL", "nats://localhost:4222")
	bootstrapPath := envOrDefault("BOOTSTRAP_JSON_PATH", "./lattice.bootstrap.json")

	must(bootstrap.Load(bootstrapPath), "load bootstrap JSON")

	conn, err := output.Connect(ctx, natsURL)
	must(err, "connect to NATS")
	defer conn.Close()

	events := watchEvents(conn)
	admin := bootstrap.BootstrapIdentityKey

	runID, err := substrate.NewNanoID()
	must(err, "generate run discriminator")
	// Lower-cased because identity-domain normalizes a contact value before
	// hashing it into its index key (`raw.lower()`), and this harness computes
	// those index keys itself to assert on them. Feeding it a value that is
	// already normalized keeps the two derivations trivially in agreement.
	tag := strings.ToLower(runID)
	sharedEmail := "erasure-ceremony-" + tag + "@dev.lattice.local"

	fmt.Printf("=== erasure ceremony run %s\n", runID)

	// --- Setup: the incumbent, so the subject arrives as a dedup duplicate ---
	// Minted FIRST and with the SAME email, which is what makes the subject's
	// CreateUnclaimedIdentity write a duplicateOf edge — a real second-class
	// row for step 4's sweep, which would otherwise only ever see `indexes`.

	incumbentName := "erasure ceremony incumbent " + tag
	incumbentReply := submitOp(ctx, conn, admin, "CreateUnclaimedIdentity", "identity", map[string]any{
		"name": incumbentName, "email": sharedEmail, "claimKeyHash": sha256Hex("incumbent-" + runID),
	}, nil)
	mustAccepted(incumbentReply.reply, "seed dedup incumbent")
	incumbentKey := incumbentReply.reply.PrimaryKey
	ok("seeded dedup incumbent %s", incumbentKey)

	// --- Setup: the subject. Disposable, minted for this run, erased below. --

	claimSecret := "erasure-ceremony-claim-" + runID
	subjectName := "erasure ceremony subject " + tag
	subjectReply := submitOp(ctx, conn, admin, "CreateUnclaimedIdentity", "identity", map[string]any{
		"name": subjectName, "email": sharedEmail, "claimKeyHash": sha256Hex(claimSecret),
	}, nil)
	mustAccepted(subjectReply.reply, "seed erasure subject")
	subjectKey := subjectReply.reply.PrimaryKey
	ok("minted disposable subject %s (this run's only erasure target)", subjectKey)

	subjectID := vertexID(subjectKey)
	nameIndexKey := identityIndexKey("name", subjectName)
	nameIndexLinkKey := "lnk." + strings.TrimPrefix(nameIndexKey, "vtx.") + ".indexes.identity." + subjectID
	duplicateOfLinkKey := "lnk." + strings.TrimPrefix(subjectKey, "vtx.") + ".duplicateOf." + strings.TrimPrefix(incumbentKey, "vtx.")

	assertLive(ctx, conn, nameIndexKey, "subject's name identityindex vertex")
	assertLive(ctx, conn, nameIndexLinkKey, "subject's indexes link")
	assertLive(ctx, conn, duplicateOfLinkKey, "subject's duplicateOf link to the incumbent")

	// --- Setup: a credential, so step 3 sweeps a real row -------------------
	// The device is provisioned as a consumer exactly as the Gateway's
	// first-touch pre-flight does, then claims the subject as itself. That
	// claim is the only thing in this harness that writes a credentialindex
	// vertex and a boundTo link — the rows step 3 must destroy.

	deviceID, err := substrate.NewNanoID()
	must(err, "generate credential NanoID")
	deviceKey := "vtx.identity." + deviceID
	consumerRoleKey := "vtx.role." + pkgmgr.RoleID("identity-domain", "consumer")
	consumerGrantKey := "lnk.identity." + deviceID + ".holdsRole.role." + vertexID(consumerRoleKey)

	provisionReply := submitOp(ctx, conn, admin, "ProvisionConsumerIdentity", "identity", map[string]any{
		"targetActorKey": deviceKey, "consumerRoleKey": consumerRoleKey,
	}, &processor.ContextHint{
		Reads:         []string{consumerRoleKey},
		OptionalReads: []string{deviceKey, consumerGrantKey},
	})
	mustAccepted(provisionReply.reply, "provision the credential as a consumer")
	ok("provisioned credential %s as a consumer", deviceKey)

	waitForRoleGrant(ctx, conn, deviceKey, "ClaimIdentity")

	claimReply := submitOp(ctx, conn, deviceKey, "ClaimIdentity", "identity", map[string]any{
		"claimKey": claimSecret, "targetIdentityKey": subjectKey,
	}, claimHint(subjectKey), withAuthTarget(deviceKey))
	mustAccepted(claimReply.reply, "bind the credential to the subject")

	credentialIndexKey := credentialIndexKey(deviceKey)
	boundToLinkKey := "lnk.identity." + deviceID + ".boundTo.identity." + subjectID
	assertLive(ctx, conn, credentialIndexKey, "credential's credentialindex vertex")
	assertLive(ctx, conn, boundToLinkKey, "credential's boundTo link to the subject")
	wrappedDEK := captureLiveEnvelope(ctx, conn, subjectKey)

	// --- Step 1: ShredIdentityKey -------------------------------------------

	piiKeyKey := subjectKey + ".piiKey"
	shred := submitOp(ctx, conn, admin, "ShredIdentityKey", "", map[string]any{
		"identityKey": subjectKey,
	}, &processor.ContextHint{
		Reads:         []string{subjectKey},
		OptionalReads: []string{piiKeyKey},
	})
	assertAccepted(shred.reply, "step 1 ShredIdentityKey")

	shreddedAt := ""
	waitFor(ctx, "step 1: "+piiKeyKey+" marked shredded over its intact key material", func() (bool, string) {
		doc, err := readDoc(ctx, conn, piiKeyKey)
		if err != nil {
			return false, err.Error()
		}
		if doc.IsDeleted {
			return false, "envelope is tombstoned"
		}
		if shredded, _ := doc.Data["shredded"].(bool); !shredded {
			return false, fmt.Sprintf("data.shredded = %v", doc.Data["shredded"])
		}
		// The shred flips a flag over the existing envelope; it never rewrites
		// the wrapped key. A changed value means the placeholder branch ran and
		// destroyed live key material that the erasure plane still needs to
		// name what was protected.
		if got, _ := doc.Data["wrappedDEK"].(string); got != wrappedDEK {
			return false, fmt.Sprintf("data.wrappedDEK changed from %d bytes of key material to %q — the shred must flip shredded=true over the existing envelope, never replace it with an empty-wrappedDEK placeholder", len(wrappedDEK), got)
		}
		stamp, _ := doc.Data["shreddedAt"].(string)
		if stamp == "" {
			return false, "data.shreddedAt is empty — the seal would have no cycle discriminator"
		}
		shreddedAt = stamp
		return true, ""
	})
	events.assertEmitted(ctx, shred.requestID, "privacy.keyShredded", map[string]string{"identityKey": subjectKey})

	// Every assertion from here on is stated against the shred's own cycle
	// discriminator, so without one they would compare "" to "" and report
	// green inside a red run. There is nothing to seal either: the seal
	// refuses an identity whose envelope carries no stamp.
	if shreddedAt == "" {
		fail("step 1 recorded no shreddedAt stamp — steps 2 through 4 are not attempted, because every assertion they make is anchored on it")
		report()
	}

	// --- Step 2: SealIdentityForErasure -------------------------------------

	markerKey := subjectKey + ".erasureRequested"
	seal := submitOp(ctx, conn, admin, "SealIdentityForErasure", "", map[string]any{
		"subjectKey": subjectKey,
	}, &processor.ContextHint{
		Reads:         []string{subjectKey},
		OptionalReads: []string{piiKeyKey, markerKey, subjectKey + ".mergedInto"},
	})
	assertAccepted(seal.reply, "step 2 SealIdentityForErasure")

	waitFor(ctx, "step 2: "+markerKey+" written", func() (bool, string) {
		doc, err := readDoc(ctx, conn, markerKey)
		if err != nil {
			return false, err.Error()
		}
		if doc.IsDeleted {
			return false, "marker is tombstoned"
		}
		if doc.Class != "erasureRequested" {
			return false, fmt.Sprintf("class = %q, want erasureRequested — the sweeps check the class, not the key", doc.Class)
		}
		if requestedAt, _ := doc.Data["requestedAt"].(string); requestedAt == "" {
			return false, "data.requestedAt is empty"
		}
		if got, _ := doc.Data["shreddedAt"].(string); got != shreddedAt {
			return false, fmt.Sprintf("data.shreddedAt = %q, want the envelope's %q", got, shreddedAt)
		}
		return true, ""
	})
	events.assertEmitted(ctx, seal.requestID, "privacy.erasureRequested", map[string]string{
		"identityKey": subjectKey, "shreddedAt": shreddedAt,
	})

	// --- Step 3: UnbindIdentityCredentials ----------------------------------

	unbind := submitOp(ctx, conn, admin, "UnbindIdentityCredentials", "", map[string]any{
		"subjectKey": subjectKey,
	}, &processor.ContextHint{
		Reads:         []string{subjectKey},
		OptionalReads: []string{markerKey},
		Enumerations: []processor.EnumerationHint{
			{Hub: subjectKey, Relation: "boundTo", Direction: "in"},
			{Hub: subjectKey, Relation: "boundTo", Direction: "out"},
		},
	})
	assertAccepted(unbind.reply, "step 3 UnbindIdentityCredentials")

	waitForTombstoned(ctx, conn, credentialIndexKey, "step 3: credentialindex vertex")
	waitForTombstoned(ctx, conn, boundToLinkKey, "step 3: boundTo link")
	events.assertEmitted(ctx, unbind.requestID, "identity.unbound", map[string]string{
		"identityKey": subjectKey, "actorKey": deviceKey,
	})

	// --- Step 4: PurgeIdentityDedupFootprint --------------------------------
	// One relation class per commit by design, so the sweep is driven to
	// convergence rather than submitted once: the terminal answer is a pass
	// that reports purged=0, which is the signal the erasure target reads.

	drivePurgeToConvergence(ctx, conn, events, admin, subjectKey, markerKey)
	waitForTombstoned(ctx, conn, nameIndexLinkKey, "step 4: indexes link")
	waitForTombstoned(ctx, conn, nameIndexKey, "step 4: name identityindex vertex")
	waitForTombstoned(ctx, conn, duplicateOfLinkKey, "step 4: duplicateOf link")

	report()
}

// report prints the assertion tally and exits with the status it implies. It
// is the only exit from a run that got as far as making assertions, so a
// ceremony that stops early still tells the reader how far it got.
func report() {
	fmt.Printf("\n%d OK, %d FAIL\n", okCount, failCount)
	if failCount > 0 {
		os.Exit(1)
	}
	os.Exit(0)
}

// drivePurgeToConvergence re-submits PurgeIdentityDedupFootprint until a pass
// reports it purged nothing, mirroring what the identityErasureComplete target
// does with its gap dispatch. Each pass's own privacy.dedupFootprintSwept
// event carries the count, so convergence is read from the op's own report
// rather than inferred from the keyspace.
func drivePurgeToConvergence(ctx context.Context, conn *substrate.Conn, events *eventLog, admin, subjectKey, markerKey string) {
	hint := &processor.ContextHint{
		Reads:         []string{subjectKey},
		OptionalReads: []string{markerKey},
		Enumerations: []processor.EnumerationHint{
			{Hub: subjectKey, Relation: "indexes", Direction: "in"},
			{Hub: subjectKey, Relation: "duplicateOf", Direction: "out"},
			{Hub: subjectKey, Relation: "duplicateOf", Direction: "in"},
		},
	}
	totalPurged := 0
	for pass := 1; pass <= sweepPasses; pass++ {
		purge := submitOp(ctx, conn, admin, "PurgeIdentityDedupFootprint", "", map[string]any{
			"subjectKey": subjectKey,
		}, hint)
		if !assertAccepted(purge.reply, fmt.Sprintf("step 4 PurgeIdentityDedupFootprint (pass %d)", pass)) {
			return
		}
		ev, found := events.await(ctx, purge.requestID, "privacy.dedupFootprintSwept")
		if !found {
			fail("step 4 pass %d: no privacy.dedupFootprintSwept event for requestId %s", pass, purge.requestID)
			return
		}
		purged, present := intField(ev.Payload, "purged")
		if !present {
			fail("step 4 pass %d: privacy.dedupFootprintSwept carries no numeric purged field, so the sweep reports no convergence signal to read", pass)
			return
		}
		relation, _ := ev.Payload["relation"].(string)
		if purged == 0 {
			if relation != "" {
				fail("step 4: converged with purged=0 but relation=%q — a zero-row pass names no relation", relation)
				return
			}
			// The subject was minted with residue on TWO relation classes and
			// the op sweeps one per commit, so reaching zero without ever
			// purging anything means the sweep never saw that residue — the
			// zero-row path wearing the convergence answer.
			if totalPurged == 0 {
				fail("step 4: reported purged=0 on pass %d having swept nothing, but the subject carries an indexes edge and a duplicateOf edge — the sweep did not reach the residue it was pointed at", pass)
				return
			}
			ok("step 4: converged after %d passes, %d links purged in total, final pass reports purged=0", pass, totalPurged)
			return
		}
		totalPurged += purged
		ok("step 4 pass %d: purged %d %s link(s)", pass, purged, relation)
	}
	fail("step 4: PurgeIdentityDedupFootprint did not converge within %d passes", sweepPasses)
}

// submission pairs an operation's reply with the requestId it was submitted
// under, which is what correlates the domain event this harness asserts on to
// THIS submission rather than to a concurrent erasure on the same stack.
type submission struct {
	requestID string
	reply     *processor.OperationReply
}

// envelopeOption adjusts an envelope before submission.
type envelopeOption func(*processor.OperationEnvelope)

// withAuthTarget declares the scope=self auth path (Contract #2 §2.8) — the
// credential naming itself as it claims the subject, which is the disposition
// step 3's auth check requires of ClaimIdentity.
func withAuthTarget(target string) envelopeOption {
	return func(env *processor.OperationEnvelope) {
		env.AuthContext = &processor.AuthContext{Target: target}
	}
}

// submitOp submits an operation as actorKey over NATS and fatals on a
// transport error. A REJECTED reply is returned to the caller: a setup
// submission treats it as fatal, a spine step records it as a failed
// assertion, and those are different things.
func submitOp(ctx context.Context, conn *substrate.Conn, actorKey, operationType, class string, payload map[string]any, hint *processor.ContextHint, opts ...envelopeOption) submission {
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
	for _, opt := range opts {
		opt(env)
	}
	reply, err := output.SubmitOp(ctx, conn, env)
	must(err, "submit "+operationType)
	return submission{requestID: reqID, reply: reply}
}

// claimHint projects identityceremony.ClaimContextHint — the one builder every
// shipped ClaimIdentity dispatcher uses — so this harness sends the read
// disposition the real clients send rather than its own transcription of it.
func claimHint(targetKey string) *processor.ContextHint {
	return identityceremony.ClaimContextHint(targetKey)
}

// coreDoc is the Core KV document envelope, in the three fields this harness
// adjudicates on: the class (which the erasure sweeps gate on, not the key),
// the body, and the tombstone flag.
type coreDoc struct {
	Class     string         `json:"class"`
	Data      map[string]any `json:"data"`
	IsDeleted bool           `json:"isDeleted"`
}

func readDoc(ctx context.Context, conn *substrate.Conn, key string) (coreDoc, error) {
	entry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, key)
	if err != nil {
		return coreDoc{}, err
	}
	var doc coreDoc
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		return coreDoc{}, err
	}
	return doc, nil
}

// waitFor polls cond to convergence and reports the elapsed time on success.
// cond returns (satisfied, why-not); the why-not of the LAST attempt is what a
// timeout reports, so a failure says what the state actually was rather than
// only that it was not what was wanted.
func waitFor(ctx context.Context, label string, cond func() (bool, string)) bool {
	start := time.Now()
	deadline := start.Add(convergenceCeiling)
	last := "no attempt completed"
	for {
		satisfied, why := cond()
		if satisfied {
			ok("%s (converged in %s)", label, elapsed(start))
			return true
		}
		last = why
		if time.Now().After(deadline) {
			fail("%s: still unsatisfied after %s — %s", label, elapsed(start), last)
			return false
		}
		select {
		case <-ctx.Done():
			fail("%s: context cancelled after %s — %s", label, elapsed(start), last)
			return false
		case <-time.After(pollInterval):
		}
	}
}

// elapsed renders a duration at millisecond resolution — the scale these waits
// actually resolve at, and the number a reader compares between runs.
func elapsed(start time.Time) time.Duration {
	return time.Since(start).Round(time.Millisecond)
}

// assertLive checks a key exists and is not tombstoned, polling to convergence
// because the setup ops commit asynchronously relative to their reply.
func assertLive(ctx context.Context, conn *substrate.Conn, key, label string) {
	waitFor(ctx, "setup: "+label+" "+key+" is live", func() (bool, string) {
		doc, err := readDoc(ctx, conn, key)
		if err != nil {
			return false, err.Error()
		}
		if doc.IsDeleted {
			return false, "tombstoned"
		}
		return true, ""
	})
}

// waitForTombstoned polls key until its document carries isDeleted. An absent
// key is NOT accepted as tombstoned: every key this harness waits on was
// asserted live during setup, so absence here would mean it was destroyed by
// something other than the sweep under test.
func waitForTombstoned(ctx context.Context, conn *substrate.Conn, key, label string) {
	waitFor(ctx, label+" "+key+" tombstoned", func() (bool, string) {
		doc, err := readDoc(ctx, conn, key)
		if err != nil {
			if errors.Is(err, substrate.ErrKeyNotFound) {
				return false, "key is absent — it was expected to survive as a tombstone"
			}
			return false, err.Error()
		}
		if !doc.IsDeleted {
			return false, "still live"
		}
		return true, ""
	})
}

// captureLiveEnvelope waits for the subject's piiKey envelope to be live,
// unshredded and carrying real key material, and returns its wrappedDEK — the
// value step 1 must leave byte-identical.
//
// A non-empty wrappedDEK is what separates the two branches ShredIdentityKey
// can take. On an already-minted envelope it copies the existing body and only
// flips shredded=true, keeping the real bytes (privacy-base/lenses.go states
// that invariant outright, because the envelope lens depends on it); on an
// identity that never received a sensitive write it REPLACES the body with an
// empty-wrappedDEK placeholder. Both write shredded=true and shreddedAt, so
// those two fields alone cannot tell a correct shred from a placeholder that
// has just overwritten live key material — the worst outcome in this domain.
// Carrying the pre-shred value forward is what makes the post-state able to.
//
// A failure here is fatal rather than a recorded assertion: this is setup, and
// with no captured key material the post-state comparison would degenerate
// into checking "" against "" and pass on exactly the case it exists to catch.
func captureLiveEnvelope(ctx context.Context, conn *substrate.Conn, subjectKey string) string {
	key := subjectKey + ".piiKey"
	wrappedDEK := ""
	satisfied := waitFor(ctx, "setup: "+key+" is a live, unshredded envelope holding real key material", func() (bool, string) {
		doc, err := readDoc(ctx, conn, key)
		if err != nil {
			return false, err.Error()
		}
		if doc.IsDeleted {
			return false, "tombstoned"
		}
		if shredded, _ := doc.Data["shredded"].(bool); shredded {
			return false, "already shredded before the ceremony began"
		}
		wrapped, _ := doc.Data["wrappedDEK"].(string)
		if wrapped == "" {
			return false, "wrappedDEK is empty — no real key was minted, so step 1 would only exercise the placeholder path"
		}
		wrappedDEK = wrapped
		return true, ""
	})
	if !satisfied {
		fmt.Fprintf(os.Stderr, "FATAL setup: %s never became a live envelope with real key material, so the ceremony has no live DEK to erase\n", key)
		os.Exit(1)
	}
	return wrappedDEK
}

// waitForRoleGrant polls actorKey's cap.roles projection until it carries
// operationType. Refractor re-projects the capabilityRoles lens asynchronously
// after a holdsRole change (Contract #6) with no synchronous "projection done"
// signal, so this is a genuinely unbounded wait — polled to convergence, never
// against a fixed SLA.
func waitForRoleGrant(ctx context.Context, conn *substrate.Conn, actorKey, operationType string) {
	rolesKey, err := capabilitykv.RolesKeyFromActor(actorKey)
	must(err, "derive roles key")
	waitFor(ctx, "setup: "+operationType+" projected into "+rolesKey, func() (bool, string) {
		entry, err := conn.KVGet(ctx, bootstrap.CapabilityKVBucket, rolesKey)
		if err != nil {
			return false, err.Error()
		}
		doc, err := capabilitykv.ParseCapabilityDoc(entry.Value)
		if err != nil {
			return false, err.Error()
		}
		for _, p := range doc.PlatformPermissions {
			if p.OperationType == operationType {
				return true, ""
			}
		}
		return false, "the projection carries no " + operationType + " entry yet"
	})
}

// eventLog collects every domain event published while the ceremony runs, so a
// step's emission can be asserted by the requestId it was committed under. A
// core subscription on `events.>` is enough: the outbox publishes each event to
// `events.<class>` and a core subscriber sees the same message the stream does.
type eventLog struct {
	mu     sync.Mutex
	events []domainEvent
}

// domainEvent is the outbox's published event body (internal/processor's
// Event), in the fields this harness correlates and asserts on.
type domainEvent struct {
	RequestID string         `json:"requestId"`
	EventType string         `json:"eventType"`
	TargetKey string         `json:"targetKey"`
	Payload   map[string]any `json:"payload"`
}

func watchEvents(conn *substrate.Conn) *eventLog {
	log := &eventLog{}
	_, err := conn.NATS().Subscribe(bootstrap.EventsWildcardSubject, func(msg *nats.Msg) {
		var ev domainEvent
		if err := json.Unmarshal(msg.Data, &ev); err != nil {
			return
		}
		log.mu.Lock()
		log.events = append(log.events, ev)
		log.mu.Unlock()
	})
	must(err, "subscribe to "+bootstrap.EventsWildcardSubject)
	return log
}

// await polls the collected events for one published under requestID with the
// given class. Publication follows the commit through the outbox consumer, so
// the wait is to convergence like every other one here.
func (l *eventLog) await(ctx context.Context, requestID, class string) (domainEvent, bool) {
	var found domainEvent
	satisfied := waitFor(ctx, class+" emitted for requestId "+requestID, func() (bool, string) {
		l.mu.Lock()
		defer l.mu.Unlock()
		for _, ev := range l.events {
			if ev.RequestID == requestID && ev.EventType == class {
				found = ev
				return true, ""
			}
		}
		return false, "not published yet"
	})
	return found, satisfied
}

// assertEmitted waits for the event and then checks the payload fields the
// spine's downstream consumers bind to.
func (l *eventLog) assertEmitted(ctx context.Context, requestID, class string, wantFields map[string]string) {
	ev, found := l.await(ctx, requestID, class)
	if !found {
		return
	}
	for field, want := range wantFields {
		got, _ := ev.Payload[field].(string)
		if got != want {
			fail("%s payload.%s = %q, want %q", class, field, got, want)
			return
		}
	}
	ok("%s payload names the subject correctly", class)
}

// intField reads a JSON number field, which decodes as float64 through
// map[string]any, and reports whether it was there to read. Presence is
// returned separately because a missing field and a genuine zero decode
// identically, and here zero is the convergence answer — an absent field
// wearing it would read as a converged sweep.
func intField(payload map[string]any, field string) (int, bool) {
	v, present := payload[field].(float64)
	return int(v), present
}

// identityIndexKey rebuilds identity-domain's contact index key for an
// already-normalized value.
//
// derived-key: the subject's identityindex vertex, derived only to ASSERT that
// step 4 tombstoned it. It is not a declared read on any submission this
// harness makes — PurgeIdentityDedupFootprint reaches these vertices by
// enumerating the subject's `indexes` links, never by key — so there is no
// derive_reads for the owning package to compute in its place.
func identityIndexKey(contactType, normalized string) string {
	// derived-key: an assertion target, not a declared read — no submission here
	// names this key, so no derive_reads could compute it for this caller.
	return "vtx.identityindex." + substrate.SHA256NanoID(contactType+":"+normalized)
}

// credentialIndexKey rebuilds identity-domain's credential index key.
//
// derived-key: the credential's credentialindex vertex, derived only to ASSERT
// that step 3 tombstoned it. UnbindIdentityCredentials reaches it from the
// enumerated boundTo link's source, so it is a read of no submission here and
// no package derive_reads produces it for this caller.
func credentialIndexKey(actorKey string) string {
	// derived-key: an assertion target, not a declared read — step 3 reaches this
	// vertex from the enumerated boundTo link, never by a key any caller derives.
	return "vtx.credentialindex." + substrate.SHA256NanoID(actorKey)
}

// vertexID returns the NanoID segment of a `vtx.<type>.<NanoID>` key — the
// segment Contract #1 link keys are assembled from.
func vertexID(vertexKey string) string {
	parts := strings.Split(vertexKey, ".")
	if len(parts) != 3 {
		fmt.Fprintf(os.Stderr, "FATAL not a vtx.<type>.<NanoID> key: %s\n", vertexKey)
		os.Exit(1)
	}
	return parts[2]
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
