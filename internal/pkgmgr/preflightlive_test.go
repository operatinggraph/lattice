package pkgmgr

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/lenscolumns"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// The fixtures below all share one shape: a package declaring a single lens
// and a single weaver target bound to it, differing only in the lens's
// projection and the target's gaps map — so every refusal is attributable to
// the binding rule and nothing else.

const bindingLensBucket = "weaver-targets"

// plainBindingLens is a lens whose row columns are its RETURN names — the
// shape every capability-artifact lens has, and the one only a wired
// SpecParser can read.
func plainBindingLens(canonicalName string, gapColumns ...string) LensSpec {
	rule := "MATCH (n:identity) RETURN n.key AS key"
	for _, col := range gapColumns {
		rule += ", true AS " + col
	}
	return LensSpec{
		CanonicalName: canonicalName,
		Class:         "meta.lens",
		Adapter:       "nats-kv",
		Bucket:        bindingLensBucket,
		Engine:        "full",
		Spec:          rule,
	}
}

// aggregateBindingLens is an actor-aggregate lens: its row columns are its
// Output descriptor's declared lists, read with no cypher parse at all.
func aggregateBindingLens(canonicalName, targetID string, bodyColumns []string, staticEmpty []string) LensSpec {
	return LensSpec{
		CanonicalName:  canonicalName,
		Class:          "meta.lens",
		Adapter:        "nats-kv",
		Bucket:         bindingLensBucket,
		Engine:         "full",
		ProjectionKind: ActorAggregateProjectionKind,
		Spec:           "MATCH (n:identity) RETURN n.key AS key",
		Output: &OutputDescriptorSpec{
			AnchorType:         "identity",
			OutputKeyPattern:   targetID + ".{key}",
			BodyColumns:        append([]string{"key"}, bodyColumns...),
			StaticEmptyColumns: staticEmpty,
			EmptyBehavior:      "omit",
		},
	}
}

// eventStreamBindingLens is a Chronicler-fed lens: its rows are its source's
// project.columns mapping, and no cypher is read for them at all.
func eventStreamBindingLens(canonicalName string, columns ...string) LensSpec {
	cols := map[string]ColumnMapping{"key": {Path: "event.key"}}
	for _, c := range columns {
		cols[c] = ColumnMapping{Path: "event." + c}
	}
	return LensSpec{
		CanonicalName: canonicalName,
		Class:         "meta.lens",
		Adapter:       "nats-kv",
		Bucket:        bindingLensBucket,
		Engine:        "full",
		Source: &SourceConfig{
			Kind:     "eventStream",
			Subjects: []string{"events.orchestration.>"},
			Project:  &EventProjection{Key: "event.key", Columns: cols},
		},
	}
}

// bindingTarget is a target declaring one directOp gap per column named.
func bindingTarget(targetID, lensRef string, declared ...string) WeaverTargetSpec {
	gaps := map[string]GapActionSpec{}
	for _, col := range declared {
		gaps[col] = GapActionSpec{Action: "directOp", Operation: "SampleOp"}
	}
	return WeaverTargetSpec{TargetID: targetID, LensRef: lensRef, Gaps: gaps}
}

// bindingDef assembles the package. Version is a parameter so an Apply-path
// vector can upgrade the same package.
func bindingDef(pkgName, version string, lenses []LensSpec, targets ...WeaverTargetSpec) Definition {
	return Definition{
		Name:          pkgName,
		Version:       version,
		Description:   "weaver-target lens-binding test package",
		Lenses:        lenses,
		WeaverTargets: targets,
	}
}

// requireBindingRefusal asserts an error is the lens-binding refusal and
// returns its text, so each vector pins the sentence an author reads as well
// as the class a caller switches on.
func requireBindingRefusal(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("expected the weaver-target lens-binding refusal, got nil")
	}
	if !errors.Is(err, ErrLensBindingRefused) {
		t.Fatalf("refusal must wrap ErrLensBindingRefused so every renderer can name it (cmd/loupe maps it to 409); got %v", err)
	}
	return err.Error()
}

