package pkgmgr

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/lenscolumns"
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

// The plain pair. A plain lens declares no Output at all, so before this rule
// nothing at install time could say what its rows carry.
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

// The §10.3 companion pair reaching a lens this Definition does not declare —
// the skip that retires with the live read. The marker column comes from the
// INSTALLED lens, which the pure gate could never see.
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
