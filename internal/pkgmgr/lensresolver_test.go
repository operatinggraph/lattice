package pkgmgr

import (
	"errors"
	"testing"

	"github.com/operatinggraph/lattice/internal/lenscolumns"
)

// The resolver is driven against a REAL installed kernel throughout: every
// fixture below is written by an actual Install, so what it reads is the
// stored shape build.go writes rather than a hand-assembled envelope that
// could drift from it.

func TestCoreKVLensResolver_InstalledAggregate_ReadsTheOutputUnion(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	lensID := installOutOfBatchLens(t, ctx, inst, "resolver-agg",
		aggregateBindingLens("resolverAggLens", "resolverAggTarget",
			[]string{"missing_followUp"}, []string{"missing_escalate"}))

	cols, found, err := NewCoreKVLensResolver(ctx, conn, fullCypherParser{}).ResolveLensColumns(lensID)
	if err != nil || !found {
		t.Fatalf("ResolveLensColumns = (found %v, err %v), want a readable installed lens", found, err)
	}
	if got := cols.Columns["missing_followUp"]; got != lenscolumns.ProvenanceBodyColumns {
		t.Errorf("missing_followUp provenance = %q, want %q", got, lenscolumns.ProvenanceBodyColumns)
	}
	if got := cols.Columns["missing_escalate"]; got != lenscolumns.ProvenanceStaticEmptyColumns {
		t.Errorf("missing_escalate provenance = %q, want %q — a StaticEmptyColumns entry is a PRESENT key in the projected row", got, lenscolumns.ProvenanceStaticEmptyColumns)
	}
	if _, ok := lenscolumns.Gaps(cols)["missing_escalate"]; !ok {
		t.Errorf("the gap scan must see both lists, got %v", lenscolumns.Gaps(cols))
	}
}

func TestCoreKVLensResolver_InstalledPlain_ReadsTheReturnNames(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	lensID := installOutOfBatchLens(t, ctx, inst, "resolver-plain",
		plainBindingLens("resolverPlainLens", "missing_reminder"))

	cols, found, err := NewCoreKVLensResolver(ctx, conn, fullCypherParser{}).ResolveLensColumns(lensID)
	if err != nil || !found {
		t.Fatalf("ResolveLensColumns = (found %v, err %v), want a readable installed lens", found, err)
	}
	if got := cols.Columns["missing_reminder"]; got != lenscolumns.ProvenanceReturn {
		t.Errorf("missing_reminder provenance = %q, want %q", got, lenscolumns.ProvenanceReturn)
	}
	if _, ok := cols.Columns["key"]; !ok {
		t.Errorf("every RETURN item's name is a row key; got %v", cols.Columns)
	}
}

// THE NIL-PARSER BOUNDARY, against the same installed lens as the vector
// above: with no parser the columns are not DERIVABLE, which is a different
// answer from "no columns" — and the one every holder fails closed on.
func TestCoreKVLensResolver_InstalledPlainNoParser_Unreadable(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	lensID := installOutOfBatchLens(t, ctx, inst, "resolver-plain-noparser",
		plainBindingLens("resolverPlainLens", "missing_reminder"))

	cols, found, err := NewCoreKVLensResolver(ctx, conn, nil).ResolveLensColumns(lensID)
	if !found {
		t.Fatalf("the lens IS installed; an unreadable one must not answer 'not found'")
	}
	if !errors.Is(err, lenscolumns.ErrUnreadable) {
		t.Fatalf("err = %v, want lenscolumns.ErrUnreadable", err)
	}
	if len(cols.Columns) != 0 {
		t.Fatalf("an unreadable lens declares no columns a caller may act on, got %v", cols.Columns)
	}
}

func TestCoreKVLensResolver_AbsentID_NotFound(t *testing.T) {
	ctx, conn, _ := newInstallerHarness(t)

	_, found, err := NewCoreKVLensResolver(ctx, conn, fullCypherParser{}).ResolveLensColumns("appointmentReminders")
	if err != nil {
		t.Fatalf("an absent key is a non-answer, not a read failure: %v", err)
	}
	if found {
		t.Fatalf("no lens is installed under that id")
	}
}

// A ref that is not NanoID-shaped names no installed meta-vertex at all, and
// is answered without a read.
func TestCoreKVLensResolver_NonNanoIDRef_NotFound(t *testing.T) {
	ctx, conn, _ := newInstallerHarness(t)

	_, found, err := NewCoreKVLensResolver(ctx, conn, fullCypherParser{}).ResolveLensColumns("someCanonicalName")
	if err != nil || found {
		t.Fatalf("ResolveLensColumns = (found %v, err %v), want (false, nil) for a ref that is not id-shaped", found, err)
	}
}

// The class test, on the id of a real meta-vertex of another class: a weaver
// target's own meta. Without it the canonical-name path would admit a DDL or
// op-meta id as a lens.
func TestCoreKVLensResolver_WrongClass_NotFound(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	def := bindingDef("resolver-class", "0.1.0",
		[]LensSpec{plainBindingLens("classLens", "missing_reminder")},
		bindingTarget("classTarget", "classLens", "missing_reminder"))
	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("install: %v", err)
	}

	targetMetaID := entityNanoID("resolver-class", "weaverTarget:classTarget")
	_, found, err := NewCoreKVLensResolver(ctx, conn, fullCypherParser{}).ResolveLensColumns(targetMetaID)
	if err != nil {
		t.Fatalf("a live document of the wrong class is a verdict, not a read failure: %v", err)
	}
	if found {
		t.Fatalf("a meta.weaverTarget root is not a lens — its spec would decode, which is exactly why the CLASS is the test")
	}
}

// A tombstoned lens — this package's own uninstall — is gone as far as any
// binding is concerned. Contract #1 keeps the key occupied, so the tombstone
// flag is the only thing that distinguishes it from a live lens.
func TestCoreKVLensResolver_TombstonedLens_NotFound(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	const pkgName = "resolver-tombstone"
	lensID := installOutOfBatchLens(t, ctx, inst, pkgName, plainBindingLens("goneLens", "missing_reminder"))

	if _, found, err := NewCoreKVLensResolver(ctx, conn, fullCypherParser{}).ResolveLensColumns(lensID); !found || err != nil {
		t.Fatalf("precondition: the lens must be readable before the uninstall (found %v, err %v)", found, err)
	}
	if _, err := inst.Uninstall(ctx, pkgName, UninstallOptions{}); err != nil {
		t.Fatalf("uninstall: %v", err)
	}

	_, found, err := NewCoreKVLensResolver(ctx, conn, fullCypherParser{}).ResolveLensColumns(lensID)
	if err != nil {
		t.Fatalf("a tombstoned root is a verdict, not a read failure: %v", err)
	}
	if found {
		t.Fatalf("an uninstalled lens must not answer a binding")
	}
}
