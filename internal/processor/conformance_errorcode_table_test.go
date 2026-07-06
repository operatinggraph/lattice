package processor

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// emittedErrorCodes is the authoritative set of ErrorCodes the Processor emits
// on the wire. TestConformance_ErrorCode_ClosedEnum and the §2.6 contract-table
// check both read from it, so the closed enum and the frozen contract table stay
// in lockstep.
var emittedErrorCodes = map[ErrorCode]bool{
	ErrCodeEnvelopeMalformed:         true,
	ErrCodeLaneUnauthorized:          true,
	ErrCodeAuthDenied:                true,
	ErrCodeAuthContextMismatch:       true,
	ErrCodeInternalError:             true,
	ErrCodeHydrationFailed:           true,
	ErrCodeScriptFailed:              true,
	ErrCodeDDLViolation:              true,
	ErrCodeRevisionConflict:          true,
	ErrCodeProtectedKey:              true,
	ErrCodeAuthInfrastructureFailure: true,
	ErrCodeClaimKeyInvalid:           true,
	ErrCodeBatchTooLarge:             true,
}

// contract2ReservedErrorCodes are §2.6 codes documented but deliberately not yet
// wire-emitted (reserved for a future phase). They appear in the contract table
// but not in emittedErrorCodes.
var contract2ReservedErrorCodes = map[string]bool{
	"CellMoved": true, // Multi-cell, Phase 3
}

// TestConformance_ErrorCodeTable_MatchesWire parses Contract #2 §2.6's error-code
// table and asserts it equals the emitted ErrorCode set plus the explicitly
// reserved codes — so the frozen table can never again drift from the wire. (The
// 2026-07 arch-review found the table listed 7 codes never emitted and omitted 6
// codes that are; this test is the pin that keeps them reconciled.)
func TestConformance_ErrorCodeTable_MatchesWire(t *testing.T) {
	table := parseContract2ErrorCodeTable(t)

	for code := range emittedErrorCodes {
		if !table[string(code)] {
			t.Errorf("emitted ErrorCode %q is missing from Contract #2 §2.6 table", code)
		}
	}
	for code := range table {
		if contract2ReservedErrorCodes[code] {
			continue
		}
		if !emittedErrorCodes[ErrorCode(code)] {
			t.Errorf("Contract #2 §2.6 lists %q, which the Processor never emits and is not a reserved code", code)
		}
	}
}

// contract2CodeRowRe matches a §2.6 table data row whose first cell is a
// backticked code, e.g. "| `ScriptFailed` | … | … |". The header ("| Code |")
// and separator ("|------|") rows do not match (no backticks).
var contract2CodeRowRe = regexp.MustCompile("^\\|\\s*`([A-Za-z]+)`\\s*\\|")

// parseContract2ErrorCodeTable extracts the code names from the §2.6 table of
// docs/contracts/02-operation-envelope.md. Fails if the section or table is gone.
func parseContract2ErrorCodeTable(t *testing.T) map[string]bool {
	t.Helper()
	path := filepath.Join("..", "..", "docs", "contracts", "02-operation-envelope.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read Contract #2: %v", err)
	}
	codes := map[string]bool{}
	inSection := false
	for _, ln := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(ln, "### 2.6") {
			inSection = true
			continue
		}
		if inSection && strings.HasPrefix(ln, "### ") {
			break // reached the next section
		}
		if inSection {
			if m := contract2CodeRowRe.FindStringSubmatch(ln); m != nil {
				codes[m[1]] = true
			}
		}
	}
	if len(codes) == 0 {
		t.Fatalf("no error codes parsed from Contract #2 §2.6 — did the table format change?")
	}
	return codes
}
