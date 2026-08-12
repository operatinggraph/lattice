package main

import (
	"context"
	"testing"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/projection"
	"github.com/operatinggraph/lattice/internal/refractor/subjects"
	"github.com/operatinggraph/lattice/internal/substrate"
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

// TestCapReadShredTargets proves the discovery filter matches exactly a
// cap-read.* PerEntry producer — the base capabilityRead lens's shape and a
// package-generated AnchorWalk producer's shape alike — and excludes every
// adjacent shape a live registry can hold: a non-cap-read PerEntry lens
// (e.g. a future entryKeyColumn adopter elsewhere), a cap-read.* lens that
// is NOT PerEntry (impossible today but not structurally ruled out), a
// plain doc-mode lens with no Output descriptor at all, a lens whose
// OutputKeyPattern merely contains "cap-read." without it being the prefix
// (must not false-positive on a substring match), and a lens whose pattern
// starts with the literal "cap-read" but not the "cap-read." separator (must
// not false-positive on a prefix check that dropped the trailing dot).
func TestCapReadShredTargets(t *testing.T) {
	registry := map[string]*pipelineEntry{
		"base-lens": {
			output: &lens.OutputDescriptorSpec{
				OutputKeyPattern: "cap-read.{actorSuffix}",
				EntryKeyColumn:   "anchorId",
			},
		},
		"edge-manifest-producer": {
			output: &lens.OutputDescriptorSpec{
				OutputKeyPattern: "cap-read.edgeManifestReadGrants.{actorSuffix}",
				EntryKeyColumn:   "anchorId",
			},
		},
		"unrelated-perentry-lens": {
			output: &lens.OutputDescriptorSpec{
				OutputKeyPattern: "cap.ephemeral.{actorSuffix}",
				EntryKeyColumn:   "anchorId",
			},
		},
		"capread-not-perentry": {
			output: &lens.OutputDescriptorSpec{
				OutputKeyPattern: "cap-read.legacy.{actorSuffix}",
			},
		},
		"doc-mode-no-output": {},
		"substring-not-prefix": {
			output: &lens.OutputDescriptorSpec{
				OutputKeyPattern: "unroutedTasks.cap-read.{actorSuffix}",
				EntryKeyColumn:   "anchorId",
			},
		},
		"no-separator-after-cap-read": {
			output: &lens.OutputDescriptorSpec{
				OutputKeyPattern: "cap-readable.{actorSuffix}",
				EntryKeyColumn:   "anchorId",
			},
		},
	}

	targets := capReadShredTargets(registry)

	got := make(map[string]bool, len(targets))
	for _, tgt := range targets {
		if !tgt.PerEntry {
			t.Fatalf("target %q must be PerEntry", tgt.RuleID)
		}
		got[tgt.RuleID] = true
	}
	want := map[string]bool{"base-lens": true, "edge-manifest-producer": true}
	if len(got) != len(want) {
		t.Fatalf("got %v, want exactly %v", got, want)
	}
	for id := range want {
		if !got[id] {
			t.Fatalf("expected %q to be discovered, got %v", id, got)
		}
	}
}

// TestCapReadShredTargets_Empty proves an empty registry (nothing activated
// yet, e.g. very early boot) yields nil rather than panicking or erroring —
// SetTargetLister's caller treats an empty result as a vacuous no-op.
func TestCapReadShredTargets_Empty(t *testing.T) {
	if got := capReadShredTargets(map[string]*pipelineEntry{}); len(got) != 0 {
		t.Fatalf("expected no targets from an empty registry, got %v", got)
	}
}

// bareAdapter implements adapter.Adapter and nothing else — the shape a
// future non-NATS-KV PerEntry producer (e.g. Postgres) would take today,
// since anchorwalk.go hardcodes "nats-kv" for every real producer.
type bareAdapter struct{}

func (bareAdapter) Upsert(context.Context, map[string]any, map[string]any, uint64) error { return nil }
func (bareAdapter) Delete(context.Context, map[string]any, uint64) error                 { return nil }
func (bareAdapter) Probe(context.Context) error                                          { return nil }
func (bareAdapter) Close() error                                                         { return nil }

// prefixListingAdapter additionally implements adapter.PrefixKeyLister — the
// capability NatsKVAdapter has today.
type prefixListingAdapter struct{ bareAdapter }

func (prefixListingAdapter) ListKeysPrefix(context.Context, string) ([]map[string]any, error) {
	return nil, nil
}

var _ adapter.Adapter = bareAdapter{}
var _ adapter.Adapter = prefixListingAdapter{}
var _ adapter.PrefixKeyLister = prefixListingAdapter{}

// TestValidatePerEntryCapReadAdapter proves the activation-time guard refuses
// a PerEntry cap-read.* lens whose adapter cannot enumerate keys by prefix —
// closing cap-read-per-anchor-grant-keys-design.md's Fire 4 residual, where
// such a lens would otherwise only fail at the first live shred event,
// pausing the whole auth-plane lens rather than just refusing to activate.
func TestValidatePerEntryCapReadAdapter(t *testing.T) {
	tests := []struct {
		name    string
		rule    *lens.Rule
		adpt    adapter.Adapter
		wantErr bool
	}{
		{
			name:    "perEntry cap-read on a non-lister adapter is refused",
			rule:    &lens.Rule{ID: "future-postgres-producer", Output: &lens.OutputDescriptorSpec{OutputKeyPattern: "cap-read.{actorSuffix}", EntryKeyColumn: "anchorId"}},
			adpt:    bareAdapter{},
			wantErr: true,
		},
		{
			name:    "perEntry cap-read on a PrefixKeyLister adapter is allowed",
			rule:    &lens.Rule{ID: "base-lens", Output: &lens.OutputDescriptorSpec{OutputKeyPattern: "cap-read.{actorSuffix}", EntryKeyColumn: "anchorId"}},
			adpt:    prefixListingAdapter{},
			wantErr: false,
		},
		{
			name:    "doc-mode cap-read (no EntryKeyColumn) is untouched by this guard",
			rule:    &lens.Rule{ID: "cap-ephemeral", Output: &lens.OutputDescriptorSpec{OutputKeyPattern: "cap-read.legacy.{actorSuffix}"}},
			adpt:    bareAdapter{},
			wantErr: false,
		},
		{
			name:    "non-cap-read PerEntry lens is untouched by this guard",
			rule:    &lens.Rule{ID: "unroutedTasks", Output: &lens.OutputDescriptorSpec{OutputKeyPattern: "unroutedTasks.{actorSuffix}", EntryKeyColumn: "anchorId"}},
			adpt:    bareAdapter{},
			wantErr: false,
		},
		{
			name:    "nil Output (non-actor-aggregate lens) is untouched by this guard",
			rule:    &lens.Rule{ID: "plain-lens"},
			adpt:    bareAdapter{},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePerEntryCapReadAdapter(tt.rule, tt.adpt)
			if tt.wantErr && err == nil {
				t.Fatalf("expected an error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
		})
	}
}

// TestHolderTypeRebuildTargets asserts the enumeration a retention-class key
// destruction is delivered through: every lens with a secure column DECLARING
// the destroyed holder's type, and nothing else. The excluded shapes are the
// ones a live registry actually holds — a lens whose columns declare only
// "identity" (the six migrated ones, which must keep their in-band CDC scrub
// and must not be dragged into a rebuild), and a plain lens with no secure
// columns at all.
func TestHolderTypeRebuildTargets(t *testing.T) {
	registry := map[string]*pipelineEntry{
		"clinical-notes": {secureColumns: []lens.SecureColumn{
			{Column: "note", HolderTypes: []string{"retentionclass"}},
		}},
		"mixed-after-reclassification": {secureColumns: []lens.SecureColumn{
			{Column: "email", HolderTypes: []string{"identity"}},
			{Column: "note", HolderTypes: []string{"identity", "retentionclass"}},
		}},
		"identity-only": {secureColumns: []lens.SecureColumn{
			{Column: "name", HolderTypes: []string{"identity"}},
		}},
		"plain-lens": {},
	}

	got := map[string]bool{}
	for _, tgt := range holderTypeRebuildTargets(registry, "retentionclass") {
		got[tgt.RuleID] = true
	}
	want := map[string]bool{"clinical-notes": true, "mixed-after-reclassification": true}
	if len(got) != len(want) {
		t.Fatalf("got %v, want exactly %v", got, want)
	}
	for id := range want {
		if !got[id] {
			t.Fatalf("expected %q to be enumerated, got %v", id, got)
		}
	}

	// A lens declaring a type is enumerated ONCE even when several of its
	// columns name it — a duplicate would rebuild the same lens twice.
	ids := holderTypeRebuildTargets(registry, "identity")
	seen := map[string]int{}
	for _, tgt := range ids {
		seen[tgt.RuleID]++
	}
	if seen["mixed-after-reclassification"] != 1 {
		t.Fatalf("a lens with two columns naming the type must be enumerated once, got %v", seen)
	}
}

// An empty registry, or a holder type nothing declares, yields no targets —
// which the consumer treats as "no read model holds this plaintext" and attests
// on. It must therefore never be an error or a panic.
func TestHolderTypeRebuildTargets_NoDeclarers(t *testing.T) {
	if got := holderTypeRebuildTargets(map[string]*pipelineEntry{}, "retentionclass"); len(got) != 0 {
		t.Fatalf("expected no targets from an empty registry, got %v", got)
	}
	registry := map[string]*pipelineEntry{
		"identity-only": {secureColumns: []lens.SecureColumn{
			{Column: "name", HolderTypes: []string{"identity"}},
		}},
	}
	if got := holderTypeRebuildTargets(registry, "retentionclass"); len(got) != 0 {
		t.Fatalf("a type nothing declares must yield no targets, got %v", got)
	}
}

// TestVerifiesReadPathPosture pins the set of lenses whose Probe adjudicates
// their structural condition — the single predicate behind BOTH the fail-closed
// InitialPause gate and the StructuralProbe opt-in
// (structural-pause-recovery-design.md §4.2d).
//
// The EXCLUSIONS are the half that matters. Nothing fails when a lens is opted
// in wrongly: a plain Postgres lens's Probe is pool.Ping, which passes while the
// structural condition still holds, so it would resume, re-pause and resume
// again with the health entry reading `active` for a share of every cycle —
// churn dressed as recovery, and strictly worse than the honest structural pause
// it replaced. A NATS-KV or NATS-subject lens would be opted into a recovery
// path with no live consumer at all. Neither shows up as a test failure anywhere
// else, which is why the boundary is asserted here rather than left to the
// comment beside the call.
func TestVerifiesReadPathPosture(t *testing.T) {
	tests := []struct {
		name string
		into lens.IntoConfig
		want bool
	}{
		{
			name: "protected postgres lens — Probe is VerifyProtectedTable",
			into: lens.IntoConfig{Target: "postgres", Table: "clinic_encounters", Protected: true},
			want: true,
		},
		{
			name: "grant-table lens — Probe is VerifyGrantTable",
			into: lens.IntoConfig{Target: "postgres", Table: "actor_read_grants", GrantTable: true},
			want: true,
		},
		{
			name: "both flags set",
			into: lens.IntoConfig{Target: "postgres", Protected: true, GrantTable: true},
			want: true,
		},
		{
			name: "plain postgres lens — Probe is pool.Ping, which passes while the fault holds",
			into: lens.IntoConfig{Target: "postgres", Table: "loftspace_listings"},
			want: false,
		},
		{
			name: "explicitly public postgres lens — no RLS, same pool.Ping probe",
			into: lens.IntoConfig{Target: "postgres", Table: "loftspace_listings", Public: true},
			want: false,
		},
		{
			name: "nats_kv lens — structural class real but no live consumer",
			into: lens.IntoConfig{Target: "nats_kv", Bucket: "weaver-targets"},
			want: false,
		},
		{
			name: "nats_subject lens — same",
			into: lens.IntoConfig{Target: "nats_subject", SubjectPrefix: "personal", Stream: "PERSONAL"},
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := verifiesReadPathPosture(tc.into); got != tc.want {
				t.Fatalf("verifiesReadPathPosture = %v, want %v — this predicate gates BOTH the "+
					"verify-before-first-projection pause and the structural self-heal opt-in", got, tc.want)
			}
		})
	}
}

// TestLensConsumerSpec_PauseWiring is the gate the rest of this item did not
// have. Before the spec was a named function, setting `StructuralProbe: false`
// in the literal shipped the feature DEAD in the binary with every test in the
// change still green: TestVerifiesReadPathPosture exercises the predicate in
// isolation, and the e2e sets the flag on its own spec by hand. Nothing
// connected the predicate to what the production consumer actually registers.
//
// It asserts both fields together on purpose. They are licensed by one property
// — the Probe adjudicates the condition — so a lens that verifies before its
// first projection but cannot probe out of a structural pause, or the reverse,
// is incoherent rather than merely different.
func TestLensConsumerSpec_PauseWiring(t *testing.T) {
	filters := []string{"$KV.core-kv.vtx.encounter.>"}

	t.Run("a protected lens verifies before projecting AND probes its way back", func(t *testing.T) {
		spec := lensConsumerSpec(&lens.Rule{
			ID:   "lns0000000000000001a",
			Into: lens.IntoConfig{Target: "postgres", Table: "clinic_encounters", Protected: true},
		}, "core-kv", filters, "")

		if spec.InitialPause != substrate.PauseInfra {
			t.Fatalf("InitialPause = %q, want infra — the RLS posture must be verified before the first projection", spec.InitialPause)
		}
		if !spec.StructuralProbe {
			t.Fatal("StructuralProbe = false — the feature is wired dead: a protected lens can never self-heal a structural pause")
		}
	})

	t.Run("a grant-table lens likewise", func(t *testing.T) {
		spec := lensConsumerSpec(&lens.Rule{
			ID:   "lns0000000000000002a",
			Into: lens.IntoConfig{Target: "postgres", Table: "actor_read_grants", GrantTable: true},
		}, "core-kv", filters, "")

		if spec.InitialPause != substrate.PauseInfra || !spec.StructuralProbe {
			t.Fatalf("grant lens spec = {InitialPause:%q StructuralProbe:%v}, want {infra true}", spec.InitialPause, spec.StructuralProbe)
		}
	})

	t.Run("a plain lens gets neither — its Probe is pool.Ping and would churn", func(t *testing.T) {
		spec := lensConsumerSpec(&lens.Rule{
			ID:   "lns0000000000000003a",
			Into: lens.IntoConfig{Target: "postgres", Table: "loftspace_listings"},
		}, "core-kv", filters, "")

		if spec.InitialPause != "" {
			t.Fatalf("InitialPause = %q, want the zero value — a plain lens drains immediately", spec.InitialPause)
		}
		if spec.StructuralProbe {
			t.Fatal("StructuralProbe = true on a plain lens: a pool.Ping probe passes while the condition holds, which is resume/re-pause churn")
		}
	})

	t.Run("a nats_kv lens gets neither", func(t *testing.T) {
		spec := lensConsumerSpec(&lens.Rule{
			ID:   "lns0000000000000004a",
			Into: lens.IntoConfig{Target: "nats_kv", Bucket: "weaver-targets"},
		}, "core-kv", filters, "")

		if spec.InitialPause != "" || spec.StructuralProbe {
			t.Fatalf("nats_kv spec = {InitialPause:%q StructuralProbe:%v}, want {\"\" false}", spec.InitialPause, spec.StructuralProbe)
		}
	})

	// The rest of the spec is carried through unchanged — the extraction moved
	// the literal, it did not re-decide anything in it.
	t.Run("the surrounding spec is unchanged", func(t *testing.T) {
		spec := lensConsumerSpec(&lens.Rule{
			ID:   "lns0000000000000005a",
			Into: lens.IntoConfig{Target: "nats_kv", Bucket: "weaver-targets"},
		}, "core-kv", filters, "$KV.core-kv.>")

		if spec.Name != subjects.LensDurable("lns0000000000000005a") || spec.DeliverGroup != spec.Name {
			t.Fatalf("durable/queue group = %q/%q", spec.Name, spec.DeliverGroup)
		}
		if spec.Stream != subjects.CoreKVStream("core-kv") {
			t.Fatalf("Stream = %q", spec.Stream)
		}
		if spec.DeliverPolicy != substrate.DeliverLastPerSubject {
			t.Fatalf("DeliverPolicy = %v", spec.DeliverPolicy)
		}
		if spec.AckWait != lensAckWait {
			t.Fatalf("AckWait = %s, want %s", spec.AckWait, lensAckWait)
		}
		if len(spec.FilterSubjects) != 1 || spec.FilterSubject != "$KV.core-kv.>" {
			t.Fatalf("filters = %v / %q", spec.FilterSubjects, spec.FilterSubject)
		}
	})
}
