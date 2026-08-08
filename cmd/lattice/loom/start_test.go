package loom

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

func setupStartEnv(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	url := testutil.StartEmbeddedNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: url, Name: "loom-start-test"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(conn.Close)
	testutil.ProvisionHarness(t, ctx, conn)
	return ctx, conn
}

// seedMetaVertex writes a meta-vertex of the given class plus its `.spec`
// aspect — the two-key shape a live loom pattern actually has (verified against
// a running stack: a pattern vertex carries a root and a `.spec`, and no
// `.canonicalName`, which is why the name is read off the spec's patternId
// rather than through the reader `lattice lens` uses).
//
// The spec body is wrapped in the standard envelope's `data`, the shape
// pkgmgr installs and Weaver's unwrapSpecBody probes.
func seedMetaVertex(t *testing.T, ctx context.Context, conn *substrate.Conn, key, class, patternID string, deleted bool) {
	t.Helper()
	vtx, err := json.Marshal(map[string]any{
		"class": class, "vertexKey": key, "isDeleted": deleted, "data": map[string]any{},
	})
	if err != nil {
		t.Fatalf("marshal %s: %v", key, err)
	}
	if _, err := conn.KVPut(ctx, bootstrap.CoreKVBucket, key, vtx); err != nil {
		t.Fatalf("seed %s: %v", key, err)
	}
	if patternID == "" {
		return
	}
	asp, err := json.Marshal(map[string]any{
		"class": "spec", "vertexKey": key, "localName": "spec", "isDeleted": false,
		"data": map[string]any{
			"patternId":   patternID,
			"subjectType": "identity",
			"steps":       []any{},
		},
	})
	if err != nil {
		t.Fatalf("marshal %s.spec: %v", key, err)
	}
	if _, err := conn.KVPut(ctx, bootstrap.CoreKVBucket, key+".spec", asp); err != nil {
		t.Fatalf("seed %s.spec: %v", key, err)
	}
}

// TestResolvePatternKey_ByVertexNanoID — the third reference form Weaver's
// registry accepts. An operator reading `loom list` or a Health card has the
// vertex id, not always the patternId.
func TestResolvePatternKey_ByVertexNanoID(t *testing.T) {
	ctx, conn := setupStartEnv(t)
	seedMetaVertex(t, ctx, conn, erasurePatternKey, loomPatternClass, "identityErasure", false)

	got, err := resolvePatternKey(ctx, conn, strings.TrimPrefix(erasurePatternKey, "vtx.meta."))
	if err != nil {
		t.Fatalf("resolvePatternKey by vertex id: %v", err)
	}
	if got != erasurePatternKey {
		t.Fatalf("resolved to %q, want %q", got, erasurePatternKey)
	}
}

// TestResolvePatternKey_SpecWithoutPatternIdIsStillReachable — a spec carrying
// no patternId leaves the pattern nameless, but it must not vanish: it is still
// reachable by key and must still be listed, or an operator sees a pattern in
// `loom list` that this command claims is not installed.
func TestResolvePatternKey_SpecWithoutPatternIdIsStillReachable(t *testing.T) {
	ctx, conn := setupStartEnv(t)
	seedMetaVertex(t, ctx, conn, erasurePatternKey, loomPatternClass, "", false)

	got, err := resolvePatternKey(ctx, conn, erasurePatternKey)
	if err != nil {
		t.Fatalf("a pattern with no patternId is unreachable by its own key: %v", err)
	}
	if got != erasurePatternKey {
		t.Fatalf("resolved to %q, want %q", got, erasurePatternKey)
	}

	_, err = resolvePatternKey(ctx, conn, "anything")
	if err == nil || !strings.Contains(err.Error(), erasurePatternKey) {
		t.Fatalf("error = %v, want the nameless pattern listed by key so the operator can reach it", err)
	}
}

const (
	erasurePatternKey = "vtx.meta.PatErasureAAAAAAAAAA"
	secondPatternKey  = "vtx.meta.PatSecondBBBBBBBBBBB"
)

