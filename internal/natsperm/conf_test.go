package natsperm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/nats-io/nkeys"
	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/jsstore"
	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// deniedTimeout bounds a publish we expect the server to reject: a denied
// JetStream publish receives no PubAck (the permissions violation is delivered
// out-of-band on the connection), so the Put blocks until its context expires.
// The owner's positive write on the same bucket returns promptly, so a timeout
// here means "the write was rejected" — the only variable between the owner and
// the rogue is the connection's permission set. The denial itself is enforced
// synchronously by the embedded, loopback-only server before any store I/O —
// nothing ever arrives late — so this only needs to clear real scheduling
// jitter, not network latency; 500ms leaves a wide margin over that.
const deniedTimeout = 500 * time.Millisecond

// repoRoot walks up from this test file to the module root (the dir holding
// go.mod), so the test finds deploy/ regardless of the working directory.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// internal/natsperm/conf_test.go -> repo root is two dirs up.
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
}

func confPath(t *testing.T) string { return filepath.Join(repoRoot(t), "deploy", "nats-server.conf") }
func seedPath(t *testing.T, c string) string {
	return filepath.Join(repoRoot(t), "deploy", "nkeys", c+".nk")
}

// startServerFromConf loads the committed production transport-auth config and
// runs it as an embedded JetStream server, overriding only the port/store so the
// test is parallel-safe. The authorization block (per-component NKey users +
// permissions) is taken verbatim from deploy/nats-server.conf — this is what
// makes the test a proof of the real artifact, not a hand-built fixture.
func startServerFromConf(t *testing.T) string {
	t.Helper()
	url, _ := startServerFromConfDual(t)
	return url
}

// startServerFromConfDual is startServerFromConf returning both listeners' dial
// URLs: the TCP one every component connects over, and the WebSocket one the
// browser Edge node connects over (edge-browser-node-design.md §3.1). The Edge
// auth vectors run over both to prove the callout is transport-invariant.
func startServerFromConfDual(t *testing.T) (tcpURL, wsURL string) {
	t.Helper()
	opts, err := server.ProcessConfigFile(confPath(t))
	if err != nil {
		t.Fatalf("parse deploy/nats-server.conf: %v", err)
	}
	if len(opts.Nkeys) == 0 {
		t.Fatal("config parsed but defined no NKey users")
	}
	opts.Port = -1
	// The WS listener must come from the committed conf, not from this harness:
	// overriding the port unconditionally would happily fabricate a listener the
	// real artifact never declares, and every WS vector would then pass against
	// a server the test invented. Assert the conf declared one before touching
	// it, so this helper's "proof of the real artifact" claim holds for the WS
	// half too.
	if opts.Websocket.Port == 0 {
		t.Fatal("deploy/nats-server.conf declares no websocket listener — the WS vectors would prove nothing; regenerate with `go run ./deploy/gen-dev-nkeys`")
	}
	// The conf binds a fixed port (9222); every server this package starts must
	// take an ephemeral one instead or parallel tests collide on it — the same
	// parallel-safety reason opts.Port is overridden above. -1 makes the server
	// pick a free port and write it back into opts under the same lock the
	// readiness check reads, and the fixture does not return until the listener
	// is bound, so the port below is settled and race-free.
	opts.Websocket.Port = -1
	opts.JetStream = true
	opts.StoreDir = jsstore.Dir(t)
	opts.NoLog = true
	opts.NoSigs = true
	// deploy/nats-server.conf sets no auth_timeout, so nats-server defaults
	// to 2s (no TLS). Under CI CPU contention (this package's 32
	// t.Parallel() tests plus sibling packages sharing the runner), a
	// slow-but-correct auth-callout round trip can exceed 2s and the
	// server closes the connection as an Authorization Violation before
	// the test ever exercises the permission model. Test-only override —
	// deploy/nats-server.conf itself is untouched.
	opts.AuthTimeout = 10
	s := natsfixture.StartServerWith(t, opts)
	return s.ClientURL(), fmt.Sprintf("ws://127.0.0.1:%d", opts.Websocket.Port)
}

// connectAs opens an authenticated connection using a component's committed dev
// NKey seed.
func connectAs(t *testing.T, url, component string) *substrate.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	c, err := substrate.Connect(ctx, substrate.ConnectOpts{
		URL:          url,
		Name:         component + "-conformance",
		NKeySeedFile: seedPath(t, component),
	})
	if err != nil {
		t.Fatalf("connect as %q: %v", component, err)
	}
	t.Cleanup(c.Close)
	return c
}

// provision creates a plain KV bucket as the bootstrap provisioner — mirroring
// the kernel-seed path that creates every bucket before components connect.
func provision(t *testing.T, c *substrate.Conn, bucket string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := c.JetStream().CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: bucket}); err != nil {
		t.Fatalf("provision bucket %q as bootstrap: %v", bucket, err)
	}
}

// provisionObjectStore creates the object store as the bootstrap provisioner
// (bootstrap holds $O.> + $JS.API.>) — mirroring provision for the object
// plane (object-plane-nats-permissions-design.md §5).
func provisionObjectStore(t *testing.T, c *substrate.Conn, bucket string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := c.JetStream().CreateObjectStore(ctx, jetstream.ObjectStoreConfig{Bucket: bucket}); err != nil {
		t.Fatalf("provision object store %q as bootstrap: %v", bucket, err)
	}
}

// provisionStream creates a plain JetStream stream (not a KV/Object bucket) as
// the bootstrap provisioner — mirroring provision/provisionObjectStore for the
// plain-subject plane (ops.> lanes, the Personal Lens sync stream).
func provisionStream(t *testing.T, c *substrate.Conn, name string, subjects []string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := c.JetStream().CreateStream(ctx, jetstream.StreamConfig{Name: name, Subjects: subjects}); err != nil {
		t.Fatalf("provision stream %q: %v", name, err)
	}
}

// assertDeniedPublish is assertDeniedPuts' plain-subject analogue: asserts none
// of components can JetStream-publish to subject.
func assertDeniedPublish(t *testing.T, url, subject string, components []string) {
	t.Helper()
	for _, component := range components {
		component := component
		t.Run("denied/"+subject+"/"+component, func(t *testing.T) {
			t.Parallel()
			c := connectAs(t, url, component)
			ctx, cancel := context.WithTimeout(context.Background(), deniedTimeout)
			defer cancel()
			if err := c.Publish(ctx, subject, []byte("forged"), nil); err == nil {
				t.Errorf("%s Publish %q: want transport denial, got success", component, subject)
			}
		})
	}
}

// assertDeniedPuts asserts that none of the components can publish to a
// protected bucket. Each is a parallel subtest so the per-component denial
// timeouts (a denied publish blocks until its context expires) overlap rather
// than accumulate. A denied write is rejected at the transport (no PubAck),
// surfacing as a context deadline within deniedTimeout.
func assertDeniedPuts(t *testing.T, url, bucket string, components []string) {
	t.Helper()
	for _, component := range components {
		component := component
		t.Run("denied/"+bucket+"/"+component, func(t *testing.T) {
			t.Parallel()
			c := connectAs(t, url, component)
			ctx, cancel := context.WithTimeout(context.Background(), deniedTimeout)
			defer cancel()
			if _, err := c.KVPut(ctx, bucket, "rogue.key", []byte("forged")); err == nil {
				t.Errorf("%s KVPut %q: want transport denial, got success", component, bucket)
			}
		})
	}
}

// TestConfigParses is the cheap first line of defense: the committed config must
// parse and define one NKey user per deploy/nkeys seed.
func TestConfigParses(t *testing.T) {
	opts, err := server.ProcessConfigFile(confPath(t))
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	if got, want := len(opts.Nkeys), 18; got != want {
		t.Errorf("NKey users = %d, want %d", got, want)
	}
	// Every user must carry an explicit publish allow-list (default-deny on
	// everything else); a user with no publish permissions is a config slip.
	for _, u := range opts.Nkeys {
		if u.Permissions == nil || u.Permissions.Publish == nil || len(u.Permissions.Publish.Allow) == 0 {
			t.Errorf("nkey %s: missing publish allow-list", u.Nkey)
		}
	}
}

// TestAuthCalloutConfigured pins the auth_callout block's presence and — per
// per-identity-nats-subscribe-acl-design.md §7 ("xkey payload encryption is
// enabled from day one, not a deferred hardening pass") — that xkey is set,
// so a future regeneration that drops it (e.g. reverting to the pre-xkey
// gen-dev-nkeys shape) fails loudly here instead of silently reopening the
// bearer-token-in-cleartext gap the xkey condition closed.
func TestAuthCalloutConfigured(t *testing.T) {
	opts, err := server.ProcessConfigFile(confPath(t))
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	ac := opts.AuthCallout
	if ac == nil {
		t.Fatal("auth_callout block is absent from the committed conf")
	}
	if !nkeys.IsValidPublicAccountKey(ac.Issuer) {
		t.Errorf("auth_callout.issuer = %q, want a valid public ACCOUNT key", ac.Issuer)
	}
	if !nkeys.IsValidPublicCurveKey(ac.XKey) {
		t.Errorf("auth_callout.xkey = %q, want a valid public CURVE key (day-one encryption, design §7)", ac.XKey)
	}
	if len(ac.AuthUsers) != 18 {
		t.Errorf("auth_callout.auth_users = %d entries, want 18 (every component bypasses the callout)", len(ac.AuthUsers))
	}
}

