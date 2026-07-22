package main

import (
	"testing"

	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/projection"
)

// TestIsOperationRoleIndexLens asserts the role-index routing predicate fires
// only for a lens that is BOTH keyed solely by operationType AND targets the
// capability-kv bucket (Contract #6 §6.1). A package lens that happens to
// share the operationType key but projects into a different nats_kv bucket
// must not be force-rewritten into the cap.role-by-operation.<op> shape.
func TestIsOperationRoleIndexLens(t *testing.T) {
	tests := []struct {
		name string
		rule *lens.Rule
		want bool
	}{
		{
			name: "real role-index lens (operationType key + capability-kv bucket)",
			rule: &lens.Rule{
				Into: lens.IntoConfig{
					Target: "nats_kv",
					Bucket: projection.AuthPlaneBucket,
					Key:    lens.KeyField{"operationType"},
				},
			},
			want: true,
		},
		{
			name: "package lens with operationType key but a different bucket",
			rule: &lens.Rule{
				Into: lens.IntoConfig{
					Target: "nats_kv",
					Bucket: "some-other-bucket",
					Key:    lens.KeyField{"operationType"},
				},
			},
			want: false,
		},
		{
			name: "capability-kv lens keyed by something else",
			rule: &lens.Rule{
				Into: lens.IntoConfig{
					Target: "nats_kv",
					Bucket: projection.AuthPlaneBucket,
					Key:    lens.KeyField{"actorId"},
				},
			},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isOperationRoleIndexLens(tc.rule); got != tc.want {
				t.Fatalf("isOperationRoleIndexLens() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestThreadsKeyColumns pins the exemption set both the activation path and
// the MATCH-update (hot-reload) path share. A Personal Lens is the case that
// matters most: its reserved "__actor" key field is injected by the envelope
// and is never a RETURN alias, so threading Into.Key at it fails validation
// and REFUSES the update — which silently pins the running pipeline to its
// old cypher until the process restarts, making every Personal Lens cypher
// edit look like it simply did not take.
func TestThreadsKeyColumns(t *testing.T) {
	tests := []struct {
		name string
		rule *lens.Rule
		want bool
	}{
		{
			name: "plain projection lens threads its key columns",
			rule: &lens.Rule{
				Into: lens.IntoConfig{Target: "nats_kv", Bucket: "weaver-targets", Key: lens.KeyField{"entityId"}},
			},
			want: true,
		},
		{
			name: "personal lens is exempt (__actor comes from the envelope)",
			rule: &lens.Rule{
				Into: lens.IntoConfig{Target: "nats_subject", Personal: true, Key: lens.KeyField{"__actor", "ns"}},
			},
			want: false,
		},
		{
			name: "operation-role-index lens is exempt",
			rule: &lens.Rule{
				Into: lens.IntoConfig{Target: "nats_kv", Bucket: projection.AuthPlaneBucket, Key: lens.KeyField{"operationType"}},
			},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := threadsKeyColumns(tc.rule); got != tc.want {
				t.Fatalf("threadsKeyColumns() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestHotReloadKeyColumns pins what the MATCH-update (hot-reload) path threads
// onto a rule's newly compiled form, per lens kind.
//
// The Personal Lens row is the regression this exists for. Being exempt from
// threading Into.Key verbatim (the reserved "__actor" is envelope-injected and
// never a RETURN alias) is correct; threading NOTHING as a result is not. An
// unset KeyColumns drops the executor to its first-RETURN-item fallback, so a
// multi-key Personal Lens emits a keys map carrying only its first alias. The
// adapter then rejects every write with `key field "ns" absent from keys map`
// and retries it for as long as the process runs — observed live as ~135k
// identical errors in four hours after one cypher edit to the edgeCatalog
// (manifest.op) lens, whose key is ["__actor", "ns", "entityId"].
func TestHotReloadKeyColumns(t *testing.T) {
	tests := []struct {
		name         string
		rule         *lens.Rule
		wantCols     []string
		wantThreaded bool
	}{
		{
			name: "plain projection lens threads Into.Key verbatim",
			rule: &lens.Rule{
				Into: lens.IntoConfig{Target: "nats_kv", Bucket: "weaver-targets", Key: lens.KeyField{"entityId"}},
			},
			wantCols:     []string{"entityId"},
			wantThreaded: true,
		},
		{
			name: "personal lens threads its business keys, not __actor",
			rule: &lens.Rule{
				Into: lens.IntoConfig{Target: "nats_subject", Personal: true, Key: lens.KeyField{"__actor", "ns", "entityId"}},
			},
			wantCols:     []string{"ns", "entityId"},
			wantThreaded: true,
		},
		{
			name: "single-business-key personal lens still threads that key",
			rule: &lens.Rule{
				Into: lens.IntoConfig{Target: "nats_subject", Personal: true, Key: lens.KeyField{"__actor", "ns"}},
			},
			wantCols:     []string{"ns"},
			wantThreaded: true,
		},
		{
			name: "operation-role-index lens threads nothing",
			rule: &lens.Rule{
				Into: lens.IntoConfig{Target: "nats_kv", Bucket: projection.AuthPlaneBucket, Key: lens.KeyField{"operationType"}},
			},
			wantCols:     nil,
			wantThreaded: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cols, threaded := hotReloadKeyColumns(tc.rule)
			if threaded != tc.wantThreaded {
				t.Fatalf("hotReloadKeyColumns() threaded = %v, want %v", threaded, tc.wantThreaded)
			}
			if len(cols) != len(tc.wantCols) {
				t.Fatalf("hotReloadKeyColumns() = %v, want %v", cols, tc.wantCols)
			}
			for i, want := range tc.wantCols {
				if cols[i] != want {
					t.Fatalf("hotReloadKeyColumns()[%d] = %q, want %q", i, cols[i], want)
				}
			}
		})
	}
}

// TestHotReloadKeyColumns_MatchesActivation is the invariant behind the bug:
// whatever the activation path installs on a Personal Lens, the hot-reload
// path must install the same thing. When they disagree, a lens works at boot
// and breaks the moment its cypher is edited — which is exactly how this
// surfaced (the lens whose spec was written BEFORE the process started worked;
// the one written after did not).
func TestHotReloadKeyColumns_MatchesActivation(t *testing.T) {
	personal := &lens.Rule{
		Into: lens.IntoConfig{Target: "nats_subject", Personal: true, Key: lens.KeyField{"__actor", "ns", "entityId"}},
	}
	activation := projection.PersonalBusinessKeys(personal)
	hotReload, threaded := hotReloadKeyColumns(personal)
	if !threaded {
		t.Fatal("a personal lens must thread key columns on hot-reload")
	}
	if len(hotReload) != len(activation) {
		t.Fatalf("hot-reload %v != activation %v", hotReload, activation)
	}
	for i := range activation {
		if hotReload[i] != activation[i] {
			t.Fatalf("hot-reload %v != activation %v", hotReload, activation)
		}
	}
}