// TestResolvePatternKey_ByCanonicalName is the whole point of the subcommand.
// An operator knows the pattern as "identityErasure"; the operation must carry
// the meta-vertex key, both as patternRef and as authContext.target, because
// per-pattern authorization anchors on the definition vertex (Contract #10
// §10.8). Resolving by hand is what makes `lattice op submit` the wrong tool.
func TestResolvePatternKey_ByCanonicalName(t *testing.T) {
	ctx, conn := setupStartEnv(t)
	seedMetaVertex(t, ctx, conn, erasurePatternKey, loomPatternClass, "identityErasure", false)
	seedMetaVertex(t, ctx, conn, secondPatternKey, loomPatternClass, "applicantOnboarding", false)

	got, err := resolvePatternKey(ctx, conn, "identityErasure")
	if err != nil {
		t.Fatalf("resolvePatternKey: %v", err)
	}
	if got != erasurePatternKey {
		t.Fatalf("resolved to %q, want %q", got, erasurePatternKey)
	}
}

// TestResolvePatternKey_ByMetaKeyIsVerifiedNotTrusted — a vtx.meta reference is
// checked against the installed patterns rather than passed through. An
// authContext.target naming some other meta-vertex would authorize against the
// wrong vertex, and the refusal that follows names neither the pattern nor the
// reason.
func TestResolvePatternKey_ByMetaKeyIsVerifiedNotTrusted(t *testing.T) {
	ctx, conn := setupStartEnv(t)
	seedMetaVertex(t, ctx, conn, erasurePatternKey, loomPatternClass, "identityErasure", false)
	// A meta-vertex of a DIFFERENT class, which a pass-through would accept.
	seedMetaVertex(t, ctx, conn, "vtx.meta.PatNotAPatternCCCCCC", "meta.ddl.vertexType", "someDDL", false)

	if got, err := resolvePatternKey(ctx, conn, erasurePatternKey); err != nil || got != erasurePatternKey {
		t.Fatalf("resolvePatternKey(%s) = (%q, %v), want the key and no error", erasurePatternKey, got, err)
	}

	_, err := resolvePatternKey(ctx, conn, "vtx.meta.PatNotAPatternCCCCCC")
	if err == nil {
		t.Fatal("a meta-vertex that is not a meta.loomPattern resolved — the op would have authorized against the wrong target")
	}
	if !strings.Contains(err.Error(), "not a live meta.loomPattern") {
		t.Fatalf("error = %v, want it to say the reference is not a pattern", err)
	}
}

// TestResolvePatternKey_TombstonedPatternDoesNotResolve — a retired pattern
// must not start. It is still in the bucket (Core KV holds logical deletes by
// design), so a scan that ignored isDeleted would start an instance of a
// pattern nothing maintains.
func TestResolvePatternKey_TombstonedPatternDoesNotResolve(t *testing.T) {
	ctx, conn := setupStartEnv(t)
	seedMetaVertex(t, ctx, conn, erasurePatternKey, loomPatternClass, "identityErasure", true)

	for _, ref := range []string{"identityErasure", erasurePatternKey} {
		if _, err := resolvePatternKey(ctx, conn, ref); err == nil {
			t.Fatalf("resolvePatternKey(%q) resolved a tombstoned pattern", ref)
		}
	}
}

// TestResolvePatternKey_UnknownNameListsWhatIsInstalled — the error an operator
// actually hits is a typo, and it is only useful if it says what the choices
// were.
func TestResolvePatternKey_UnknownNameListsWhatIsInstalled(t *testing.T) {
	ctx, conn := setupStartEnv(t)
	seedMetaVertex(t, ctx, conn, erasurePatternKey, loomPatternClass, "identityErasure", false)

	_, err := resolvePatternKey(ctx, conn, "identityErasre")
	if err == nil {
		t.Fatal("a misspelled pattern name resolved")
	}
	if !strings.Contains(err.Error(), "identityErasure") {
		t.Fatalf("error = %v, want it to list the installed patterns so the typo is visible", err)
	}
}

// TestResolvePatternKey_AmbiguousNameRefuses — two live vertices sharing a
// canonical name is a corrupt registry, and picking either would start a
// pattern the operator did not choose.
func TestResolvePatternKey_AmbiguousNameRefuses(t *testing.T) {
	ctx, conn := setupStartEnv(t)
	seedMetaVertex(t, ctx, conn, erasurePatternKey, loomPatternClass, "identityErasure", false)
	seedMetaVertex(t, ctx, conn, secondPatternKey, loomPatternClass, "identityErasure", false)

	_, err := resolvePatternKey(ctx, conn, "identityErasure")
	if err == nil {
		t.Fatal("an ambiguous pattern name resolved — the op would have started an arbitrary one of the two")
	}
	for _, want := range []string{erasurePatternKey, secondPatternKey} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %v, want it to name %s so the operator can disambiguate", err, want)
		}
	}
}
