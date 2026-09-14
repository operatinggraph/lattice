package testutil

import (
	"strings"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
)

// gapRowTemplatePrefix marks a gap enumeration hub template that substitutes
// off the violation row (strategist.go's own rowTemplatePrefix, restated here
// because this package resolves the descriptor rather than the engine's
// runtime plan and so does not import the weaver package's unexported
// constant).
const gapRowTemplatePrefix = "row."

// DeclaredGapEnumerations resolves the enumerations a Weaver target's
// GapActionSpec declares for one gap column into the concrete hints a
// hand-built envelope carries, substituting each hub against the dispatching
// actor and the violation row.
//
// WHY A TEST HELPER RESOLVES THIS RATHER THAN EACH FIXTURE STATING IT — the
// same reasoning DeclaredEnumerations gives for the OpDispatchSpec surface
// applies here verbatim, one authoring surface over. The drift guard reads the
// ENVELOPE (read_drift_guard.go: env.ContextHint.Enumerations), never the
// spec, so a target's GapActionSpec can declare `{actor} holdsRole out` and
// every Go test that hand-builds its own envelope still submits nothing. A
// fixture that hardcodes the hint instead agrees with the spec only by
// coincidence: deleting the spec's declaration leaves every test green and
// silently returns the op to undeclared-walk debt, with its baseline row
// already retired. Resolving the hint FROM the spec makes the fixture a
// revert-proof of the declaration — the same reason production resolves it
// from the same field, one hop later (the Weaver's own buildPlan, off the
// target the CDC registry loaded).
//
// The substitution is the same narrow vocabulary D1 admitted for the
// OpDispatchSpec surface, extended by the one arm a gap's row carries and an
// op dispatch's submitting client never did: `{actor}` becomes actorKey;
// `row.<column>` substitutes that column's own value out of row (a non-string
// or absent column is unresolvable — a null/absent row column is a data error
// at dispatch, never a hint a fixture should silently invent); a hub with
// neither shape is already a literal key and passes through unchanged.
// Anything else — a `{payload.<field>}` template, or a shape this surface's
// grammar does not admit — is left to the caller, and reported by
// DeclaredGapEnumerationsWithSkips' skippedHubs so a fixture cannot silently
// drop one and retire a baseline row against a declaration it never sent.
func DeclaredGapEnumerations(targetID, gapColumn, actorKey string, row map[string]any, targetSets ...[]pkgmgr.WeaverTargetSpec) []processor.EnumerationHint {
	hints, _ := DeclaredGapEnumerationsWithSkips(targetID, gapColumn, actorKey, row, targetSets...)
	return hints
}

// DeclaredGapEnumerationsWithSkips is DeclaredGapEnumerations plus the hubs it
// could not resolve, so a caller that dispatches a gap with a
// payload-templated or otherwise-unresolvable hub can see it must supply that
// one itself rather than assume the empty result meant "this gap declares
// nothing".
func DeclaredGapEnumerationsWithSkips(targetID, gapColumn, actorKey string, row map[string]any, targetSets ...[]pkgmgr.WeaverTargetSpec) (hints []processor.EnumerationHint, skippedHubs []string) {
	for _, t := range flattenTargets(targetSets) {
		if t.TargetID != targetID {
			continue
		}
		gap, ok := t.Gaps[gapColumn]
		if !ok {
			continue
		}
		for _, e := range gap.Enumerations {
			hub, ok := resolveGapHubTemplate(e.Hub, actorKey, row)
			if !ok {
				skippedHubs = append(skippedHubs, e.Hub)
				continue
			}
			hints = append(hints, processor.EnumerationHint{
				Hub:       hub,
				Relation:  e.Relation,
				Direction: e.Direction,
			})
		}
	}
	return hints, skippedHubs
}

// flattenTargets concatenates the caller's target sets. A dispatcher may
// declare a target whose spec is owned by a DIFFERENT package than the one
// under test, mirroring flattenMetas' reason for the OpDispatchSpec surface,
// so the caller names every package whose targets it dispatches.
func flattenTargets(sets [][]pkgmgr.WeaverTargetSpec) []pkgmgr.WeaverTargetSpec {
	if len(sets) == 1 {
		return sets[0]
	}
	var all []pkgmgr.WeaverTargetSpec
	for _, s := range sets {
		all = append(all, s...)
	}
	return all
}

// resolveGapHubTemplate substitutes the two hub templates a gap's
// enumerations grammar admits: `{actor}`, and `row.<column>` off the violation
// row this fixture is standing in for. A hub carrying neither shape is a
// literal key and is already concrete.
func resolveGapHubTemplate(hub, actorKey string, row map[string]any) (string, bool) {
	if hub == "{actor}" {
		return actorKey, true
	}
	if col, ok := strings.CutPrefix(hub, gapRowTemplatePrefix); ok {
		v, present := row[col]
		if !present {
			return "", false
		}
		s, isString := v.(string)
		if !isString || s == "" {
			return "", false
		}
		return s, true
	}
	return hub, true
}
