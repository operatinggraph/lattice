package bridge

import (
	"errors"
	"testing"

	"github.com/operatinggraph/lattice/internal/lenscolumns"
)

// catalogResolverFixture builds the resolver the way the adapter does — from a
// catalog read over the fixture's own seeded rows — so these vectors judge the
// real row shapes (internal/bridge's seedCatalog), not a hand-made index.
func catalogResolverFixture(t *testing.T) catalogLensResolver {
	t.Helper()
	rows := buildCatalogRead(catalogFixtureKeys(), catalogFixtureValue)
	return rows.lensResolver(fixtureReturnColumns)
}

// THE POSITIVE VECTOR: a plain lens row resolves to the RETURN names its
// cypher projects. The catalog carries `m.spec.data AS spec`, which is the
// same body internal/pkgmgr/build.go writes, so the columns come out of the
// declaration the installer would read too.
func TestCatalogLensResolver_PlainLensRowYieldsReturnColumns(t *testing.T) {
	t.Parallel()
	cols, found, err := catalogResolverFixture(t).ResolveLensColumns(staleLensNanoID)
	if err != nil || !found {
		t.Fatalf("ResolveLensColumns = (found %v, err %v), want the seeded lens read", found, err)
	}
	if got := cols.Columns["missing_reminder"]; got != lenscolumns.ProvenanceReturn {
		t.Errorf("missing_reminder provenance = %q, want %q", got, lenscolumns.ProvenanceReturn)
	}
	if _, ok := lenscolumns.Gaps(cols)["missing_reminder"]; !ok {
		t.Errorf("the gap scan must see the projected column, got %v", lenscolumns.Gaps(cols))
	}
}

// The class test, over a real catalog row of another class: a weaverTarget meta
// carries a spec too, so membership has to be decided on `class`, not on
// "has a spec". Without this a target's own id would bind as a lens.
func TestCatalogLensResolver_NonLensClassIsNotFound(t *testing.T) {
	t.Parallel()
	_, found, err := catalogResolverFixture(t).ResolveLensColumns(existingTargetID)
	if err != nil {
		t.Fatalf("a row of the wrong class is a verdict, not a failure: %v", err)
	}
	if found {
		t.Fatalf("a meta.weaverTarget row must not answer a lens binding")
	}
	_, found, _ = catalogResolverFixture(t).ResolveLensColumns(nudgePatternID)
	if found {
		t.Fatalf("a meta.loomPattern row must not answer a lens binding either")
	}
}

func TestCatalogLensResolver_AbsentIDIsNotFound(t *testing.T) {
	t.Parallel()
	_, found, err := catalogResolverFixture(t).ResolveLensColumns("appointmentReminders")
	if err != nil || found {
		t.Fatalf("ResolveLensColumns = (found %v, err %v), want (false, nil) for an id no row carries", found, err)
	}
}

// An installed lens whose columns cannot be derived is FOUND and unreadable —
// never column-less. The two answers mean different things downstream: one
// refuses the binding naming the reason, the other would silently accept a
// target that declares nothing.
func TestCatalogLensResolver_UnreadableSpecIsFoundAndUnreadable(t *testing.T) {
	t.Parallel()
	const id = "unreadabXYZabcdefghi"
	rows := buildCatalogRead([]string{metaKeyPrefix + id}, func(string) []byte {
		return []byte(`{"key":"` + metaKeyPrefix + id + `","class":"meta.lens","canonicalName":"unreadable",` +
			`"spec":{"cypherRule":"MATCH (i:identity)"}}`)
	})
	cols, found, err := rows.lensResolver(fixtureReturnColumns).ResolveLensColumns(id)
	if !found {
		t.Fatalf("the lens IS in the catalog; an underivable one must not answer 'not found'")
	}
	if !errors.Is(err, lenscolumns.ErrUnreadable) {
		t.Fatalf("err = %v, want lenscolumns.ErrUnreadable", err)
	}
	if len(cols.Columns) != 0 {
		t.Fatalf("an unreadable lens declares no columns a caller may act on, got %v", cols.Columns)
	}
}

// A nil column parse leaves every PLAIN lens unreadable — and every
// capability-artifact lens is plain. Pinned as behaviour because it is what
// makes the constructor's nil refusal load-bearing rather than decorative.
func TestCatalogLensResolver_NilParseLeavesPlainLensesUnreadable(t *testing.T) {
	t.Parallel()
	rows := buildCatalogRead(catalogFixtureKeys(), catalogFixtureValue)
	_, found, err := rows.lensResolver(nil).ResolveLensColumns(staleLensNanoID)
	if !found {
		t.Fatalf("the lens is in the catalog whether or not anything can parse it")
	}
	if !errors.Is(err, lenscolumns.ErrUnreadable) {
		t.Fatalf("err = %v, want lenscolumns.ErrUnreadable — never an empty column set", err)
	}
}
