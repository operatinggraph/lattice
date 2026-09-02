package processor

// Test-only exports of the commit path's two conditioning helpers.
//
// They are reached from the EXTERNAL processor_test package, which is the only
// place a test may import a packages/ definition: every packages/ package
// imports this one, so an internal test file that did the same would close an
// import cycle. Exercising these over a real shipped DDL script's output —
// rather than a hand-built mutation slice — is what proves a script's mutation
// KINDS actually select the conditioning its design rests on.
var (
	ApplyHydratedRevisionsForTest   = applyHydratedRevisions
	AbsentConditionedCreatesForTest = absentConditionedCreates
)
