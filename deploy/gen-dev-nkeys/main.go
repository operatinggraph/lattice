// Command gen-dev-nkeys mints the per-component NATS NKey seeds and renders the
// Lattice transport-authorization config (deploy/nats-server.conf) that enforces
// the NATS account-level write restriction (Path A: static config + per-component
// NKey users).
//
// It is the single source of truth for the permission matrix: the Go `matrix`
// below defines each component's publish allow/deny set, and this tool renders
// both the seed files (deploy/nkeys/<component>.nk) and the server config that
// references their public keys. Run it after editing the matrix (e.g. adding a
// component):
//
//	go run ./deploy/gen-dev-nkeys
//
// An existing seed file is REUSED, not rotated — the run is idempotent per
// component, so adding one new entry does not churn every other component's
// dev identity. Delete a component's deploy/nkeys/<name>.nk first to force a
// deliberate rotation of just that seed.
//
// The seeds it writes are DEV-ONLY, committed like POSTGRES_PASSWORD: lattice_dev;
// production injects real seeds via mounted secrets / Vault and never commits them.
//
// The rendered config + committed seeds are exercised end-to-end by
// internal/natsperm (the offline conformance proof of the matrix).
package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nats-io/nkeys"

	"github.com/asolgan/lattice/internal/bootstrap"
)

// protectedStream lists the JetStream stream-admin verbs a non-owner connection
// must be denied on a protected KV stream. Denying the KV publish subject
// ($KV.<bucket>.>) blocks ordinary writes, but a holder of the broad $JS.API.>
// grant could otherwise mutate or destroy the backing stream directly via the
// JetStream API (the backing-stream side-channel). These are the write-shaped
// verbs; reads (MSG.GET, DIRECT.GET, INFO) and consumer ops stay allowed so CDC
// readers are unaffected.
func protectedStreamDenies(stream string) []string {
	return []string{
		"$JS.API.STREAM.CREATE." + stream,
		"$JS.API.STREAM.UPDATE." + stream,
		"$JS.API.STREAM.DELETE." + stream,
		"$JS.API.STREAM.PURGE." + stream,
		"$JS.API.STREAM.MSG.DELETE." + stream,
	}
}

// The protected KV streams whose integrity is load-bearing.
const (
	coreKVStream               = "KV_core-kv"
	capabilityKVStream         = "KV_capability-kv"
	orchestrationHistoryStream = "KV_orchestration-history"
)

// component is one NATS user — a Lattice binary's scoped connection.
type component struct {
	// name is both the seed filename (deploy/nkeys/<name>.nk) and the NATS
	// connection identity. It maps to a cmd/<name> binary.
	name string
	// desc documents the component's role in the rendered config.
	desc string
	// pubAllow is the publish allow-list. With an allow-list present, NATS
	// denies any publish that does not match an entry, so the allow-list is the
	// primary boundary; pubDeny removes destructive verbs the broad $JS.API.>
	// grant would otherwise admit.
	pubAllow []string
	// pubDeny removes subjects from the allow-list (deny wins over allow).
	pubDeny []string
	// allowResponses grants a request-reply responder a one-time publish to the
	// reply subject of each received request (control planes, micro.Service
	// discovery). Without it, a responder goes silent under enforcement.
	allowResponses bool
}

// denyProtected returns the publish denies for a non-owner of the named
// protected streams: the explicit KV publish subject plus the stream-admin
// verbs (belt-and-suspenders; the KV publish is already excluded by the
// allow-list, but the stream-admin verbs are reachable via $JS.API.>).
func denyProtected(kvSubjects []string, streams ...string) []string {
	denies := append([]string{}, kvSubjects...)
	for _, s := range streams {
		denies = append(denies, protectedStreamDenies(s)...)
	}
	return denies
}