// installOutOfBatchLens installs a package carrying ONLY the lens, and returns
// the installed lens's id — the NanoID a later package's target binds by, the
// cross-package shape nothing but a live read can resolve.
func installOutOfBatchLens(t *testing.T, ctx context.Context, inst *Installer, pkgName string, lens LensSpec) string {
	t.Helper()
	def := bindingDef(pkgName, "0.1.0", []LensSpec{lens})
	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("install the lens package %s: %v", pkgName, err)
	}
	return LensID(pkgName, lens.CanonicalName)
}

// --- in-batch: the lens the same Definition declares ------------------------

// THE POSITIVE VECTOR for the in-batch aggregate pair: the target declares
// every missing_* column its lens's Output puts in the row, and installs.
func TestPreflightLive_InBatchAggregateDeclared_Installs(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	def := bindingDef("bind-agg-ok", "0.1.0",
		[]LensSpec{aggregateBindingLens("aggLens", "aggTarget", []string{"missing_followUp"}, []string{"missing_escalate"})},
		bindingTarget("aggTarget", "aggLens", "missing_followUp", "missing_escalate"))
	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("a fully declared target must install: %v", err)
	}
}

// One column removed from the gaps map of the vector above — nothing else
// differs.
func TestPreflightLive_InBatchAggregateUndeclared_Refused(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	def := bindingDef("bind-agg-bad", "0.1.0",
		[]LensSpec{aggregateBindingLens("aggLens", "aggTarget", []string{"missing_followUp"}, []string{"missing_escalate"})},
		bindingTarget("aggTarget", "aggLens", "missing_followUp"))
	_, err := inst.Install(ctx, def)
	msg := requireBindingRefusal(t, err)
	for _, want := range []string{
		`WeaverTarget[0] "aggTarget"`,
		`projects gap column "missing_escalate"`,
		lenscolumns.ProvenanceStaticEmptyColumns,
		"declare it `surface`",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal must contain %q so the author knows which list to edit; got %v", want, msg)
		}
	}
}

// The plain pair. A plain lens declares no Output at all: its row columns are
// the RETURN items' names, which only the injected SpecParser can read.
func TestPreflightLive_InBatchPlainDeclared_Installs(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	def := bindingDef("bind-plain-ok", "0.1.0",
		[]LensSpec{plainBindingLens("plainLens", "missing_reminder")},
		bindingTarget("plainTarget", "plainLens", "missing_reminder"))
	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("a plain lens whose RETURN gap column is declared must install: %v", err)
	}
}

func TestPreflightLive_InBatchPlainUndeclared_Refused(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	def := bindingDef("bind-plain-bad", "0.1.0",
		[]LensSpec{plainBindingLens("plainLens", "missing_reminder")},
		bindingTarget("plainTarget", "plainLens"))
	msg := requireBindingRefusal(t, mustFailInstall(t, ctx, inst, def))
	if !strings.Contains(msg, `projects gap column "missing_reminder" (in RETURN)`) {
		t.Fatalf("refusal must attribute the column to the RETURN clause; got %v", msg)
	}
}

// The widened nil-SpecParser semantics, stated as behaviour: an installer with
// no parser cannot read a plain lens's columns, and "not derivable" is refused
// rather than read as "no columns". The refusal names the missing parser, so
// an operator who hits it knows the fix is wiring, not authoring.
func TestPreflightLive_InBatchPlainNoParser_Refused(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)
	inst.SpecParser = nil

	def := bindingDef("bind-plain-noparser", "0.1.0",
		[]LensSpec{plainBindingLens("plainLens", "missing_reminder")},
		bindingTarget("plainTarget", "plainLens", "missing_reminder"))
	msg := requireBindingRefusal(t, mustFailInstall(t, ctx, inst, def))
	if !strings.Contains(msg, "no cypher parser supplied") {
		t.Fatalf("refusal must name the absent parser; got %v", msg)
	}
}

// --- out-of-batch: an already-installed lens, by id -------------------------

// THE LOOKALIKE (§1's hole): a 20-character canonicalName from another package
// reads as an id at every layer below. Nothing in the batch declares it and no
// installed lens carries it, so the target that would have installed bound to
// nothing is refused — with the sentence that names the trap.
func TestPreflightLive_OutOfBatchAbsentID_Refused(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	const lookalike = "appointmentReminders"
	def := bindingDef("bind-absent", "0.1.0", nil,
		bindingTarget("absentTarget", lookalike, "missing_reminder"))
	msg := requireBindingRefusal(t, mustFailInstall(t, ctx, inst, def))
	for _, want := range []string{
		`LensRef "appointmentReminders" names no installed meta.lens`,
		"20 alphabet characters reads as an id here",
		"declare the lens in this package, or bind the installed lens's id",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal must contain %q; got %v", want, msg)
		}
	}
}

