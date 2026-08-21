package pkgmgr

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"slices"
	"testing"

	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// TestUninstallGuardAgreesWithDestructionOracleOnEveryLensShape is the
// mechanized form of a review class this component keeps re-learning: a guard
// protecting what a CONSUMER reads must enumerate every condition under which
// that consumer stops seeing the thing.
//
// The uninstall gate's whole correctness rests on one predicate —
// lensDeclaredToDestructionOracle — answering the same question Refractor's
// destruction-readiness oracle answers: does this lens still declare key
// custody? Three separate reviews found three separate shapes where the two
// disagreed (the eventStream skip's typed-decode requirement, the two
// targetConfig levels, a spec the oracle cannot use at all), and every one was
// found by EXECUTING both readers against the same KV state, never by reading
// the code. So the executing is what gets kept.
//
// Each row commits a root+spec shape to Core KV, then asks both readers:
//
//   - the REAL health.RegistryProbe, through its exported ReconcileNow, with an
//     empty registered set so the result IS the declared set. Driving the real
//     probe is the point — a local reimplementation of declaredLensIDs would
//     re-mint the very divergence this test exists to catch, and would agree
//     with the guard for exactly as long as both were wrong in the same way.
//   - the guard's own predicate, with whatever bytes the `.spec` key holds.
//
// A row where the two differ must say WHY in divergence: the guard is
// deliberately more conservative in two places, and an unexplained mismatch is
// the bug. Adding a new spec shape means adding a row here — if you cannot
// state both answers, the guard does not yet know about the shape.
func TestUninstallGuardAgreesWithDestructionOracleOnEveryLensShape(t *testing.T) {
	cases := []struct {
		name string
		// mutate rewrites the committed lens root and/or `.spec`. Either
		// argument may be written back; returning false for keepSpec purges the
		// `.spec` key instead.
		mutate func(t *testing.T, ctx context.Context, conn *substrate.Conn, rootKey, specKey string)
		// oracleDeclares is what the REAL probe must report for this lens.
		oracleDeclares bool
		// guardVisible is what lensDeclaredToDestructionOracle must answer.
		guardVisible bool
		// divergence must be non-empty exactly when the two answers differ.
		divergence string
	}{
		{
			name:           "clean secure lens",
			mutate:         func(*testing.T, context.Context, *substrate.Conn, string, string) {},
			oracleDeclares: true,
			guardVisible:   true,
		},
		{
			name: "root soft-deleted",
			mutate: func(t *testing.T, ctx context.Context, conn *substrate.Conn, rootKey, _ string) {
				patchDoc(t, ctx, conn, rootKey, func(doc map[string]any) { doc["isDeleted"] = true })
			},
			oracleDeclares: false,
			guardVisible:   false,
		},
		{
			name: "root class flipped away from meta.lens",
			mutate: func(t *testing.T, ctx context.Context, conn *substrate.Conn, rootKey, _ string) {
				patchDoc(t, ctx, conn, rootKey, func(doc map[string]any) { doc["class"] = "meta.ddl.vertexType" })
			},
			oracleDeclares: false,
			guardVisible:   false,
		},
		{
			name: "root absent",
			mutate: func(t *testing.T, ctx context.Context, conn *substrate.Conn, rootKey, _ string) {
				if err := conn.KVPurge(ctx, CoreBucket, rootKey); err != nil {
					t.Fatalf("KVPurge %s: %v", rootKey, err)
				}
			},
			oracleDeclares: false,
			guardVisible:   false,
		},
		{
			name: "spec soft-deleted under a live root",
			mutate: func(t *testing.T, ctx context.Context, conn *substrate.Conn, _, specKey string) {
				patchDoc(t, ctx, conn, specKey, func(doc map[string]any) { doc["isDeleted"] = true })
			},
			// The oracle never reads the spec's own isDeleted: it selects lenses
			// by their ROOT and reads a soft-deleted spec's body perfectly well.
			oracleDeclares: true,
			guardVisible:   true,
		},
		{
			name: "eventStream spec",
			mutate: func(t *testing.T, ctx context.Context, conn *substrate.Conn, _, specKey string) {
				patchDoc(t, ctx, conn, specKey, func(doc map[string]any) {
					specBody(t, doc)["source"] = map[string]any{"kind": "eventStream"}
				})
			},
			oracleDeclares: false,
			guardVisible:   false,
		},
		{
			name: "eventStream spec with poisoned holderTypes",
			mutate: func(t *testing.T, ctx context.Context, conn *substrate.Conn, _, specKey string) {
				patchDoc(t, ctx, conn, specKey, func(doc map[string]any) {
					body := specBody(t, doc)
					body["source"] = map[string]any{"kind": "eventStream"}
					poisonHolderTypes(t, body)
				})
			},
			// The oracle's eventStream skip runs only on a spec that decoded
			// cleanly into its probe, and a non-string in holderTypes defeats
			// that decode — so the skip does not apply and the lens is declared.
			oracleDeclares: true,
			guardVisible:   true,
		},
		{
			name: "decoy top-level targetConfig",
			mutate: func(t *testing.T, ctx context.Context, conn *substrate.Conn, _, specKey string) {
				patchDoc(t, ctx, conn, specKey, func(doc map[string]any) {
					doc["targetConfig"] = map[string]any{"bucket": "decoy"}
				})
			},
			oracleDeclares: true,
			guardVisible:   true,
		},
		{
			name: "no targetConfig at either level",
			mutate: func(t *testing.T, ctx context.Context, conn *substrate.Conn, _, specKey string) {
				patchDoc(t, ctx, conn, specKey, func(doc map[string]any) {
					delete(specBody(t, doc), "targetConfig")
				})
			},
			oracleDeclares: true,
			guardVisible:   true,
		},
		{
			name: "secureColumns is not a list",
			mutate: func(t *testing.T, ctx context.Context, conn *substrate.Conn, _, specKey string) {
				patchDoc(t, ctx, conn, specKey, func(doc map[string]any) {
					specBody(t, doc)["targetConfig"].(map[string]any)["secureColumns"] = "not-a-list"
				})
			},
			oracleDeclares: true,
			guardVisible:   true,
		},
		{
			name: "spec purged",
			mutate: func(t *testing.T, ctx context.Context, conn *substrate.Conn, _, specKey string) {
				if err := conn.KVPurge(ctx, CoreBucket, specKey); err != nil {
					t.Fatalf("KVPurge %s: %v", specKey, err)
				}
			},
			oracleDeclares: true,
			guardVisible:   true,
		},
		{
			name: "root isDeleted is a string rather than a bool",
			mutate: func(t *testing.T, ctx context.Context, conn *substrate.Conn, rootKey, _ string) {
				patchDoc(t, ctx, conn, rootKey, func(doc map[string]any) { doc["isDeleted"] = "true" })
			},
			// The oracle's vertex probe types isDeleted as a bool, so this
			// envelope fails its decode and the lens is dropped.
			oracleDeclares: false,
			guardVisible:   true,
			divergence: "the guard reads a non-bool isDeleted as NOT deleted and over-refuses: one extra attestation, " +
				"no custody record lost. Refusing in the safe direction beats mirroring a skip whose cause is a decode error.",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if (tc.oracleDeclares != tc.guardVisible) != (tc.divergence != "") {
				t.Fatalf("row states answers %v/%v with divergence %q — a mismatch must be explained, and an explained row must mismatch",
					tc.oracleDeclares, tc.guardVisible, tc.divergence)
			}
			ctx, conn, inst := newInstallerHarness(t)
			def := defWithSecureLens("0.1.0", []string{"identity"}, "")
			if _, err := inst.Install(ctx, def); err != nil {
				t.Fatalf("Install: %v", err)
			}
			specKey := secureLensSpecKey(def)
			rootKey := metaVertexPrefix + entityNanoID(def.Name, "lens:sampleSecureLens")
			lensID := entityNanoID(def.Name, "lens:sampleSecureLens")
			tc.mutate(t, ctx, conn, rootKey, specKey)

			declared := declaredLensIDsViaProbe(t, ctx, conn)
			if got := slices.Contains(declared, lensID); got != tc.oracleDeclares {
				t.Fatalf("the REAL RegistryProbe declares %s = %v, want %v (declared set %v)", lensID, got, tc.oracleDeclares, declared)
			}

			raw := rawOrNil(t, ctx, conn, specKey)
			got, err := inst.lensDeclaredToDestructionOracle(ctx, specKey, raw)
			if err != nil {
				t.Fatalf("lensDeclaredToDestructionOracle: %v", err)
			}
			if got != tc.guardVisible {
				t.Fatalf("the uninstall guard says visible = %v, want %v", got, tc.guardVisible)
			}
			if got != tc.oracleDeclares && tc.divergence == "" {
				t.Fatalf("guard (%v) and oracle (%v) disagree with no stated reason", got, tc.oracleDeclares)
			}
		})
	}
}

