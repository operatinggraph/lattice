package natsperm

import (
	"fmt"
	"sort"
	"strings"

	"github.com/asolgan/lattice/internal/bootstrap"
)

// bootstrapComponentName is the sanctioned provisioning-time user (Contract
// #7 §7.1) — exempt from the registry-derived deny loop: it seeds every
// platform bucket and stream before any other component connects.
const bootstrapComponentName = "bootstrap"

// coreKVStream is the backing stream for the core-kv bucket — referenced
// directly (not via the registry loop) by Bridge's extra read-side denies
// (Component.ExtraPubDeny), which close a $JS.API.DIRECT.GET side channel
// scoped to core-kv specifically (sensitive-param-egress-design.md §For-
// Andrew #1/§8).
const coreKVStream = "KV_" + bootstrap.CoreKVBucket

// Component is one NATS user — a Lattice binary's scoped connection.
type Component struct {
	// Name is both the seed filename (deploy/nkeys/<name>.nk) and the NATS
	// connection identity. It maps to a cmd/<name> binary.
	Name string
	// Desc documents the component's role in the rendered config.
	Desc string
	// ExtraPubAllow is every publish grant NOT derived from the platform-
	// bucket registry: ops/events/schedule lanes, the object plane, control-
	// plane subjects, vault RPCs, JetStream API/ACK, etc. The registry-
	// derived owner/shared-write bucket grants (Allow) are appended at
	// render/test time — do not hand-list a platform bucket's $KV.<b>.>
	// subject here (see PlatformBuckets).
	ExtraPubAllow []string
	// ExtraPubDeny is every publish deny NOT derived from the platform-bucket
	// registry — e.g. Bridge's DIRECT.GET read-side denies on the core-kv
	// backing stream. The registry-derived non-owner bucket denies + stream-
	// admin denies (Deny) are appended at render/test time.
	ExtraPubDeny []string
	// AllowResponses grants a request-reply responder a one-time publish to
	// the reply subject of each received request (control planes,
	// micro.Service discovery). Without it, a responder goes silent under
	// enforcement.
	AllowResponses bool
}

// protectedStreamDenies returns the JetStream stream-admin verbs a non-owner
// connection must be denied on a protected KV stream. Denying the KV publish
// subject ($KV.<bucket>.>) blocks ordinary writes, but a holder of the broad
// $JS.API.> grant could otherwise mutate or destroy the backing stream
// directly via the JetStream API (the backing-stream side-channel). These are
// the write-shaped verbs; reads (MSG.GET, DIRECT.GET, INFO) and consumer ops
// stay allowed so CDC readers are unaffected.
func protectedStreamDenies(stream string) []string {
	return []string{
		"$JS.API.STREAM.CREATE." + stream,
		"$JS.API.STREAM.UPDATE." + stream,
		"$JS.API.STREAM.DELETE." + stream,
		"$JS.API.STREAM.PURGE." + stream,
		"$JS.API.STREAM.MSG.DELETE." + stream,
	}
}

// Allow returns c's full publish allow-list: its hand-authored extras plus
// the universal consumer-protocol grants, plus, for every non-bootstrap
// component, the registry-derived grant on each platform bucket it owns or
// shares write of. Bootstrap is exempt from the registry loop (it already
// holds the blanket $KV.> / $O.> provisioner grant) but receives the
// protocol grants like everyone else.
//
// $JS.FC.> is granted to every component unconditionally: it is the
// flow-control ack subject of JetStream push consumers ("$JS.FC.<stream>.
// <consumer>.<token>"), which nats.go's KV watcher — the machinery under
// every KVListKeys / Watch — publishes empty replies to when the server
// sends a flow-control or stalled-consumer control message. A connection
// that cannot publish this ack stalls its own listing PERMANENTLY once a
// bucket is large enough to trigger flow control mid-delivery (the server
// pauses delivery waiting for the ack, and the stall-recovery heartbeat
// response is the same denied subject). Like $JS.ACK.>, it is consumer
// protocol plumbing scoped to consumers the connection itself receives
// deliveries for — not a data-plane privilege.
func (c Component) Allow(buckets []bootstrap.PlatformBucket) []string {
	allow := append([]string{}, c.ExtraPubAllow...)
	allow = append(allow, "$JS.FC.>")
	if c.Name == bootstrapComponentName {
		return allow
	}
	for _, b := range buckets {
		if b.Owner == c.Name || b.SharedWrite {
			allow = append(allow, "$KV."+b.Name+".>")
		}
	}
	return allow
}

