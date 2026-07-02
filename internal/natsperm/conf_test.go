package natsperm

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/asolgan/lattice/internal/jsstore"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/nats-io/nats-server/v2/server"
	natsserver "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go/jetstream"
)

// deniedTimeout bounds a publish we expect the server to reject: a denied
// JetStream publish receives no PubAck (the permissions violation is delivered
// out-of-band on the connection), so the Put blocks until its context expires.
// The owner's positive write on the same bucket returns promptly, so a timeout
// here means "the write was rejected" — the only variable between the owner and
// the rogue is the connection's permission set.
const deniedTimeout = 2 * time.Second

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
	opts, err := server.ProcessConfigFile(confPath(t))
	if err != nil {
		t.Fatalf("parse deploy/nats-server.conf: %v", err)
	}
	if len(opts.Nkeys) == 0 {
		t.Fatal("config parsed but defined no NKey users")
	}
	opts.Port = -1
	opts.JetStream = true
	opts.StoreDir = jsstore.Dir(t)
	opts.NoLog = true
	opts.NoSigs = true
	s := natsserver.RunServer(opts)
	t.Cleanup(s.Shutdown)
	return s.ClientURL()
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
	if got, want := len(opts.Nkeys), 13; got != want {
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

// TestCoreKVWriteIsolation: only the processor (and the bootstrap provisioner)
// may write Core KV; every other component — including refractor, which holds a
// broad $KV.> grant but an explicit $KV.core-kv.> deny — is rejected.
func TestCoreKVWriteIsolation(t *testing.T) {
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

	assertDeniedPuts(t, url, "core-kv", []string{"refractor", "loom", "weaver", "bridge", "loupe", "lattice", "gateway", "loftspace-app", "clinic-app", "object-store-manager"})
}

// TestCapabilityKVWriteIsolation: only refractor (and bootstrap) may write the
// auth projection; even the processor — the Core-KV owner — is denied.
func TestCapabilityKVWriteIsolation(t *testing.T) {
	url := startServerFromConf(t)

	boot := connectAs(t, url, "bootstrap")
	provision(t, boot, "capability-kv")

	ref := connectAs(t, url, "refractor")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := ref.KVPut(ctx, "capability-kv", "cap.test", []byte("v")); err != nil {
		t.Fatalf("refractor KVPut capability-kv: want success, got %v", err)
	}

	assertDeniedPuts(t, url, "capability-kv", []string{"processor", "loom", "weaver", "loupe", "lattice", "gateway"})
}

// TestLensTargetWriteIsolation: refractor (the sole projector) may write a
// lens-target read model; a non-projector cannot (it is not in its allow-list).
func TestLensTargetWriteIsolation(t *testing.T) {
	url := startServerFromConf(t)

	boot := connectAs(t, url, "bootstrap")
	provision(t, boot, "weaver-targets")

	ref := connectAs(t, url, "refractor")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := ref.KVPut(ctx, "weaver-targets", "target.1", []byte("v")); err != nil {
		t.Fatalf("refractor KVPut weaver-targets: want success, got %v", err)
	}

	assertDeniedPuts(t, url, "weaver-targets", []string{"loom", "loupe", "lattice", "gateway"})
}

// TestBackingStreamSideChannel: denying $KV.core-kv.> publish is not enough — a
// holder of the broad $JS.API.> grant could otherwise destroy the backing
// stream directly. The stream-admin verbs on KV_core-kv are denied for
// non-owners; the owner (processor) retains them.
func TestBackingStreamSideChannel(t *testing.T) {
	url := startServerFromConf(t)

	boot := connectAs(t, url, "bootstrap")
	provision(t, boot, "core-kv")

	// processor (owner) may purge its own stream.
	proc := connectAs(t, url, "processor")
	if _, err := proc.NATS().Request("$JS.API.STREAM.PURGE.KV_core-kv", []byte("{}"), 3*time.Second); err != nil {
		t.Fatalf("processor PURGE KV_core-kv: want success, got %v", err)
	}

	// a non-owner's purge is denied at the door — the request gets no reply.
	for _, component := range []string{"loom", "loupe", "refractor"} {
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
