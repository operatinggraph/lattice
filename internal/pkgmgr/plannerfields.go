package pkgmgr

import (
	"fmt"

	"github.com/operatinggraph/lattice/internal/guardgrammar"
	"github.com/operatinggraph/lattice/internal/weaver/planner"
)

// Planner-extension target modes (Contract #10 §10.8 Planner extension).
// Re-stated here (not imported) so the installer validates a target's Mode
// without depending on the engine, mirroring the engine's own targetModeShadow
// / targetModePlanned constants (internal/weaver/registry.go).
const (
	targetModeShadow  = "shadow"
	targetModePlanned = "planned"
)

// validateTargetMode rejects an unrecognized WeaverTargetSpec.Mode, mirroring
// the engine's validateTarget mode check.
func validateTargetMode(targetIdx int, targetID, mode string) error {
	if mode != "" && mode != targetModeShadow && mode != targetModePlanned {
		return fmt.Errorf("pkgmgr: WeaverTarget[%d] %q: Mode %q is not a known planner mode (%s | %s)",
			targetIdx, targetID, mode, targetModeShadow, targetModePlanned)
	}
	return nil
}

// validateGapPlannerFields runs the §10.8 Planner-extension install-time
// validations on one gap's optional Goal/GoalColumns/Actions (the R1 goal-
// authoring surface — loftspace-lease-renewal-goal-authored-target-design),
// mirroring the engine's validateGapPlannerFields/parseGoalColumns/
// validateActionsCatalog (internal/weaver/registry.go) so a package that would
// fail the engine's own CDC-load validation fails loudly at install instead.
// Candidates (Fire 5) authoring is a separate, not-yet-built pkgmgr surface —
// out of scope here.
func validateGapPlannerFields(targetIdx int, targetID, col string, ga GapActionSpec) error {
	var goalGuard *guardgrammar.Guard
	if len(ga.Goal) > 0 {
		g, err := guardgrammar.Parse(ga.Goal)
		if err != nil {
			return fmt.Errorf("pkgmgr: WeaverTarget[%d] %q: gaps key %q: goal: %w", targetIdx, targetID, col, err)
		}
		goalGuard = g
	}
	var goalColumnPaths map[string]guardgrammar.Path
	if len(ga.GoalColumns) > 0 {
		paths, err := parseGoalColumnsSpec(targetIdx, targetID, col, ga.GoalColumns, goalGuard)
		if err != nil {
			return err
		}
		goalColumnPaths = paths
	}
	return validateActionsCatalogSpec(targetIdx, targetID, col, ga, goalColumnPaths)
}

// parseGoalColumnsSpec install-validates one gap's GoalColumns, mirroring the
// engine's parseGoalColumns: each value must parse as a well-formed §10.5
// guard-grammar path AND be aspect-qualified (a root-shaped entry is redundant
// — rowState already addresses every column at subject.data.<column> by
// default); values must be unique (two columns mapping to the same path would
// make rowState's result depend on Go's map-iteration order); every parsed
// path must be referenced somewhere in goal (an entry goal never asks about is
// exactly as inert as a typo'd column name).
func parseGoalColumnsSpec(targetIdx int, targetID, col string, cols map[string]string, goal *guardgrammar.Guard) (map[string]guardgrammar.Path, error) {
	if goal == nil {
		return nil, fmt.Errorf("pkgmgr: WeaverTarget[%d] %q: gaps key %q: goalColumns is set but goal is empty — nothing references the declared aspect paths",
			targetIdx, targetID, col)
	}
	paths := make(map[string]guardgrammar.Path, len(cols))
	seen := make(map[guardgrammar.Path]string, len(cols))
	for column, raw := range cols {
		p, err := guardgrammar.ParsePath(raw)
		if err != nil {
			return nil, fmt.Errorf("pkgmgr: WeaverTarget[%d] %q: gaps key %q: goalColumns[%q]: %w", targetIdx, targetID, col, column, err)
		}
		if p.Aspect == "" {
			return nil, fmt.Errorf("pkgmgr: WeaverTarget[%d] %q: gaps key %q: goalColumns[%q]: path %q is root-shaped (subject.data.<field>) — goalColumns is only for aspect-qualified paths; a root column already addresses itself by default",
				targetIdx, targetID, col, column, raw)
		}
		if other, dup := seen[p]; dup {
			return nil, fmt.Errorf("pkgmgr: WeaverTarget[%d] %q: gaps key %q: goalColumns[%q] and [%q] both map to path %q — rowState's result would depend on Go's map-iteration order over the row",
				targetIdx, targetID, col, column, other, raw)
		}
		seen[p] = column
		paths[column] = p
	}
	referenced := guardPaths(goal)
	for column, p := range paths {
		if !referenced[p] {
			return nil, fmt.Errorf("pkgmgr: WeaverTarget[%d] %q: gaps key %q: goalColumns[%q] (%s) is never referenced by goal — remove it or fix the path",
				targetIdx, targetID, col, column, formatGuardPath(p))
		}
	}
	return paths, nil
}