// Deny returns c's full publish deny-list: its hand-authored extras plus,
// for every non-bootstrap component, a publish deny on every platform
// bucket it does not own/share-write, and — for every non-bootstrap
// component INCLUDING the owner — the stream-admin denies on every
// registered platform bucket's backing stream (the Chronicler precedent: a
// row writer never needs to create/update/delete/purge its own backing
// stream; bootstrap primordially provisions all of them). This is what
// closes the $JS.API.> backing-stream side channel matrix-wide, not just for
// the buckets a component doesn't own.
func (c Component) Deny(buckets []bootstrap.PlatformBucket) []string {
	if c.Name == bootstrapComponentName {
		return nil
	}
	deny := append([]string{}, c.ExtraPubDeny...)
	for _, b := range buckets {
		if b.Owner != c.Name && !b.SharedWrite {
			deny = append(deny, "$KV."+b.Name+".>")
		}
		deny = append(deny, protectedStreamDenies("KV_"+b.Name)...)
	}
	return deny
}

// Matrix is the permission matrix (natsperm-matrix-hygiene-design.md §3 /
// nats-account-write-restriction-design.md §3.2). The load-bearing
// invariant: only `processor` may publish $KV.core-kv.> and only
// `refractor` may publish $KV.capability-kv.> / the lens-target buckets; the
// `bootstrap` provisioner is the sanctioned pre-Processor kernel seeder.
// Every other platform-bucket owner-allow and non-owner-deny is derived from
// bootstrap.PlatformBuckets() at render/test time (see Allow/Deny) — do not
// hand-list a platform bucket's $KV.<b>.> subject in ExtraPubAllow/
// ExtraPubDeny below.
//
// Health is written to the `health-kv` KV bucket (keys
// health.<component>.<inst>), so the publish subject is $KV.health-kv.> —
// not the bare `health.>` the design prose abbreviated; health-kv is
// SharedWrite in the registry so every component's Allow() picks it up
// automatically. The object-plane grants (objmgr, loupe, loftspace-app) are
// vendor-pinned to nats.go's ObjectStore subject shape ($O.<bucket>.{C,M}.>)
// and conformance-tested by internal/natsperm
// (object-plane-nats-permissions-design.md).
var Matrix = []Component{
	{
		Name: "processor",
		Desc: "the sole Core-KV writer; runs the atomic-batch commit + event outbox",
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
		ExtraPubAllow: []string{bootstrap.EventsWildcardSubject, "$JS.API.>", "$JS.ACK.>", "_INBOX.>", bootstrap.OpsWildcardSubject},
	},
	{
		Name: "refractor",
		Desc: "the sole lens projector — writes every KV target EXCEPT Core KV (CDC-read-only on Core)",
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
		ExtraPubAllow:  []string{"$KV.>", "$JS.API.>", "$JS.ACK.>", "lattice.refractor.>", bootstrap.OpsWildcardSubject, "lattice.sync.>"},
		AllowResponses: true, // control responder (lattice.ctrl.refractor.>)
	},
	{
		Name: "loom",
		Desc: "pattern engine; mutates Core state only by submitting ops (P2); owns loom-state",
		// lattice.op.status — the §10.6 deadline+probe's RPC to the
		// Processor-hosted Contract #4 tracker projection (Fire 3 of
		// op-status-read-surface-design.md), replacing Loom's direct
		// Core-KV tracker/task-vertex reads.
		ExtraPubAllow:  []string{bootstrap.OpsWildcardSubject, "lattice.ctrl.loom.>", "$JS.API.>", "$JS.ACK.>", "lattice.op.status"},
		AllowResponses: true, // control responder (lattice.ctrl.loom.>)
	},
	{
		Name: "weaver",
		Desc: "reconciliation engine; owns weaver-state; targets are Refractor-written, Weaver-read",
		// The control responder only SUBSCRIBES to lattice.ctrl.weaver.> (already
		// covered by the wildcard subscribe grant) and replies via allowResponses —
		// it never publishes to the subject itself, so no explicit publish grant is
		// needed here (mirrors refractor's control responder, which carries the
		// same allowResponses-only posture for lattice.ctrl.refractor.>).
		ExtraPubAllow:  []string{bootstrap.OpsWildcardSubject, bootstrap.SchedulesWildcardSubject, "$JS.API.>", "$JS.ACK.>"},
		AllowResponses: true, // control responder (lattice.ctrl.weaver.>)
	},
	{
		Name: "bridge",
		Desc: "external-I/O egress; replies via ops; consumes its external-call/schedule durables (consumer names, not KV buckets — bridge's only KV write is health-kv)",
		// $O.core-objects.> — the docGen reference vendor adapter's byte-plane
		// write (cmd/bridge registers it with the core-objects bucket): the
		// rendered executed-lease artifact is ObjectPut by the adapter and stays
		// inert until an AttachObject op anchors it. Bridge is one of the four
		// sanctioned object-plane writers (TestObjectStoreWriteAccess).
		// lattice.vault.decryptref — the egress-unwrap boundary (design
		// sensitive-ref-mac-provenance-design.md §3.3/§8 Fire 2): the bridge's
		// decrypt authority shrinks from the wholesale lattice.vault.decrypt
		// (Loupe's inspector RPC) to this ref-verified endpoint, which
		// mandatorily checks the Processor-minted MAC before decrypting a
		// sensitive-ref param at the last possible moment before a vendor call —
		// a compromised bridge can no longer decrypt arbitrary ciphertext, only
		// tuples the Processor actually minted for egress.
		// lattice.op.status — the skip-on-redelivery probe's RPC to the
		// Processor-hosted Contract #4 tracker projection (Fire 1 of
		// op-status-read-surface-design.md); replaces the direct core-kv
		// DIRECT.GET the B2 read-tightening denied below.
		ExtraPubAllow: []string{bootstrap.OpsWildcardSubject, bootstrap.SchedulesWildcardSubject, "$O.core-objects.>", "$JS.API.>", "$JS.ACK.>", "lattice.vault.decryptref", "lattice.op.status"},
		// The registry-derived denies (Deny) cover the core-kv/capability-kv
		// WRITE side (the $KV.<b>.> publish subject + every registered bucket's
		// backing-stream admin verbs). The two extra denies below close the
		// READ side of the same grant's blast radius (design §For-Andrew #1/§8,
		// adversarial finding B2): the broad $JS.API.> grant every component
		// holds admits $JS.API.DIRECT.GET / STREAM.MSG.GET requests — a
		// JetStream KV read, not a write, and not covered by the registry deny
		// loop — so a decrypt-RPC-holding bridge could otherwise reach the whole
		// core-kv corpus via the backing-stream side channel. Denying them here
		// pins the bridge's reachable read set to the ONE lens bucket
		// (privacy-pii-key-envelopes) its egress unwrap actually needs.
		ExtraPubDeny: []string{
			// The BARE form (no trailing token) is also a live request shape —
			// nats.go's direct-get-by-sequence (KeyValue.GetRevision) publishes to
			// exactly this subject with no subject-suffix — and NATS' `>` wildcard
			// requires at least one token after the prefix, so "...KV_core-kv.>"
			// alone does NOT match it: the bare-subject deny is required alongside
			// the wildcarded one, or the read-tightening is sequence-walk-bypassable
			// (adversarial review finding, nats-account-write-restriction fire).
			"$JS.API.DIRECT.GET." + coreKVStream,
			"$JS.API.DIRECT.GET." + coreKVStream + ".>",
			"$JS.API.STREAM.MSG.GET." + coreKVStream,
		},
		AllowResponses: true, // may respond to requests
	},
	{
		Name:          "object-store-manager",
		Desc:          "object GC actor; writes the object store, mutates Core state via ops",
		ExtraPubAllow: []string{bootstrap.OpsWildcardSubject, "$O.core-objects.>", "$JS.API.>", "$JS.ACK.>"},
	},
	{
		Name: "chronicler",
		Desc: "event-stream-to-KV-row materializer; CDC-reads vtx.meta.> for eventStream lens definitions, writes only its own lens-target buckets",
		// CDC-subscribing core-kv (vtx.meta.>, read-only) and core-events (its
		// definitions' subjects) needs no publish grant — reads are unrestricted;
		// this account-level matrix gates writes only. Chronicler writes its own
		// eventStream lens targets (orchestration-history is the only one today,
		// chronicler-host-reconciliation-design.md / orchestration-history-read-
		// model-design.md) + health-kv (both registry-derived, Chronicler being
		// the owner of orchestration-history and health-kv being shared-write),
		// and submits no ops (P2: it is a pure read-model materializer, never a
		// Core-KV writer). The registry-derived stream-admin denies apply to
		// Chronicler on its OWN backing stream too (owner-included, §Deny) —
		// bootstrap already primordially provisions orchestration-history (like
		// weaver-targets/loom-state), so chronicler only ever needs the ordinary
		// $KV. publish subject, never stream administration.
		ExtraPubAllow: []string{"$JS.API.>", "$JS.ACK.>"},
	},
	{
		Name: "bootstrap",
		Desc: "provisioning-time privileged user — the sanctioned non-Processor direct Core-KV writer; seeds the kernel before the Processor exists and creates streams/buckets",
		// No denies: the provisioner seeds core-kv/capability-kv and creates
		// every stream/bucket before any component connects. Exempt from the
		// registry-derived deny loop (Deny returns nil for bootstrap).
		ExtraPubAllow: []string{"$KV.>", "$O.>", "$JS.API.>", "$JS.ACK.>", bootstrap.EventsWildcardSubject, bootstrap.OpsWildcardSubject},
	},
	{
		Name:          "lattice-pkg",
		Desc:          "package installer — InstallPackage / UninstallPackage kernel ops",
		ExtraPubAllow: []string{bootstrap.OpsWildcardSubject, "$JS.API.>", "$JS.ACK.>"},
	},
	{
		Name: "loupe",
		Desc: "trusted inspector — reads all KV (subscribe/get); writes state only via ops, even it gets no direct Core-KV write",
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
		ExtraPubAllow: []string{bootstrap.OpsWildcardSubject, "$O.core-objects.>", "$JS.API.>", "$JS.ACK.>", "lattice.ctrl.>", "lattice.vault.decrypt", "lattice.vault.wrapkey", "lattice.vault.unwrapkey"},
	},
	{
		Name: "lattice",
		Desc: "operator CLI + verify tools — submits ops, reads",
		// lattice.ctrl.> — CLI control commands (pause/resume/rebuild/…) request
		// the component control planes, same operator surface as Loupe's.
		// lattice.op.status — `lattice op status` (Fire 4 of
		// op-status-read-surface-design.md): replaces the CLI's former raw
		// Core-KV tracker KVGet with the Processor-hosted RPC, the last of the
		// four named submitters (§1.5) to migrate off a direct tracker read.
		ExtraPubAllow: []string{bootstrap.OpsWildcardSubject, "$JS.API.>", "$JS.ACK.>", "lattice.ctrl.>", "lattice.op.status"},
	},
	{
		Name: "gateway",
		Desc: "external write-path translator — verifies JWTs, stamps the verified actor, submits ops; mutates Core state only via ops (P2); " +
			"owns token-revocation (materialized from its own events.gateway.> consumer, gateway-token-revocation-activation-design.md) and " +
			"credential-bindings (materialized from its own credential→identity resolution set); hosts the auth-callout responder " +
			"(internal/gateway/natsauth, per-identity-nats-subscribe-acl-design.md) — allow_responses covers its reply to the server's " +
			"dynamic $SYS.REQ.USER.AUTH reply-to inbox",
		// lattice.op.status — GET /v1/operations/{requestId} (Fire 2 of
		// op-status-read-surface-design.md): turns the write path's 202
		// fallback into a real read-your-own-writes poll for browser actors,
		// backed by the Processor-hosted Contract #4 tracker projection —
		// never a direct Core-KV read (P5/P2 stay intact).
		ExtraPubAllow:  []string{bootstrap.OpsWildcardSubject, "$JS.API.>", "$JS.ACK.>", "lattice.op.status"},
		AllowResponses: true,
	},
	{
		Name: "loftspace-app",
		Desc: "vertical app (P5 reader); writes go browser-direct through the Gateway — holds NO core-operations (ops.>) publish so a compromised app cannot forge an env.Actor (#75 Fire 2b)",
		// $O.core-objects.> — document byte uploads (objects.go ObjectPut); bytes
		// are inert until a browser-direct AttachObject (via the Gateway) anchors
		// them, so the byte-ingest grant carries no actor authority.
		// lattice.vault.wrapkey / lattice.vault.unwrapkey — the blob-plane
		// envelope-key RPCs (object-store-crypto-shred-design.md §3.1 Fire 2,
		// §9 Fire 4 Increment 1), extended from Loupe-only to loftspace-app
		// (✅ Andrew-ratified 2026-07-07 — narrowest widening, same two
		// subjects Loupe already has, no broader Vault or Core-KV access): the
		// lease-signing PDF upload generates a per-object CEK client-side and
		// wraps/unwraps it via the Processor's Vault, mirroring Loupe's Fire 2
		// path, rather than holding the master KEK itself.
		ExtraPubAllow: []string{"$O.core-objects.>", "$JS.API.>", "$JS.ACK.>", "lattice.vault.wrapkey", "lattice.vault.unwrapkey"},
	},
	{
		Name:          "clinic-app",
		Desc:          "vertical app (P5 reader); writes go browser-direct through the Gateway — holds NO core-operations (ops.>) publish so a compromised app cannot forge an env.Actor (#75 Fire 2b)",
		ExtraPubAllow: []string{"$JS.API.>", "$JS.ACK.>"},
	},
	{
		Name:          "cafe-app",
		Desc:          "vertical app (P5 reader); writes go browser-direct through the Gateway — holds NO core-operations (ops.>) publish so a compromised app cannot forge an env.Actor (#75 Fire 2b)",
		ExtraPubAllow: []string{"$JS.API.>", "$JS.ACK.>"},
	},
	{
		Name:          "wellness-app",
		Desc:          "vertical app (P5 reader); writes go browser-direct through the Gateway — holds NO core-operations (ops.>) publish so a compromised app cannot forge an env.Actor (#75 Fire 2b)",
		ExtraPubAllow: []string{"$JS.API.>", "$JS.ACK.>"},
	},
}