// A ref that is neither an in-batch canonicalName nor NanoID-shaped never
// reaches the live read at all — resolveLensRef refuses it when the batch is
// built, and the preflight says so first, with the target's own index.
func TestPreflightLive_UnresolvableCanonicalName_Refused(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	def := bindingDef("bind-unresolvable", "0.1.0", nil,
		bindingTarget("danglingTarget", "noSuchLens", "missing_reminder"))
	msg := requireBindingRefusal(t, mustFailInstall(t, ctx, inst, def))
	if !strings.Contains(msg, "matches no declared lens canonicalName and is not a valid NanoID") {
		t.Fatalf("refusal must be resolveLensRef's own verdict; got %v", msg)
	}
}

// An id of the right SHAPE whose root is a different class — here a weaver
// target's own meta-vertex — is not a lens, and binding to one is refused on
// exactly the same sentence an absent id gets. Both holders apply this test,
// so the validator's found=false and this refusal agree.
func TestPreflightLive_OutOfBatchWrongClass_Refused(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	first := bindingDef("bind-class-owner", "0.1.0",
		[]LensSpec{plainBindingLens("ownerLens", "missing_reminder")},
		bindingTarget("ownerTarget", "ownerLens", "missing_reminder"))
	if _, err := inst.Install(ctx, first); err != nil {
		t.Fatalf("install the package owning the non-lens meta: %v", err)
	}
	targetMetaID := entityNanoID("bind-class-owner", "weaverTarget:ownerTarget")

	def := bindingDef("bind-class-bad", "0.1.0", nil,
		bindingTarget("borrowerTarget", targetMetaID, "missing_reminder"))
	msg := requireBindingRefusal(t, mustFailInstall(t, ctx, inst, def))
	if !strings.Contains(msg, "names no installed meta.lens") {
		t.Fatalf("a meta.weaverTarget id is not a lens; got %v", msg)
	}
}

// The cross-package aggregate pair, resolved by the live read.
func TestPreflightLive_OutOfBatchAggregate_RefusedThenInstalls(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	lensID := installOutOfBatchLens(t, ctx, inst, "bind-remote-agg-lens",
		aggregateBindingLens("remoteAggLens", "remoteAggTarget", []string{"missing_followUp"}, nil))

	bad := bindingDef("bind-remote-agg-bad", "0.1.0", nil,
		bindingTarget("remoteAggTarget", lensID))
	msg := requireBindingRefusal(t, mustFailInstall(t, ctx, inst, bad))
	if !strings.Contains(msg, `projects gap column "missing_followUp"`) {
		t.Fatalf("the installed lens's Output union must be read; got %v", msg)
	}

	ok := bindingDef("bind-remote-agg-ok", "0.1.0", nil,
		bindingTarget("remoteAggTargetOk", lensID, "missing_followUp"))
	if _, err := inst.Install(ctx, ok); err != nil {
		t.Fatalf("declaring the column the installed lens projects must install: %v", err)
	}
}

// The cross-package plain pair — the shape every Studio/AI-authored target
// takes, and the one the CI lint cannot see at all.
func TestPreflightLive_OutOfBatchPlain_RefusedThenInstalls(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	lensID := installOutOfBatchLens(t, ctx, inst, "bind-remote-plain-lens",
		plainBindingLens("remotePlainLens", "missing_reminder"))

	bad := bindingDef("bind-remote-plain-bad", "0.1.0", nil,
		bindingTarget("remotePlainTarget", lensID))
	msg := requireBindingRefusal(t, mustFailInstall(t, ctx, inst, bad))
	if !strings.Contains(msg, `projects gap column "missing_reminder" (in RETURN)`) {
		t.Fatalf("the installed lens's RETURN names must be read; got %v", msg)
	}

	ok := bindingDef("bind-remote-plain-ok", "0.1.0", nil,
		bindingTarget("remotePlainTargetOk", lensID, "missing_reminder"))
	if _, err := inst.Install(ctx, ok); err != nil {
		t.Fatalf("declaring the column the installed lens projects must install: %v", err)
	}
}

