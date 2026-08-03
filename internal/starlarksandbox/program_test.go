package starlarksandbox

import (
	"context"
	"testing"
	"time"

	starlarklib "go.starlark.net/starlark"
)

func progBudget() Budget { return Budget{Wall: 5 * time.Second, MaxSteps: 100_000} }

// TestCompile_ProgramServesManyRuns is the property the whole split exists
// for: one compile, many Runs, each with its own globals. A compiled program
// holds no execution state, so a caller running two passes over one source
// pays one compile.
func TestCompile_ProgramServesManyRuns(t *testing.T) {
	t.Parallel()
	src := `
def run(n):
    return n * factor
`
	globals := starlarklib.StringDict{"factor": starlarklib.MakeInt(3)}
	prog, sErr := Compile(src, globals.Has)
	if sErr != nil {
		t.Fatalf("Compile: %+v", sErr)
	}

	for _, tc := range []struct{ factor, arg, want int }{{3, 2, 6}, {10, 4, 40}} {
		out, sErr := Run(context.Background(), prog, "run",
			starlarklib.Tuple{starlarklib.MakeInt(tc.arg)},
			starlarklib.StringDict{"factor": starlarklib.MakeInt(tc.factor)}, progBudget())
		if sErr != nil {
			t.Fatalf("Run: %+v", sErr)
		}
		got, _ := starlarklib.AsInt32(out)
		if got != tc.want {
			t.Fatalf("got %d, want %d — each Run must see its OWN globals", got, tc.want)
		}
	}
}

// TestCompile_TwoEntrypointsOneCompile pins the two-pass shape directly: the
// same program answers a pre-pass entrypoint and a main one.
func TestCompile_TwoEntrypointsOneCompile(t *testing.T) {
	t.Parallel()
	src := `
def derive_reads(op):
    return "derived"

def execute(state, op):
    return "executed"
`
	globals := starlarklib.StringDict{}
	prog, sErr := Compile(src, globals.Has)
	if sErr != nil {
		t.Fatalf("Compile: %+v", sErr)
	}
	for entry, args := range map[string]starlarklib.Tuple{
		"derive_reads": {starlarklib.None},
		"execute":      {starlarklib.None, starlarklib.None},
	} {
		out, sErr := Run(context.Background(), prog, entry, args, globals, progBudget())
		if sErr != nil {
			t.Fatalf("Run(%s): %+v", entry, sErr)
		}
		if _, ok := out.(starlarklib.String); !ok {
			t.Fatalf("Run(%s) returned %s", entry, out.Type())
		}
	}
}

// TestDefinesTopLevel answers the optional-entrypoint question without paying
// an Init — which is what makes "a DDL declaring no derivation costs nothing"
// true rather than aspirational.
func TestDefinesTopLevel(t *testing.T) {
	t.Parallel()
	src := `
CONST = 1

def defined(op):
    return 1

assigned = defined

def _outer():
    def nested(op):
        return 2
    return nested
`
	prog, sErr := Compile(src, func(string) bool { return false })
	if sErr != nil {
		t.Fatalf("Compile: %+v", sErr)
	}
	for _, name := range []string{"CONST", "defined", "assigned", "_outer"} {
		if !prog.DefinesTopLevel(name) {
			t.Fatalf("%q is bound at the top level but DefinesTopLevel says no", name)
		}
	}
	// A nested def is not a top-level binding, and a name the script never
	// mentions obviously is not either.
	for _, name := range []string{"nested", "absent"} {
		if prog.DefinesTopLevel(name) {
			t.Fatalf("%q is not a top-level binding but DefinesTopLevel says it is", name)
		}
	}
}

// TestDefinesTopLevel_AssignmentCounts is the fail-open direction, pinned on
// its own: a caller probing for an OPTIONAL entrypoint must not silently skip
// one the script provides by assignment rather than by `def`.
func TestDefinesTopLevel_AssignmentCounts(t *testing.T) {
	t.Parallel()
	prog, sErr := Compile("derive_reads = lambda op: {}\n", func(string) bool { return false })
	if sErr != nil {
		t.Fatalf("Compile: %+v", sErr)
	}
	if !prog.DefinesTopLevel("derive_reads") {
		t.Fatalf("an assigned entrypoint must count as defined — skipping it is fail-open")
	}
}

// TestCompile_UnboundNameIsASandboxViolation keeps the compile-time posture
// where the split moved it: the predeclared-name probe is Compile's argument
// now, so the violation must still be reported there.
func TestCompile_UnboundNameIsASandboxViolation(t *testing.T) {
	t.Parallel()
	_, sErr := Compile("def run():\n    return os.getenv('X')\n", func(string) bool { return false })
	if sErr == nil {
		t.Fatalf("an unbound name must not compile")
	}
	if sErr.Code != SandboxViolation {
		t.Fatalf("Code = %q, want SandboxViolation", sErr.Code)
	}
}
