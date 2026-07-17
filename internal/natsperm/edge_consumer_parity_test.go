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
func runDriver(t *testing.T, node, driver, wsURL, token, identity, device, stream, filter, mode string) (string, bool) {
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
	t.Run("cross-identity filter create is denied", func(t *testing.T) {
		identity := nanoID(t)
		other := nanoID(t)
		tok := mintBearerToken(t, priv, "test-kid", identity, time.Now().Add(time.Hour))
		crossFilter := "lattice.sync.user." + other
		line, ok := runDriver(t, node, driver, wsURL, tok, identity, device, syncStream, crossFilter, "create-denied")
		if !ok || !strings.HasPrefix(line, "CREATE_DENIED") {
			t.Fatalf("cross-identity filter: want CREATE_DENIED, got %q (ok=%v)", line, ok)
		}
	})
}
