package edgemanifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

func TestPackage_ManifestMatchesDefinition(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	m, err := pkgmgr.ParseManifest(filepath.Join(wd, "manifest.yaml"))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if err := m.VerifyAgainstDefinition(Package); err != nil {
		t.Fatalf("manifest <-> Definition drift: %v", err)
	}
}

func TestPackage_NoDDLsOrPermissions(t *testing.T) {
	if got := len(Package.DDLs); got != 0 {
		t.Fatalf("expected 0 DDLs (edge-manifest is projection-only), got %d", got)
	}
	if got := len(Package.Permissions); got != 0 {
		t.Fatalf("expected 0 permissions (edge-manifest is projection-only), got %d", got)
	}
}

// manifestLensNames are the fourteen Personal Lenses (edge-showcase-app-
// design.md §3.2; the three manifest.ent entity lenses per
// facet-entity-browse-design.md; the staff siblings per
// facet-staff-worlds-design.md §3.3; the workplace-spine work-order lens per
// its §6 F5; the three provider-hat siblings per persona-worlds-design.md
// Fire W0). readGrantLensNames are their read-grant
// producers (nats-kv, actorAggregate) — a structurally different class (never
// Personal, never nats-subject) that
// TestPackage_LensesAreNatsSubjectPersonal/
// TestPackage_LensRowKeysAreManifestNamespaced correctly exclude.
var manifestLensNames = map[string]bool{
	"edgeIdentity": true, "edgeServices": true, "edgeCatalog": true,
	"edgeTasks": true, "edgeInstances": true,
	"edgeEntitySessions": true, "edgeEntityProviders": true,
	"edgeEntityBookings": true,
	"edgeCatalogRoles":   true, "edgeTasksQueued": true,
	"edgeStaffWorkOrders":  true,
	"edgeProviderSchedule": true, "edgeProviderQueue": true,
	"edgeInstructorSessions": true,
}

const readGrantLensName = "edgeManifestReadGrants"

// readGrantLensNames is every Path B cap-read producer this package ships. The
// staff and provider slices are each separate from the base one on purpose
// (§3.3 / persona-worlds-design.md Fire W0): §6.14 unions slices, so a new
// slice costs nothing, while folding its branches into the base producer
// would multiply that lens's existing cross-product fan-out for every actor.
var readGrantLensNames = map[string]bool{
	readGrantLensName:                true,
	"edgeManifestStaffReadGrants":    true,
	"edgeManifestProviderReadGrants": true,
}

func TestPackage_SeventeenLenses(t *testing.T) {
	if got := len(Package.Lenses); got != 17 {
		t.Fatalf("expected 17 lenses (14 manifest + 3 read-grant producers), got %d", got)
	}
	names := map[string]bool{}
	for _, l := range Package.Lenses {
		names[l.CanonicalName] = true
	}
	for want := range manifestLensNames {
		if !names[want] {
			t.Fatalf("missing lens %q (have %v)", want, names)
		}
	}
	for want := range readGrantLensNames {
		if !names[want] {
			t.Fatalf("missing read-grant lens %q (have %v)", want, names)
		}
	}
}

// TestPackage_LensesAreNatsSubjectPersonal pins the Personal Lens transport
// shape every MANIFEST lens must share (edge-showcase-app-design.md §3.1):
// nats-subject adapter, the shared SYNC stream + lattice.sync.user subject
// prefix, Personal:true fan-out, and __actor present in IntoKey
// (bucketguard.go enforces this at build time — this test pins the intent
// so a future lens addition doesn't silently omit it). The read-grant
// producer lens is a deliberately different shape (nats-kv, actorAggregate
// — see TestPackage_ReadGrantLensIsActorAggregateNatsKV) and is excluded
// here.
func TestPackage_LensesAreNatsSubjectPersonal(t *testing.T) {
	for _, l := range Package.Lenses {
		if !manifestLensNames[l.CanonicalName] {
			continue
		}
		if l.Adapter != "nats-subject" {
			t.Errorf("%s: adapter = %q, want nats-subject", l.CanonicalName, l.Adapter)
		}
		if l.SubjectPrefix != "lattice.sync.user" {
			t.Errorf("%s: subjectPrefix = %q, want lattice.sync.user", l.CanonicalName, l.SubjectPrefix)
		}
		if l.Stream != "SYNC" {
			t.Errorf("%s: stream = %q, want SYNC", l.CanonicalName, l.Stream)
		}
		if !l.Personal {
			t.Errorf("%s: Personal = false, want true", l.CanonicalName)
		}
		hasActor := false
		for _, k := range l.IntoKey {
			if k == "__actor" {
				hasActor = true
			}
		}
		if !hasActor {
			t.Errorf("%s: IntoKey %v missing __actor", l.CanonicalName, l.IntoKey)
		}
	}
}

