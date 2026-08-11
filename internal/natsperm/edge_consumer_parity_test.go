//go:build edgeparity

// The consumer-create wire-form parity test (edge-browser-node-design.md
// §2.3/§5): the browser Edge node's transport is the vendored `nats.js`
// JetStream client, not nats.go, and its create-consumer wire form is the one
// thing that can fail CLOSED in a user's tab while every Go test here passes —
// the ACL grants only the filtered form
// `$JS.API.CONSUMER.CREATE.SYNC.<durable>.<filter>` (internal/gateway/natsauth
// PermissionsFor), so a client emitting a legacy or differently-shaped form is
// denied. The Go vectors in auth_callout_test.go prove that grant against
// nats.go; this proves the SAME grant against the actual browser client, by
// driving the real shell transport core (internal/edge/browser/shell) from Node
// against this package's real callout harness.
//
// It is build-tagged out of the default `go test ./...` because it needs a Node
// runtime and the vendored bundle; it runs under `make test-edge-consumer-parity`
// (CI job edge-consumer-parity), where Node is present.
package natsperm

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// driverDir is the shell package's testdata, relative to this package dir
// (internal/natsperm) — where the Node driver and the vendored bundle live.
const driverRelDir = "../edge/browser/shell/testdata"

// requireNode locates the Node runtime, skipping (not failing) when absent so a
// developer without Node is not blocked; the make target + CI job that own this
// test always have it.
func requireNode(t *testing.T) string {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found on PATH; run via `make test-edge-consumer-parity`")
	}
	return node
}

// driverPath resolves the Node driver's absolute path and asserts the vendored
// bundle it imports is checked in — a missing bundle is a build error to shout
// about, not a reason to skip (the whole point is that the vendored client is
// present and speaks the granted protocol).
func driverPath(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join(driverRelDir, "consumer_create_driver.mjs"))
	if err != nil {
		t.Fatalf("resolve driver path: %v", err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("driver missing: %v", err)
	}
	bundle := filepath.Join(filepath.Dir(abs), "..", "nats.js.mjs")
	if _, err := os.Stat(bundle); err != nil {
		t.Fatalf("vendored nats.js bundle missing (%s): %v — regenerate per internal/edge/browser/shell/VENDOR.md", bundle, err)
	}
	return abs
}

// runDriver runs the Node driver once and returns its final stdout line (the
// machine-readable verdict). A non-zero exit is reported with stderr, since the
// driver exits non-zero exactly when the observed outcome contradicts MODE.
// extraEnv appends further KEY=VALUE pairs (e.g. DURABLE, STARTSEQ) the driver
// reads as overrides — see consumer_create_driver.mjs's env doc.
func runDriver(t *testing.T, node, driver, wsURL, token, identity, device, stream, filter, mode string, extraEnv ...string) (string, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, node, driver)
	cmd.Env = append(os.Environ(),
		"WS_URL="+wsURL,
		"TOKEN="+token,
		"IDENTITY="+identity,
		"DEVICE="+device,
		"STREAM="+stream,
		"FILTER="+filter,
		"MODE="+mode,
	)
	cmd.Env = append(cmd.Env, extraEnv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if s := strings.TrimSpace(stderr.String()); s != "" {
		t.Logf("driver stderr:\n%s", s)
	}
	line := lastLine(stdout.String())
	return line, err == nil
}

func lastLine(s string) string {
	var last string
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		if t := strings.TrimSpace(sc.Text()); t != "" {
			last = t
		}
	}
	return last
}

