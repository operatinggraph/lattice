package pkgmgr

import (
	"fmt"
	"slices"
	"strings"
)

// retirementDecl is one Definition.RetiredSecureColumns entry resolved against
// the keyspace it excuses.
//
// keys holds the lens `.spec` key(s) the entry's Lens resolves to and columns
// the selector(s) it matches. Both are plural for one reason: Lens and Column
// are validated after trimming but a lens NanoID is salted by the RAW
// canonicalName the installer minted it from, so an entry carrying stray
// whitespace would validate and then match nothing — and the refusal would
// then instruct the author to add a declaration visually identical to the one
// already in their file. Registering both readings of the author's own string
// closes that dead end without letting an entry reach a lens it does not name:
// LensID is a hash, and the trimmed and untrimmed spellings are the only two
// candidates for what was meant.
type retirementDecl struct {
	lens    string
	column  string
	keys    []string
	columns []string
	matched bool
}

// validateRetiredSecureColumns is the pure, install-time half of the
// Secure-Lens retirement guard: every Definition.RetiredSecureColumns entry
// must name a Lens, carry a Note, and be unique in (Lens, Column).
//
// It runs from validateAll, so a malformed declaration is refused by Install
// as well as by Upgrade/Apply. That matters because the enforcement half only
// ever sees the entries an upgrade's actual erasures reach: a package could
// otherwise ship a noteless retirement, install clean, and surface it at some
// later version's upgrade — on a run whose author may not be the one who wrote
// it. The enforcer repeats these checks rather than trusting this one; it is
// the fail-closed floor for any path that reaches it with an unvalidated
// Definition.
func (def Definition) validateRetiredSecureColumns() error {
	seen := make(map[string]struct{}, len(def.RetiredSecureColumns))
	for i, decl := range def.RetiredSecureColumns {
		lens := strings.TrimSpace(decl.Lens)
		column := strings.TrimSpace(decl.Column)
		if lens == "" {
			return fmt.Errorf(
				"pkgmgr: RetiredSecureColumns[%d] names no Lens — set it to the lens's canonicalName as this package "+
					"previously declared it (for a rename, the OLD name: the lens NanoID is salted by the name, so the "+
					"old key is the one losing its secure columns)", i)
		}
		if strings.TrimSpace(decl.Note) == "" {
			return fmt.Errorf(
				"pkgmgr: RetiredSecureColumns[%d] (lens %q, column %s) has an empty Note — a retirement attests that the "+
					"ciphertext those holder types encrypted is safe to stop tracking, and the Note is the only record of "+
					"who decided that and why; state it",
				i, lens, retiredColumnLabel(column))
		}
		id := lens + "\x00" + column
		if _, dup := seen[id]; dup {
			return fmt.Errorf(
				"pkgmgr: RetiredSecureColumns declares lens %q, column %s twice — one erasure, one attestation; "+
					"two Notes for the same retirement leave no answer to which one is the reason",
				lens, retiredColumnLabel(column))
		}
		seen[id] = struct{}{}
	}
	return nil
}