// declaredLensIDsViaProbe runs the REAL destruction-readiness probe over the
// harness bucket and returns the lens IDs it declares. The registered set is
// empty, so ReconcileNow's "declared but not registered" answer is the declared
// set itself; ReconcileNow publishes nothing, so calling it leaves no trace.
func declaredLensIDsViaProbe(t *testing.T, ctx context.Context, conn *substrate.Conn) []string {
	t.Helper()
	probe := health.NewRegistryProbe(conn, CoreBucket, func() []string { return nil },
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	declared, err := probe.ReconcileNow(ctx)
	if err != nil {
		t.Fatalf("RegistryProbe.ReconcileNow: %v", err)
	}
	return declared
}

// patchDoc reads a committed document, hands it to mutate, and writes it back
// — the out-of-band edit that stands up a KV state no package can produce.
func patchDoc(t *testing.T, ctx context.Context, conn *substrate.Conn, key string, mutate func(map[string]any)) {
	t.Helper()
	entry, err := conn.KVGet(ctx, CoreBucket, key)
	if err != nil {
		t.Fatalf("KVGet %s: %v", key, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", key, err)
	}
	mutate(doc)
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal %s: %v", key, err)
	}
	if _, err := conn.KVPut(ctx, CoreBucket, key, raw); err != nil {
		t.Fatalf("KVPut %s: %v", key, err)
	}
}

// specBody returns the spec aspect's stored envelope body, the level a produced
// lens spec actually carries its source and targetConfig at.
func specBody(t *testing.T, doc map[string]any) map[string]any {
	t.Helper()
	body, ok := doc["data"].(map[string]any)
	if !ok {
		t.Fatalf("spec document carries no data envelope: %v", doc)
	}
	return body
}

// poisonHolderTypes puts a non-string into the first secure column's
// holderTypes — the edit that defeats the oracle's []string-typed probe while
// leaving the document valid JSON.
func poisonHolderTypes(t *testing.T, body map[string]any) {
	t.Helper()
	cfg, ok := body["targetConfig"].(map[string]any)
	if !ok {
		t.Fatalf("spec body carries no targetConfig: %v", body)
	}
	cols, ok := cfg["secureColumns"].([]any)
	if !ok || len(cols) == 0 {
		t.Fatalf("spec body carries no secureColumns: %v", cfg)
	}
	cols[0].(map[string]any)["holderTypes"] = []any{"identity", 5}
}

// rawOrNil returns a key's committed bytes, or nil when it is absent — the same
// two inputs Uninstall hands the visibility predicate.
func rawOrNil(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) []byte {
	t.Helper()
	entry, err := conn.KVGet(ctx, CoreBucket, key)
	if err != nil {
		return nil
	}
	return entry.Value
}
