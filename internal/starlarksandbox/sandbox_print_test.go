package starlarksandbox

import (
	"context"
	"io"
	"os"
	"testing"
	"time"

	starlarklib "go.starlark.net/starlark"
)

// captureStderr runs fn with os.Stderr replaced by a pipe and returns whatever
// was written to it. Stderr is the only place a discarded print could surface —
// go.starlark.net's own fallback writes there — so observing it is the only
// honest way to assert the sandbox has no host output channel.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	fn()

	os.Stderr = orig
	if err := w.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("close pipe reader: %v", err)
	}
	return string(out)
}

func printBudget() Budget {
	return Budget{Wall: 5 * time.Second, MaxSteps: 100_000}
}

// TestExecute_PrintWritesNothingToTheHost: a script's values are the
// operation's data, and for a Processor DDL script they include documents
// hydration already decrypted — so a print must not render them into the host
// process's log, which happens while the script runs, ahead of every
// commit-time check.
func TestExecute_PrintWritesNothingToTheHost(t *testing.T) {
	const src = `
def main(x):
    print({"ssn": "123-45-6789", "note": "sensitive"})
    print("plain label")
    return "ok"
`
	var out starlarklib.Value
	var serr *SandboxError
	stderr := captureStderr(t, func() {
		out, serr = Execute(context.Background(), src, "main", starlarklib.Tuple{starlarklib.None}, starlarklib.StringDict{}, printBudget())
	})

	if serr != nil {
		t.Fatalf("Execute: %v", serr)
	}
	// print stays a legal call that returns None and lets the script finish —
	// dropping the output must not turn a debugging line into a script failure.
	if s, ok := starlarklib.AsString(out); !ok || s != "ok" {
		t.Fatalf("script return = %v, want \"ok\"", out)
	}
	if stderr != "" {
		t.Fatalf("print reached the host process: stderr=%q", stderr)
	}
}

// TestValidate_PrintWritesNothingToTheHost covers the other thread: Validate
// runs a script's TOP-LEVEL statements, so a print outside the entrypoint
// executes at load time, before any operation is even in flight.
func TestValidate_PrintWritesNothingToTheHost(t *testing.T) {
	const src = `
print({"ssn": "123-45-6789"})

def main(x):
    return "ok"
`
	var serr *SandboxError
	stderr := captureStderr(t, func() {
		serr = Validate(src, "main", 1, starlarklib.StringDict{}, printBudget())
	})

	if serr != nil {
		t.Fatalf("Validate: %v", serr)
	}
	if stderr != "" {
		t.Fatalf("a top-level print reached the host process: stderr=%q", stderr)
	}
}

// TestCaptureStderr_SeesRealOutput is the discriminating control: without it,
// both tests above would pass just as well against a capture that can never
// observe anything.
func TestCaptureStderr_SeesRealOutput(t *testing.T) {
	stderr := captureStderr(t, func() {
		if _, err := os.Stderr.WriteString("visible\n"); err != nil {
			t.Errorf("write: %v", err)
		}
	})
	if stderr != "visible\n" {
		t.Fatalf("capture did not observe a real write: %q", stderr)
	}
}