// TestWebsocketConfigured pins the websocket block's shape (edge-browser-node-design.md
// §3.1) — the browser Edge node's listener. The load-bearing assertion is the
// origins one: NATS treats an empty allowed_origins as ALLOW-ANY-ORIGIN, so a
// regeneration that drops the list does not fail loudly, it silently opens the
// handshake to every origin. Pinning non-emptiness here makes that fail-open
// vendor default structurally unreachable.
func TestWebsocketConfigured(t *testing.T) {
	opts, err := server.ProcessConfigFile(confPath(t))
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	ws := opts.Websocket
	if ws.Port != WebsocketPort {
		t.Errorf("websocket.port = %d, want %d (explicit — NATS's own 8080 default collides with the Gateway)", ws.Port, WebsocketPort)
	}
	if len(ws.AllowedOrigins) == 0 {
		t.Error("websocket.allowed_origins is empty — NATS reads that as allow-ANY-origin (fail-open); the conf must always render an explicit list")
	}
	if !ws.NoTLS {
		t.Error("websocket.no_tls = false but the conf ships no tls block — the server would fail to start")
	}
}

// TestWebsocketOriginEnforced is TestWebsocketConfigured's behavioral half.
// The config-shape pin proves the allow-list is non-empty; it cannot prove the
// server ENFORCES it — a websocket block in the wrong scope, or an origins key
// the vendor stops honoring, would satisfy the shape pin while every origin
// sailed through. This drives the real handshake against the real committed
// conf instead.
//
// The no-Origin case is not an oversight, it is the documented vendor contract
// (RFC 6455 §1.6 — a non-browser client can forge any origin, so NATS accepts a
// missing one): it is what lets the Go node and these tests dial ws:// at all,
// and it is why the origin gate is CSRF-class hardening for browsers rather
// than the trust boundary. The bearer token is the boundary, and none of these
// handshakes carries one — an accepted upgrade here is not an authorized
// session; the callout still runs on CONNECT.
func TestWebsocketOriginEnforced(t *testing.T) {
	t.Parallel()
	if len(WebsocketAllowedOrigins) == 0 {
		t.Fatal("no allowed origins to exercise — see TestWebsocketConfigured for the real diagnosis")
	}
	_, wsURL := startServerFromConfDual(t)
	endpoint := strings.Replace(wsURL, "ws://", "http://", 1)
	// Bounded: a wedged handshake must fail this test, not hang until the
	// package-level go-test timeout.
	client := &http.Client{Timeout: 5 * time.Second}

	// Handshake with a bad Origin must be refused before any NATS protocol.
	for _, tc := range []struct {
		name   string
		origin string
		want   int
	}{
		{"disallowed-origin", "http://evil.example.com", http.StatusForbidden},
		{"allowed-origin", WebsocketAllowedOrigins[0], http.StatusSwitchingProtocols},
		{"no-origin-header", "", http.StatusSwitchingProtocols},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req, err := http.NewRequest(http.MethodGet, endpoint, nil)
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			req.Header.Set("Connection", "Upgrade")
			req.Header.Set("Upgrade", "websocket")
			req.Header.Set("Sec-WebSocket-Version", "13")
			// Any 16-byte base64 value; the server echoes a derived accept key.
			req.Header.Set("Sec-WebSocket-Key", base64.StdEncoding.EncodeToString(make([]byte, 16)))
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("websocket handshake: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Errorf("handshake with origin %q: status = %d, want %d", tc.origin, resp.StatusCode, tc.want)
			}
		})
	}
}

// TestCoreKVWriteIsolation: only the processor (and the bootstrap provisioner)
// may write Core KV; every other component — including refractor, which holds a
// broad $KV.> grant but an explicit $KV.core-kv.> deny — is rejected.
func TestCoreKVWriteIsolation(t *testing.T) {
	t.Parallel()
	url := startServerFromConf(t)

	boot := connectAs(t, url, "bootstrap")
	provision(t, boot, "core-kv")

	// Owner write succeeds — proves the bucket exists and writes work, so the
	// rogue failures below are permission-based, not bucket-absence.
	proc := connectAs(t, url, "processor")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := proc.KVPut(ctx, "core-kv", "vtx.test.1.x", []byte("v")); err != nil {
		t.Fatalf("processor KVPut core-kv: want success, got %v", err)
	}

	assertDeniedPuts(t, url, "core-kv", []string{"refractor", "loom", "weaver", "bridge", "loupe", "lattice", "gateway", "loftspace-app", "clinic-app", "object-store-manager", "chronicler"})
}

// TestCapabilityKVWriteIsolation: only refractor (and bootstrap) may write the
// auth projection; even the processor — the Core-KV owner — is denied.
func TestCapabilityKVWriteIsolation(t *testing.T) {
	t.Parallel()
	url := startServerFromConf(t)

	boot := connectAs(t, url, "bootstrap")
	provision(t, boot, "capability-kv")

	ref := connectAs(t, url, "refractor")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := ref.KVPut(ctx, "capability-kv", "cap.test", []byte("v")); err != nil {
		t.Fatalf("refractor KVPut capability-kv: want success, got %v", err)
	}

	assertDeniedPuts(t, url, "capability-kv", []string{"processor", "loom", "weaver", "loupe", "lattice", "gateway", "chronicler"})
}

// TestGatewayCapabilityKVReadAccess proves the read-side complement to
// TestCapabilityKVWriteIsolation's gateway write-deny pin above (unchanged):
// the Gateway's whoami roles resolver (internal/gateway/rolesanchors,
// persona-worlds-design.md §10) reads the rbac-domain capabilityRoles
// projection out of capability-kv, and the Gateway's per-bucket deny list
// only closes stream-management/write subjects there (mirrors
// TestBridgeCoreKVReadIsolation's positive-control shape, bridge_egress_
// test.go:99) — $JS.API.> stays broadly allowed for the Gateway's publish
// set, so a KVGet reaches the store exactly like the refractor owner's.
func TestGatewayCapabilityKVReadAccess(t *testing.T) {
	t.Parallel()
	url := startServerFromConf(t)

	boot := connectAs(t, url, "bootstrap")
	provision(t, boot, "capability-kv")

	ref := connectAs(t, url, "refractor")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := ref.KVPut(ctx, "capability-kv", "cap.roles.identity.test", []byte(`{"roles":["vtx.role.test"]}`)); err != nil {
		t.Fatalf("refractor KVPut capability-kv: want success, got %v", err)
	}

	gw := connectAs(t, url, "gateway")
	if _, err := gw.KVGet(ctx, "capability-kv", "cap.roles.identity.test"); err != nil {
		t.Fatalf("gateway KVGet capability-kv: want success, got %v", err)
	}
}

// TestChroniclerOrchestrationHistoryWriteAccess: chronicler (the eventStream
// lens materializer) may write its own orchestration-history read model; a
// non-chronicler component cannot — the direct proof for
// chronicler-host-reconciliation's new matrix entry (only Chronicler writes
// this bucket, mirroring TestLensTargetWriteIsolation's refractor pin for
// weaver-targets). refractor is included in the denied roster post natsperm-
// matrix-hygiene Fire 1 — its broad $KV.> no longer reaches non-owned
// platform buckets.
func TestChroniclerOrchestrationHistoryWriteAccess(t *testing.T) {
	t.Parallel()
	url := startServerFromConf(t)

	boot := connectAs(t, url, "bootstrap")
	provision(t, boot, "orchestration-history")

	chr := connectAs(t, url, "chronicler")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := chr.KVPut(ctx, "orchestration-history", "instance_id.test", []byte("v")); err != nil {
		t.Fatalf("chronicler KVPut orchestration-history: want success, got %v", err)
	}

	assertDeniedPuts(t, url, "orchestration-history", []string{"refractor", "loom", "weaver", "loupe", "lattice", "gateway", "loftspace-app", "clinic-app"})
}

// TestLensTargetWriteIsolation: refractor (the sole projector) may write a
// lens-target read model; a non-projector cannot (it is not in its allow-list).
func TestLensTargetWriteIsolation(t *testing.T) {
	t.Parallel()
	url := startServerFromConf(t)

	boot := connectAs(t, url, "bootstrap")
	provision(t, boot, "weaver-targets")

	ref := connectAs(t, url, "refractor")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := ref.KVPut(ctx, "weaver-targets", "target.1", []byte("v")); err != nil {
		t.Fatalf("refractor KVPut weaver-targets: want success, got %v", err)
	}

	assertDeniedPuts(t, url, "weaver-targets", []string{"loom", "loupe", "lattice", "gateway", "weaver"})
}

