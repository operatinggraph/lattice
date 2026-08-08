package identity

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// The driver's whole job before it submits anything is classification: which
// credentials need an edge, which already have one, and which were deliberately
// unlinked. Getting that wrong is what would turn a repair into either a no-op
// or a mass revival of removed credentials, so it is asserted directly, in
// dry-run — where no operation is submitted and the counts are the only output.

const (
	reconCredA = "vtx.identity.JrcnCredAHJKMNPQRSTU"
	reconCredB = "vtx.identity.JrcnCredBHJKMNPQRSTU"
	reconCredC = "vtx.identity.JrcnCredCHJKMNPQRSTU"
	reconCredD = "vtx.identity.JrcnCredDHJKMNPQRSTU"
	reconOwner = "vtx.identity.JrcnWnerHJKMNPQRSTUV"
)

func setupReconcileEnv(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	url := testutil.StartEmbeddedNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: url, Name: "reconcile-test"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(conn.Close)
	testutil.ProvisionHarness(t, ctx, conn)
	return ctx, conn
}

func putDoc(t *testing.T, ctx context.Context, conn *substrate.Conn, key string, doc map[string]any) {
	t.Helper()
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal %s: %v", key, err)
	}
	if _, err := conn.KVPut(ctx, bootstrap.CoreKVBucket, key, data); err != nil {
		t.Fatalf("KVPut %s: %v", key, err)
	}
}

func seedIndex(t *testing.T, ctx context.Context, conn *substrate.Conn, hash, actorKey, ownerKey string, deleted bool) {
	t.Helper()
	putDoc(t, ctx, conn, credentialIndexPrefix+hash, map[string]any{
		"class":     "credentialindex",
		"isDeleted": deleted,
		"data": map[string]any{
			"actorKey":    actorKey,
			"identityKey": ownerKey,
			"boundAt":     "2026-05-01T00:00:00Z",
		},
	})
}

func seedBoundTo(t *testing.T, ctx context.Context, conn *substrate.Conn, credKey, ownerKey string, deleted bool) {
	t.Helper()
	linkKey, err := boundToKey(credKey, ownerKey)
	if err != nil {
		t.Fatalf("boundToKey: %v", err)
	}
	putDoc(t, ctx, conn, linkKey, map[string]any{
		"class":        "boundTo",
		"isDeleted":    deleted,
		"sourceVertex": credKey,
		"targetVertex": ownerKey,
		"localName":    "boundTo",
		"data":         map[string]any{"boundAt": "2026-05-01T00:00:00Z"},
	})
}

