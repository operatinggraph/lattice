package natsperm

import (
	"fmt"
	"sort"
	"strings"

	"github.com/operatinggraph/lattice/internal/bootstrap"
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
	// SubscribeAllow overrides the default unrestricted subscribe grant
	// ("gates writes only; reads are unrestricted", the matrix's stance for
	// trusted platform daemons). Nil renders the default `[">"]`; a non-nil
	// list pins the component's subscribe side to exactly those subjects —
	// for a component whose only subscribe need is its own publish-ack reply
	// inbox (facet-host-health-emission-design.md §4.2).
	SubscribeAllow []string
}

// protectedStreamDenies returns the JetStream stream-admin verbs a non-owner
// connection must be denied on a protected KV stream. Denying the KV publish
// subject ($KV.<bucket>.>) blocks ordinary writes, but a holder of the broad
// $JS.API.> grant could otherwise mutate or destroy the backing stream
// directly via the JetStream API (the backing-stream side-channel). These are
// the write-shaped verbs plus the two whole-stream backup/restore verbs
// (SNAPSHOT/RESTORE) that reach far beyond CDC-reader territory — SNAPSHOT is
// a bulk byte-level export of the ENTIRE backing stream (every key's full
// history, not the current-value reads MSG.GET/DIRECT.GET/INFO cover) and
// RESTORE wholesale-replaces it; verified no production code in this repo
// uses either (both are external-operator disaster-recovery tools, never
// this codebase's own runtime path), so closing them costs nothing.
// LEADER.STEPDOWN belongs to the same family for the same reason: it forces a
// re-election and a delivery pause on the stream, which is a write-shaped
// effect on availability rather than a read. It is inert on a single server
// (the endpoint answers 503 without clustering) and no code in this repo
// invokes it, so closing it costs nothing now and is already closed the day
// JetStream is clustered — the point at which it would otherwise become a
// live stall primitive for any $JS.API.> holder.
// Ordinary reads (MSG.GET, DIRECT.GET, INFO) and consumer ops stay allowed
// so CDC readers are unaffected.
func protectedStreamDenies(stream string) []string {
	return []string{
		"$JS.API.STREAM.CREATE." + stream,
		"$JS.API.STREAM.UPDATE." + stream,
		"$JS.API.STREAM.DELETE." + stream,
		"$JS.API.STREAM.PURGE." + stream,
		"$JS.API.STREAM.MSG.DELETE." + stream,
		"$JS.API.STREAM.SNAPSHOT." + stream,
		"$JS.API.STREAM.RESTORE." + stream,
		"$JS.API.STREAM.LEADER.STEPDOWN." + stream,
	}
}

// coreEventsProtectedConsumer names a security-plane durable consumer on
// core-events whose administration and delivery are restricted to its
// owning component — the consumer-level analogue of PlatformBuckets(). The
// board item named the crypto-shred pair ("either shred worker's durable");
// this registry also covers the other static, package-const-named
// consumers on the SAME stream that guard an equivalently silent,
// equivalently irreversible security outcome — PII nullification after a
// shred (Refractor) and auth-plane materialization (Gateway). It
// deliberately excludes non-security core-events consumers with the
// identical mechanical exposure (bridge-external, the object-store
// byte-janitor/cascade, chronicler's history projector, every loom/weaver
// per-domain consumer) — those are reliability/data-integrity concerns, not
// this fire's security-boundary scope; filed separately. Every name below
// is verified wired with its package default in production (cmd/processor,
// cmd/refractor, cmd/gateway pass no override), so nothing legitimate runs
// under a name outside this list.
type coreEventsProtectedConsumer struct {
	name  string
	owner string
}

var coreEventsProtectedConsumers = []coreEventsProtectedConsumer{
	{name: "privacy-worker", owner: "processor"},
	{name: "privacy-worker-retention", owner: "processor"},
	{name: "refractor-keyshredded", owner: "refractor"},
	{name: "refractor-classkeyshredded", owner: "refractor"},
	{name: "gateway-revocation", owner: "gateway"},
	{name: "gateway-credential-bindings", owner: "gateway"},
}