// TestOpsSystemPublishAccess: refractor's keyshredded manager
// (internal/refractor/keyshredded, wired in cmd/refractor) and processor's
// co-located privacy-worker (internal/privacyworker, wired on the Processor's
// own connection in cmd/processor/main.go) both submit RecordShredFinalization
// to ops.system — a JetStream publish through the core-operations stream, so a
// transport denial surfaces as a store-ack timeout exactly like a denied
// KVPut. Neither grant existed before this fix (refractor-publish-acl-gap).
// chronicler is the pinned negative: its own matrix comment declares it
// "submits no ops" (P2 — a pure read-model materializer).
func TestOpsSystemPublishAccess(t *testing.T) {
	t.Parallel()
	url := startServerFromConf(t)

	boot := connectAs(t, url, "bootstrap")
	provisionStream(t, boot, bootstrap.CoreOpsStreamName, []string{bootstrap.OpsWildcardSubject})

	for _, component := range []string{"refractor", "processor"} {
		component := component
		t.Run("allowed/"+component, func(t *testing.T) {
			c := connectAs(t, url, component)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := c.Publish(ctx, "ops.system", []byte("{}"), nil); err != nil {
				t.Fatalf("%s Publish ops.system: want success, got %v", component, err)
			}
		})
	}

	assertDeniedPublish(t, url, "ops.system", []string{"chronicler"})
}

// TestVerticalAppOpsPublishDenied: the vertical apps write browser-direct through
// the Gateway (which authenticates the caller + strips/stamps the actor), so they
// hold NO core-operations (ops.>) publish — a compromised app cannot forge an
// env.Actor over the transport (#75 Fire 2b). All four vertical apps are closed:
// loftspace-app's executed-lease document is generated by the bridge's docGen
// externalTask flow and anchored by Weaver's AttachObject dispatch, so the app
// submits no operation (the Gateway is the only write door for apps).
func TestVerticalAppOpsPublishDenied(t *testing.T) {
	t.Parallel()
	url := startServerFromConf(t)
	assertDeniedPublish(t, url, "ops.default", verticalAppNames)
}

// verticalAppNames is the cmd/<x>-app tier — the P5 readers the platform does
// not trust with a write door. Spelled out rather than derived: the Matrix
// carries no "is a vertical app" field, and deriving the roster from the
// grants under test would make the vectors circular.
var verticalAppNames = []string{"clinic-app", "cafe-app", "loftspace-app", "wellness-app"}

// TestPersonalSyncPublishAccess: refractor's nats_subject Personal Lens
// adapter (internal/refractor/adapter/natssubject.go) publishes delta
// envelopes to lattice.sync.user.<actor> — latent (no lens installs one yet)
// but transport-reachable in code, and denied before this fix
// (refractor-publish-acl-gap). Only Refractor's Personal Lens pipeline ever
// publishes here.
func TestPersonalSyncPublishAccess(t *testing.T) {
	t.Parallel()
	url := startServerFromConf(t)

	boot := connectAs(t, url, "bootstrap")
	provisionStream(t, boot, "SYNC", []string{"lattice.sync.user.>"})

	ref := connectAs(t, url, "refractor")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ref.Publish(ctx, "lattice.sync.user.test-actor", []byte("{}"), nil); err != nil {
		t.Fatalf("refractor Publish lattice.sync.user.test-actor: want success, got %v", err)
	}

	assertDeniedPublish(t, url, "lattice.sync.user.test-actor", []string{"processor", "loom", "weaver", "bridge",
		"loupe", "lattice", "gateway", "loftspace-app", "clinic-app", "object-store-manager", "chronicler"})
}

// TestObjectStoreWriteAccess: the four legitimate object-plane writers
// (object-store-manager, loupe, loftspace-app, bridge) can actually ObjectPut
// into core-objects, and object-store-manager can ObjectDelete what it put —
// the positive matrix pin (object-plane-nats-permissions-design.md §5). The
// bridge writes as the docGen reference vendor adapter (the rendered
// executed-lease artifact's bytes — inert until an AttachObject op anchors
// them); loupe + loftspace-app are the trusted-client uploaders.
func TestObjectStoreWriteAccess(t *testing.T) {
	t.Parallel()
	url := startServerFromConf(t)

	boot := connectAs(t, url, "bootstrap")
	provisionObjectStore(t, boot, "core-objects")

	for _, component := range []string{"object-store-manager", "loupe", "loftspace-app", "bridge"} {
		component := component
		t.Run("allowed/"+component, func(t *testing.T) {
			c := connectAs(t, url, component)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			name := "obj-" + component
			if _, err := c.ObjectPut(ctx, "core-objects", name, strings.NewReader("blob"), 0); err != nil {
				t.Fatalf("%s ObjectPut core-objects: want success, got %v", component, err)
			}
		})
	}

	objmgr := connectAs(t, url, "object-store-manager")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := objmgr.ObjectDelete(ctx, "core-objects", "obj-object-store-manager"); err != nil {
		t.Fatalf("object-store-manager ObjectDelete core-objects: want success, got %v", err)
	}
}

// TestObjectStoreWriteIsolation: non-writers stay denied on the object plane —
// proving the new $O.core-objects.> grant is scoped, not a blanket $O.> leak.
// clinic-app has no ObjectPut call site (grep-verified) and is the pinned
// negative: whoever gives clinic blob upload must move it into the positive
// set (object-plane-nats-permissions-design.md §8).
func TestObjectStoreWriteIsolation(t *testing.T) {
	t.Parallel()
	url := startServerFromConf(t)

	boot := connectAs(t, url, "bootstrap")
	provisionObjectStore(t, boot, "core-objects")

	for _, component := range []string{"clinic-app", "gateway", "weaver"} {
		component := component
		t.Run("denied/"+component, func(t *testing.T) {
			t.Parallel()
			c := connectAs(t, url, component)
			ctx, cancel := context.WithTimeout(context.Background(), deniedTimeout)
			defer cancel()
			if _, err := c.ObjectPut(ctx, "core-objects", "rogue-"+component, strings.NewReader("forged"), 0); err == nil {
				t.Errorf("%s ObjectPut core-objects: want transport denial, got success", component)
			}
		})
	}
}

// TestBridgeNoPhantomKVGrants: bridge's grant used to carry $KV.bridge-external.>
// and $KV.bridge-schedule.> — those names are its JetStream *consumer* durables
// (internal/bridge/engine.go's externalDurable, internal/bridge/schedule.go's
// scheduleConsumerName), not KV buckets; bridge's only real KV write is
// health-kv (health.go's KVPut). Pins the tightened matrix (natsperm-matrix-
// hygiene, arch #19) — a phantom grant is a silent widen, not a working path.
func TestBridgeNoPhantomKVGrants(t *testing.T) {
	t.Parallel()
	url := startServerFromConf(t)

	boot := connectAs(t, url, "bootstrap")
	provision(t, boot, "bridge-external")
	provision(t, boot, "bridge-schedule")

	assertDeniedPuts(t, url, "bridge-external", []string{"bridge"})
	assertDeniedPuts(t, url, "bridge-schedule", []string{"bridge"})
}

// gatewayOwnedBucketDeniedComponents lists every matrix component other than
// gateway (the owner) and bootstrap (the exempt provisioner) — refractor is
// now included: natsperm-matrix-hygiene Fire 1 closed the broad-$KV.>-with-
// no-per-bucket-denies gap these tests used to carve out for it. Shared by
// both gateway-owned-bucket isolation tests below so the roster can't drift
// between them independently of the real component matrix.
var gatewayOwnedBucketDeniedComponents = []string{
	"processor", "refractor", "loom", "weaver", "bridge", "chronicler", "object-store-manager",
	"lattice-pkg", "loupe", "lattice", "loftspace-app", "clinic-app", "cafe-app",
	"wellness-app",
}

// TestGatewayRevocationBucketWriteIsolation: only the gateway (its own
// events.gateway.> materializer) may write the token-revocation kill-switch
// set — pins the gateway-token-revocation-activation-design.md §2.8 grant as
// scoped, not a blanket leak. bootstrap is excluded (the exempt provisioner);
// refractor is now included in the denied roster post natsperm-matrix-hygiene
// Fire 1.
func TestGatewayRevocationBucketWriteIsolation(t *testing.T) {
	t.Parallel()
	url := startServerFromConf(t)

	boot := connectAs(t, url, "bootstrap")
	provision(t, boot, "token-revocation")

	gw := connectAs(t, url, "gateway")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := gw.KVPut(ctx, "token-revocation", "vtx.identity.test", []byte("v")); err != nil {
		t.Fatalf("gateway KVPut token-revocation: want success, got %v", err)
	}

	assertDeniedPuts(t, url, "token-revocation", gatewayOwnedBucketDeniedComponents)
}

// TestGatewayCredentialBindingsWriteIsolation: only the gateway (its own
// credential-bindings materializer, internal/gateway/credential_bindings_materializer.go)
// may write the credential→identity resolution set. This pins the natsperm-
// matrix-hygiene Fire-0 fix — the grant was previously missing, so the shipped
// materializer was silently transport-denied under enforcement (a live bug).
func TestGatewayCredentialBindingsWriteIsolation(t *testing.T) {
	t.Parallel()
	url := startServerFromConf(t)

	boot := connectAs(t, url, "bootstrap")
	provision(t, boot, "credential-bindings")

	gw := connectAs(t, url, "gateway")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := gw.KVPut(ctx, "credential-bindings", "vtx.identity.test", []byte("v")); err != nil {
		t.Fatalf("gateway KVPut credential-bindings: want success, got %v", err)
	}

	assertDeniedPuts(t, url, "credential-bindings", gatewayOwnedBucketDeniedComponents)
}