// enforceSecureColumnRetirement is the Secure-Lens key-custody retirement
// guard (retention-class-key-custody-design.md §30): a package version that
// erases a committed `targetConfig.secureColumns` entry must declare the
// retirement in Definition.RetiredSecureColumns, per lens and per column.
//
// The two erasures it covers are the two the narrowing widen cannot reach
// (§29.6's named non-goals). A column dropped from a lens the package still
// declares vanishes from the persisted spec — widenSecureColumnsForUpdate
// unions holder types only for columns both specs name. A lens removed or
// renamed has its whole spec tombstoned, and since a lens NanoID is salted by
// the canonicalName, a rename is a removal of the old key plus a create of a
// new one. Both land the same place: Refractor's destruction-readiness oracle
// (internal/refractor/health/registry_probe.go) answers "which lenses hold
// ciphertext for this holder type?" from the CURRENT spec alone, reading an
// absent secure column — and a tombstoned lens — as a genuine no. Meanwhile
// no package diff has ever touched the target store, so the ciphertext those
// columns wrote is still sitting there and the platform now attests
// destruction coverage over rows it can no longer see.
//
// The check is unconditional, exactly as the op-meta retirement guard's is: an
// environment whose target table happens to be empty today must not be able to
// wave the declaration through and leave prod to discover it. It is also pure
// and side-effect-free, which is why the callers run it before their
// empty-delta and dry-run early returns rather than after — an author whose
// declaration is missing or malformed learns it from a preview, not from the
// one apply that also had a real mutation in it.
//
// Matching: a declaration resolves to the key it excuses through
// LensID(def.Name, decl.Lens) — the same derivation the installer used to mint
// that key — never by string-comparing a canonicalName against a key. The
// selector must then match EXACTLY. A named column excuses that column being
// dropped from a surviving lens; Column:"" excuses only the whole spec going
// at once. Neither covers the other, so a blanket entry left behind after the
// removal it described cannot silently excuse the next author's per-column
// erasure under a Note written about something else.
//
// Returns the number of secure COLUMNS whose custody record a declaration
// excused (the same unit SecureColumnsWidened counts, so the two are
// comparable) and the labels of the declarations that matched nothing — a
// retirement that has outlived its edit reads as load-bearing until something
// says otherwise.
func enforceSecureColumnRetirement(def Definition, dropped []droppedSecureColumn) (int, []string, error) {
	decls := make([]retirementDecl, 0, len(def.RetiredSecureColumns))
	for i, raw := range def.RetiredSecureColumns {
		lens := strings.TrimSpace(raw.Lens)
		column := strings.TrimSpace(raw.Column)
		if lens == "" {
			return 0, nil, fmt.Errorf(
				"pkgmgr: RetiredSecureColumns[%d] names no Lens — set it to the lens's canonicalName as this package "+
					"previously declared it (for a rename, the OLD name)", i)
		}
		if strings.TrimSpace(raw.Note) == "" {
			return 0, nil, fmt.Errorf(
				"pkgmgr: RetiredSecureColumns[%d] (lens %q, column %s) has an empty Note — a retirement attests that the "+
					"ciphertext those holder types encrypted is safe to stop tracking, and the Note is the only record of "+
					"who decided that and why; state it",
				i, lens, retiredColumnLabel(column))
		}
		decls = append(decls, retirementDecl{
			lens:    lens,
			column:  column,
			keys:    spellings(metaVertexPrefix+LensID(def.Name, lens)+".spec", metaVertexPrefix+LensID(def.Name, raw.Lens)+".spec"),
			columns: spellings(column, raw.Column),
		})
	}

	retired := 0
	for _, drop := range dropped {
		excused := false
		for idx := range decls {
			if !decls[idx].covers(drop) {
				continue
			}
			decls[idx].matched = true
			excused = true
			break
		}
		if !excused {
			return 0, nil, undeclaredSecureColumnDropError(drop, blanketDeclaredFor(decls, drop.Key))
		}
		// The unit is columns, not erasure events: a removed lens that took
		// twenty secure columns with it retired twenty custody records, and
		// reporting that as 1 would make the number incomparable with
		// SecureColumnsWidened beside it. An erasure whose columns are all
		// unnameable still erased something, so it counts once.
		if n := len(drop.Erased); n > 0 {
			retired += n
		} else {
			retired++
		}
	}

	var unused []string
	for _, decl := range decls {
		if !decl.matched {
			unused = append(unused, decl.lens+" / "+retiredColumnLabel(decl.column))
		}
	}
	return retired, unused, nil
}

// covers reports whether this declaration excuses the given erasure: the same
// lens spec key, and a selector that matches exactly. "" attests to a whole
// spec going at once and a column name attests to that column; neither stands
// in for the other.
func (d retirementDecl) covers(drop droppedSecureColumn) bool {
	if !slices.Contains(d.keys, drop.Key) {
		return false
	}
	return slices.Contains(d.columns, drop.Column)
}