// validateActionsCatalogSpec install-validates one gap's optional Actions
// catalog, mirroring the engine's validateActionsCatalog: required alongside
// Goal in both directions (a goal with no catalog can never synthesize a
// plan; a catalog with no goal has nothing to synthesize toward); each entry's
// Ref must be unique within the gap; every Pre/Effects path must be
// row-reachable (root-shaped, or aspect-shaped and bridged by this gap's
// GoalColumns) so no entry is permanently ineligible or un-satisfiable; each
// Effects atom must be a concrete assertion (planner.ApplyEffects rejects
// anyOf/not).
func validateActionsCatalogSpec(targetIdx int, targetID, col string, ga GapActionSpec, goalColumnPaths map[string]guardgrammar.Path) error {
	if len(ga.Actions) == 0 {
		if len(ga.Goal) > 0 {
			return fmt.Errorf("pkgmgr: WeaverTarget[%d] %q: gaps key %q: goal is set but actions is empty — synthesis has no catalog to plan over",
				targetIdx, targetID, col)
		}
		return nil
	}
	if len(ga.Goal) == 0 {
		return fmt.Errorf("pkgmgr: WeaverTarget[%d] %q: gaps key %q: actions is set but goal is empty — the catalog has no synthesis target",
			targetIdx, targetID, col)
	}
	refs := make(map[string]bool, len(ga.Actions))
	for i, entry := range ga.Actions {
		if entry.Ref == "" {
			return fmt.Errorf("pkgmgr: WeaverTarget[%d] %q: gaps key %q: actions[%d] has no ref", targetIdx, targetID, col, i)
		}
		if refs[entry.Ref] {
			return fmt.Errorf("pkgmgr: WeaverTarget[%d] %q: gaps key %q: actions[%d]: ref %q is declared more than once in this gap's catalog",
				targetIdx, targetID, col, i, entry.Ref)
		}
		refs[entry.Ref] = true
		if entry.Action == "" {
			return fmt.Errorf("pkgmgr: WeaverTarget[%d] %q: gaps key %q: actions[%d] (ref %q) has no action", targetIdx, targetID, col, i, entry.Ref)
		}
		if entry.Cost < 0 {
			return fmt.Errorf("pkgmgr: WeaverTarget[%d] %q: gaps key %q: actions[%d] (ref %q): cost %d must be >= 0",
				targetIdx, targetID, col, i, entry.Ref, entry.Cost)
		}
		if len(entry.Pre) > 0 {
			g, err := guardgrammar.Parse(entry.Pre)
			if err != nil {
				return fmt.Errorf("pkgmgr: WeaverTarget[%d] %q: gaps key %q: actions[%d] (ref %q).pre: %w", targetIdx, targetID, col, i, entry.Ref, err)
			}
			if err := requireRowReachable(targetIdx, targetID, col, fmt.Sprintf("actions[%s].pre", entry.Ref), g, goalColumnPaths); err != nil {
				return err
			}
		}
		if len(entry.Effects) == 0 {
			return fmt.Errorf("pkgmgr: WeaverTarget[%d] %q: gaps key %q: actions[%d] (ref %q) has no effects — an entry with nothing it entails can never advance a plan",
				targetIdx, targetID, col, i, entry.Ref)
		}
		for j, raw := range entry.Effects {
			g, err := guardgrammar.Parse(raw)
			if err != nil {
				return fmt.Errorf("pkgmgr: WeaverTarget[%d] %q: gaps key %q: actions[%d] (ref %q).effects[%d]: %w",
					targetIdx, targetID, col, i, entry.Ref, j, err)
			}
			if _, err := planner.ApplyEffects(planner.State{}, []*guardgrammar.Guard{g}); err != nil {
				return fmt.Errorf("pkgmgr: WeaverTarget[%d] %q: gaps key %q: actions[%d] (ref %q).effects[%d]: %w",
					targetIdx, targetID, col, i, entry.Ref, j, err)
			}
			if err := requireRowReachable(targetIdx, targetID, col, fmt.Sprintf("actions[%s].effects[%d]", entry.Ref, j), g, goalColumnPaths); err != nil {
				return err
			}
		}
	}
	return nil
}

// requireRowReachable rejects a guard tree that addresses a path no live
// planner.State can ever carry: a root path (subject.data.<field>) is always
// reachable (rowState's default mapping); an aspect path is reachable only
// when it is one of this gap's own goalColumnPaths values (the bridge
// GoalColumns installs) — an aspect path from a different gap's bridge, or no
// bridge at all, can never appear in this gap's State.
func requireRowReachable(targetIdx int, targetID, col, field string, g *guardgrammar.Guard, goalColumnPaths map[string]guardgrammar.Path) error {
	for p := range guardPaths(g) {
		if p.Aspect == "" {
			continue
		}
		reachable := false
		for _, bridged := range goalColumnPaths {
			if bridged == p {
				reachable = true
				break
			}
		}
		if !reachable {
			return fmt.Errorf("pkgmgr: WeaverTarget[%d] %q: gaps key %q: %s: path %q is aspect-shaped but not bridged by this gap's goalColumns — "+
				"a row-derived State can never carry it, so this entry could never see it as (un)met",
				targetIdx, targetID, col, field, formatGuardPath(p))
		}
	}
	return nil
}

// guardPaths collects every subject-path an install-validated guard tree
// references — every present/absent/equals atom's Path, recursing through
// allOf/anyOf/not children — deduplicated. Install-time use only.
func guardPaths(g *guardgrammar.Guard) map[guardgrammar.Path]bool {
	paths := make(map[guardgrammar.Path]bool)
	collectGuardPaths(g, paths)
	return paths
}

func collectGuardPaths(g *guardgrammar.Guard, out map[guardgrammar.Path]bool) {
	if g == nil {
		return
	}
	switch g.Kind {
	case guardgrammar.KindPresent, guardgrammar.KindAbsent, guardgrammar.KindEquals:
		out[g.Path] = true
	case guardgrammar.KindAllOf, guardgrammar.KindAnyOf, guardgrammar.KindNot:
		for _, c := range g.Children {
			collectGuardPaths(c, out)
		}
	}
}

// formatGuardPath renders a guardgrammar.Path back into its §10.5 subject-path
// string, for error messages only.
func formatGuardPath(p guardgrammar.Path) string {
	if p.Aspect == "" {
		return "subject.data." + p.Field
	}
	return "subject." + p.Aspect + ".data." + p.Field
}