// matrix is the permission matrix (design §3.2 — NATS account write restriction).
// The load-bearing invariant: only `processor` may publish $KV.core-kv.> and only
// `refractor` may publish $KV.capability-kv.> / the lens-target buckets; the
// `bootstrap` provisioner is the sanctioned pre-Processor kernel seeder.
//
// Health is written to the `health-kv` KV bucket (keys health.<component>.<inst>),
// so the publish subject is $KV.health-kv.> — not the bare `health.>` the design
// prose abbreviated. The object-plane grants (objmgr, loupe, loftspace-app) are
// vendor-pinned to nats.go's ObjectStore subject shape ($O.<bucket>.{C,M}.>) and
// conformance-tested by internal/natsperm (object-plane-nats-permissions-design.md).
var matrix = []component{
	{
		name: "processor",
		desc: "the sole Core-KV writer; runs the atomic-batch commit + event outbox",
		// _INBOX.> — op-submission replies. commit_path.go's replyTo does a plain
		// nc.Publish to the caller's Lattice-Reply-Inbox header (or msg.ReplySubject),
		// not the standard Msg.Reply request-reply protocol allow_responses covers —
		// so the dynamic reply-authorization mechanism doesn't apply here and an
		// explicit grant is required (verified against the live stack, Fire 2).
		// ops.> — internal/privacyworker (the async half of crypto-shredding) runs
		// on the Processor's own connection (cmd/processor/main.go) and submits
		// RecordShredFinalization to ops.system; a sanctioned op-submit exception,
		// the same shape every other op-submitting component already carries
		// (refractor-publish-acl-gap).
		pubAllow: []string{"$KV.core-kv.>", bootstrap.EventsWildcardSubject, "$KV.health-kv.>", "$JS.API.>", "$JS.ACK.>", "_INBOX.>", bootstrap.OpsWildcardSubject},
		// Owns core-kv; denied the destructive verbs on the capability stream it
		// does not own (Refractor is the sole capability-kv writer).
		pubDeny: denyProtected([]string{"$KV.capability-kv.>"}, capabilityKVStream),
	},
	{
		name: "refractor",
		desc: "the sole lens projector — writes every KV target EXCEPT Core KV (CDC-read-only on Core)",
		// $KV.> covers capability-kv, every lens read-model target (including
		// dynamically-named package buckets) and health-kv without enumeration.
		// lattice.refractor.> covers the per-lens dlq/metrics/audit subjects
		// (internal/refractor/subjects.go) — verified against the live stack, Fire 2.
		// ops.> — internal/refractor/keyshredded submits RecordShredFinalization to
		// ops.system (a sanctioned op-submit exception, mirroring every other
		// op-submitting component). lattice.sync.> — the Personal Lens nats_subject
		// adapter's per-actor delta publish (lattice.sync.user.<actor>); latent
		// today (no lens installs it yet) but transport-reachable in code
		// (refractor-publish-acl-gap).
		pubAllow:       []string{"$KV.>", "$JS.API.>", "$JS.ACK.>", "lattice.refractor.>", bootstrap.OpsWildcardSubject, "lattice.sync.>"},
		pubDeny:        denyProtected([]string{"$KV.core-kv.>"}, coreKVStream),
		allowResponses: true, // control responder (lattice.ctrl.refractor.>)
	},
	{
		name:           "loom",
		desc:           "pattern engine; mutates Core state only by submitting ops (P2); owns loom-state",
		pubAllow:       []string{bootstrap.OpsWildcardSubject, "$KV.loom-state.>", "lattice.ctrl.loom.>", "$KV.health-kv.>", "$JS.API.>", "$JS.ACK.>"},
		pubDeny:        denyProtected([]string{"$KV.core-kv.>", "$KV.capability-kv.>"}, coreKVStream, capabilityKVStream),
		allowResponses: true, // control responder (lattice.ctrl.loom.>)
	},
	{
		name: "weaver",
		desc: "reconciliation engine; owns weaver-state; targets are Refractor-written, Weaver-read",
		// The control responder only SUBSCRIBES to lattice.ctrl.weaver.> (already
		// covered by the wildcard subscribe grant) and replies via allowResponses —
		// it never publishes to the subject itself, so no explicit publish grant is
		// needed here (mirrors refractor's control responder, which carries the
		// same allowResponses-only posture for lattice.ctrl.refractor.>).
		pubAllow:       []string{bootstrap.OpsWildcardSubject, "$KV.weaver-state.>", bootstrap.SchedulesWildcardSubject, "$KV.health-kv.>", "$JS.API.>", "$JS.ACK.>"},
		pubDeny:        denyProtected([]string{"$KV.core-kv.>", "$KV.capability-kv.>"}, coreKVStream, capabilityKVStream),
		allowResponses: true, // control responder (lattice.ctrl.weaver.>)
	},
	{
		name:           "bridge",
		desc:           "external-I/O egress; replies via ops; consumes its external-call/schedule durables (consumer names, not KV buckets — bridge's only KV write is health-kv)",
		pubAllow:       []string{bootstrap.OpsWildcardSubject, bootstrap.SchedulesWildcardSubject, "$KV.health-kv.>", "$JS.API.>", "$JS.ACK.>"},
		pubDeny:        denyProtected([]string{"$KV.core-kv.>", "$KV.capability-kv.>"}, coreKVStream, capabilityKVStream),
		allowResponses: true, // may respond to requests
	},
	{
		name:     "object-store-manager",
		desc:     "object GC actor; writes the object store, mutates Core state via ops",
		pubAllow: []string{bootstrap.OpsWildcardSubject, "$O.core-objects.>", "$KV.health-kv.>", "$JS.API.>", "$JS.ACK.>"},
		pubDeny:  denyProtected([]string{"$KV.core-kv.>", "$KV.capability-kv.>"}, coreKVStream, capabilityKVStream),
	},
	{
		name: "chronicler",
		desc: "event-stream-to-KV-row materializer; CDC-reads vtx.meta.> for eventStream lens definitions, writes only its own lens-target buckets",
		// CDC-subscribing core-kv (vtx.meta.>, read-only) and core-events (its
		// definitions' subjects) needs no publish grant — reads are unrestricted;
		// this account-level matrix gates writes only. Chronicler writes its own
		// eventStream lens targets (orchestration-history is the only one today,
		// chronicler-host-reconciliation-design.md / orchestration-history-read-
		// model-design.md) + health-kv, and submits no ops (P2: it is a pure
		// read-model materializer, never a Core-KV writer).
		// The stream-admin verbs (CREATE/UPDATE/DELETE/PURGE) on chronicler's own
		// backing stream are denied to chronicler itself too — bootstrap already
		// primordially provisions orchestration-history (like weaver-targets/
		// loom-state), so chronicler only ever needs the ordinary $KV.
		// publish subject, never stream administration. This does NOT close the
		// side channel for every OTHER component's pre-existing broad $JS.API.>
		// grant (refractor, processor, loom, weaver, …) — that is the same
		// natsperm-matrix-hygiene-tracked debt TestGatewayRevocationBucketWriteIsolation
		// already documents for weaver-targets/token-revocation, now also
		// covering this new bucket, not newly introduced here.
		pubAllow: []string{"$KV.orchestration-history.>", "$KV.health-kv.>", "$JS.API.>", "$JS.ACK.>"},
		pubDeny:  denyProtected([]string{"$KV.core-kv.>", "$KV.capability-kv.>"}, coreKVStream, capabilityKVStream, orchestrationHistoryStream),
	},
	{
		name: "bootstrap",
		desc: "provisioning-time privileged user — the sanctioned non-Processor direct Core-KV writer; seeds the kernel before the Processor exists and creates streams/buckets",
		// No denies: the provisioner seeds core-kv/capability-kv and creates
		// every stream/bucket before any component connects.
		pubAllow: []string{"$KV.>", "$O.>", "$JS.API.>", "$JS.ACK.>", bootstrap.EventsWildcardSubject, bootstrap.OpsWildcardSubject},
	},
	{
		name:     "lattice-pkg",
		desc:     "package installer — InstallPackage / UninstallPackage kernel ops",
		pubAllow: []string{bootstrap.OpsWildcardSubject, "$KV.health-kv.>", "$JS.API.>", "$JS.ACK.>"},
		pubDeny:  denyProtected([]string{"$KV.core-kv.>", "$KV.capability-kv.>"}, coreKVStream, capabilityKVStream),
	},
	{
		name: "loupe",
		desc: "trusted inspector — reads all KV (subscribe/get); writes state only via ops, even it gets no direct Core-KV write",
		// lattice.ctrl.> — the Control surface issues per-name requests to the
		// Refractor/Weaver/Loom control planes (lattice.ctrl.<comp>.<name>.<op>);
		// the planes reply via allow_responses on their own users. $O.core-objects.>
		// — the admin object-upload surface (cmd/loupe/objects.go ObjectPut).
		// lattice.vault.decrypt — the trusted-tool PII decrypt RPC (the Processor
		// responds; vault-crypto-shredding-design.md §2.3, Loupe F12 Reveal).
		// lattice.vault.wrapkey / lattice.vault.unwrapkey — the blob-plane
		// envelope-key RPCs (object-store-crypto-shred-design.md §3.1 Fire 2):
		// Loupe generates a per-object CEK client-side and wraps/unwraps it via
		// the Processor's Vault rather than holding the master KEK itself.
		// Loupe is a named trusted plaintext consumer; this is the transport
		// gate authorizing it to reach the responder (only Loupe + the Processor
		// carry it).
		pubAllow: []string{bootstrap.OpsWildcardSubject, "$KV.health-kv.>", "$O.core-objects.>", "$JS.API.>", "$JS.ACK.>", "lattice.ctrl.>", "lattice.vault.decrypt", "lattice.vault.wrapkey", "lattice.vault.unwrapkey"},
		pubDeny:  denyProtected([]string{"$KV.core-kv.>", "$KV.capability-kv.>"}, coreKVStream, capabilityKVStream),
	},
	{
		name: "lattice",
		desc: "operator CLI + verify tools — submits ops, reads",
		// lattice.ctrl.> — CLI control commands (pause/resume/rebuild/…) request
		// the component control planes, same operator surface as Loupe's.
		pubAllow: []string{bootstrap.OpsWildcardSubject, "$KV.health-kv.>", "$JS.API.>", "$JS.ACK.>", "lattice.ctrl.>"},
		pubDeny:  denyProtected([]string{"$KV.core-kv.>", "$KV.capability-kv.>"}, coreKVStream, capabilityKVStream),
	},
	{
		name: "gateway",
		desc: "external write-path translator — verifies JWTs, stamps the verified actor, submits ops; mutates Core state only via ops (P2); " +
			"owns token-revocation (materialized from its own events.gateway.> consumer, gateway-token-revocation-activation-design.md)",
		pubAllow: []string{bootstrap.OpsWildcardSubject, "$KV.health-kv.>", "$KV.token-revocation.>", "$JS.API.>", "$JS.ACK.>"},
		pubDeny:  denyProtected([]string{"$KV.core-kv.>", "$KV.capability-kv.>"}, coreKVStream, capabilityKVStream),
	},
	{
		name: "loftspace-app",
		desc: "vertical app (P5 reader); writes via ops",
		// $O.core-objects.> — lease-PDF + ID/signature uploads (lease_document.go,
		// objects.go ObjectPut).
		pubAllow: []string{bootstrap.OpsWildcardSubject, "$KV.health-kv.>", "$O.core-objects.>", "$JS.API.>", "$JS.ACK.>"},
		pubDeny:  denyProtected([]string{"$KV.core-kv.>", "$KV.capability-kv.>"}, coreKVStream, capabilityKVStream),
	},
	{
		name:     "clinic-app",
		desc:     "vertical app (P5 reader); writes via ops",
		pubAllow: []string{bootstrap.OpsWildcardSubject, "$KV.health-kv.>", "$JS.API.>", "$JS.ACK.>"},
		pubDeny:  denyProtected([]string{"$KV.core-kv.>", "$KV.capability-kv.>"}, coreKVStream, capabilityKVStream),
	},
	{
		name:     "cafe-app",
		desc:     "vertical app (P5 reader); writes via ops",
		pubAllow: []string{bootstrap.OpsWildcardSubject, "$KV.health-kv.>", "$JS.API.>", "$JS.ACK.>"},
		pubDeny:  denyProtected([]string{"$KV.core-kv.>", "$KV.capability-kv.>"}, coreKVStream, capabilityKVStream),
	},
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gen-dev-nkeys:", err)
		os.Exit(1)
	}
}