// The exemption, and its boundary. A target escalating `unplannable` routes an
// undeclared column to the reasoning tier by design, so the subset rule is
// waived — but existence is not: escalation says nothing about a lens that is
// not there.
func TestPreflightLive_UnplannableExemptSubsetOnly(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	exempt := bindingTarget("exemptTarget", "plainLens")
	exempt.Augur = &AugurSpec{Escalate: []string{escalateUnplannable}}
	def := bindingDef("bind-exempt", "0.1.0",
		[]LensSpec{plainBindingLens("plainLens", "missing_reminder")}, exempt)
	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("an unplannable-escalating target is exempt from the subset rule: %v", err)
	}

	dangling := bindingTarget("danglingExemptTarget", "appointmentReminders")
	dangling.Augur = &AugurSpec{Escalate: []string{escalateUnplannable}}
	msg := requireBindingRefusal(t, mustFailInstall(t, ctx, inst,
		bindingDef("bind-exempt-dangling", "0.1.0", nil, dangling)))
	if !strings.Contains(msg, "names no installed meta.lens") {
		t.Fatalf("the exemption must not widen into 'this target is not checked at all'; got %v", msg)
	}
}

// The §10.3 companion pair over a lens this Definition does not declare: the
// marker column comes from the INSTALLED lens, which only the live read
// resolves.
func TestPreflightLive_CompanionPairOverOutOfBatchLens(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	lensID := installOutOfBatchLens(t, ctx, inst, "bind-companion-lens",
		aggregateBindingLens("companionLens", "companionTarget",
			[]string{"missing_dedupe", "inflight_dedupe"}, nil))

	uncapped := bindingDef("bind-companion-bad", "0.1.0", nil,
		bindingTarget("companionTarget", lensID, "missing_dedupe"))
	msg := requireBindingRefusal(t, mustFailInstall(t, ctx, inst, uncapped))
	for _, want := range []string{`declares row-body column "inflight_dedupe"`, `but no "maxretries_dedupe"`} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal must contain %q; got %v", want, msg)
		}
	}

	// The positive vector: the same binding against a lens that declares the
	// cap alongside the marker.
	cappedID := installOutOfBatchLens(t, ctx, inst, "bind-companion-lens-capped",
		aggregateBindingLens("companionLensCapped", "companionTargetOk",
			[]string{"missing_dedupe", "inflight_dedupe", "maxretries_dedupe"}, nil))
	ok := bindingDef("bind-companion-ok", "0.1.0", nil,
		bindingTarget("companionTargetOk", cappedID, "missing_dedupe"))
	if _, err := inst.Install(ctx, ok); err != nil {
		t.Fatalf("a declared companion pair must install: %v", err)
	}
}

// --- the other entry point, and the preview -------------------------------

// Apply is a second entry point, not a wrapper around Install: a DryRun
// returns before any install path is reached. The refusal has to precede that
// return, or a preview reports a delta the real apply refuses.
func TestPreflightLive_ApplyDryRun_ReportsTheRefusalNotADelta(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	def := bindingDef("bind-dryrun", "0.1.0",
		[]LensSpec{plainBindingLens("plainLens", "missing_reminder")},
		bindingTarget("dryRunTarget", "plainLens"))
	res, err := inst.Apply(ctx, def, ApplyOptions{DryRun: true})
	if res != nil {
		t.Fatalf("a refused preview must return no delta, got %+v", res)
	}
	msg := requireBindingRefusal(t, err)
	if !strings.Contains(msg, `projects gap column "missing_reminder"`) {
		t.Fatalf("the preview must carry the refusal's own reason; got %v", msg)
	}
}

// Apply's real (non-preview) path, and its positive twin — plus idempotency:
// the rule is pure over (target spec, lens spec), so a second apply of the same
// Definition answers the same way.
func TestPreflightLive_Apply_RefusedThenInstallsIdempotently(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	bad := bindingDef("bind-apply", "0.1.0",
		[]LensSpec{plainBindingLens("plainLens", "missing_reminder")},
		bindingTarget("applyTarget", "plainLens"))
	if _, err := inst.Apply(ctx, bad, ApplyOptions{}); !errors.Is(err, ErrLensBindingRefused) {
		t.Fatalf("Apply must run the same bound as Install; got %v", err)
	}

	ok := bindingDef("bind-apply", "0.1.0",
		[]LensSpec{plainBindingLens("plainLens", "missing_reminder")},
		bindingTarget("applyTarget", "plainLens", "missing_reminder"))
	first, err := inst.Apply(ctx, ok, ApplyOptions{})
	if err != nil {
		t.Fatalf("the declared target must apply: %v", err)
	}
	if first.Skipped {
		t.Fatalf("the first apply of an uninstalled package must install it, got %+v", first)
	}
	if _, err := inst.Apply(ctx, ok, ApplyOptions{}); err != nil {
		t.Fatalf("re-applying the same Definition must stay idempotent: %v", err)
	}
}

