package bootstrap

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// currentCapabilityLens pairs one kernel capability lens's definition with
// this deployment's own id for it — positionally, the same pairing
// primordial.go's seed order makes (primordial.go:642,655,671,690) — so
// StrandedCapabilityLenses derives its reserved-name set, its id-exclusion
// set, AND its cypher-divergence check from ONE list, never a second,
// hand-copied one. A future rename or cypher edit of any of the four is
// picked up automatically instead of silently muting the detector (the
// asymmetry a cold review caught: the id exclusion set was already derived
// this way, the name set was not).
type currentCapabilityLens struct {
	Definition LensDefinition
	ID         string
}

func currentCapabilityLenses() [4]currentCapabilityLens {
	return [4]currentCapabilityLens{
		{CapabilityLensDefinition(), CapabilityLensID},
		{CapabilityReadLensDefinition(), CapabilityReadLensID},
		{CapabilityReadGrantsLensDefinition(), CapabilityReadGrantsLensID},
		{CapabilityReadWildcardGrantsLensDefinition(), CapabilityReadWildcardGrantsLensID},
	}
}

// StrandedCapabilityLens names one live vtx.meta.* vertex of class meta.lens
// whose canonicalName is one of the four kernel capability-lens names and
// whose id this deployment's primordial table does not name.
//
// Unlike StrandedOperatorEpoch, this residue is never destroyed by this scan
// or any Fire 2 remedy: all four lenses seed with data.protected=true
// (primordial.go:643,656,672,691), and rejectProtectedMutations
// (internal/processor/step8_commit.go:721-742) refuses any update/tombstone
// mutation whose root carries that flag — including a tombstone of the
// `.spec`/`.cypherRule` aspects, since protectedRootKey maps them back to the
// same root. The Starlark TombstoneMetaVertex op carries the identical
// is_protected check (meta_ddl.go:114-129,390-391) for the same reason.
// Retiring one needs a guard exemption Andrew has not granted for this class,
// exactly as he declined one for the stranded role vertex itself
// (primordial-epoch-stranded-authority-design.md §7 item 4) — so this type
// exists to REPORT the residue, not to drive a tombstone.
//
// CypherDiverges is what actually decides whether this residue is inert. A
// stranded lens carries the cypher rule the BINARY THAT SEEDED IT wrote, at a
// fixed point in time — kernel reconcile cannot reach a prior epoch's meta
// ids, so nothing ever updates it. The three consumer lenses this design
// traces (CapabilityLens, CapabilityReadWildcardGrantsLens,
// rbac-domain's capabilityRolesSpec) reach a role's authority only through a
// live holdsRole/grantedBy edge into it ONLY as of the CURRENT cypher; commit
// c9a8031265b3f0fbc81c5af84922132703ec70f4 (2026-07-02) rewrote exactly these
// two lenses from matching `identity.data.protected == true` directly to
// matching via holdsRole->operator topology. A stranded lens seeded by a
// binary that predates that rewrite reads no holdsRole/grantedBy edge at all
// and is not touched by PlanStrandedEpochRetirement — it still projects
// installation-wide root to every `data.protected` identity, the exact
// escalation the rewrite closed. This is not a hypothetical future risk: it
// is what already happened once to this codebase's own kernel. A stranded
// lens whose stored cypher differs from the current definition's is therefore
// ranked a failure; one whose cypher is byte-identical (after the same
// whitespace trim the seeder itself applies) computes the same result set the
// current epoch's own lens does, and is genuinely inert.
type StrandedCapabilityLens struct {
	// LensKey is the lens's vertex key, vtx.meta.<id>.
	LensKey string
	// CanonicalName is which of the four kernel lenses this is.
	CanonicalName string
	// CypherDiverges is true when the stranded lens's stored cypher rule does
	// not match the current definition's — see the type doc for why this is
	// the actual severity discriminator, not mere staleness.
	CypherDiverges bool
	// CypherUnreadable is true when the stranded lens's own cypherRule aspect
	// could not be read or parsed. Ranked the same as CypherDiverges — an
	// unread cypher is a lower bound on danger, never an all-clear (mirrors
	// StrandedOperatorEpoch.UnreadableEdges's posture).
	CypherUnreadable bool
}

// StrandedLensSeverity ranks one finding by consequence, mirroring
// StrandedSeverity's role-plane shape.
type StrandedLensSeverity int

const (
	// StrandedLensSeverityInert is a residue lens whose stored cypher is
	// confirmed identical to the current definition's — redundant compute,
	// not a wider grant.
	StrandedLensSeverityInert StrandedLensSeverity = iota
	// StrandedLensSeverityDiverged is a residue lens whose stored cypher
	// differs from (or could not be confirmed to match) the current
	// definition's — a live, potentially broader rule with no remedy this
	// gate can apply.
	StrandedLensSeverityDiverged
)

// Severity ranks the finding — see the type doc for why cypher divergence,
// not mere existence, is what makes a stranded lens dangerous.
func (l StrandedCapabilityLens) Severity() StrandedLensSeverity {
	if l.CypherDiverges || l.CypherUnreadable {
		return StrandedLensSeverityDiverged
	}
	return StrandedLensSeverityInert
}

