package processor

// Test-only exports reached from the EXTERNAL processor_test package.
//
// That package is the only place a test may import a packages/ definition —
// every packages/ package imports this one, so an internal test file that did
// the same would close an import cycle — and likewise the only place it may
// import internal/testutil, which the harness a real seeded graph needs lives
// in.
//
// The two conditioning helpers are exercised over a real shipped DDL script's
// output rather than a hand-built mutation slice, which is what proves a
// script's mutation KINDS actually select the conditioning its design rests on.
var (
	ApplyHydratedRevisionsForTest   = applyHydratedRevisions
	AbsentConditionedCreatesForTest = absentConditionedCreates

	// IsSeededLinkShapeForTest is the kernel-link arm's admission predicate.
	// It is reached externally so a pin can feed it what internal/bootstrap's
	// SEEDER actually emits: this package deliberately does not import
	// internal/bootstrap (the key set is threaded in through AuthWiring
	// precisely so it need not), and a test that instead restated the seeder's
	// shape would pin the predicate against its own copy of the very thing the
	// predicate is supposed to track.
	IsSeededLinkShapeForTest = isSeededLinkShape

	// CommitterInjectedEnvelopeFieldsForTest are the top-level fields
	// buildMutationValue supplies on the committer's own authority. A stored
	// document carries them; the script document the guard adjudicates never
	// does, so a pin comparing the two has to set exactly these aside.
	CommitterInjectedEnvelopeFieldsForTest = []string{
		"key", "createdAt", "createdBy", "createdByOp",
		"lastModifiedAt", "lastModifiedBy", "lastModifiedByOp",
	}
)
