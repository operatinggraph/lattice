package substrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/jsstore"
	"github.com/nats-io/nats-server/v2/server"
	natsserver "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
)

// newUserNKey mints a fresh NATS user NKey, writes its seed to a temp file,
// and returns (seedFilePath, publicKey). The seed authenticates the client;
// the public key is what the server's user list is configured with.
func newUserNKey(t *testing.T) (seedFile, publicKey string) {
	t.Helper()
	kp, err := nkeys.CreateUser()
	if err != nil {
		t.Fatalf("create user nkey: %v", err)
	}
	seed, err := kp.Seed()
	if err != nil {
		t.Fatalf("extract seed: %v", err)
	}
	pub, err := kp.PublicKey()
	if err != nil {
		t.Fatalf("derive public key: %v", err)
	}
	path := filepath.Join(t.TempDir(), "user.nk")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed file: %v", err)
	}
	return path, pub
}

// writeUserSeed mints a fresh NATS user NKey and writes only its seed to a
// temp file — a valid seed whose public key no server is configured to accept.
func writeUserSeed(t *testing.T) string {
	t.Helper()
	seedFile, _ := newUserNKey(t)
	return seedFile
}

// startEmbeddedNATSWithNKey runs an in-process JetStream server that requires
// NKey authentication for the single configured user (publicKey). An anonymous
// or mismatched client is rejected at the handshake.
func startEmbeddedNATSWithNKey(t *testing.T, publicKey string) (url string) {
	t.Helper()
	opts := natsserver.DefaultTestOptions
	opts.Port = -1
	opts.JetStream = true
	opts.StoreDir = jsstore.Dir(t)
	opts.Nkeys = []*server.NkeyUser{{Nkey: publicKey}}
	s := natsserver.RunServer(&opts)
	t.Cleanup(func() {
		if jsCfg := s.JetStreamConfig(); jsCfg != nil {
			defer os.RemoveAll(jsCfg.StoreDir)
		}
		s.Shutdown()
	})
	return s.ClientURL()
}

// Empty credential fields ⇒ today's anonymous connect: the compatibility
// hinge that keeps the embedded harness (and every existing test) unchanged.
func TestConnect_EmptyCredentials_AnonymousRoundTrip(t *testing.T) {
	t.Parallel()
	url := startEmbeddedNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	c, err := Connect(ctx, ConnectOpts{URL: url, Name: "anon-test"})
	if err != nil {
		t.Fatalf("anonymous Connect: %v", err)
	}
	t.Cleanup(c.Close)

	const bucket = "anon-bucket"
	provisionCoreBucket(ctx, t, c, bucket)
	if _, err := c.KVPut(ctx, bucket, "k", []byte("v")); err != nil {
		t.Fatalf("KVPut: %v", err)
	}
	entry, err := c.KVGet(ctx, bucket, "k")
	if err != nil {
		t.Fatalf("KVGet: %v", err)
	}
	if string(entry.Value) != "v" {
		t.Fatalf("KVGet = %q, want %q", entry.Value, "v")
	}
}

// End-to-end credential proof against an NKey-authenticated server: the
// matching seed authenticates and round-trips, while an anonymous client
// (empty credentials) is rejected at the handshake. This proves the seam
// actually applies the credential — a dropped option would fail the positive
// case, and a credential that wasn't required would pass the negative one.
func TestConnect_NKeyAuthenticatedServer(t *testing.T) {
	t.Parallel()
	seedFile, pub := newUserNKey(t)
	url := startEmbeddedNATSWithNKey(t, pub)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	// Matching seed → authenticated connect + KV round-trip.
	c, err := Connect(ctx, ConnectOpts{URL: url, Name: "nkey-test", NKeySeedFile: seedFile})
	if err != nil {
		t.Fatalf("authenticated NKey Connect: %v", err)
	}
	t.Cleanup(c.Close)

	const bucket = "nkey-bucket"
	provisionCoreBucket(ctx, t, c, bucket)
	if _, err := c.KVPut(ctx, bucket, "k", []byte("v")); err != nil {
		t.Fatalf("KVPut over authenticated nkey conn: %v", err)
	}

	// No credentials → the auth-required server rejects the connection.
	if _, err := Connect(ctx, ConnectOpts{URL: url, Name: "anon-rejected"}); err == nil {
		t.Fatal("expected anonymous Connect to be rejected by the NKey-auth server, got nil")
	}
}