// TestControlPlaneOperatorAccess: the operator surfaces (loupe, the lattice CLI)
// may request the component control planes (lattice.ctrl.<comp>.<name>.<op>);
// the responding engine replies through allow_responses. Positive pin: a missing
// lattice.ctrl.> publish grant silences every operator control action with an
// opaque request timeout, so this asserts the round trip, not just denials.
func TestControlPlaneOperatorAccess(t *testing.T) {
	t.Parallel()
	url := startServerFromConf(t)

	// The refractor user stands in for its own control plane: subscribe allows
	// ">", and allow_responses grants the reply publish — exactly the live wiring.
	ref := connectAs(t, url, "refractor")
	sub, err := ref.NATS().Subscribe("lattice.ctrl.refractor.*.health", func(m *nats.Msg) {
		_ = m.Respond([]byte(`{"ok":true}`))
	})
	if err != nil {
		t.Fatalf("refractor subscribe control subject: %v", err)
	}
	// Cleanup (not defer): parallel subtests resume after this function body
	// returns, and the responder must outlive them.
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	if err := ref.NATS().Flush(); err != nil {
		t.Fatalf("flush refractor subscription: %v", err)
	}

	for _, component := range []string{"loupe", "lattice"} {
		component := component
		t.Run("allowed/"+component, func(t *testing.T) {
			t.Parallel()
			c := connectAs(t, url, component)
			reply, err := c.NATS().Request("lattice.ctrl.refractor.lens1.health", nil, 3*time.Second)
			if err != nil {
				t.Fatalf("%s control request: want reply, got %v", component, err)
			}
			if len(reply.Data) == 0 {
				t.Errorf("%s control request: empty reply", component)
			}
		})
	}

	// A vertical app is NOT an operator surface — its control request stays denied.
	t.Run("denied/loftspace-app", func(t *testing.T) {
		t.Parallel()
		c := connectAs(t, url, "loftspace-app")
		if _, err := c.NATS().Request("lattice.ctrl.refractor.lens1.health", nil, deniedTimeout); err == nil {
			t.Error("loftspace-app control request: want denial, got a reply")
		}
	})
}

// TestBackingStreamSideChannel: denying $KV.core-kv.> publish is not enough — a
// holder of the broad $JS.API.> grant could otherwise destroy the backing
// stream directly. Only bootstrap (the provisioner) may administer the
// stream; post natsperm-matrix-hygiene Fire 1 the owner (processor) is denied
// too — the Chronicler precedent (a row writer never needs to administer its
// own backing stream) now applies matrix-wide, not just to orchestration-
// history.
func TestBackingStreamSideChannel(t *testing.T) {
	t.Parallel()
	url := startServerFromConf(t)

	boot := connectAs(t, url, "bootstrap")
	provision(t, boot, "core-kv")

	// bootstrap (the provisioner) may purge the stream it created.
	if _, err := boot.NATS().Request("$JS.API.STREAM.PURGE.KV_core-kv", []byte("{}"), 3*time.Second); err != nil {
		t.Fatalf("bootstrap PURGE KV_core-kv: want success, got %v", err)
	}

	// every non-bootstrap component's purge — including the owner's — is
	// denied at the door; the request gets no reply.
	for _, component := range []string{"processor", "loom", "loupe", "refractor", "weaver"} {
		component := component
		t.Run("denied-purge/"+component, func(t *testing.T) {
			t.Parallel()
			c := connectAs(t, url, component)
			if _, err := c.NATS().Request("$JS.API.STREAM.PURGE.KV_core-kv", []byte("{}"), deniedTimeout); err == nil {
				t.Errorf("%s PURGE KV_core-kv: want denial, got a reply", component)
			}
		})
	}
}

// TestCoreEventsSideChannel: core-events is a plain stream outside the
// PlatformBuckets() registry, so protectedStreamDenies never ran over it
// before this test's fix — any $JS.API.> holder could purge the stream or
// administer a protected consumer's durable (board item: "[natsperm]
// $JS.API.> lets any component delete a durable or purge core-events").
// Registry-driven per TestRegistryDrivenStreamAdminSideChannel's precedent
// (not the hand-picked-component-subset shape this test used before this
// fire): every non-bootstrap component (nonBootstrapComponentNames()) is
// exercised, not a fixed sample. Two denial classes per protected consumer:
// DELETE/RESET/PAUSE denied to every component including the owner (nobody
// administers their own durable — the Chronicler precedent), and
// CREATE/DURABLE.CREATE/MSG.NEXT denied to every component EXCEPT the
// owner (who needs both to run at all). Positive controls on both axes:
// bootstrap may purge/create/delete (the provisioner), and each consumer's
// owner may CREATE its own durable (proves the owner-exception is real, not
// an accidental blanket deny).
func TestCoreEventsSideChannel(t *testing.T) {
	t.Parallel()
	url := startServerFromConf(t)

	boot := connectAs(t, url, "bootstrap")
	provisionStream(t, boot, "core-events", []string{"events.>"})

	// bootstrap (the provisioner) may purge the stream it created.
	if _, err := boot.NATS().Request("$JS.API.STREAM.PURGE.core-events", []byte("{}"), 3*time.Second); err != nil {
		t.Fatalf("bootstrap PURGE core-events: want success, got %v", err)
	}

	// every non-bootstrap component's purge is denied at the door,
	// including the stream's own publisher (Processor).
	for _, component := range nonBootstrapComponentNames() {
		component := component
		t.Run("denied-purge/"+component, func(t *testing.T) {
			t.Parallel()
			c := connectAs(t, url, component)
			if _, err := c.NATS().Request("$JS.API.STREAM.PURGE.core-events", []byte("{}"), deniedTimeout); err == nil {
				t.Errorf("%s PURGE core-events: want denial, got a reply", component)
			}
		})
	}

	// the legacy single-wildcard create endpoint carries no consumer name
	// in the subject (the name is read from the request body), so it can
	// hijack ANY consumer on the stream and cannot be scoped per-name —
	// closed matrix-wide instead. bootstrap may use it (positive control);
	// every other component, including every protected consumer's own
	// owner, is denied.
	t.Run("bootstrap-legacy-create", func(t *testing.T) {
		c := connectAs(t, url, "bootstrap")
		body := []byte(`{"stream_name":"core-events","config":{"filter_subject":"events.probe"}}`)
		if _, err := c.NATS().Request("$JS.API.CONSUMER.CREATE.core-events", body, 3*time.Second); err != nil {
			t.Fatalf("bootstrap legacy CONSUMER.CREATE: want success, got %v", err)
		}
	})
	for _, component := range nonBootstrapComponentNames() {
		component := component
		t.Run("denied-legacy-create/"+component, func(t *testing.T) {
			t.Parallel()
			c := connectAs(t, url, component)
			if _, err := c.NATS().Request("$JS.API.CONSUMER.CREATE.core-events", []byte("{}"), deniedTimeout); err == nil {
				t.Errorf("%s legacy CONSUMER.CREATE core-events: want denial, got a reply", component)
			}
		})
	}

	for _, pc := range coreEventsProtectedConsumers {
		pc := pc

		// bootstrap may create the durable directly (positive control for
		// the admin-verb class: proves the "no reply" signal below means
		// ACL denial, not just "nobody would ever succeed here"). Run
		// sequentially (no t.Parallel()) so it completes — including its
		// own cleanup — before owner-create below touches the same name.
		t.Run("bootstrap-admin/"+pc.name, func(t *testing.T) {
			c := connectAs(t, url, "bootstrap")
			createSubj := "$JS.API.CONSUMER.DURABLE.CREATE.core-events." + pc.name
			body := []byte(`{"stream_name":"core-events","config":{"durable_name":"` + pc.name + `","ack_policy":"explicit"}}`)
			if _, err := c.NATS().Request(createSubj, body, 3*time.Second); err != nil {
				t.Fatalf("bootstrap DURABLE.CREATE %s: want success, got %v", pc.name, err)
			}
			if _, err := c.NATS().Request("$JS.API.CONSUMER.DELETE.core-events."+pc.name, []byte("{}"), 3*time.Second); err != nil {
				t.Fatalf("bootstrap DELETE %s: want success, got %v", pc.name, err)
			}
		})

		// the owner may create its own durable — the owner-only exception
		// is real, not an accidental blanket deny that also caught it. Also
		// sequential: it mutates the same named resource as the subtest
		// above and must not race it.
		t.Run("owner-create/"+pc.name, func(t *testing.T) {
			boot := connectAs(t, url, "bootstrap")
			owner := connectAs(t, url, pc.owner)
			createSubj := "$JS.API.CONSUMER.CREATE.core-events." + pc.name + ".events.>"
			if _, err := owner.NATS().Request(createSubj, []byte(`{"stream_name":"core-events","config":{"durable_name":"`+pc.name+`","filter_subject":"events.>","ack_policy":"explicit"}}`), 3*time.Second); err != nil {
				t.Fatalf("%s CREATE own consumer %s: want success, got %v", pc.owner, pc.name, err)
			}
			// clean up as bootstrap so the next subtest starts fresh.
			_, _ = boot.NATS().Request("$JS.API.CONSUMER.DELETE.core-events."+pc.name, []byte("{}"), 3*time.Second)
		})

		// admin verbs (DELETE/RESET/PAUSE): denied to every component,
		// owner included.
		for _, verb := range []string{"DELETE", "RESET", "PAUSE", "UNPIN"} {
			verb := verb
			for _, component := range nonBootstrapComponentNames() {
				component := component
				t.Run("denied-"+verb+"/"+pc.name+"/"+component, func(t *testing.T) {
					t.Parallel()
					c := connectAs(t, url, component)
					subject := "$JS.API.CONSUMER." + verb + ".core-events." + pc.name
					if _, err := c.NATS().Request(subject, []byte("{}"), deniedTimeout); err == nil {
						t.Errorf("%s %s core-events consumer %s: want denial, got a reply", component, verb, pc.name)
					}
				})
			}
		}

		// owner-only verbs (CREATE/DURABLE.CREATE/MSG.NEXT): denied to
		// every component except the owner.
		for _, subject := range []string{
			"$JS.API.CONSUMER.CREATE.core-events." + pc.name,
			"$JS.API.CONSUMER.CREATE.core-events." + pc.name + ".events.privacy.somefilter",
			"$JS.API.CONSUMER.DURABLE.CREATE.core-events." + pc.name,
			"$JS.API.CONSUMER.MSG.NEXT.core-events." + pc.name,
		} {
			subject := subject
			for _, component := range nonBootstrapComponentNames() {
				if component == pc.owner {
					continue
				}
				component := component
				t.Run("denied-owner-only/"+pc.name+"/"+subject+"/"+component, func(t *testing.T) {
					t.Parallel()
					c := connectAs(t, url, component)
					if _, err := c.NATS().Request(subject, []byte("{}"), deniedTimeout); err == nil {
						t.Errorf("%s %s: want denial, got a reply", component, subject)
					}
				})
			}
		}
	}
}