// TestPackage_ReadGrantLensIsActorAggregateNatsKV pins the read-grant
// producer shape (Fire 2): nats-kv adapter into the shared "capability"
// bucket, ProjectionKind "actorAggregate" with a §6.13 Output descriptor
// targeting "cap-read.edgeManifest.{actorSuffix}" — the same declarative
// shape internal/bootstrap/lenses.go's CapabilityReadLensDefinition uses at
// the kernel level, this package's own instance of it (Path B — gates
// Personal Lens publication via capabilityread.IsReadable — NOT the
// Postgres GrantTable shape packages/console-operator/clinic-domain use,
// which is Path A / RLS for Protected postgres reads and irrelevant here).
func TestPackage_ReadGrantLensIsActorAggregateNatsKV(t *testing.T) {
	var found *pkgmgr.LensSpec
	for i := range Package.Lenses {
		if Package.Lenses[i].CanonicalName == readGrantLensName {
			found = &Package.Lenses[i]
		}
	}
	if found == nil {
		t.Fatalf("lens %q not found", readGrantLensName)
	}
	if found.Adapter != "nats-kv" {
		t.Errorf("adapter = %q, want nats-kv", found.Adapter)
	}
	if found.Bucket != "capability-kv" {
		t.Errorf("bucket = %q, want capability-kv", found.Bucket)
	}
	if found.ProjectionKind != "actorAggregate" {
		t.Errorf("ProjectionKind = %q, want actorAggregate", found.ProjectionKind)
	}
	if found.Output == nil {
		t.Fatal("Output descriptor is nil")
	}
	if found.Output.OutputKeyPattern != "cap-read.edgeManifest.{actorSuffix}" {
		t.Errorf("OutputKeyPattern = %q, want cap-read.edgeManifest.{actorSuffix}", found.Output.OutputKeyPattern)
	}
	if found.Personal {
		t.Error("Personal = true, want false (actorAggregate uses its own $actorKey re-execution, not the nats-subject Personal flag)")
	}
}