// A malformed seed file must fail loudly at the seam (before dialing), not
// degrade to an anonymous connect — proving the NKey path is actually wired.
func TestConnect_BadNKeySeed_Errors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "garbage.nk")
	if err := os.WriteFile(path, []byte("not-a-valid-nkey-seed"), 0o600); err != nil {
		t.Fatalf("write garbage seed: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	_, err := Connect(ctx, ConnectOpts{URL: "nats://127.0.0.1:1", NKeySeedFile: path})
	if err == nil {
		t.Fatal("expected error for malformed NKey seed, got nil")
	}
	if !strings.Contains(err.Error(), "load NKey seed") {
		t.Fatalf("error = %q, want it to mention loading the NKey seed", err)
	}
}

// A missing credentials file surfaces as a connect error rather than a silent
// anonymous fallback.
func TestConnect_MissingCredsFile_Errors(t *testing.T) {
	t.Parallel()
	url := startEmbeddedNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	_, err := Connect(ctx, ConnectOpts{URL: url, CredsFile: filepath.Join(t.TempDir(), "absent.creds")})
	if err == nil {
		t.Fatal("expected error for missing creds file, got nil")
	}
}

// Wrap adapts a caller-constructed *nats.Conn (dialed with options substrate
// does not expose via ConnectOpts) into a working *Conn: KV operations must
// round-trip exactly as they do through Connect.
func TestWrap_RoundTrip(t *testing.T) {
	t.Parallel()
	url := startEmbeddedNATS(t)
	nc, err := nats.Connect(url, nats.Name("wrap-test"))
	if err != nil {
		t.Fatalf("nats.Connect: %v", err)
	}
	t.Cleanup(nc.Close)

	c, err := Wrap(nc)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if c.NATS() != nc {
		t.Fatalf("Wrap must retain the caller's *nats.Conn")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	const bucket = "wrap-bucket"
	provisionCoreBucket(ctx, t, c, bucket)
	if _, err := c.KVPut(ctx, bucket, "k", []byte("v")); err != nil {
		t.Fatalf("KVPut over a Wrap()'d conn: %v", err)
	}
	entry, err := c.KVGet(ctx, bucket, "k")
	if err != nil || string(entry.Value) != "v" {
		t.Fatalf("KVGet over a Wrap()'d conn: entry=%v err=%v", entry, err)
	}
}

// Wrap itself only constructs a JetStream context over nc — it does not probe
// the connection, so it succeeds even against an already-closed *nats.Conn.
// The closed connection surfaces at the first actual KV operation instead.
func TestWrap_ClosedConn_FailsOnUse(t *testing.T) {
	t.Parallel()
	url := startEmbeddedNATS(t)
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatalf("nats.Connect: %v", err)
	}
	nc.Close()

	c, err := Wrap(nc)
	if err != nil {
		t.Fatalf("Wrap over a closed *nats.Conn must still construct, got: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	if _, err := c.KVPut(ctx, "any-bucket", "k", []byte("v")); err == nil {
		t.Fatal("expected a KV operation over a closed connection to fail, got nil")
	}
}

// Supplying both credential kinds is a configuration error caught before any
// dial — exactly one credential may authenticate a connection.
func TestConnect_BothCredentials_Rejected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	_, err := Connect(ctx, ConnectOpts{
		URL:          "nats://127.0.0.1:1",
		NKeySeedFile: writeUserSeed(t),
		CredsFile:    filepath.Join(t.TempDir(), "x.creds"),
	})
	if err == nil {
		t.Fatal("expected error when both NKeySeedFile and CredsFile are set, got nil")
	}
	if !strings.Contains(err.Error(), "exactly one credential") {
		t.Fatalf("error = %q, want the both-set guard message", err)
	}
}

// A Token alongside another credential kind is rejected the same way —
// Token is a third mutually-exclusive credential (per-identity subscribe-ACL
// design §7: cmd/edge's bearer-token connect).
func TestConnect_TokenWithNKeySeed_Rejected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	_, err := Connect(ctx, ConnectOpts{
		URL:          "nats://127.0.0.1:1",
		NKeySeedFile: writeUserSeed(t),
		Token:        "some.jwt.token",
	})
	if err == nil {
		t.Fatal("expected error when both NKeySeedFile and Token are set, got nil")
	}
	if !strings.Contains(err.Error(), "exactly one credential") {
		t.Fatalf("error = %q, want the both-set guard message", err)
	}
}