// mustFailInstall runs an install expected to be refused and returns the error,
// failing the test if the install committed instead — so a vector can never
// pass by asserting on a nil error's absence.
func mustFailInstall(t *testing.T, ctx context.Context, inst *Installer, def Definition) error {
	t.Helper()
	res, err := inst.Install(ctx, def)
	if err == nil {
		t.Fatalf("expected the install to be refused, got %+v", res)
	}
	return err
}

// --- the third entry point, and the in-place upgrade branch -----------------

// THE UPGRADE ENTRY. A lens GAINS a column on a version bump — the commonest
// way a target that was fully declared stops being so — and Upgrade is a third
// exported mutating entry, not a wrapper around Install or Apply. Without its
// own live preflight the one shape the rule exists for commits.
func TestPreflightLive_Upgrade_RefusedThenUpgrades(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	const pkgName = "bind-upgrade"
	v1 := bindingDef(pkgName, "0.1.0",
		[]LensSpec{plainBindingLens("plainLens", "missing_reminder")},
		bindingTarget("upgradeTarget", "plainLens", "missing_reminder"))
	if _, err := inst.Install(ctx, v1); err != nil {
		t.Fatalf("the v1 target is fully declared and must install: %v", err)
	}

	// v2: the lens projects a second gap column the target never declared.
	v2 := bindingDef(pkgName, "0.2.0",
		[]LensSpec{plainBindingLens("plainLens", "missing_reminder", "missing_escalate")},
		bindingTarget("upgradeTarget", "plainLens", "missing_reminder"))
	msg := requireBindingRefusal(t, mustFailUpgrade(t, ctx, inst, v2))
	if !strings.Contains(msg, `projects gap column "missing_escalate"`) {
		t.Fatalf("the upgrade must be judged against the UPGRADED lens; got %v", msg)
	}

	// The declared twin, differing in the gaps map alone, upgrades.
	declared := bindingDef(pkgName, "0.2.0",
		[]LensSpec{plainBindingLens("plainLens", "missing_reminder", "missing_escalate")},
		bindingTarget("upgradeTarget", "plainLens", "missing_reminder", "missing_escalate"))
	if _, err := inst.Upgrade(ctx, declared); err != nil {
		t.Fatalf("declaring the new column must let the upgrade through: %v", err)
	}
}

// The same sequence through Apply's IN-PLACE branch (the package is already
// installed, so this is not applyFreshInstall's route into Install): §5's last
// row, the `make reinstall-package` shape the lint alone could not hold.
func TestPreflightLive_ApplyInPlaceUpgrade_RefusedThenApplies(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	const pkgName = "bind-apply-inplace"
	v1 := bindingDef(pkgName, "0.1.0",
		[]LensSpec{plainBindingLens("plainLens", "missing_reminder")},
		bindingTarget("inplaceTarget", "plainLens", "missing_reminder"))
	if _, err := inst.Apply(ctx, v1, ApplyOptions{}); err != nil {
		t.Fatalf("the v1 target must install: %v", err)
	}

	v2 := bindingDef(pkgName, "0.2.0",
		[]LensSpec{plainBindingLens("plainLens", "missing_reminder", "missing_escalate")},
		bindingTarget("inplaceTarget", "plainLens", "missing_reminder"))
	_, err := inst.Apply(ctx, v2, ApplyOptions{})
	msg := requireBindingRefusal(t, err)
	if !strings.Contains(msg, `projects gap column "missing_escalate"`) {
		t.Fatalf("the in-place apply must be judged against the upgraded lens; got %v", msg)
	}

	declaredV2 := bindingDef(pkgName, "0.2.0",
		[]LensSpec{plainBindingLens("plainLens", "missing_reminder", "missing_escalate")},
		bindingTarget("inplaceTarget", "plainLens", "missing_reminder", "missing_escalate"))
	res, err := inst.Apply(ctx, declaredV2, ApplyOptions{})
	if err != nil {
		t.Fatalf("the declared twin must apply in place: %v", err)
	}
	if res.Skipped {
		t.Fatalf("a version bump is not a skip: %+v", res)
	}
}