// RenderConf renders deploy/nats-server.conf from Matrix + the platform-
// bucket registry, given each component's minted public key plus the
// auth-callout responder's issuer (ACCOUNT) and xkey (CURVE) public keys
// (per-identity-nats-subscribe-acl-design.md §3.1/§7 — xkey payload
// encryption is enabled from day one, not a deferred hardening pass). The
// sole producer of the committed conf's authorization block — gen-dev-nkeys
// calls this after minting/reusing seeds; the drift test (TestConfMatchesMatrix)
// calls it again at test time and diffs against the committed file.
func RenderConf(pubKeys map[string]string, calloutIssuerPub, calloutXkeyPub string) string {
	buckets := bootstrap.PlatformBuckets()
	var b strings.Builder
	b.WriteString(`# Lattice NATS transport-authorization config (NATS account-level write restriction).
#
# GENERATED by deploy/gen-dev-nkeys — do not hand-edit; change internal/natsperm.Matrix
# and regenerate (go run ./deploy/gen-dev-nkeys).
#
# Path A (static config + per-component NKey users). Each Lattice binary connects
# with its own scoped NKey seed (deploy/nkeys/<component>.nk, DEV-ONLY). The
# load-bearing invariant: only the processor may publish $KV.core-kv.> and only
# refractor may publish $KV.capability-kv.> / the lens-target buckets; bootstrap is
# the sanctioned provisioning-time writer. The seeds here are dev credentials
# (like POSTGRES_PASSWORD: lattice_dev); production injects real seeds via mounted
# secrets and never commits them.
#
# auth_callout (per-identity-nats-subscribe-acl-design.md): every connection
# NOT listed in auth_users below is delegated to internal/gateway/natsauth
# (hosted in cmd/gateway) — the untrusted Edge sync-plane connections. The
# component users here all bypass the callout unchanged. xkey seals every
# callout request/response (§7 — enabled from day one, not a deferred
# hardening pass).

jetstream {
  store_dir: "/data/jetstream"
}

authorization {
  auth_callout {
    issuer: ` + fmt.Sprintf("%q", calloutIssuerPub) + `
    xkey: ` + fmt.Sprintf("%q", calloutXkeyPub) + `
    auth_users: [` + quoteList(sortedValues(pubKeys)) + `]
  }
  users = [
`)
	for _, c := range Matrix {
		b.WriteString("    {\n")
		fmt.Fprintf(&b, "      # %s — %s\n", c.Name, c.Desc)
		fmt.Fprintf(&b, "      nkey: %q\n", pubKeys[c.Name])
		b.WriteString("      permissions {\n")
		b.WriteString("        publish {\n")
		b.WriteString("          allow: [" + quoteList(c.Allow(buckets)) + "]\n")
		if deny := c.Deny(buckets); len(deny) > 0 {
			b.WriteString("          deny: [" + quoteList(deny) + "]\n")
		}
		b.WriteString("        }\n")
		b.WriteString("        subscribe { allow: [\">\"] }\n")
		if c.AllowResponses {
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

// sortedValues returns m's values in sorted order — auth_users must list every
// component's public key, and a deterministic order keeps regeneration a
// stable, reviewable diff (map iteration order is not guaranteed).
func sortedValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