// jsAckNoWaitPull is the body every ack-plane vector below publishes. "+NXT"
// makes the server ack the message the subject names and then run a
// next-message request on that consumer, delivering the result to the
// PUBLISHER's own reply subject with no check on who published (nats-server
// v2.14.0 server/consumer.go:2716, :2736-2738) — the ack subject is a read
// path, which is what these vectors exist to police. The JSON tail is
// TrimSpace'd and parsed as an ordinary pull request (:3861-3862), and
// no_wait is load-bearing: with it the server ALWAYS answers a permitted
// request — a message when one is available, a 404 status reply when the
// pending set is empty (:4494-4497) — so "no reply at all" can only mean the
// publish was denied at the transport, never "the consumer had nothing".
var jsAckNoWaitPull = []byte(`+NXT {"batch":1,"no_wait":true}`)

// msgNextNoWaitPull is the same no-wait pull sent the front way, through
// $JS.API.CONSUMER.MSG.NEXT.
var msgNextNoWaitPull = []byte(`{"batch":1,"no_wait":true}`)

// assertDeliveredMessage fails unless msg is a real stream delivery rather
// than one of JetStream's status replies (404 no-messages, 408 request
// timeout, 409 limit exceeded). A status reply means the request was
// PERMITTED but found nothing — a positive control that accepted one would
// claim the wrong thing.
func assertDeliveredMessage(t *testing.T, what string, msg *nats.Msg) {
	t.Helper()
	if status := msg.Header.Get("Status"); status != "" {
		t.Fatalf("%s: want a delivered message, got JetStream status %q %q", what, status, msg.Header.Get("Description"))
	}
	if len(msg.Data) == 0 {
		t.Fatalf("%s: want a delivered message with a body, got an empty payload", what)
	}
}

// globalAccountHash reproduces the second token of a v2 ack subject the way
// nats-server derives it: the first 8 bytes of sha256(<account name>), each
// byte folded into a 62-character alphabet (v2.14.0 server/events.go:1151-1163,
// with digits/base at server/accounts.go:2363-2364), over the account the
// connection lands in. deploy/nats-server.conf declares no accounts block, so
// every component user is in the global account "$G" (server/const.go:247).
//
// Derived, never a hardcoded literal: a stale constant would name a subject
// nothing is subscribed to, so every v2 denial vector would pass vacuously.
// Getting it wrong instead fails the v2 positive control loudly.
func globalAccountHash() string {
	const digits = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	sum := sha256.Sum256([]byte("$G"))
	h := make([]byte, 8)
	for i := range h {
		h[i] = digits[int(sum[i])%len(digits)]
	}
	return string(h)
}

// v2AckSubject builds the v2 wire form of a consumer's ack subject —
// $JS.ACK.<domain>.<accHash>.<stream>.<consumer>.<5 counters> (v2.14.0
// server/consumer.go:1395-1398). The domain is the literal "_" because the
// committed conf's jetstream block configures none (:1377-1379). The trailing
// counters need not correspond to a real delivery: processAck's "+NXT" arm
// runs the next-message request whatever ackReplyInfo made of them
// (:2736-2738).
func v2AckSubject(stream, consumer string) string {
	return "$JS.ACK._." + globalAccountHash() + "." + stream + "." + consumer + ".1.1.1.0.0"
}

// TestCoreEventsAckPlaneSideChannel: a protected consumer's ack subject is a
// pull door, not just an acknowledgement lane — publishing "+NXT" there reads
// the next message into whatever reply subject the caller chose. Without
// coreEventsAckDenies, every component holding the matrix-wide $JS.ACK.>
// grant reads (and drains) a shred, revocation or credential-binding backlog
// that the MSG.NEXT deny closes the front door on. These vectors pin that the
// back door is closed too.
//
// Both wire forms are exercised because the server subscribes both regardless
// of the js_ack_fc_v2 feature flag (server/consumer.go:1699-1707): the v1 form
// this deployment's traffic actually uses, and the v2 form that is invisible
// on the wire yet live in the subscription set. Positive controls come first
// on both — the v1 subject is taken from a real delivery, the v2 subject is
// synthesized from the server's own derivation — so the denials below are
// known to be about permissions and not about a subject nobody listens on.
func TestCoreEventsAckPlaneSideChannel(t *testing.T) {
	t.Parallel()
	url := startServerFromConf(t)

	boot := connectAs(t, url, "bootstrap")
	provisionStream(t, boot, "core-events", []string{"events.>"})

	// the Processor holds the events.> publish grant — these are real stream
	// messages, so every consumer below has something genuine to deliver.
	proc := connectAs(t, url, "processor")
	published := 0
	publishEvent := func(t *testing.T) {
		t.Helper()
		published++
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		subject := fmt.Sprintf("events.ackplane.%d", published)
		if err := proc.Publish(ctx, subject, []byte(`{"probe":true}`), nil); err != nil {
			t.Fatalf("processor Publish %s: want success, got %v", subject, err)
		}
	}

	for _, pc := range coreEventsProtectedConsumers {
		pc := pc
		// sequential per consumer: each stage below consumes what the
		// previous one published, so they must not interleave.
		t.Run(pc.name, func(t *testing.T) {
			owner := connectAs(t, url, pc.owner)
			createSubj := "$JS.API.CONSUMER.CREATE.core-events." + pc.name + ".events.>"
			createBody := []byte(`{"stream_name":"core-events","config":{"durable_name":"` + pc.name + `","filter_subject":"events.>","ack_policy":"explicit"}}`)
			if _, err := owner.NATS().Request(createSubj, createBody, 3*time.Second); err != nil {
				t.Fatalf("%s CREATE own consumer %s: want success, got %v", pc.owner, pc.name, err)
			}
			t.Cleanup(func() {
				_, _ = boot.NATS().Request("$JS.API.CONSUMER.DELETE.core-events."+pc.name, []byte("{}"), 3*time.Second)
			})

			// v1 positive control, derived from the server rather than from a
			// template: pull the front way and keep the ack subject the
			// server stamped on the delivery.
			publishEvent(t)
			delivered, err := owner.NATS().Request("$JS.API.CONSUMER.MSG.NEXT.core-events."+pc.name, msgNextNoWaitPull, 3*time.Second)
			if err != nil {
				t.Fatalf("%s MSG.NEXT %s: want a delivered message, got %v", pc.owner, pc.name, err)
			}
			assertDeliveredMessage(t, pc.owner+" MSG.NEXT "+pc.name, delivered)
			v1Ack := delivered.Reply
			wantPrefix := "$JS.ACK.core-events." + pc.name + "."
			if !strings.HasPrefix(v1Ack, wantPrefix) || strings.Count(v1Ack, ".") != 8 {
				t.Fatalf("delivered ack subject = %q, want the v1 form %s<5 counters> (9 tokens)", v1Ack, wantPrefix)
			}

			// the premise: that real ack subject is a live inbound READ path.
			publishEvent(t)
			pulled, err := owner.NATS().Request(v1Ack, jsAckNoWaitPull, 3*time.Second)
			if err != nil {
				t.Fatalf("%s +NXT on its own v1 ack subject %q: want a delivered message, got %v", pc.owner, v1Ack, err)
			}
			assertDeliveredMessage(t, pc.owner+" +NXT "+v1Ack, pulled)

			// v2 positive control: proves the synthesized shape is the one the
			// server actually subscribed, hash and domain included. A failure
			// here is a broken derivation, not a broken permission — and would
			// make every v2 denial below meaningless.
			v2Ack := v2AckSubject("core-events", pc.name)
			publishEvent(t)
			pulledV2, err := owner.NATS().Request(v2Ack, jsAckNoWaitPull, 3*time.Second)
			if err != nil {
				t.Fatalf("%s +NXT on the v2 ack subject %q: want a delivered message, got %v — the server subscribes the v2 form unconditionally (server/consumer.go:1699-1707), so this failing means the domain/account-hash derivation is wrong", pc.owner, v2Ack, err)
			}
			assertDeliveredMessage(t, pc.owner+" +NXT "+v2Ack, pulledV2)

			// leave a message PENDING so the denials below run against a
			// consumer that genuinely has something to hand out.
			publishEvent(t)

			// grouped so the parallel denials all complete before the
			// pending-set check that follows.
			t.Run("denied", func(t *testing.T) {
				for _, component := range nonBootstrapComponentNames() {
					if component == pc.owner {
						continue
					}
					for _, subject := range []string{v1Ack, v2Ack} {
						component, subject := component, subject
						t.Run(component+"/"+subject, func(t *testing.T) {
							t.Parallel()
							c := connectAs(t, url, component)
							if _, err := c.NATS().Request(subject, jsAckNoWaitPull, deniedTimeout); err == nil {
								t.Errorf("%s +NXT %s: want denial, got a reply", component, subject)
							}
						})
					}
				}
			})

			// the pending message is still there: no denied component pulled
			// it, and the vectors above were not shadow-boxing an empty
			// consumer.
			survivor, err := owner.NATS().Request("$JS.API.CONSUMER.MSG.NEXT.core-events."+pc.name, msgNextNoWaitPull, 3*time.Second)
			if err != nil {
				t.Fatalf("%s MSG.NEXT %s after the denial vectors: want the pending message, got %v", pc.owner, pc.name, err)
			}
			assertDeliveredMessage(t, pc.owner+" MSG.NEXT "+pc.name+" (post-denial)", survivor)
		})
	}
}

