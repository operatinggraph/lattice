package processor

import (
	"testing"

	"github.com/operatinggraph/lattice/internal/starlarksandbox"
)

// classifyScriptError only reclassifies a generic ScriptError as
// ClaimKeyInvalid — matching the original single-file classifier's
// priority order, where undefined:/load: were checked (and returned)
// before ClaimKeyInvalid was ever considered. A SandboxViolation must never
// be reinterpreted as ClaimKeyInvalid even if its message happens to
// contain that substring.
func TestClassifyScriptError_ClaimKeyInvalidOnlyReclassifiesScriptError(t *testing.T) {
	cases := []struct {
		name     string
		sErr     *starlarksandbox.SandboxError
		wantCode string
	}{
		{
			name:     "ScriptError with ClaimKeyInvalid detail reclassifies",
			sErr:     &starlarksandbox.SandboxError{Code: starlarksandbox.ScriptError, Message: `fail: ClaimKeyInvalid: wrong-state`},
			wantCode: "ClaimKeyInvalid",
		},
		{
			name:     "SandboxViolation is never reinterpreted as ClaimKeyInvalid",
			sErr:     &starlarksandbox.SandboxError{Code: starlarksandbox.SandboxViolation, Message: `undefined: os (ClaimKeyInvalid: irrelevant)`},
			wantCode: "SandboxViolation",
		},
		{
			name:     "ScriptTimeout is never reinterpreted as ClaimKeyInvalid",
			sErr:     &starlarksandbox.SandboxError{Code: starlarksandbox.ScriptTimeout, Message: `script exceeded wall budget (ClaimKeyInvalid: irrelevant)`},
			wantCode: "ScriptTimeout",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyScriptError(tc.sErr, "req-1")
			if got.Code != tc.wantCode {
				t.Fatalf("Code = %q, want %q", got.Code, tc.wantCode)
			}
		})
	}
}