func run() error {
	deployDir, err := deployRoot()
	if err != nil {
		return err
	}
	nkeysDir := filepath.Join(deployDir, "nkeys")
	if err := os.MkdirAll(nkeysDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", nkeysDir, err)
	}

	pubKeys := make(map[string]string, len(matrix))
	for _, c := range matrix {
		seedPath := filepath.Join(nkeysDir, c.name+".nk")

		// Idempotent by component: an existing seed file is REUSED, not
		// rotated. Minting a fresh keypair for every component on every run
		// (the original behavior) rotates every OTHER component's dev
		// identity as a side effect of adding one new component — a
		// disruptive, unreviewable diff. Delete the seed file to force a
		// deliberate rotation for that one component.
		if existing, err := os.ReadFile(seedPath); err == nil {
			kp, err := nkeys.FromSeed(bytes.TrimSpace(existing))
			if err != nil {
				return fmt.Errorf("parse existing seed %s: %w", seedPath, err)
			}
			pub, err := kp.PublicKey()
			if err != nil {
				return fmt.Errorf("public key for existing %s: %w", c.name, err)
			}
			pubKeys[c.name] = pub
			continue
		}

		kp, err := nkeys.CreateUser()
		if err != nil {
			return fmt.Errorf("create nkey for %s: %w", c.name, err)
		}
		seed, err := kp.Seed()
		if err != nil {
			return fmt.Errorf("seed for %s: %w", c.name, err)
		}
		pub, err := kp.PublicKey()
		if err != nil {
			return fmt.Errorf("public key for %s: %w", c.name, err)
		}
		if err := os.WriteFile(seedPath, append(seed, '\n'), 0o600); err != nil {
			return fmt.Errorf("write seed %s: %w", seedPath, err)
		}
		pubKeys[c.name] = pub
	}

	conf := renderConf(pubKeys)
	confPath := filepath.Join(deployDir, "nats-server.conf")
	if err := os.WriteFile(confPath, []byte(conf), 0o644); err != nil {
		return fmt.Errorf("write conf %s: %w", confPath, err)
	}
	fmt.Printf("wrote %d seeds to %s and %s\n", len(matrix), nkeysDir, confPath)
	return nil
}