// blanketDeclaredFor reports whether any declaration retires the WHOLE spec at
// specKey. A refusal uses it to tell an author who has such an entry why it did
// not apply, instead of leaving them to conclude the guard is broken.
func blanketDeclaredFor(decls []retirementDecl, specKey string) bool {
	for _, d := range decls {
		if slices.Contains(d.keys, specKey) && slices.Contains(d.columns, "") {
			return true
		}
	}
	return false
}

// spellings returns the distinct non-duplicate readings of one authored value.
func spellings(trimmed, raw string) []string {
	if trimmed == raw {
		return []string{trimmed}
	}
	return []string{trimmed, raw}
}

// undeclaredSecureColumnDropError renders the refusal. It names the erasure
// precisely — the lens, its spec key, the columns going, and the holder types
// they recorded — and then the exact declaration to add.
//
// The remedy is never "drop it from the manifest": that IS the move being
// refused, and an error whose suggested fix defeats the guard is worse than no
// guard, because it launders the erasure through an apparent instruction. It
// is also always a compiling Go literal, including when the lens's own
// canonicalName could not be read — a remedy that has to be repaired before it
// can be pasted is a remedy an author edits their way around.
func undeclaredSecureColumnDropError(drop droppedSecureColumn, blanketDeclared bool) error {
	lens := drop.Lens
	unresolved := ""
	if lens == "" {
		// The sibling .canonicalName aspect was unreadable. The literal keeps a
		// valid placeholder string and the sentence after it says what to put
		// there, rather than embedding prose where Go expects a name.
		lens = "<lens canonicalName>"
		unresolved = fmt.Sprintf(
			" This package's canonicalName for that lens could not be read from Core KV; substitute the name it declares for spec key %s.",
			drop.Key)
	}
	if drop.Column == "" {
		return fmt.Errorf(
			"pkgmgr: this upgrade tombstones lens %q (%s), whose committed spec still declares secure column(s) %v holding key custody for %v — "+
				"the removal (or rename) erases that custody record while every row those columns encrypted stays in the target store, "+
				"so the destruction-readiness oracle would attest coverage it no longer has. "+
				"Declare the retirement: add pkgmgr.RetiredSecureColumn{Lens: %q, Column: \"\", Note: \"why this history is safe to stop carrying\"} "+
				"to Definition.RetiredSecureColumns (Column \"\" is the whole-spec selector; for a RENAME, Lens is the OLD canonicalName).%s",
			lens, drop.Key, drop.Erased, drop.Holders, lens, unresolved)
	}
	blanketNote := ""
	if blanketDeclared {
		blanketNote = " A Column:\"\" retirement is already declared for this lens, and deliberately does not apply here: it attests to the whole " +
			"spec going at once, so it cannot excuse one column dropped from a lens that survives — and a Note written for that removal is not a " +
			"reason for this one."
	}
	return fmt.Errorf(
		"pkgmgr: this upgrade stops declaring secure column %q on lens %q (%s), whose committed spec still holds it with holderTypes %v — "+
			"every row it encrypted stays in the target store, so a spec that has forgotten the column makes the destruction-readiness oracle "+
			"attest coverage it no longer has. "+
			"Declare the retirement: add pkgmgr.RetiredSecureColumn{Lens: %q, Column: %q, Note: \"why this history is safe to stop carrying\"} "+
			"to Definition.RetiredSecureColumns.%s%s",
		drop.Column, lens, drop.Key, drop.Holders, lens, drop.Column, blanketNote, unresolved)
}

// retiredColumnLabel renders a RetiredSecureColumn.Column for an error
// message, spelling out what the empty selector means rather than printing an
// empty pair of quotes an author has to decode.
func retiredColumnLabel(column string) string {
	if column == "" {
		return `"" (the whole spec — a removed or renamed lens)`
	}
	return column
}
