package pkgmgr

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/operatinggraph/lattice/internal/lenscolumns"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// preflightLive is the install-time bound on Contract #10 §10.8's weaver-target
// binding: a LensRef names a Lens that exists, whose row columns are derivable,
// and every missing_* column those rows carry is one of the target's `gaps`
// keys.
//
// It is the LIVE half of preflight — the pure gates run first (i.preflight),
// and this one adds the single fact they cannot hold: what an ALREADY-INSTALLED
// lens projects. Both installer entry points call it (Install and Apply), and
// Apply calls it ahead of its DryRun return, so a preview that the real apply
// would refuse reports the refusal rather than a delta nobody can commit.
//
// Per target:
//
//   - an empty LensRef declares no binding and passes through (resolveLensRef's
//     own case; the lint flags it for packages and the artifact validator
//     requires one);
//   - a LensRef naming a lens THIS batch declares is read from that
//     declaration — which is what brings an in-batch PLAIN lens under the rule,
//     since its columns are its RETURN names and only the injected SpecParser
//     can tell anyone what they are;
//   - anything else must be a NanoID naming an installed lens, read from Core
//     KV: absent, tombstoned or of another class is refused, as is a lens whose
//     columns are not derivable, as is an undeclared gap column.
//
// The subset rule — and only the subset rule — is waived for a target whose
// augur policy escalates `unplannable`: such a column is routed to the
// reasoning tier by design. Existence and readability are enforced for every
// target, escalating or not, because escalation says nothing about a lens that
// is not there.
//
// Cost: one KVGetMulti of at most 2 keys per out-of-batch target. A package
// whose targets all bind in-batch lenses (every package in the corpus today)
// makes no live read at all.
func (i *Installer) preflightLive(ctx context.Context, def Definition) error {
	for idx := range def.WeaverTargets {
		t := def.WeaverTargets[idx]
		if t.LensRef == "" {
			continue
		}
		cols, lensName, err := i.resolveTargetLens(ctx, idx, t, def)
		if err != nil {
			return err
		}
		if err := checkTargetLensBinding(idx, t, lensName, cols); err != nil {
			return err
		}
	}
	return nil
}

// resolveTargetLens answers the columns a target's bound lens projects, from
// the batch's own declaration when it holds one and from the installed kernel
// otherwise. Every error it returns is already final for the install: a
// refusal wrapping ErrLensBindingRefused, or a failed read wrapping the
// substrate's error.
func (i *Installer) resolveTargetLens(ctx context.Context, idx int, t WeaverTargetSpec, def Definition) (lenscolumns.Result, string, error) {
	if lens := def.lensByCanonicalName(t.LensRef); lens != nil {
		cols, err := lenscolumns.Projected(lensColumnsSpec(*lens), lensColumnsFromParser(i.SpecParser))
		if err != nil {
			return lenscolumns.Result{}, "", fmt.Errorf("%w: pkgmgr: WeaverTarget[%d] %q: LensRef %q names a lens in this package whose projected columns cannot be derived: %v",
				ErrLensBindingRefused, idx, t.TargetID, t.LensRef, err)
		}
		return cols, lens.CanonicalName, nil
	}
	if !substrate.IsValidNanoID(t.LensRef) {
		// resolveLensRef refuses this same ref when the batch is built; the
		// refusal is raised here so the target's own index and id name it, and
		// so every LensRef reaching the read below is genuinely id-shaped.
		return lenscolumns.Result{}, "", fmt.Errorf("%w: pkgmgr: WeaverTarget[%d] %q: LensRef %q matches no declared lens canonicalName and is not a valid NanoID",
			ErrLensBindingRefused, idx, t.TargetID, t.LensRef)
	}

	cols, found, err := NewCoreKVLensResolver(ctx, i.Conn, i.SpecParser).ResolveLensColumns(t.LensRef)
	switch {
	case err != nil && !errors.Is(err, lenscolumns.ErrUnreadable):
		// A failed read is never an empty one: a gate that cannot read the
		// kernel has not found this binding sound (declaredKeyOccupants holds
		// the same line for its own probe).
		return lenscolumns.Result{}, "", fmt.Errorf("pkgmgr: WeaverTarget[%d] %q: read the lens LensRef %q names: %w",
			idx, t.TargetID, t.LensRef, err)
	case !found:
		return lenscolumns.Result{}, "", fmt.Errorf("%w: pkgmgr: WeaverTarget[%d] %q: LensRef %q names no installed meta.lens (a canonicalName of exactly 20 alphabet characters reads as an id here — declare the lens in this package, or bind the installed lens's id)",
			ErrLensBindingRefused, idx, t.TargetID, t.LensRef)
	case err != nil:
		return lenscolumns.Result{}, "", fmt.Errorf("%w: pkgmgr: WeaverTarget[%d] %q: LensRef %q names a lens whose projected columns cannot be derived: %v",
			ErrLensBindingRefused, idx, t.TargetID, t.LensRef, err)
	}
	return cols, t.LensRef, nil
}

// checkTargetLensBinding applies the two per-column rules over a resolved lens:
// §10.8's subset rule (every missing_* column the rows carry is a gaps key)
// and §10.3's companion pair (a declared inflight_<g> marker needs its
// maxretries_<g> cap).
func checkTargetLensBinding(idx int, t WeaverTargetSpec, lensName string, cols lenscolumns.Result) error {
	if !t.escalatesUnplannable() {
		declared := make(map[string]bool, len(t.Gaps))
		for col := range t.Gaps {
			declared[col] = true
		}
		// EVERY undeclared column, not the first: an author holding three of
		// them should be told all three once, not discover them over three
		// apply attempts. The record-time validator reports them all for the
		// same reason, and the two verdicts must not differ in what they name.
		var refusals []string
		for _, col := range undeclaredGapColumns(cols, declared) {
			refusals = append(refusals, UndeclaredGapColumnRefusal(lensName, col, cols.Columns[col], sortedGapColumns(t.Gaps)))
		}
		if len(refusals) > 0 {
			return fmt.Errorf("%w: pkgmgr: WeaverTarget[%d] %q: %s",
				ErrLensBindingRefused, idx, t.TargetID, strings.Join(refusals, " "))
		}
	}
	for _, col := range sortedGapColumns(t.Gaps) {
		if err := validateGapCompanionPairDeclared(idx, t, col, t.Gaps[col], lensName, cols.Columns); err != nil {
			return fmt.Errorf("%w: %w", ErrLensBindingRefused, err)
		}
	}
	return nil
}