// TestVerticalAppHoldsNoAckGrant pins the vertical-app tier's absence of
// $JS.ACK.> behaviorally, against a durable the protected-consumer registry
// does NOT cover — so what fails is the missing grant, not a per-consumer
// deny. The four apps run no JetStream consumer at all (their listing path is
// nats.go's ordered consumer, built AckNonePolicy, for which the server
// subscribes no ack subject), so an ack grant would buy them nothing but an
// unscoped "+NXT" read on every other component's consumers.
//
// The positive control is a component that does hold the grant: the Processor
// gets a reply on the very same subject, which is what makes the apps'
// silence a permission result rather than a property of the subject. The
// second half is the read path the apps actually depend on — a KV handle on
// token-revocation, a get and a key listing — which needs no ack grant to
// work.
func TestVerticalAppHoldsNoAckGrant(t *testing.T) {
	t.Parallel()
	url := startServerFromConf(t)

	boot := connectAs(t, url, "bootstrap")
	provisionStream(t, boot, "core-events", []string{"events.>"})

	// deliberately NOT a coreEventsProtectedConsumers name.
	const probe = "app-tier-ack-probe"
	createBody := []byte(`{"stream_name":"core-events","config":{"durable_name":"` + probe + `","ack_policy":"explicit"}}`)
	if _, err := boot.NATS().Request("$JS.API.CONSUMER.DURABLE.CREATE.core-events."+probe, createBody, 3*time.Second); err != nil {
		t.Fatalf("bootstrap DURABLE.CREATE %s: want success, got %v", probe, err)
	}

	proc := connectAs(t, url, "processor")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for i := 0; i < 3; i++ {
		if err := proc.Publish(ctx, fmt.Sprintf("events.apptier.%d", i), []byte(`{"probe":true}`), nil); err != nil {
			t.Fatalf("processor Publish events.apptier: want success, got %v", err)
		}
	}

	// take the real ack subject off a real delivery (bootstrap is denied
	// nothing), so the vectors below use the wire form the server stamped.
	delivered, err := boot.NATS().Request("$JS.API.CONSUMER.MSG.NEXT.core-events."+probe, msgNextNoWaitPull, 3*time.Second)
	if err != nil {
		t.Fatalf("bootstrap MSG.NEXT %s: want a delivered message, got %v", probe, err)
	}
	assertDeliveredMessage(t, "bootstrap MSG.NEXT "+probe, delivered)
	v1Ack := delivered.Reply
	v2Ack := v2AckSubject("core-events", probe)

	t.Run("positive-control/processor", func(t *testing.T) {
		for _, subject := range []string{v1Ack, v2Ack} {
			pulled, err := proc.NATS().Request(subject, jsAckNoWaitPull, 3*time.Second)
			if err != nil {
				t.Fatalf("processor +NXT %s: want a delivered message (it holds $JS.ACK.>), got %v", subject, err)
			}
			assertDeliveredMessage(t, "processor +NXT "+subject, pulled)
		}
	})

	// The apps hold no allow entry that matches either form, so nothing they
	// send here reaches the server. A permitted request always draws a reply
	// — a message, or a 404 status once the pending set empties — so "no
	// reply" is unambiguously a transport denial.
	for _, app := range verticalAppNames {
		app := app
		for _, subject := range []string{v1Ack, v2Ack} {
			subject := subject
			t.Run("denied/"+app+"/"+subject, func(t *testing.T) {
				t.Parallel()
				c := connectAs(t, url, app)
				if _, err := c.NATS().Request(subject, jsAckNoWaitPull, deniedTimeout); err == nil {
					t.Errorf("%s +NXT %s: want denial (no ack grant), got a reply", app, subject)
				}
			})
		}
	}

	t.Run("read-path-still-works", func(t *testing.T) {
		provision(t, boot, bootstrap.GatewayRevocationBucket)
		const key = "revoked.actor.probe"
		putCtx, putCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer putCancel()
		if _, err := boot.KVPut(putCtx, bootstrap.GatewayRevocationBucket, key, []byte(`{"revoked":true}`)); err != nil {
			t.Fatalf("bootstrap KVPut %s: want success, got %v", bootstrap.GatewayRevocationBucket, err)
		}

		for _, app := range verticalAppNames {
			app := app
			t.Run(app, func(t *testing.T) {
				t.Parallel()
				c := connectAs(t, url, app)
				readCtx, readCancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer readCancel()
				if _, err := c.KVGet(readCtx, bootstrap.GatewayRevocationBucket, key); err != nil {
					t.Fatalf("%s KVGet %s: want success, got %v", app, bootstrap.GatewayRevocationBucket, err)
				}
				keys, err := c.KVListKeys(readCtx, bootstrap.GatewayRevocationBucket)
				if err != nil {
					t.Fatalf("%s KVListKeys %s: want success, got %v", app, bootstrap.GatewayRevocationBucket, err)
				}
				found := false
				for _, k := range keys {
					if k == key {
						found = true
					}
				}
				if !found {
					t.Errorf("%s KVListKeys %s = %v, want it to contain %q", app, bootstrap.GatewayRevocationBucket, keys, key)
				}
			})
		}
	})
}

// ackGrantRoster is every matrix component's ack-plane posture, and the reason
// for it. $JS.ACK.> is a data-plane privilege (see coreEventsAckDenies), so
// holding it is a decision, and so is not holding it — a component that runs a
// durable consumer and loses the grant stalls silently, since a denied publish
// draws no error anyone reads. Both directions are pinned by
// TestAckGrantRoster.
//
// The three plain vertical-app rows and chronicler declare byte-identical
// ExtraPubAllow literals, so nothing but this roster distinguishes an
// intentional removal from one that swept up a neighbour.
var ackGrantRoster = map[string]struct {
	granted bool
	because string
}{
	"processor":            {true, "runs privacy-worker and privacy-worker-retention"},
	"refractor":            {true, "runs refractor-keyshredded and refractor-classkeyshredded"},
	"gateway":              {true, "runs gateway-revocation and gateway-credential-bindings"},
	"loom":                 {true, "runs the loom-trigger and loom-deadline durables"},
	"weaver":               {true, "runs the weaver-temporal and weaver-sweep durables"},
	"bridge":               {true, "runs the bridge-external and schedule durables"},
	"object-store-manager": {true, "runs the object tombstone and cascade durables"},
	"chronicler":           {true, "runs the eventStream materializer's durable"},
	"bootstrap":            {true, "the provisioner, exempt from the registry deny loop entirely"},
	"lattice-pkg":          {true, "consumes op-completion events during an install"},
	"loupe":                {true, "the inspector consumes streams on demand"},
	"lattice":              {true, "the operator CLI consumes streams on demand"},

	"model-runner":  {false, "serves micro request/reply and runs no consumer"},
	"facet":         {false, "health-plane only; per-identity traffic is on the callout connections"},
	"clinic-app":    {false, "runs no consumer; listings use an AckNone ordered consumer"},
	"cafe-app":      {false, "runs no consumer; listings use an AckNone ordered consumer"},
	"wellness-app":  {false, "runs no consumer; listings use an AckNone ordered consumer"},
	"loftspace-app": {false, "runs no consumer; listings use an AckNone ordered consumer"},
}