// deployRoot locates the deploy/ directory relative to this source file so the
// tool works regardless of the caller's working directory.
func deployRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	// Walk up to the repo root (the dir containing go.mod), then into deploy/.
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "deploy"), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not find go.mod above %s", wd)
		}
		dir = parent
	}
}

func renderConf(pubKeys map[string]string) string {
	var b strings.Builder
	b.WriteString(`# Lattice NATS transport-authorization config (NATS account-level write restriction).
#
# GENERATED by deploy/gen-dev-nkeys — do not hand-edit; change the matrix in
# deploy/gen-dev-nkeys/main.go and regenerate (go run ./deploy/gen-dev-nkeys).
#
# Path A (static config + per-component NKey users). Each Lattice binary connects
# with its own scoped NKey seed (deploy/nkeys/<component>.nk, DEV-ONLY). The
# load-bearing invariant: only the processor may publish $KV.core-kv.> and only
# refractor may publish $KV.capability-kv.> / the lens-target buckets; bootstrap is
# the sanctioned provisioning-time writer. The seeds here are dev credentials
# (like POSTGRES_PASSWORD: lattice_dev); production injects real seeds via mounted
# secrets and never commits them.

jetstream {
  store_dir: "/data/jetstream"
}

authorization {
  users = [
`)
	for _, c := range matrix {
		b.WriteString("    {\n")
		fmt.Fprintf(&b, "      # %s — %s\n", c.name, c.desc)
		fmt.Fprintf(&b, "      nkey: %q\n", pubKeys[c.name])
		b.WriteString("      permissions {\n")
		b.WriteString("        publish {\n")
		b.WriteString("          allow: [" + quoteList(c.pubAllow) + "]\n")
		if len(c.pubDeny) > 0 {
			b.WriteString("          deny: [" + quoteList(c.pubDeny) + "]\n")
		}
		b.WriteString("        }\n")
		b.WriteString("        subscribe { allow: [\">\"] }\n")
		if c.allowResponses {
			b.WriteString("        allow_responses: true\n")
		}
		b.WriteString("      }\n")
		b.WriteString("    }\n")
	}
	b.WriteString("  ]\n}\n")
	return b.String()
}

func quoteList(items []string) string {
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = fmt.Sprintf("%q", s)
	}
	return strings.Join(quoted, ", ")
}