// TestReconcileBindings_ClassifiesEachCase covers all four states one pass can
// meet. C is the one that matters most: a tombstoned index is a credential
// somebody removed, and counting it as work would ask the operation to revive
// an unbinding a person chose.
func TestReconcileBindings_ClassifiesEachCase(t *testing.T) {
	ctx, conn := setupReconcileEnv(t)

	// A — the migration case: live index, no edge at all.
	seedIndex(t, ctx, conn, "reconHashAAAAAAAAAAA", reconCredA, reconOwner, false)
	// B — already converged: live index, live edge.
	seedIndex(t, ctx, conn, "reconHashBBBBBBBBBBB", reconCredB, reconOwner, false)
	seedBoundTo(t, ctx, conn, reconCredB, reconOwner, false)
	// C — deliberately unlinked: both tombstoned.
	seedIndex(t, ctx, conn, "reconHashCCCCCCCCCCC", reconCredC, reconOwner, true)
	seedBoundTo(t, ctx, conn, reconCredC, reconOwner, true)
	// D — the diverged shape: live index, TOMBSTONED edge. Nothing currently
	// writes a boundTo tombstone without also retiring its index, but the
	// reconciler must not trust that and republish anyway — counting it as
	// work would restore, decrypt-free, whatever credential-to-person
	// association the missing link was severing.
	seedIndex(t, ctx, conn, "reconHashDDDDDDDDDDD", reconCredD, reconOwner, false)
	seedBoundTo(t, ctx, conn, reconCredD, reconOwner, true)
	// E — a merge's implicit self-credential: an index whose actorKey and
	// identityKey are the same vertex. No edge can exist for it, and submitting
	// it earns a self-loop rejection on every run forever.
	seedIndex(t, ctx, conn, "reconHashEEEEEEEEEEE", reconOwner, reconOwner, false)

	report, err := reconcileBindings(ctx, conn, testActorKey, true)
	if err != nil {
		t.Fatalf("reconcileBindings: %v", err)
	}
	if report.Scanned != 5 {
		t.Fatalf("scanned = %d, want 5", report.Scanned)
	}
	if report.AlreadyOK != 1 {
		t.Errorf("alreadyLinked = %d, want 1 (B)", report.AlreadyOK)
	}
	if report.Tombstoned != 1 {
		t.Errorf("tombstoned = %d, want 1 (C) — an unlinked credential must never be counted as work", report.Tombstoned)
	}
	if report.Retracted != 1 {
		t.Errorf("retracted = %d, want 1 (D) — an erased edge must never be counted as work", report.Retracted)
	}
	if report.SelfLoop != 1 {
		t.Errorf("selfLoop = %d, want 1 (E) — a merge self-credential would reject on every run", report.SelfLoop)
	}
	if report.Submitted != 1 {
		t.Errorf("submitted = %d, want 1 (A alone)", report.Submitted)
	}
	if report.Rejected != 0 {
		t.Errorf("rejected = %d, want 0 in dry-run", report.Rejected)
	}
	if !report.DryRun {
		t.Error("report does not record that this was a dry run")
	}
}

// TestReconcileBindings_ShortKeyIsLoudNotAPanic — both halves of the link key
// come out of a stored document. A blind slice would take the whole run down
// with a runtime panic instead of naming the malformed vertex.
func TestReconcileBindings_ShortKeyIsLoudNotAPanic(t *testing.T) {
	ctx, conn := setupReconcileEnv(t)

	putDoc(t, ctx, conn, credentialIndexPrefix+"reconHashFFFFFFFFFFF", map[string]any{
		"class":     "credentialindex",
		"isDeleted": false,
		"data":      map[string]any{"actorKey": "vtx.id", "identityKey": reconOwner},
	})

	if _, err := reconcileBindings(ctx, conn, testActorKey, true); err == nil {
		t.Fatal("want an error for a malformed actorKey, got nil")
	}
}

// TestReconcileBindings_EmptyKeyspaceIsClean — a corpus with no credentials at
// all must report zeroes rather than error. The prefix lister returns no keys
// there, and a driver that treated that as a failure would make the command
// unrunnable on a fresh stack.
func TestReconcileBindings_EmptyKeyspaceIsClean(t *testing.T) {
	ctx, conn := setupReconcileEnv(t)

	report, err := reconcileBindings(ctx, conn, testActorKey, true)
	if err != nil {
		t.Fatalf("reconcileBindings on an empty keyspace: %v", err)
	}
	if report.Scanned != 0 || report.Submitted != 0 {
		t.Fatalf("scanned=%d submitted=%d, want 0/0", report.Scanned, report.Submitted)
	}
}

// TestReconcileBindings_MalformedIndexFailsLoud — an index vertex naming no
// owner cannot be reconciled, and guessing one would write an edge nothing
// asserts. The driver stops instead of skipping quietly.
func TestReconcileBindings_MalformedIndexFailsLoud(t *testing.T) {
	ctx, conn := setupReconcileEnv(t)

	putDoc(t, ctx, conn, credentialIndexPrefix+"reconHashEEEEEEEEEEE", map[string]any{
		"class":     "credentialindex",
		"isDeleted": false,
		"data":      map[string]any{"actorKey": reconCredA},
	})

	if _, err := reconcileBindings(ctx, conn, testActorKey, true); err == nil {
		t.Fatal("want an error for an index vertex with no identityKey, got nil")
	}
}