// Report renders the finding as one line for a gate's output.
func (l StrandedCapabilityLens) Report() string {
	switch {
	case l.CypherUnreadable:
		return fmt.Sprintf(
			"STRANDED CAPABILITY LENS: %s is named %q, one of the kernel's four capability lenses, but is not"+
				" this deployment's %s lens, AND its stored cypher rule could not be read — this is a LOWER BOUND,"+
				" not an all-clear. Cannot be tombstoned without a guard exemption on rejectProtectedMutations"+
				" (data.protected=true) — deliberately left as residue.",
			l.LensKey, l.CanonicalName, l.CanonicalName)
	case l.CypherDiverges:
		return fmt.Sprintf(
			"STRANDED CAPABILITY LENS: %s is named %q, one of the kernel's four capability lenses, but is not"+
				" this deployment's %s lens, AND its stored cypher rule DIFFERS from the current %s lens's —"+
				" it is still live and may project a broader rule than the one this deployment runs today"+
				" (the exact shape of the 2026-07-02 holdsRole->operator re-convergence, c9a80312). Cannot be"+
				" tombstoned without a guard exemption on rejectProtectedMutations (data.protected=true) —"+
				" deliberately left as residue.",
			l.LensKey, l.CanonicalName, l.CanonicalName, l.CanonicalName)
	default:
		return fmt.Sprintf(
			"STRANDED CAPABILITY LENS: %s is named %q, one of the kernel's four capability lenses, but is not"+
				" this deployment's %s lens. Confirmed inert: its stored cypher rule is identical to the current"+
				" %s lens's, so — once the companion stranded-operator-role finding for the same rotation is"+
				" remedied — it computes the same result the current epoch's own lens computes. Cannot be"+
				" tombstoned without a guard exemption on rejectProtectedMutations (data.protected=true) —"+
				" deliberately left as residue. Check for this BEFORE narrowing any current capability lens's"+
				" cypher rule: a surviving stranded twin, even a currently-identical one, would keep projecting"+
				" the OLD rule forever once the current one changes.",
			l.LensKey, l.CanonicalName, l.CanonicalName, l.CanonicalName)
	}
}

// StrandedCapabilityLenses scans Core KV for live vtx.meta.* vertices of
// class meta.lens named by one of the four kernel capability-lens
// canonicalNames whose id this deployment's primordial table does not name.
// It is StrandedOperatorEpochs's lens-plane sibling — same rotation, same
// create-only seed path, same reason kernel reconcile cannot reach it (a
// prior epoch's meta ids carry the prior epoch's provenance, and
// scanKernelOrphans keys on the CURRENT bootstrap op).
//
// Deliberately NOT wired into planReconcile/ReconcilePrimordial's boot path:
// its listing (vtx.meta.*.canonicalName) enumerates the WHOLE meta-root
// population — DDLs, lenses, op-metas, hundreds and growing with every
// installed package — not the tens-sized vtx.role.* population
// StrandedOperatorEpochs bounds itself to. docs/components/bootstrap.md's
// dossier entry 2 already names the cost of paying a per-key read over that
// population on every boot; this scan runs only where ReadKernelReport's
// callers (scripts/verify-kernel.go, `bootstrap verify`) already accept a
// slower, occasional check.
//
// Every candidate that clears the predicate is reported; nothing here writes.
// Refuses with ErrPrimordialIDsUnloaded before touching the graph if any of
// the four current-epoch lens ids is unloaded.
func StrandedCapabilityLenses(ctx context.Context, kv jetstream.KeyValue) ([]StrandedCapabilityLens, error) {
	current := currentCapabilityLenses()
	exclude := make(map[string]bool, len(current))
	names := make(map[string]bool, len(current))
	cyphers := make(map[string]string, len(current))
	for _, l := range current {
		if l.ID == "" {
			return nil, fmt.Errorf("%w: capability lens ids", ErrPrimordialIDsUnloaded)
		}
		exclude[l.ID] = true
		names[l.Definition.CanonicalName] = true
		cyphers[l.Definition.CanonicalName] = strings.TrimSpace(l.Definition.CypherRule)
	}

	var out []StrandedCapabilityLens
	err := walkDistinctKeys(ctx, kv, "vtx.meta.*.canonicalName", func(page []string) error {
		for _, aspectKey := range page {
			lensKey, _, lensID, _, ok := substrate.ParseAspectKey(aspectKey)
			if !ok || exclude[lensID] {
				continue
			}
			aspect, state, err := readDocument(ctx, kv, aspectKey)
			if err != nil {
				return err
			}
			if state != docLive {
				continue
			}
			name, _ := aspect.Data["value"].(string)
			if !names[name] {
				continue
			}
			root, state, err := readDocument(ctx, kv, lensKey)
			if err != nil {
				return err
			}
			if state != docLive || root.Class != "meta.lens" {
				continue
			}

			cypherKey := substrate.AspectKey(lensKey, "cypherRule")
			cypherDoc, cypherState, err := readDocument(ctx, kv, cypherKey)
			if err != nil {
				return err
			}
			finding := StrandedCapabilityLens{LensKey: lensKey, CanonicalName: name}
			switch {
			case cypherState != docLive:
				finding.CypherUnreadable = true
			default:
				rule, ok := cypherDoc.Data["rule"].(string)
				if !ok {
					finding.CypherUnreadable = true
				} else {
					finding.CypherDiverges = strings.TrimSpace(rule) != cyphers[name]
				}
			}
			out = append(out, finding)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(out, func(i, j int) bool { return out[i].LensKey < out[j].LensKey })
	return out, nil
}
