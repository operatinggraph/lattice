package privacybase

// The three maxretries_<g> caps identityErasureResidueSpec projects are derived
// numbers, not preferences: each is the summed reach of the arms its op sweeps,
// divided by the live links one commit can tombstone (retry_budget.go states
// the derivation). A test that only checked the projected column against the
// same constant would pin nothing — set the constant to 7 and it stays green
// while a real erasure strands after seven pages. So this file re-derives all
// three from the SWEEP SCRIPTS THEMSELVES and fails if a constant drifts from
// the op it bounds.
//
// The inputs are Starlark constants living inside Go string constants in two
// packages, so they are read by PATH with go/ast: the DDL script constants are
// unexported, and identity-domain's could not be reached by import at all. This
// mirrors internal/pkgmgr's gapcompanionpin_test, which pins the installer's
// restated engine vocabulary by parsing internal/weaver the same way. Every
// helper below fails the test when what it looks for is missing, so deleting or
// renaming a constant this derivation rests on is a loud failure and never a
// vacuous pass.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	credentialSweepSource = "../identity-domain/unbind_identity_credentials.go"
	dedupSweepSource      = "purge_identity_dedup_footprint.go"

	// The engine's fallback budget for an external gap that declares no usable
	// cap (internal/weaver's defaultDirectOpRetryBudget). Restated rather than
	// imported — a package must not depend on an engine — and used here only as
	// the floor every derived cap has to clear, which is the whole reason these
	// gaps declare their own.
	engineDefaultDirectOpRetryBudget = 3
)

// starlarkScript returns the named package-level string constant from a Go
// source file — the DDL script text a package embeds.
func starlarkScript(t *testing.T, path, constName string) string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	require.NoErrorf(t, err, "parse %s", path)

	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) != 1 || len(vs.Values) != 1 || vs.Names[0].Name != constName {
				continue
			}
			lit, ok := vs.Values[0].(*ast.BasicLit)
			require.Truef(t, ok && lit.Kind == token.STRING, "%s in %s is not a string literal", constName, path)
			text, err := strconv.Unquote(lit.Value)
			require.NoErrorf(t, err, "unquote %s in %s", constName, path)
			return text
		}
	}
	t.Fatalf("%s declares no string constant %s — this pin is reading the wrong place", path, constName)
	return ""
}

// starlarkInt returns the value of a top-level `NAME = <int>` assignment in a
// Starlark script.
func starlarkInt(t *testing.T, script, source, name string) int {
	t.Helper()
	m := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(name) + ` = (\d+)$`).FindStringSubmatch(script)
	require.Lenf(t, m, 2, "%s declares no top-level %s — the cap derived from it has lost its basis", source, name)
	v, err := strconv.Atoi(m[1])
	require.NoError(t, err)
	require.Positivef(t, v, "%s in %s must be positive", name, source)
	return v
}

// sweepArms counts the collect_live_sweep call sites in a sweep script — one
// per arm the op drains, since each commit runs the first arm that still
// returns live links and leaves the rest to the next dispatch.
func sweepArms(t *testing.T, script, source string) int {
	t.Helper()
	n := strings.Count(script, "collect_live_sweep(subject_key,")
	require.Positivef(t, n, "%s has no collect_live_sweep(subject_key, …) call sites — "+
		"the arm count its cap is derived from cannot be read", source)
	return n
}

// TestErasureRetryCaps_AreDerivedFromTheSweepsTheyBound proves each cap is the
// number retry_budget.go says it is: Σ_arms(MAX_PAGES × PAGE_LIMIT) /
// SWEEP_LIMIT, read out of the two sweep scripts. A cap below that strands an
// erasure whose fan-out the op could still have reached — the sweeps drain one
// arm per commit and a drained arm simply returns empty and yields to the next,
// so dispatches keep being progress until every arm has exhausted its own
// paging window.
func TestErasureRetryCaps_AreDerivedFromTheSweepsTheyBound(t *testing.T) {
	credential := starlarkScript(t, credentialSweepSource, "unbindIdentityCredentialsDDLScript")
	dedup := starlarkScript(t, dedupSweepSource, "purgeIdentityDedupFootprintDDLScript")

	credentialArms := sweepArms(t, credential, credentialSweepSource)
	require.Equal(t, 2, credentialArms,
		"the credential sweep drains boundTo inbound then outbound; a new arm changes the cap below it")
	dedupArms := sweepArms(t, dedup, dedupSweepSource)
	require.Equal(t, 3, dedupArms,
		"the dedup sweep drains indexes inbound, then duplicateOf outbound, then inbound")

	credentialReach := starlarkInt(t, credential, credentialSweepSource, "MAX_BOUND_TO_PAGES") *
		starlarkInt(t, credential, credentialSweepSource, "BOUND_TO_PAGE_LIMIT")
	dedupReach := starlarkInt(t, dedup, dedupSweepSource, "MAX_LINK_PAGES") *
		starlarkInt(t, dedup, dedupSweepSource, "LINK_PAGE_LIMIT")

	credentialPerCommit := starlarkInt(t, credential, credentialSweepSource, "SWEEP_LIMIT")
	dedupPerCommit := starlarkInt(t, dedup, dedupSweepSource, "SWEEP_LIMIT")

	require.Equal(t, credentialArms*credentialReach/credentialPerCommit, maxCredentialResidueRetries,
		"maxCredentialResidueRetries must be Σ_arms(MAX_BOUND_TO_PAGES × BOUND_TO_PAGE_LIMIT) / SWEEP_LIMIT "+
			"— %d arms × %d links / %d per commit", credentialArms, credentialReach, credentialPerCommit)
	require.Equal(t, dedupArms*dedupReach/dedupPerCommit, maxDedupResidueRetries,
		"maxDedupResidueRetries must be Σ_arms(MAX_LINK_PAGES × LINK_PAGE_LIMIT) / SWEEP_LIMIT "+
			"— %d arms × %d links / %d per commit", dedupArms, dedupReach, dedupPerCommit)

	// The seal has no paging of its own to derive from, so it is pinned to the
	// judgement retry_budget.go states: the widest sibling, so that no gap on
	// this target parks while another could still legitimately be converging.
	require.Equal(t, max(maxCredentialResidueRetries, maxDedupResidueRetries), maxErasureSealRetries,
		"the seal cap must match the widest sweep cap — a shorter fuse parks a live erasure on a transient cause")

	// And every cap has to beat the fallback it exists to replace: a paged
	// sweep suppressed after three dispatches is the stranded erasure this
	// whole declaration is for.
	for name, budget := range map[string]int{
		"maxCredentialResidueRetries": maxCredentialResidueRetries,
		"maxDedupResidueRetries":      maxDedupResidueRetries,
		"maxErasureSealRetries":       maxErasureSealRetries,
	} {
		require.Greaterf(t, budget, engineDefaultDirectOpRetryBudget,
			"%s must exceed the engine's default directOp budget or declaring it buys nothing", name)
	}
}