// Token and TokenHandler are mutually exclusive too — nats.go itself refuses
// both set (ErrTokenAlreadySet), so substrate's own guard catches it before
// ever dialing, matching every other credential-pair rejection above.
func TestConnect_TokenWithTokenHandler_Rejected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	_, err := Connect(ctx, ConnectOpts{
		URL:          "nats://127.0.0.1:1",
		Token:        "static-token",
		TokenHandler: func() string { return "dynamic-token" },
	})
	if err == nil {
		t.Fatal("expected error when both Token and TokenHandler are set, got nil")
	}
	if !strings.Contains(err.Error(), "exactly one credential") {
		t.Fatalf("error = %q, want the both-set guard message", err)
	}
}

// startEmbeddedNATSWithToken runs an in-process JetStream server that
// requires the single static token to authenticate — the same fixed-secret
// posture opts.Authorization configures, mirroring startEmbeddedNATSWithNKey
// for the Token/TokenHandler credential kind.
func startEmbeddedNATSWithToken(t *testing.T, token string) (url string) {
	t.Helper()
	opts := natsserver.DefaultTestOptions
	opts.Port = -1
	opts.JetStream = true
	opts.StoreDir = jsstore.Dir(t)
	opts.Authorization = token
	s := natsserver.RunServer(&opts)
	t.Cleanup(func() {
		if jsCfg := s.JetStreamConfig(); jsCfg != nil {
			defer os.RemoveAll(jsCfg.StoreDir)
		}
		s.Shutdown()
	})
	return s.ClientURL()
}

// TestConnect_TokenHandler_AuthenticatesLikeToken proves TokenHandler is
// actually wired into the dial (not silently dropped): the value IT RETURNS,
// not a fixed string, is what the token-requiring server sees. The seam this
// exists for (cmd/facet's newEngine, engine.go) needs exactly this — a
// caller who re-mints on every (re)connect attempt rather than replaying a
// value captured once.
func TestConnect_TokenHandler_AuthenticatesLikeToken(t *testing.T) {
	t.Parallel()
	const want = "the-current-token"
	url := startEmbeddedNATSWithToken(t, want)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	var calls int
	c, err := Connect(ctx, ConnectOpts{
		URL:  url,
		Name: "tokenhandler-test",
		TokenHandler: func() string {
			calls++
			return want
		},
	})
	if err != nil {
		t.Fatalf("TokenHandler-authenticated Connect: %v", err)
	}
	t.Cleanup(c.Close)
	if calls == 0 {
		t.Fatal("TokenHandler was never invoked — the option did not reach nats.go's dial")
	}

	const bucket = "tokenhandler-bucket"
	provisionCoreBucket(ctx, t, c, bucket)
	if _, err := c.KVPut(ctx, bucket, "k", []byte("v")); err != nil {
		t.Fatalf("KVPut over a TokenHandler-authenticated conn: %v", err)
	}

	// A handler returning the WRONG token is rejected exactly like a wrong
	// static Token would be — proving the returned value, not merely the
	// handler's presence, is what the server checks.
	if _, err := Connect(ctx, ConnectOpts{
		URL:          url,
		Name:         "tokenhandler-wrong",
		TokenHandler: func() string { return "not-the-token" },
	}); err == nil {
		t.Fatal("expected Connect with a wrong TokenHandler value to be rejected, got nil")
	}
}