// TestPackage_LensRowKeysAreManifestNamespaced pins that every MANIFEST
// lens's first non-actor RETURN column is a literal "manifest.<ns>" string
// (the buildKey dot-join anchor) — the reserved namespace edge/store.go's
// isStorableKey exemption recognizes. The read-grant producer lens has no
// `ns`/manifest-key column at all (a different RETURN shape entirely) and
// is excluded.
func TestPackage_LensRowKeysAreManifestNamespaced(t *testing.T) {
	want := map[string]string{
		"edgeIdentity":        `"manifest.me" AS ns`,
		"edgeServices":        `"manifest.svc" AS ns`,
		"edgeCatalog":         `"manifest.op" AS ns`,
		"edgeTasks":           `"manifest.task" AS ns`,
		"edgeInstances":       `"manifest.inst" AS ns`,
		"edgeEntitySessions":  `"manifest.ent" AS ns`,
		"edgeEntityProviders": `"manifest.ent" AS ns`,
		"edgeEntityBookings":  `"manifest.ent" AS ns`,
		// The staff siblings share their non-staff counterpart's namespace on
		// purpose: same ns + same entityId means an op or task reachable by
		// both paths projects the identical row under the identical key, and
		// the renderer never learns which path a row arrived by.
		"edgeCatalogRoles": `"manifest.op" AS ns`,
		"edgeTasksQueued":  `"manifest.task" AS ns`,
		// The work-order lens is its own namespace: it answers "what work
		// exists at my workplace", not "what has been handed to me".
		"edgeStaffWorkOrders": `"manifest.work" AS ns`,
		// The provider-hat siblings (persona-worlds-design.md Fire W0) share
		// manifest.ent with the browse lenses above — same reason as the
		// staff siblings: same ns + same entityId means a row reachable by
		// both a provider binding and residence/staff reachability projects
		// the identical row under the identical key.
		"edgeProviderSchedule":   `"manifest.ent" AS ns`,
		"edgeProviderQueue":      `"manifest.ent" AS ns`,
		"edgeInstructorSessions": `"manifest.ent" AS ns`,
	}
	for _, l := range Package.Lenses {
		if readGrantLensNames[l.CanonicalName] {
			continue
		}
		lit, ok := want[l.CanonicalName]
		if !ok {
			t.Fatalf("unexpected lens %q", l.CanonicalName)
		}
		if !strings.Contains(l.Spec, lit) {
			t.Errorf("%s: spec missing %q", l.CanonicalName, lit)
		}
	}
}

// TestPackage_TaskLensesNameBothScopedSubjects pins the two subjects a task's
// scopedName resolves from, because losing either one silently regresses the
// renderer's display-name floor to a bare NanoID (display-name-convention-
// design.md §2): a lease task names its applied-for unit, and a maintenance
// task names its work order's own report summary. Both task lenses must carry
// both — edgeTasksQueued shows the work before it is claimed, edgeTasks after.
func TestPackage_TaskLensesNameBothScopedSubjects(t *testing.T) {
	for _, name := range []string{"edgeTasks", "edgeTasksQueued"} {
		var spec string
		for _, l := range Package.Lenses {
			if l.CanonicalName == name {
				spec = l.Spec
			}
		}
		if spec == "" {
			t.Fatalf("%s: lens not found", name)
		}
		for _, lit := range []string{
			"scopedUnit.presentation.data.name",
			"tgt.report.data.summary",
		} {
			if !strings.Contains(spec, lit) {
				t.Errorf("%s: scopedName no longer resolves %q — the renderer falls back to a bare NanoID", name, lit)
			}
		}
	}
}

// TestPackage_SpecsParseUnderFullEngine runs every lens's cypher through the
// same lex/parse/AST-visitor pipeline Refractor uses at activation
// (ruleengine/full.Engine.Parse) — a live-graph-free syntax + supported-
// construct check (no NATS/Postgres needed), catching the class of bug a
// unit test can't otherwise reach for a brand-new package with no running
// stack to install against.
func TestPackage_SpecsParseUnderFullEngine(t *testing.T) {
	eng := full.New()
	for _, l := range Package.Lenses {
		if _, err := eng.Parse(l.Spec); err != nil {
			t.Errorf("%s: cypher failed to parse under the full engine: %v\nspec:\n%s", l.CanonicalName, err, l.Spec)
		}
	}
}

// TestPackage_SpecsUseNullTestNotIsNull guards against reintroducing the
// silently-mis-evaluated `IS NULL`/`IS NOT NULL` form (full/visitor.go
// only supports `= null` / `<> null` — see lease-signing/lenses.go:565 and
// semantic-contracts/lenses.go:61 for the same guard elsewhere).
func TestPackage_SpecsUseNullTestNotIsNull(t *testing.T) {
	for _, l := range Package.Lenses {
		upper := strings.ToUpper(l.Spec)
		if strings.Contains(upper, "IS NULL") || strings.Contains(upper, "IS NOT NULL") {
			t.Errorf("%s: spec uses unsupported IS [NOT] NULL — use = null / <> null instead", l.CanonicalName)
		}
	}
}