// TestAckGrantRoster pins each component's ack-plane posture against
// ackGrantRoster, in both directions and with no component unaccounted for.
//
// The behavioural vectors elsewhere in this file prove the tier that must NOT
// reach the ack plane cannot. They cannot prove the converse: a component that
// wrongly LOSES the grant fails only at runtime, on a redelivery loop nobody
// is watching, because substrate registers no nats.ErrorHandler and a denied
// publish surfaces as silence. This is the check that catches that edit, and
// the roster's exhaustiveness clause is what forces a newly added component to
// state which side it is on rather than inheriting a neighbour's literal.
func TestAckGrantRoster(t *testing.T) {
	t.Parallel()
	buckets := bootstrap.PlatformBuckets()

	seen := make(map[string]bool, len(Matrix))
	for _, c := range Matrix {
		c := c
		seen[c.Name] = true
		want, listed := ackGrantRoster[c.Name]
		if !listed {
			t.Errorf("component %q is not in ackGrantRoster — decide whether it runs a JetStream consumer and needs $JS.ACK.>, and record the reason there", c.Name)
			continue
		}
		got := slices.Contains(c.Allow(buckets), "$JS.ACK.>")
		if got != want.granted {
			t.Errorf("%s holds $JS.ACK.> = %v, want %v (%s)", c.Name, got, want.granted, want.because)
		}
	}
	for name := range ackGrantRoster {
		if !seen[name] {
			t.Errorf("ackGrantRoster names %q, which is not a Matrix component", name)
		}
	}
}

// TestChroniclerBackingStreamSideChannel: chronicler-host-reconciliation's new
// bucket closes the backing-stream side channel from day one rather than
// reproducing the weaver-targets-class debt — chronicler itself is denied
// stream-admin verbs on its OWN backing stream (bootstrap primordially
// provisions orchestration-history; chronicler never administers it, only
// reads/writes rows). Not a full close: every OTHER component's pre-existing
// broad $JS.API.> grant (refractor, processor, loom, weaver, …) still isn't
// denied here — that is the SAME natsperm-matrix-hygiene-tracked debt
// TestGatewayRevocationBucketWriteIsolation already documents for
// weaver-targets/token-revocation, now also covering this bucket.
func TestChroniclerBackingStreamSideChannel(t *testing.T) {
	t.Parallel()
	url := startServerFromConf(t)

	boot := connectAs(t, url, "bootstrap")
	provision(t, boot, "orchestration-history")

	// bootstrap (the actual provisioner) may purge the stream it created.
	if _, err := boot.NATS().Request("$JS.API.STREAM.PURGE.KV_orchestration-history", []byte("{}"), 3*time.Second); err != nil {
		t.Fatalf("bootstrap PURGE KV_orchestration-history: want success, got %v", err)
	}

	// chronicler — the bucket's sole legitimate row-writer — is nonetheless
	// denied stream administration over its own backing stream (least
	// privilege: it never needs to create/update/delete/purge the stream
	// itself).
	t.Run("denied-purge/chronicler", func(t *testing.T) {
		t.Parallel()
		c := connectAs(t, url, "chronicler")
		if _, err := c.NATS().Request("$JS.API.STREAM.PURGE.KV_orchestration-history", []byte("{}"), deniedTimeout); err == nil {
			t.Error("chronicler PURGE KV_orchestration-history: want denial, got a reply")
		}
	})
}

// nonBootstrapComponentNames returns every Matrix component name except
// bootstrap — the roster the registry-driven tests below iterate, so a
// newly added component is automatically covered without a hand-edited list.
func nonBootstrapComponentNames() []string {
	names := make([]string, 0, len(Matrix)-1)
	for _, c := range Matrix {
		if c.Name == bootstrapComponentName {
			continue
		}
		names = append(names, c.Name)
	}
	return names
}

// TestRegistryDrivenWriteIsolation replaces the per-bucket hand vectors above
// with one registry-driven check (natsperm-matrix-hygiene-design.md §7 item
// 1): for every PlatformBuckets() row with an Owner, the owner's KVPut
// succeeds and every OTHER non-bootstrap matrix component's KVPut is denied
// — deriving both axes (buckets × components) from source so a new platform
// bucket or a new matrix component is covered automatically, and the
// already-stale hand lists this generalizes can't silently drift again.
func TestRegistryDrivenWriteIsolation(t *testing.T) {
	t.Parallel()
	url := startServerFromConf(t)
	boot := connectAs(t, url, "bootstrap")

	for _, b := range bootstrap.PlatformBuckets() {
		b := b
		if b.Owner == "" {
			continue // SharedWrite (health-kv) — covered by TestHealthKVSharedWriteAccess.
		}
		t.Run(b.Name, func(t *testing.T) {
			provision(t, boot, b.Name)

			owner := connectAs(t, url, b.Owner)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := owner.KVPut(ctx, b.Name, "registry-driven.test", []byte("v")); err != nil {
				t.Fatalf("owner %s KVPut %s: want success, got %v", b.Owner, b.Name, err)
			}

			var denied []string
			for _, name := range nonBootstrapComponentNames() {
				if name != b.Owner {
					denied = append(denied, name)
				}
			}
			assertDeniedPuts(t, url, b.Name, denied)
		})
	}
}

// TestHealthKVSharedWriteAccess: health-kv is SharedWrite — every non-
// bootstrap matrix component must be able to self-report its heartbeat
// (health.<component>.<inst>); a missing grant here silences a component's
// monitoring silently, so this is a positive pin, not just a denial check.
//
// facet is excluded from the plain-KVPut loop: it has no $JS.API.> grant, so
// a bare KVPut (which opens a KV handle via STREAM.INFO) is structurally
// denied — that is the intended fail-closed shape (design §8 finding #5;
// TestFacetHealthKVWriteVector below pins its real write path, KVPutWithTTL).
func TestHealthKVSharedWriteAccess(t *testing.T) {
	t.Parallel()
	url := startServerFromConf(t)

	boot := connectAs(t, url, "bootstrap")
	provision(t, boot, "health-kv")

	for _, name := range nonBootstrapComponentNames() {
		if name == "facet" {
			continue
		}
		name := name
		t.Run("allowed/"+name, func(t *testing.T) {
			t.Parallel()
			c := connectAs(t, url, name)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := c.KVPut(ctx, "health-kv", "health."+name+".test", []byte("v")); err != nil {
				t.Fatalf("%s KVPut health-kv: want success, got %v", name, err)
			}
		})
	}
}

// TestFacetHealthKVWriteVector pins facet's narrowest-in-matrix credential
// (facet-host-health-emission-design.md §4.1, ratified A2): it can write its
// own heartbeat via the Reporter's actual mechanism (KVPutWithTTL — a bare
// js.PublishMsg to the KV subject, no handle open), but every broader surface
// — a plain KVPut (which opens a handle via STREAM.INFO), core-kv, and the
// backing stream's admin API — stays denied.
func TestFacetHealthKVWriteVector(t *testing.T) {
	t.Parallel()
	url := startServerFromConf(t)

	boot := connectAs(t, url, "bootstrap")
	// health-kv is PerKeyTTL in bootstrap.PlatformBuckets() (LimitMarkerTTL on
	// the backing stream) — mirror that here, unlike the plain `provision`
	// helper other subtests use, so KVPutWithTTL below has a TTL-capable
	// bucket to write into.
	provCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := boot.JetStream().CreateKeyValue(provCtx, jetstream.KeyValueConfig{Bucket: "health-kv", LimitMarkerTTL: time.Second}); err != nil {
		t.Fatalf("provision health-kv (TTL-capable): %v", err)
	}
	provision(t, boot, bootstrap.CoreKVBucket)

	c := connectAs(t, url, "facet")

	t.Run("allowed/KVPutWithTTL", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := c.KVPutWithTTL(ctx, "health-kv", "health.facet.test", []byte("v"), 30*time.Second); err != nil {
			t.Fatalf("facet KVPutWithTTL health-kv: want success, got %v", err)
		}
	})

	t.Run("denied/plain-KVPut", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), deniedTimeout)
		defer cancel()
		if _, err := c.KVPut(ctx, "health-kv", "health.facet.test2", []byte("v")); err == nil {
			t.Error("facet plain KVPut health-kv: want transport denial (no $JS.API.> to open a KV handle), got success")
		}
	})

	t.Run("denied/core-kv", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), deniedTimeout)
		defer cancel()
		if _, err := c.KVPutWithTTL(ctx, bootstrap.CoreKVBucket, "rogue.key", []byte("forged"), 30*time.Second); err == nil {
			t.Error("facet KVPutWithTTL core-kv: want transport denial, got success")
		}
	})

	t.Run("denied/backing-stream-admin", func(t *testing.T) {
		if _, err := c.NATS().Request("$JS.API.STREAM.INFO.KV_health-kv", []byte("{}"), deniedTimeout); err == nil {
			t.Error("facet STREAM.INFO.KV_health-kv: want transport denial, got success")
		}
	})

	t.Run("denied/ops", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), deniedTimeout)
		defer cancel()
		if err := c.Publish(ctx, "ops.default", []byte("forged"), nil); err == nil {
			t.Error("facet Publish ops.default: want transport denial (no core-operations publish), got success")
		}
	})
}

