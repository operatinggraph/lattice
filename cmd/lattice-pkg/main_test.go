package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// writeBootstrapFile renders the file loadBootstrap reads, from the primordial
// ids the harness actually seeded — so the actor this CLI stamps on its ops is
// the one the kernel knows.
func writeBootstrapFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "lattice.bootstrap.json")
	raw, err := json.Marshal(map[string]any{
		"primordialIDs": map[string]string{
			"bootstrapIdentity": strings.TrimPrefix(bootstrap.BootstrapIdentityKey, "vtx.identity."),
			"operatorRole":      bootstrap.RoleOperatorID,
		},
	})
	if err != nil {
		t.Fatalf("marshal bootstrap file: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write bootstrap file: %v", err)
	}
	return path
}

// putProposalAspect writes one {isDeleted,data} aspect envelope into Core KV.
func putProposalAspect(t *testing.T, ctx context.Context, conn *substrate.Conn, key string, data map[string]any) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"isDeleted": false, "data": data})
	if err != nil {
		t.Fatalf("marshal %s: %v", key, err)
	}
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key, raw); err != nil {
		t.Fatalf("KVPut %s: %v", key, err)
	}
}

// multiEntityPackage is a vertical, non-platform-protected package with more in
// it than any single capability artifact could describe.
func multiEntityPackage() pkgmgr.Definition {
	return pkgmgr.Definition{
		Name:        "latticepkg-apply-target",
		Version:     "0.1.0",
		Description: "A multi-entity vertical package an AI-authored proposal may target.",
		Roles: []pkgmgr.RoleSpec{{
			CanonicalName: "latticePkgReviewer",
			Description:   "Reviews the target package's records.",
		}},
		Lenses: []pkgmgr.LensSpec{
			{
				CanonicalName: "latticePkgRosterLens",
				Class:         "meta.lens",
				Adapter:       "nats-kv",
				Bucket:        "latticepkg-roster",
				Engine:        "full",
				Spec:          "MATCH (p:provider) RETURN p.key AS key",
			},
			{
				CanonicalName: "latticePkgAuditLens",
				Class:         "meta.lens",
				Adapter:       "nats-kv",
				Bucket:        "latticepkg-audit",
				Engine:        "full",
				Spec:          "MATCH (a:audit) RETURN a.key AS key",
			},
		},
	}
}

// TestRunApplyProposal_RemovalRefusalStopsBeforeMarkApplied drives the CLI verb
// end to end against a real embedded stack: an approved proposal whose one lens
// does not describe the package it upgrades.
//
// The refusal reaching the caller is half of it. The other half is that
// MarkCapabilityProposalApplied is never submitted — an apply that committed
// nothing followed by a proposal stamped `applied` is a falsified audit record
// on top of a package that never changed, which is the worse of the two harms.
// The assertion is on the operations stream rather than on the proposal's own
// review state, because a submitted-then-rejected op would leave that state
// looking identical to a submit that never happened.
func TestRunApplyProposal_RemovalRefusalStopsBeforeMarkApplied(t *testing.T) {
	ctx, conn := testutil.SetupPackageTestEnv(t)

	stop := testutil.RunMetaInstallPipeline(t, ctx, conn)
	inst := testutil.NewInstaller(conn, bootstrap.BootstrapIdentityKey)
	inst.RoleIDs = testutil.StandardRoleIDs()
	target := multiEntityPackage()
	if _, err := inst.Install(ctx, target); err != nil {
		t.Fatalf("install the target package: %v", err)
	}
	stop()

	// An approved proposal carrying one lens, targeting that package as an
	// upgrade it has no coverage of.
	const proposalID = "CALatticePkgHJKMNPQR"
	proposalKey := "vtx.capabilityproposal." + proposalID
	content, err := json.Marshal(pkgmgr.LensArtifactContent{
		CanonicalName: "latticePkgProposedLens",
		Adapter:       "nats-kv",
		Bucket:        "latticepkg-proposed",
		Spec:          "MATCH (p:provider) RETURN p.key AS key",
	})
	if err != nil {
		t.Fatalf("marshal lens content: %v", err)
	}
	putProposalAspect(t, ctx, conn, proposalKey+".review", map[string]any{"state": "approved"})
	putProposalAspect(t, ctx, conn, proposalKey+".artifact", map[string]any{"kind": "lens", "content": string(content)})
	putProposalAspect(t, ctx, conn, proposalKey+".target", map[string]any{
		"packageName": target.Name,
		"mode":        "upgradeExisting",
		"baseVersion": "0.1.0",
		"newVersion":  "0.2.0",
	})

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	err = runApplyProposal(proposalID, conn.NATS().ConnectedUrl(), writeBootstrapFile(t), logger)
	if err == nil {
		t.Fatal("apply-proposal must return the removal refusal, got nil")
	}
	if !errors.Is(err, pkgmgr.ErrApplyWouldRemove) {
		t.Fatalf("want pkgmgr.ErrApplyWouldRemove, got %v", err)
	}

	// MarkCapabilityProposalApplied rides the default lane; every package
	// install this harness ran rides the meta lane. So an empty ops.default is
	// exactly "the mark-applied submit never happened".
	stream, err := conn.JetStream().Stream(ctx, testutil.HarnessOpsStream)
	if err != nil {
		t.Fatalf("read the operations stream: %v", err)
	}
	msg, err := stream.GetLastMsgForSubject(ctx, "ops.default")
	if err == nil {
		t.Fatalf("a default-lane op was submitted after the refusal: %s", string(msg.Data))
	}
	if !errors.Is(err, jetstream.ErrMsgNotFound) {
		t.Fatalf("probe ops.default: %v", err)
	}

	// The positive vector for that probe. "Nothing on ops.default" only means
	// "nothing was submitted" if a submit would in fact have landed there and
	// been visible — otherwise the assertion above passes for a stream that
	// retains nothing, which is the shape of an absence check that pins
	// nothing at all.
	if _, err := conn.JetStream().Publish(ctx, "ops.default", []byte(`{"probe":true}`)); err != nil {
		t.Fatalf("publish the probe's own control message: %v", err)
	}
	if _, err := stream.GetLastMsgForSubject(ctx, "ops.default"); err != nil {
		t.Fatalf("the ops.default probe cannot see a message that IS there, so its emptiness proved nothing: %v", err)
	}
}