// --- §5 row 1: a target that declares no binding ---------------------------

// An empty LensRef names nothing, so there is no binding to judge — the
// contract clause says so explicitly, and resolveLensRef has always passed it
// through. Pinned on BOTH entries, because turning that pass-through into a
// refusal would break every target the lint flags but the installer admits.
func TestPreflightLive_EmptyLensRefPassesThroughBothEntries(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	unbound := bindingDef("bind-unbound-install", "0.1.0",
		[]LensSpec{plainBindingLens("plainLens", "missing_reminder")},
		bindingTarget("unboundTarget", ""))
	if _, err := inst.Install(ctx, unbound); err != nil {
		t.Fatalf("a target declaring no LensRef binds nothing and must install: %v", err)
	}

	applied := bindingDef("bind-unbound-apply", "0.1.0",
		[]LensSpec{plainBindingLens("unboundApplyLens", "missing_reminder")},
		bindingTarget("unboundApplyTarget", ""))
	if _, err := inst.Apply(ctx, applied, ApplyOptions{}); err != nil {
		t.Fatalf("Apply must pass an unbound target through too: %v", err)
	}
}

// --- the shapes a lens can be unreadable in --------------------------------

// An eventStream lens's rows are its source's project.columns — no cypher at
// all. Judged on those keys, with the provenance that tells the author where to
// edit.
func TestPreflightLive_InBatchEventStream_RefusedThenInstalls(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	undeclared := bindingDef("bind-events-bad", "0.1.0",
		[]LensSpec{eventStreamBindingLens("eventLens", "missing_ack")},
		bindingTarget("eventTarget", "eventLens"))
	msg := requireBindingRefusal(t, mustFailInstall(t, ctx, inst, undeclared))
	for _, want := range []string{`projects gap column "missing_ack"`, lenscolumns.ProvenanceProjectColumns} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal must contain %q; got %v", want, msg)
		}
	}

	declared := bindingDef("bind-events-ok", "0.1.0",
		[]LensSpec{eventStreamBindingLens("eventLens", "missing_ack")},
		bindingTarget("eventTarget", "eventLens", "missing_ack"))
	if _, err := inst.Install(ctx, declared); err != nil {
		t.Fatalf("declaring the projected column must install: %v", err)
	}
}

// The same lens shape, installed by another package and bound by id: the live
// read has to reach the source descriptor, not only the cypher.
func TestPreflightLive_OutOfBatchEventStream_RefusedThenInstalls(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	lensID := installOutOfBatchLens(t, ctx, inst, "bind-events-remote-lens",
		eventStreamBindingLens("remoteEventLens", "missing_ack"))

	bad := bindingDef("bind-events-remote-bad", "0.1.0", nil, bindingTarget("remoteEventTarget", lensID))
	msg := requireBindingRefusal(t, mustFailInstall(t, ctx, inst, bad))
	if !strings.Contains(msg, lenscolumns.ProvenanceProjectColumns) {
		t.Fatalf("an installed eventStream lens must be judged on its project.columns; got %v", msg)
	}

	ok := bindingDef("bind-events-remote-ok", "0.1.0", nil, bindingTarget("remoteEventTargetOk", lensID, "missing_ack"))
	if _, err := inst.Install(ctx, ok); err != nil {
		t.Fatalf("declaring the projected column must install: %v", err)
	}
}