// TestEdgeConsumerCreateWireFormParity proves the vendored nats.js JetStream
// client emits the ACL-granted consumer-create wire form, under this package's
// real per-identity auth-callout — the same grant auth_callout_test.go pins for
// nats.go, now pinned for the browser client the Facet PWA actually ships.
func TestEdgeConsumerCreateWireFormParity(t *testing.T) {
	node := requireNode(t)
	driver := driverPath(t)

	url, wsURL := startServerFromConfDual(t)
	provisionSyncStream(t, url)
	priv, pub := rsaKeypair(t)
	startResponder(t, url, "test-kid", pub, "")

	const device = "device-1"

	// Positive: the granted filtered-create form succeeds. If nats.js emitted a
	// different create subject, the grant would deny it and this would report
	// CREATE_ERROR (a permission violation) instead.
	t.Run("granted filtered-create form succeeds", func(t *testing.T) {
		identity := nanoID(t)
		tok := mintBearerToken(t, priv, "test-kid", identity, time.Now().Add(time.Hour))
		filter := "lattice.sync.user." + identity
		line, ok := runDriver(t, node, driver, wsURL, tok, identity, device, syncStream, filter, "create")
		if !ok || !strings.HasPrefix(line, "CREATE_OK") {
			t.Fatalf("granted filtered-create: want CREATE_OK, got %q", line)
		}
	})

	// Round-trip: MSG.NEXT + $JS.ACK are granted too, so the full nats.js consume
	// path works under the grant — not just CREATE. A retained delta is published
	// first (the SYNC stream keeps it), so the consumer picks it up with no
	// publish/consume timing coordination.
	t.Run("granted consume+ack round-trips a delta", func(t *testing.T) {
		identity := nanoID(t)
		tok := mintBearerToken(t, priv, "test-kid", identity, time.Now().Add(time.Hour))
		filter := "lattice.sync.user." + identity

		ref := connectAs(t, url, "refractor")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := ref.Publish(ctx, filter, []byte(`{"delta":1}`), nil); err != nil {
			t.Fatalf("refractor publish delta: %v", err)
		}

		line, ok := runDriver(t, node, driver, wsURL, tok, identity, device, syncStream, filter, "roundtrip")
		if !ok || !strings.HasPrefix(line, "DELIVERED") {
			t.Fatalf("granted consume+ack round-trip: want DELIVERED, got %q", line)
		}
	})

	// Negative control: a cross-identity filter is denied. Without this the
	// positive could pass vacuously on a server that grants everything — this
	// proves the grant is the thing being satisfied, and that it is the FILTER
	// (the identity boundary) the create form is pinned to.
	//
	// The verdict is asserted against the EXACT subject the driver expected to
	// be denied, not merely the CREATE_DENIED prefix: the driver reports that
	// prefix only when the vendored client's PermissionViolationError names
	// this precise subject (consumer_create_driver.mjs's isPermissionDenial),
	// so a driver that can't reach the server at all — a dead WS_URL, a bad
	// TOKEN — reports ERROR here and fails this assertion instead of passing
	// on an outcome it never actually observed.
	t.Run("cross-identity filter create is denied", func(t *testing.T) {
		identity := nanoID(t)
		other := nanoID(t)
		tok := mintBearerToken(t, priv, "test-kid", identity, time.Now().Add(time.Hour))
		crossFilter := "lattice.sync.user." + other
		durable := "edge-sync-" + identity + "-" + device
		wantSubject := fmt.Sprintf("$JS.API.CONSUMER.CREATE.%s.%s.%s", syncStream, durable, crossFilter)
		line, ok := runDriver(t, node, driver, wsURL, tok, identity, device, syncStream, crossFilter, "create-denied")
		if !ok || line != "CREATE_DENIED "+wantSubject {
			t.Fatalf("cross-identity filter: want %q, got %q (ok=%v)", "CREATE_DENIED "+wantSubject, line, ok)
		}
	})

	// Positive, the first of Fire 3's two new vectors (edge-cold-signin-
	// delivery-position-design.md §6 Fire 3): a positioned create
	// (deliver_policy=by_start_sequence + opt_start_seq) emits the SAME
	// granted subject an unpositioned create does — this proves adding those
	// two config fields does not change the wire form the grant pins, which
	// "granted filtered-create form succeeds" above pins for the unpositioned
	// shape.
	t.Run("positioned create succeeds", func(t *testing.T) {
		identity := nanoID(t)
		tok := mintBearerToken(t, priv, "test-kid", identity, time.Now().Add(time.Hour))
		filter := "lattice.sync.user." + identity
		line, ok := runDriver(t, node, driver, wsURL, tok, identity, device, syncStream, filter, "create-positioned")
		if !ok || !strings.HasPrefix(line, "CREATE_OK") {
			t.Fatalf("positioned create: want CREATE_OK, got %q", line)
		}
	})

	// Positive, the second new vector: DELETE.SYNC.<durable> granted against
	// a REAL consumer, not only a not-found one (every startConsumer call
	// deletes first, so the positive vectors above already exercise
	// delete-when-not-found for free). The driver attaches once, detaches,
	// then repositions on the SAME durable — self-verifying, because a
	// server that never saw the delete would reject the changed
	// DeliverPolicy outright (nats-server 2.14 server/consumer.go:2435) and
	// this would report CREATE_ERROR, not REPOSITIONED.
	t.Run("delete against a real consumer succeeds (reposition)", func(t *testing.T) {
		identity := nanoID(t)
		tok := mintBearerToken(t, priv, "test-kid", identity, time.Now().Add(time.Hour))
		filter := "lattice.sync.user." + identity
		line, ok := runDriver(t, node, driver, wsURL, tok, identity, device, syncStream, filter, "delete")
		if !ok || !strings.HasPrefix(line, "REPOSITIONED") {
			t.Fatalf("reposition: want REPOSITIONED, got %q", line)
		}
	})

	// Negative control for the delete vector, proven only after the positive
	// above: a delete targeting a durable name outside this token's grant (a
	// different identity's) is denied, the same identity-boundary shape
	// "cross-identity filter create is denied" pins for create.
	//
	// "delete-denied" mode calls jsm.consumers.delete directly (no create
	// anywhere in the call) specifically so this vector cannot pass for the
	// wrong reason: overriding DURABLE alone puts BOTH verbs outside the
	// grant, so a mode driving the whole startConsumer could not tell which
	// one was denied — a shell.mjs regression that widened the not-found
	// tolerance into a swallow-all would still "deny" on the create and this
	// would pass without ever proving the delete itself is guarded. Asserting
	// the exact subject closes the same wrong-reason gap create-denied above
	// closes.
	t.Run("cross-identity delete is denied", func(t *testing.T) {
		identity := nanoID(t)
		other := nanoID(t)
		tok := mintBearerToken(t, priv, "test-kid", identity, time.Now().Add(time.Hour))
		filter := "lattice.sync.user." + identity
		crossDurable := "edge-sync-" + other + "-" + device
		wantSubject := fmt.Sprintf("$JS.API.CONSUMER.DELETE.%s.%s", syncStream, crossDurable)
		line, ok := runDriver(t, node, driver, wsURL, tok, identity, device, syncStream, filter, "delete-denied",
			"DURABLE="+crossDurable)
		if !ok || line != "DELETE_DENIED "+wantSubject {
			t.Fatalf("cross-identity delete: want %q, got %q (ok=%v)", "DELETE_DENIED "+wantSubject, line, ok)
		}
	})
}