// TestFacetSubscribeConfinement pins A2's one mechanism addition (design §4.2):
// facet is the first matrix row whose subscribe side is NOT the uniform ">" —
// it is pinned to _INBOX.> (the pub-ack reply inbox KVPutWithTTL's js.PublishMsg
// awaits). A raw nats.Connect (not the substrate wrapper) is required here: a
// denied SUBSCRIBE is not a synchronous error from Subscribe() itself — the
// server permits the local subscription and then delivers an async
// "Permissions Violation for Subscription" error over the connection's
// ErrorHandler, which substrate.Conn does not expose.
func TestFacetSubscribeConfinement(t *testing.T) {
	t.Parallel()
	url := startServerFromConf(t)

	nkeyOpt, err := nats.NkeyOptionFromSeed(seedPath(t, "facet"))
	if err != nil {
		t.Fatalf("load facet nkey seed: %v", err)
	}

	// nats.go's permissions-violation callback always passes a nil
	// *Subscription (processTransientError dispatches asyncErrorCB(nc, nil,
	// err) unconditionally) — the offending subject is embedded only in the
	// error text ("Permissions Violation for Subscription to %q"), so the
	// violation is matched by substring, not by identity.
	violations := make(chan string, 4)
	nc := natsfixture.Connect(t, url, nkeyOpt, nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
		if err != nil {
			violations <- err.Error()
		}
	}))

	deniedSubject := "lattice.sync.user.someidentity12345678"
	if _, err := nc.Subscribe(deniedSubject, func(*nats.Msg) {}); err != nil {
		t.Fatalf("Subscribe(%q): want local acceptance (denial is async), got %v", deniedSubject, err)
	}
	if err := nc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	select {
	case msg := <-violations:
		if !strings.Contains(msg, deniedSubject) {
			t.Errorf("permissions violation = %q, want it to name %q", msg, deniedSubject)
		}
	case <-time.After(deniedTimeout):
		t.Errorf("Subscribe(%q): want a subscribe-permissions violation, got none", deniedSubject)
	}

	allowedSubject := "_INBOX.facet-test.1"
	if _, err := nc.Subscribe(allowedSubject, func(*nats.Msg) {}); err != nil {
		t.Fatalf("Subscribe(%q): %v", allowedSubject, err)
	}
	if err := nc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	select {
	case subj := <-violations:
		t.Errorf("unexpected subscribe-permissions violation for %q (want _INBOX.> allowed)", subj)
	case <-time.After(deniedTimeout):
		// No violation arrived — the allowed subscribe is confirmed silent.
	}
}

// TestRegistryDrivenStreamAdminSideChannel generalizes
// TestBackingStreamSideChannel / TestChroniclerBackingStreamSideChannel
// matrix-wide (design §7 item 2, closing the natsperm-matrix-hygiene-tracked
// debt): for every registered platform bucket, bootstrap (the provisioner)
// may purge the backing stream and every OTHER non-bootstrap component —
// INCLUDING the bucket's own owner — is denied. A row writer never needs to
// administer its own backing stream; bootstrap primordially provisions all
// of them. Also covers SNAPSHOT (protectedStreamDenies' whole-stream
// backup/restore extension): a bulk byte-level export of the ENTIRE backing
// stream, denied the same way — no ordinary reader (MSG.GET/DIRECT.GET/INFO
// stay allowed) ever needs it.
func TestRegistryDrivenStreamAdminSideChannel(t *testing.T) {
	t.Parallel()
	url := startServerFromConf(t)
	boot := connectAs(t, url, "bootstrap")

	for _, b := range bootstrap.PlatformBuckets() {
		b := b
		t.Run(b.Name, func(t *testing.T) {
			provision(t, boot, b.Name)

			purgeSubject := "$JS.API.STREAM.PURGE.KV_" + b.Name
			if _, err := boot.NATS().Request(purgeSubject, []byte("{}"), 3*time.Second); err != nil {
				t.Fatalf("bootstrap PURGE KV_%s: want success, got %v", b.Name, err)
			}

			for _, name := range nonBootstrapComponentNames() {
				name := name
				t.Run("denied-purge/"+name, func(t *testing.T) {
					t.Parallel()
					c := connectAs(t, url, name)
					if _, err := c.NATS().Request(purgeSubject, []byte("{}"), deniedTimeout); err == nil {
						t.Errorf("%s PURGE KV_%s: want denial, got a reply", name, b.Name)
					}
				})
			}

			snapshotSubject := "$JS.API.STREAM.SNAPSHOT.KV_" + b.Name
			for _, name := range nonBootstrapComponentNames() {
				name := name
				t.Run("denied-snapshot/"+name, func(t *testing.T) {
					t.Parallel()
					c := connectAs(t, url, name)
					if _, err := c.NATS().Request(snapshotSubject, []byte("{}"), deniedTimeout); err == nil {
						t.Errorf("%s SNAPSHOT KV_%s: want denial, got a reply", name, b.Name)
					}
				})
			}
		})
	}
}

// TestRefractorPrivateBucketsWriteAccess: refractor's two platform-private
// stores (refractor-adjacency, personal-lens-interest) are owner-derived
// grants, not covered by any hand-authored positive pin before this fire —
// proves the registry's Allow() derivation actually grants them, not just
// the pre-existing $KV.> catch-all.
func TestRefractorPrivateBucketsWriteAccess(t *testing.T) {
	t.Parallel()
	url := startServerFromConf(t)
	boot := connectAs(t, url, "bootstrap")
	ref := connectAs(t, url, "refractor")

	for _, bucket := range []string{"refractor-adjacency", "personal-lens-interest"} {
		bucket := bucket
		t.Run(bucket, func(t *testing.T) {
			provision(t, boot, bucket)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := ref.KVPut(ctx, bucket, "registry-driven.test", []byte("v")); err != nil {
				t.Fatalf("refractor KVPut %s: want success, got %v", bucket, err)
			}
		})
	}
}

// TestRefractorDynamicPackageBucketWriteAccess: refractor's un-enumerable
// $KV.> allow must still admit — including auto-create — a dynamically-named
// package lens-target bucket that carries none of the platform-bucket
// registry's owner/deny treatment. This is the residual the registry design
// explicitly keeps (§3.3): narrowing by denies, not by enumerating allows.
func TestRefractorDynamicPackageBucketWriteAccess(t *testing.T) {
	t.Parallel()
	url := startServerFromConf(t)
	ref := connectAs(t, url, "refractor")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := ref.JetStream().CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "test-pkg-bucket"}); err != nil {
		t.Fatalf("refractor CreateKeyValue test-pkg-bucket: want success, got %v", err)
	}
	if _, err := ref.KVPut(ctx, "test-pkg-bucket", "target.1", []byte("v")); err != nil {
		t.Fatalf("refractor KVPut test-pkg-bucket: want success, got %v", err)
	}
}

// TestConfMatchesMatrix is the cheapest possible regen-forgotten alarm
// (design §5): re-renders deploy/nats-server.conf from internal/natsperm
// (Matrix + bootstrap.PlatformBuckets(), via the committed dev seeds) and
// asserts it is byte-identical to the committed file. A registry/matrix edit
// that forgets `go run ./deploy/gen-dev-nkeys` fails CI here instead of
// silently shipping a stale conf the embedded-server tests never notice
// (they load the committed file directly, not a live render).
func TestConfMatchesMatrix(t *testing.T) {
	pubKeys := make(map[string]string, len(Matrix))
	for _, c := range Matrix {
		seed, err := os.ReadFile(seedPath(t, c.Name))
		if err != nil {
			t.Fatalf("read seed for %s: %v", c.Name, err)
		}
		kp, err := nkeys.FromSeed(bytes.TrimSpace(seed))
		if err != nil {
			t.Fatalf("parse seed for %s: %v", c.Name, err)
		}
		pub, err := kp.PublicKey()
		if err != nil {
			t.Fatalf("public key for %s: %v", c.Name, err)
		}
		pubKeys[c.Name] = pub
	}

	issuerSeed, err := os.ReadFile(seedPath(t, "auth-callout-issuer"))
	if err != nil {
		t.Fatalf("read auth-callout issuer seed: %v", err)
	}
	issuerKP, err := nkeys.FromSeed(bytes.TrimSpace(issuerSeed))
	if err != nil {
		t.Fatalf("parse auth-callout issuer seed: %v", err)
	}
	issuerPub, err := issuerKP.PublicKey()
	if err != nil {
		t.Fatalf("auth-callout issuer public key: %v", err)
	}

	xkeySeed, err := os.ReadFile(seedPath(t, "auth-callout-xkey"))
	if err != nil {
		t.Fatalf("read auth-callout xkey seed: %v", err)
	}
	xkeyKP, err := nkeys.FromCurveSeed(bytes.TrimSpace(xkeySeed))
	if err != nil {
		t.Fatalf("parse auth-callout xkey seed: %v", err)
	}
	xkeyPub, err := xkeyKP.PublicKey()
	if err != nil {
		t.Fatalf("auth-callout xkey public key: %v", err)
	}

	rendered := RenderConf(pubKeys, issuerPub, xkeyPub)
	committed, err := os.ReadFile(confPath(t))
	if err != nil {
		t.Fatalf("read committed conf: %v", err)
	}
	if rendered != string(committed) {
		t.Error("deploy/nats-server.conf is stale — Matrix/PlatformBuckets changed but the conf was not regenerated; run `go run ./deploy/gen-dev-nkeys` and commit the result")
	}
}