// The three in-batch shapes whose columns are not derivable at all. Each is
// refused NAMING THE REASON rather than read as "no columns" — the distinction
// the whitelist exists for, since an empty answer would admit a target that
// declares nothing.
func TestPreflightLive_UnreadableInBatchShapes_RefusedNamingTheReason(t *testing.T) {
	outputless := aggregateBindingLens("aggLens", "shapeTarget", nil, nil)
	outputless.Output = nil

	entryKeyed := aggregateBindingLens("aggLens", "shapeTarget", []string{"missing_x"}, nil)
	entryKeyed.Output.EntryKeyColumn = "entryId"

	unparseable := plainBindingLens("aggLens")
	unparseable.Spec = "this is not openCypher at all"

	bothRules := plainBindingLens("aggLens", "missing_x")
	bothRules.SpecBranches = []string{"MATCH (n:identity) RETURN n.key AS key"}

	for _, tc := range []struct {
		name string
		lens LensSpec
		want string
	}{
		{"actorAggregate with no Output", outputless, "declares no Output descriptor"},
		{"per-entry list lens", entryKeyed, "per-entry list lens"},
		{"plain lens that does not parse", unparseable, "cannot be derived"},
		{"a spec declaring both cypherRule and cypherBranches", bothRules, "mutually exclusive"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _, inst := newInstallerHarness(t)
			def := bindingDef("bind-shape-"+strings.ToLower(strings.ReplaceAll(tc.name, " ", "-")), "0.1.0",
				[]LensSpec{tc.lens}, bindingTarget("shapeTarget", "aggLens", "missing_x"))
			msg := requireBindingRefusal(t, mustFailInstall(t, ctx, inst, def))
			if !strings.Contains(msg, tc.want) {
				t.Fatalf("refusal must name the reason %q; got %v", tc.want, msg)
			}
		})
	}
}

// A lens whose package was uninstalled is gone as far as any binding goes:
// Contract #1 keeps the key occupied by a tombstone, so "absent" and
// "tombstoned" are different reads of the same key and both must refuse.
func TestPreflightLive_OutOfBatchTombstonedLens_Refused(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	const lensPkg = "bind-tombstone-lens"
	lensID := installOutOfBatchLens(t, ctx, inst, lensPkg, plainBindingLens("goneLens", "missing_reminder"))
	if _, err := inst.Install(ctx, bindingDef("bind-tombstone-live", "0.1.0", nil,
		bindingTarget("liveTarget", lensID, "missing_reminder"))); err != nil {
		t.Fatalf("precondition: the binding must hold while the lens is live: %v", err)
	}
	if _, err := inst.Uninstall(ctx, lensPkg, UninstallOptions{}); err != nil {
		t.Fatalf("uninstall the lens's package: %v", err)
	}

	msg := requireBindingRefusal(t, mustFailInstall(t, ctx, inst,
		bindingDef("bind-tombstone-after", "0.1.0", nil, bindingTarget("danglingTarget", lensID, "missing_reminder"))))
	if !strings.Contains(msg, "names no installed meta.lens") {
		t.Fatalf("a tombstoned lens must refuse exactly as an absent one does; got %v", msg)
	}
}

// The two ways an INSTALLED lens's own declaration goes missing under it: the
// spec aspect absent, and the spec aspect tombstoned. Both leave a live
// meta.lens root whose columns nothing states, which is refused naming the
// reason — never read as a lens with no gap columns.
func TestPreflightLive_OutOfBatchLensWithNoReadableSpec_Refused(t *testing.T) {
	for _, tc := range []struct {
		name   string
		break_ func(t *testing.T, ctx context.Context, conn *substrate.Conn, specKey string)
		want   string
	}{
		{"spec aspect absent", func(t *testing.T, ctx context.Context, conn *substrate.Conn, specKey string) {
			if err := conn.KVDelete(ctx, CoreBucket, specKey); err != nil {
				t.Fatalf("delete %s: %v", specKey, err)
			}
		}, "carries no spec aspect"},
		{"spec aspect tombstoned", func(t *testing.T, ctx context.Context, conn *substrate.Conn, specKey string) {
			doc := kvDoc(t, ctx, conn, specKey)
			doc["isDeleted"] = true
			body, err := json.Marshal(doc)
			if err != nil {
				t.Fatalf("marshal %s: %v", specKey, err)
			}
			if _, err := conn.KVPut(ctx, CoreBucket, specKey, body); err != nil {
				t.Fatalf("tombstone %s: %v", specKey, err)
			}
		}, "is tombstoned"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, conn, inst := newInstallerHarness(t)
			lensID := installOutOfBatchLens(t, ctx, inst, "bind-nospec-lens", plainBindingLens("noSpecLens", "missing_reminder"))
			tc.break_(t, ctx, conn, metaVertexPrefix+lensID+".spec")

			// The resolver's own answer: FOUND (the lens exists) and
			// unreadable (nothing states its columns).
			cols, found, err := NewCoreKVLensResolver(ctx, conn, fullCypherParser{}).ResolveLensColumns(lensID)
			if !found {
				t.Fatalf("the lens root is live; an unreadable lens must not answer 'not found'")
			}
			if !errors.Is(err, lenscolumns.ErrUnreadable) {
				t.Fatalf("err = %v, want lenscolumns.ErrUnreadable", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to name %q", err, tc.want)
			}
			if len(cols.Columns) != 0 {
				t.Errorf("an unreadable lens declares no columns, got %v", cols.Columns)
			}

			// And both entry points refuse a target bound to it, naming that
			// reason rather than admitting a target declaring nothing.
			def := bindingDef("bind-nospec-target", "0.1.0", nil, bindingTarget("noSpecTarget", lensID, "missing_reminder"))
			msg := requireBindingRefusal(t, mustFailInstall(t, ctx, inst, def))
			if !strings.Contains(msg, "cannot be derived") || !strings.Contains(msg, tc.want) {
				t.Fatalf("Install refusal must name the reason; got %v", msg)
			}
			_, applyErr := inst.Apply(ctx, def, ApplyOptions{})
			if applyMsg := requireBindingRefusal(t, applyErr); !strings.Contains(applyMsg, tc.want) {
				t.Fatalf("Apply refusal must name the same reason; got %v", applyMsg)
			}
		})
	}
}

