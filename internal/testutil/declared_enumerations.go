package testutil

import (
	"strings"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
)

// DeclaredEnumerations resolves the enumerations an operation's own
// OpDispatchSpec declares into the concrete hints a submitted envelope carries,
// substituting the hub templates against the submitting actor.
//
// WHY A TEST HELPER RESOLVES THIS RATHER THAN EACH FIXTURE STATING IT. The
// drift guard reads the ENVELOPE (read_drift_guard.go: env.ContextHint
// .Enumerations), never the spec, so a package's op can declare
// `{actor} holdsRole out` on its OpDispatchSpec and every Go test that
// hand-builds its own envelope still submits nothing. A fixture that hardcodes
// the hint instead agrees with the spec only by coincidence: deleting the spec's
// declaration leaves every test green and silently returns the op to
// undeclared-walk debt, with its baseline row already retired. Resolving the
// hint FROM the spec makes the fixture a revert-proof of the declaration —
// the same reason production resolves it from the same field, one hop later
// (opCatalog lens -> the descriptor client's envelope build).
//
// The substitution is deliberately the narrow one D1 admitted for this surface:
// `{actor}` becomes the actor key, and a literal hub passes through. A
// `{payload.<field>}` hub needs the submitted payload, which a caller holds and
// this helper does not; such a hub is left to the caller to resolve and is
// reported by SkippedHubs so a fixture cannot silently drop it and retire a row
// against a declaration it never sent.
func DeclaredEnumerations(op, actorKey string, metaSets ...[]pkgmgr.OpMetaSpec) []processor.EnumerationHint {
	hints, _ := DeclaredEnumerationsWithSkips(op, actorKey, metaSets...)
	return hints
}

// DeclaredEnumerationsWithSkips is DeclaredEnumerations plus the hubs it could
// not resolve, so a caller that dispatches an op with a payload-templated hub
// can see it must supply that one itself rather than assume the empty result
// meant "this op declares nothing".
func DeclaredEnumerationsWithSkips(op, actorKey string, metaSets ...[]pkgmgr.OpMetaSpec) (hints []processor.EnumerationHint, skippedHubs []string) {
	for _, m := range flattenMetas(metaSets) {
		if m.OperationType != op || m.Dispatch == nil {
			continue
		}
		for _, e := range m.Dispatch.Enumerations {
			hub, ok := resolveHubTemplate(e.Hub, actorKey)
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

// flattenMetas concatenates the caller's meta sets. A dispatcher may submit an
// op whose meta is owned by a DIFFERENT package than the one under test — a
// clinic-reminders fixture books a clinic-domain CreateAppointment — and the
// declaration must come from the owning package either way, so the caller names
// every package whose ops it dispatches.
func flattenMetas(sets [][]pkgmgr.OpMetaSpec) []pkgmgr.OpMetaSpec {
	if len(sets) == 1 {
		return sets[0]
	}
	var all []pkgmgr.OpMetaSpec
	for _, s := range sets {
		all = append(all, s...)
	}
	return all
}

// resolveHubTemplate substitutes the one hub template this helper can resolve
// without the payload. A hub carrying no placeholder is a literal key and is
// already concrete.
func resolveHubTemplate(hub, actorKey string) (string, bool) {
	if hub == "{actor}" {
		return actorKey, true
	}
	if strings.ContainsAny(hub, "{}") {
		return "", false
	}
	return hub, true
}