// coreEventsAdminDenies returns the JetStream consumer-admin verbs on a
// protected core-events consumer denied to EVERY component, owner included
// — none of these six is ever programmatically deleted, reset, or paused by
// its own owner in production (verified: no .Reset/.Remove call site names
// any of them, and they are built via substrate.RunDurableConsumer /
// ConsumerSupervisor.Add, neither of which self-heals by delete-recreate
// unless a caller explicitly asks). DELETE is the board item's literal
// vector — dropping the durable outright. RESET achieves the identical
// silent-suppression effect with no delete call at all: the JetStream
// server treats a DeliverAll consumer's reset target as always allowed
// (server/consumer.go's resetStartingSeqLocked), so any $JS.API.> holder
// could otherwise jump the cursor past pending shred/revocation/binding
// events. PAUSE is an indefinite native-JetStream delivery halt this
// codebase never uses for real — ConsumerSupervisor.Pause/Resume, the only
// "pause" concept here, is a pure in-process flag that never touches the
// NATS pause verb — so denying it costs no component a legitimate
// operation. UNPIN is inert without a priority group (none of these
// consumers use one, so it always errors) but costs nothing to close too.
// LEADER.STEPDOWN is the clustered analogue of PAUSE — it forces a consumer
// re-election and the delivery gap that comes with it. Like UNPIN it is inert
// on a single server (503 without clustering) and no code here invokes it, so
// it closes for free and is already closed if this deployment is ever
// clustered.
func coreEventsAdminDenies(stream, name string) []string {
	return []string{
		"$JS.API.CONSUMER.DELETE." + stream + "." + name,
		"$JS.API.CONSUMER.RESET." + stream + "." + name,
		"$JS.API.CONSUMER.PAUSE." + stream + "." + name,
		"$JS.API.CONSUMER.UNPIN." + stream + "." + name,
		"$JS.API.CONSUMER.LEADER.STEPDOWN." + stream + "." + name,
	}
}

// coreEventsOwnerOnlyDenies returns the verbs denied to every component
// EXCEPT a protected consumer's owner — the owner needs both at runtime
// (CREATE at first boot; MSG.NEXT to pull its own backlog), so these cannot
// be universal like coreEventsAdminDenies.
//
// CREATE: nats.go's CreateOrUpdateConsumer, when a FilterSubject is set (as
// all six of these consumers are), issues CONSUMER.CREATE.<stream>.<name>.
// <filterSubject> — confirmed against the pinned nats.go v1.52.0
// (jetstream/consumer.go's apiConsumerCreateWithFilterSubjectT), so the
// wildcarded form below is required, mirroring the bridge DIRECT.GET
// bare-vs-wildcard lesson: NATS' `>` needs at least one token after the
// prefix, so the bare subject and the wildcarded one are BOTH required, not
// either/or. The legacy DURABLE.CREATE endpoint (nats-server's
// JSApiDurableCreateT) reaches the same consumer via a different subject
// family and is closed too. Any of the three, sent as an "update" against
// an already-existing durable, can silently repoint FilterSubject or add a
// MaxDeliver cap without ever calling DELETE — the same hijack-by-update
// nats-server's checkNewConsumerConfig does not reject for FilterSubject.
//
// MSG.NEXT: the pull-fetch subject (nats-server's JSApiRequestNextT). All
// six consumers are PULL consumers — RunDurableConsumer and
// ConsumerSupervisor both call jetstream.Consumer.Messages(), never a push
// DeliverSubject — so a component holding $JS.ACK.> (granted matrix-wide)
// could otherwise fetch-and-ack the owner's own pending messages: a
// steal-and-ack that starves the real consumer without touching an admin
// verb at all.
func coreEventsOwnerOnlyDenies(stream, name string) []string {
	return append([]string{
		"$JS.API.CONSUMER.CREATE." + stream + "." + name,
		"$JS.API.CONSUMER.CREATE." + stream + "." + name + ".>",
		"$JS.API.CONSUMER.DURABLE.CREATE." + stream + "." + name,
		"$JS.API.CONSUMER.MSG.NEXT." + stream + "." + name,
	}, coreEventsAckDenies(stream, name)...)
}