// EVERY undeclared column in one refusal. An author holding three of them must
// not have to discover them over three apply attempts — and the installer's
// list must not be shorter than the validator's for the same pair.
func TestPreflightLive_NamesEveryUndeclaredColumn(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	def := bindingDef("bind-many-undeclared", "0.1.0",
		[]LensSpec{plainBindingLens("plainLens", "missing_a", "missing_b", "missing_c")},
		bindingTarget("manyTarget", "plainLens", "missing_b"))
	msg := requireBindingRefusal(t, mustFailInstall(t, ctx, inst, def))
	for _, want := range []string{`"missing_a"`, `"missing_c"`} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal must name %s; got %v", want, msg)
		}
	}
	if strings.Contains(msg, `projects gap column "missing_b"`) {
		t.Errorf("the DECLARED column must not be reported: %v", msg)
	}
}

// The §10.3 companion-pair remedy has to be one the author can follow: a plain
// lens has no Output descriptor at all, so "declare it in Output.BodyColumns"
// is an instruction with nowhere to land.
func TestPreflightLive_CompanionPairRemedyFollowsTheProvenance(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	lens := plainBindingLens("plainLens", "missing_dedupe")
	lens.Spec += ", true AS inflight_dedupe"
	def := bindingDef("bind-companion-plain", "0.1.0", []LensSpec{lens},
		bindingTarget("plainCompanionTarget", "plainLens", "missing_dedupe"))
	msg := requireBindingRefusal(t, mustFailInstall(t, ctx, inst, def))
	if !strings.Contains(msg, `Add "maxretries_dedupe" to the lens's RETURN clause`) {
		t.Fatalf("a plain lens's remedy must name its RETURN clause; got %v", msg)
	}
	if strings.Contains(msg, "Output.BodyColumns") {
		t.Errorf("a plain lens has no Output descriptor to edit: %v", msg)
	}

	// The positive twin: the cap declared in the same clause.
	capped := plainBindingLens("plainLens", "missing_dedupe")
	capped.Spec += ", true AS inflight_dedupe, 5 AS maxretries_dedupe"
	ok := bindingDef("bind-companion-plain-ok", "0.1.0", []LensSpec{capped},
		bindingTarget("plainCompanionTargetOk", "plainLens", "missing_dedupe"))
	if _, err := inst.Install(ctx, ok); err != nil {
		t.Fatalf("a declared companion pair on a plain lens must install: %v", err)
	}
}

// mustFailUpgrade runs an upgrade expected to be refused and returns the error,
// failing the test if it committed instead.
func mustFailUpgrade(t *testing.T, ctx context.Context, inst *Installer, def Definition) error {
	t.Helper()
	res, err := inst.Upgrade(ctx, def)
	if err == nil {
		t.Fatalf("expected the upgrade to be refused, got %+v", res)
	}
	return err
}
