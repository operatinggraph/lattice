package natsperm

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/jsstore"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/nats-io/nats-server/v2/server"
	natsserver "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/nats-io/nkeys"
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

func confPath(t *testing.T) string  { return filepath.Join(repoRoot(t), "deploy", "nats-server.conf") }
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
	// readiness check reads, and RunServer does not return until the listener is
	// bound, so the port below is settled and race-free.
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
	s := natsserver.RunServer(opts)
	t.Cleanup(s.Shutdown)
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
	if got, want := len(opts.Nkeys), 16; got != want {
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
	if len(ac.AuthUsers) != 16 {
		t.Errorf("auth_callout.auth_users = %d entries, want 16 (every component bypasses the callout)", len(ac.AuthUsers))
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
	assertDeniedPublish(t, url, "ops.default", []string{"clinic-app", "cafe-app", "loftspace-app", "wellness-app"})
}

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
func TestHealthKVSharedWriteAccess(t *testing.T) {
	t.Parallel()
	url := startServerFromConf(t)

	boot := connectAs(t, url, "bootstrap")
	provision(t, boot, "health-kv")

	for _, name := range nonBootstrapComponentNames() {
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

// TestRegistryDrivenStreamAdminSideChannel generalizes
// TestBackingStreamSideChannel / TestChroniclerBackingStreamSideChannel
// matrix-wide (design §7 item 2, closing the natsperm-matrix-hygiene-tracked
// debt): for every registered platform bucket, bootstrap (the provisioner)
// may purge the backing stream and every OTHER non-bootstrap component —
// INCLUDING the bucket's own owner — is denied. A row writer never needs to
// administer its own backing stream; bootstrap primordially provisions all
// of them.
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