// coreEventsAckDenies returns the ack-subject family of a protected consumer
// — the fifth inbound path into it, alongside the four verbs
// coreEventsOwnerOnlyDenies names, and owner-scoped for the same reason: the
// owner publishes an ack for its own durable on every delivery, so this
// cannot be universal like coreEventsAdminDenies.
//
// What the deny is defending is control of the durable, not confidentiality
// of the stream: core-events keeps MSG.GET, DIRECT.GET and INFO open by
// design (see protectedStreamDenies), so any $JS.API.> holder can already
// read the events by creating a consumer under a name of its own. What it
// cannot otherwise do is act on THIS consumer's queue — and the ack subject
// is where that is reachable.
//
// Both halves of it are. A bare "+ACK"/"+TERM" is silent suppression of a
// pending shred, revocation or credential-binding event, with no admin verb
// touched. And a body of "+NXT" makes the server ack the referenced message
// and then run processNextMsgRequest, delivering the consumer's next message
// to the PUBLISHER's own reply subject with no check on who published
// (nats-server v2.14.0 server/consumer.go:2716, :2736-2738) — a steal that
// starves the real owner, and $JS.API.CONSUMER.MSG.NEXT by another door
// against consumers the deny above closes MSG.NEXT on. "+NXT" is pull-only:
// against a push consumer processNextMsgRequest answers 409 (:4499-4502).
// All six protected consumers are pull with explicit ack
// (internal/substrate/consumer.go:210-215), so it reaches every one of them.
//
// Both wire forms are listed because a consumer whose AckPolicy is neither
// AckNone nor AckFlowControl subscribes BOTH, unconditionally
// (server/consumer.go:1699-1707). The js_ack_fc_v2 feature flag
// (server/feature_flags.go:35, default false) only selects which form the
// server STAMPS on delivered messages; it does not gate the v2 subscription,
// so the v2 subject is live inbound in this deployment even though nothing
// here is stamped with it.
//
//	v1: $JS.ACK.<stream>.<consumer>.*.*.*.*.*   (server/consumer.go:1388-1390)
//	v2: $JS.ACK.<domain>.<accHash>.<stream>.<consumer>.*.*.*.*.>
//	                                            (server/consumer.go:1395-1398)
//
// The v2 form is wildcarded on its first two tokens because both are runtime
// values a static conf cannot name: <domain> is the configured JetStream
// domain, or the literal "_" when none is set (server/consumer.go:1377-1379),
// and <accHash> is an 8-character base-62 SHA-256 of the account name
// (server/consumer.go:1381; server/events.go:1151-1163). Wildcards over-deny
// nothing real: matching a v1 ack subject would require its five trailing
// tokens to begin with <stream>.<consumer>, and those tokens are always the
// numeric sequence/delivery counters the server formats in
// (server/consumer.go:1389).
//
// No bare-subject deny is needed, unlike the DIRECT.GET denies on the core-kv
// backing stream: both subscriptions require at least five trailing tokens,
// so "$JS.ACK.<stream>.<consumer>" with nothing after it is not a live inbound
// path at all.
func coreEventsAckDenies(stream, name string) []string {
	return []string{
		"$JS.ACK." + stream + "." + name + ".>",
		"$JS.ACK.*.*." + stream + "." + name + ".>",
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
// response is the same denied subject). It is genuinely protocol plumbing:
// a flow-control ack carries an empty body and moves no data.
//
// $JS.ACK.> is NOT the same kind of grant, and is not universal. It is a
// data-plane privilege: "+NXT" on an ack subject reads the next message off
// the consumer into the publisher's own reply subject (see
// coreEventsAckDenies), so a holder reaches any ack-policy consumer whose
// subject it can name. The components that run durable consumers hold it and
// are narrowed per-consumer by the protected-consumer denies (Deny); the
// vertical-app tier holds no ack grant at all.
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
//
// WHAT A DENY BELOW IS WORTH — every deny below, the $JS.ACK.* family
// included, is self-service defeasible by every component in this matrix.
// The governing fact is not a grant any row carries: a message published by
// the server's own internal JetStream client carries perms == nil, so the
// permission branch never runs (server/client.go:4280-4287, gated on
// c.perms != nil), and the reserved-reply guard never runs either, because
// it is gated on c.kind == CLIENT (:4305-4307). Two routes reach that
// publisher, and between them they need no grant a component can be stripped
// of:
//
//  1. Name a denied subject as the REPLY of a request. Any $JS.API.* verb
//     answers there. DIRECT.GET is the sharp one — it returns the stored
//     bytes verbatim, and the caller chose those bytes by first writing them
//     to its own health-kv key, which SharedWrite grants every non-bootstrap
//     component. Probed in both DIRECT.GET wire forms as clinic-app, which
//     carries NO AllowResponses, onto ops.default and onto arbitrary core-kv
//     and capability-kv keys (TestReplySubjectWriteAuthority). A component
//     with no $JS.API.> at all still reaches this: a plain JetStream PubAck
//     is the same internal publisher, so facet — the narrowest row here —
//     lands PubAck JSON on a denied core-kv key. What a component loses
//     without $JS.API.> is CONTENT CONTROL, not the write.
//
//  2. Point a stream's RePublish destination at the denied subject. The
//     bytes arrive with dest as their OWN subject and no reply involved, so
//     this reaches even the $JS.ACK.* family: probed, a mirror of
//     KV_health-kv republishing to a literal $JS.ACK.core-events.<consumer>
//     subject forges an ack and clears a protected consumer's pending
//     delivery, with a harmless dest as the negative control. No subject
//     deny can reach it — the mirror source is in the request BODY, so
//     STREAM.CREATE.<attacker-chosen-name> is always subject-allowed. This
//     is the CONSUMER.CREATE body-vs-subject hijack shape below, one verb
//     over.
//
// AllowResponses is a third and much narrower route, and not the governing
// one: a static deny is checked first and the client's dynamic response
// permissions consulted only if the subject was not allowed
// (server/client.go:4120-4141), and a delivery registers its reply subject
// as a response permission precisely WHEN the client is denied on it
// (:3881-3884), so the six carrying the flag (refractor, loom, weaver,
// bridge, model-runner, gateway) can also self-serve by receiving their own
// message through a wildcard subscribe. That route needs the flag, the
// subscribe and an inbound delivery, and it yields one bounded registration
// (MaxMsgs 1, 2 minutes; server/const.go:228,232). Routes 1 and 2 need none
// of that.
//
// What IS true of the $JS.ACK.* family: a CLIENT publish whose reply is
// prefixed $JS.ACK. is refused by isReservedReply (:4215-4226, called at
// :4305-4307, after the publish-subject permission check at :4281), so
// route 1 cannot reach an ack subject. Route 2 can, which is why the ack
// denies are no longer an exception to anything.
//
// A DeliverSubject is NOT a route here, and a narrowing aimed at
// CONSUMER.CREATE would buy nothing. A push or pull delivery is re-subjected
// for routing only: the capturing stream's subscription does fire and the
// message IS ingested, but under its ORIGINAL subject
// (server/stream.go:8058-8060, client.go:5088-5092), so a consumer filtered
// on the lane never sees it. What that costs is stream capacity, not
// authority (TestPushConsumerDeliverSubjectDoesNotReachOpsLane pins both
// halves, with the delivery itself as the positive control). The exception
// is a pull consumer's STATUS frame, which is an ordinary server publish and
// does reach the reply subject.
//
// So read a deny below as an honest description of a component's ordinary
// client paths, and as no barrier at all to one that goes looking — for
// every row, facet included. Closing the class needs authority the subject
// matrix cannot express; the grounding, why no narrowing here closes it, and
// the candidate remedy are in
// implementation-artifacts/protected-consumer-ack-plane-denies-design.md §8.
//
// The same precedent applies to core-events even though it is a plain
// stream outside the PlatformBuckets() registry, not a KV bucket: bootstrap
// primordially provisions it too (internal/bootstrap/primordial.go), so no
// component — including the Processor, whose outbox consumer only ever
// PUBLISHES into it — needs $JS.API stream administration over it. Applied
// matrix-wide (owner included, same as the registry loop above), plus the
// consumer-level denies for coreEventsProtectedConsumers (owner-scoped: see
// coreEventsAdminDenies / coreEventsOwnerOnlyDenies).
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
	deny = append(deny, protectedStreamDenies(bootstrap.CoreEventsStreamName)...)
	// The legacy single-wildcard create endpoint ($JS.API.CONSUMER.CREATE.
	// <stream>, nats-server's JSApiConsumerCreate) carries no consumer name
	// in the subject at all — the name comes from the request body — so it
	// cannot be closed per-consumer like coreEventsOwnerOnlyDenies below;
	// it is denied here, matrix-wide, for every non-bootstrap component
	// including every protected consumer's own owner. Safe universally:
	// no production code in this repo uses it (every consumer here is
	// created through nats.go's named jetstream.CreateOrUpdateConsumer,
	// never the legacy JetStreamContext ephemeral path).
	deny = append(deny, "$JS.API.CONSUMER.CREATE."+bootstrap.CoreEventsStreamName)
	for _, pc := range coreEventsProtectedConsumers {
		deny = append(deny, coreEventsAdminDenies(bootstrap.CoreEventsStreamName, pc.name)...)
		if c.Name != pc.owner {
			deny = append(deny, coreEventsOwnerOnlyDenies(bootstrap.CoreEventsStreamName, pc.name)...)
		}
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
		// svc.model.> — the request side of the model-runner call (natural-
		// language-weaver-targets-design.md §3.1). The bridge is the caller,
		// so it needs publish on the runner's subject space; the runner
		// answers via its own allow_responses. This grants no write on
		// model-results: the runner is that bucket's sole writer (registry-
		// derived), the bridge only reads results, and a consumed ref is
		// reaped by the bucket's per-key TTL rather than deleted by the
		// reader.
		ExtraPubAllow: []string{bootstrap.OpsWildcardSubject, bootstrap.SchedulesWildcardSubject, "$O.core-objects.>", "$JS.API.>", "$JS.ACK.>", "lattice.vault.decryptref", "lattice.op.status", "svc.model.>"},
		// The registry-derived denies (Deny) cover the core-kv/capability-kv
		// WRITE side (the $KV.<b>.> publish subject + every registered bucket's
		// backing-stream admin verbs). The two extra denies below close the
		// READ side of the same grant's blast radius (design §For-Andrew #1/§8,
		// adversarial finding B2): the broad $JS.API.> grant every component
		// holds admits $JS.API.DIRECT.GET / STREAM.MSG.GET requests — a
		// JetStream KV read, not a write, and not covered by the registry deny
		// loop — so a decrypt-RPC-holding bridge could otherwise reach the whole
		// core-kv corpus via the backing-stream side channel. Denying them here
		// closes the CORE-KV read side channel specifically; the bridge's overall
		// read set remains whatever $JS.API.> reaches minus these denies — which
		// legitimately includes its lens read-models (privacy-pii-key-envelopes
		// for the egress unwrap, capability-author-context for the authoring
		// catalog) and the model-results bucket it polls. The guarantee is
		// scoped precisely: these denies close the core-KV read side channel;
		// core-EVENTS reads remain open (protectedStreamDenies does not deny
		// MSG.GET/DIRECT.GET on the core-events stream — a pre-existing posture,
		// not this change's concern), so this is not a claim that all Core state
		// is unreadable, only that the core-KV bucket is
		// (TestCapabilityAuthorCatalogAccess pins the capability-author-context
		// direction).
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
		Name: "model-runner",
		Desc: "external-model egress; holds the vendor credential, serves svc.model.> in a queue group, writes only model-results + health-kv",
		// $JS.API.> is the whole publish surface this component needs beyond
		// its registry-derived bucket grant: opening the model-results KV
		// handle is a STREAM.INFO, and reading a result back is a DIRECT.GET.
		// Deliberately absent, each for a reason rather than an oversight:
		//   - no ops.> — the runner submits no operations and touches no Core
		//     KV at all; a model call is not a state change (P2 stays whole).
		//   - no $JS.ACK.> — it runs no JetStream consumer. Its work arrives
		//     as micro request/reply, never as a durable delivery, so there is
		//     no ack lane to grant.
		//   - no publish grant on svc.model.> — it is the RESPONDER, not the
		//     caller. It subscribes (reads are unrestricted) and replies
		//     through allow_responses, exactly like the Weaver's control
		//     plane; the bridge is the one that needs the publish side.
		ExtraPubAllow:  []string{"$JS.API.>"},
		AllowResponses: true, // micro responder (svc.model.> + $SRV.> discovery)
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
	// The four vertical-app rows below hold no $JS.ACK.> grant, deliberately
	// rather than by oversight: none of them runs a JetStream consumer. Their
	// whole NATS surface is substrate.Connect, a token-revocation KV read
	// (internal/gateway/revocation), key listings, and the health-kv heartbeat.
	//
	// The listing path is the one that has to be traced rather than assumed.
	// substrate.KVListKeys holds a jetstream.KeyValue (substrate/conn.go:502),
	// and that package's watcher builds its consumer through the LEGACY
	// push-subscribe API — nats.OrderedConsumer() as a nats.SubOpt (nats.go
	// v1.52.0 jetstream/kv.go:1304-1305) — which forces FlowControl: true and
	// AckPolicy: AckNonePolicy (js.go:1780-1781). Two consequences, and the
	// second is the one that matters here: an AckNone consumer gets no ack
	// subscription on the server at all (nats-server v2.14.0
	// server/consumer.go:1699), so there is no ack lane to grant; and what the
	// listing genuinely depends on is $JS.FC.>, which every component keeps.
	//
	// The grant would therefore buy these four nothing, while handing the tier
	// the platform already decided not to trust with ops.> an unscoped "+NXT"
	// read on every consumer in the deployment (coreEventsAckDenies).
	{
		Name: "loftspace-app",
		Desc: "vertical app (P5 reader); writes go browser-direct through the Gateway — holds NO core-operations (ops.>) publish (#75 Fire 2b), which closes the app's ordinary publish path onto the lane but NOT env.Actor forgery — a server-published reply, or a stream RePublish, reaches ops.> anyway, as it does for every row here (see Deny)",
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
		ExtraPubAllow: []string{"$O.core-objects.>", "$JS.API.>", "lattice.vault.wrapkey", "lattice.vault.unwrapkey"},
	},
	{
		Name:          "clinic-app",
		Desc:          "vertical app (P5 reader); writes go browser-direct through the Gateway — holds NO core-operations (ops.>) publish (#75 Fire 2b), which closes the app's ordinary publish path onto the lane but NOT env.Actor forgery — a server-published reply, or a stream RePublish, reaches ops.> anyway, as it does for every row here (see Deny)",
		ExtraPubAllow: []string{"$JS.API.>"},
	},
	{
		Name:          "cafe-app",
		Desc:          "vertical app (P5 reader); writes go browser-direct through the Gateway — holds NO core-operations (ops.>) publish (#75 Fire 2b), which closes the app's ordinary publish path onto the lane but NOT env.Actor forgery — a server-published reply, or a stream RePublish, reaches ops.> anyway, as it does for every row here (see Deny)",
		ExtraPubAllow: []string{"$JS.API.>"},
	},
	{
		Name:          "wellness-app",
		Desc:          "vertical app (P5 reader); writes go browser-direct through the Gateway — holds NO core-operations (ops.>) publish (#75 Fire 2b), which closes the app's ordinary publish path onto the lane but NOT env.Actor forgery — a server-published reply, or a stream RePublish, reaches ops.> anyway, as it does for every row here (see Deny)",
		ExtraPubAllow: []string{"$JS.API.>"},
	},
	{
		Name: "facet",
		Desc: "edge showcase app host — health-plane-only platform credential; " +
			"per-identity engine traffic stays on the natsauth callout connections",
		// No ExtraPubAllow at all: Allow() derives $KV.health-kv.> (SharedWrite)
		// + $JS.FC.>; KVPutWithTTL needs nothing else (no $JS.API.>, no KV-handle
		// open). The narrowest user in the matrix, by design — this host fronts
		// the hosted demo (facet-host-health-emission-design.md §4.1, ratified A2).
		SubscribeAllow: []string{"_INBOX.>"},
	},
}

// WebsocketPort is the port the WebSocket listener binds. Explicit, never a
// default: NATS's own default (8080) collides with the Gateway.
const WebsocketPort = 9222

// WebsocketAllowedOrigins is the browser Origin allow-set for the WS
// handshake — the dev hosts that serve the PWA. It is a second, independent
// origin surface from the Gateway's CORS allow-set (which gates the PWA's
// HTTP writes and never sees the WS handshake).
//
// It must never render empty: NATS treats an empty allowed_origins as
// allow-any-origin, so an empty list is fail-open. TestWebsocketConfigured
// pins non-emptiness structurally. That fail-open default is also why this is
// a static list rather than an env-var expansion in the conf: an unset
// variable would silently render the allow-any shape.
//
// The origin gate is CSRF-class hardening for browser-initiated connects, not
// the trust boundary — a non-browser client sends no Origin header and NATS
// accepts it (RFC 6455 §1.6). The bearer token remains the authn.
//
// These track cmd/facet's FACET_HTTP_ADDR default: serving the PWA from a
// non-default address needs a matching entry here plus a
// `go run ./deploy/gen-dev-nkeys`, or its WS handshake draws a bare 403.
var WebsocketAllowedOrigins = []string{
	"http://localhost:7810",
	"http://127.0.0.1:7810",
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

# websocket (edge-browser-node-design.md §3.1) — the browser Edge node's
# transport. A native NATS listener, not a bridge component: WS clients are
# authorized by the same authorization block + auth_callout below, which
# derive every subject from the verified token and never see the listener
# type. no_tls is DEV-ONLY; production ships a tls{} block here.
websocket {
  port: ` + fmt.Sprintf("%d", WebsocketPort) + `
  no_tls: true
  allowed_origins: [` + quoteList(WebsocketAllowedOrigins) + `]
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
		if c.SubscribeAllow != nil {
			b.WriteString("        subscribe { allow: [" + quoteList(c.SubscribeAllow) + "] }\n")
		} else {
			b.WriteString("        subscribe { allow: [\">\"] }\n")
		}
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
