package pkgmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ErrUndeclaredSecureLensErasure is the sentinel every unattested secure-lens
// erasure refusal wraps, for callers that only need to know an uninstall was
// refused for this reason — cmd/loupe maps it to 409, because an uninstall
// whose tombstone would erase a live Secure Lens's key-custody record from the
// destruction-readiness oracle's view fails identically on every retry until an
// operator attests it.
//
// The refusal happens before the UninstallPackage op is submitted because the
// tombstone is the erasure: once the batch commits, registry_probe.declaredLensIDs
// stops seeing the lens, and nothing in the platform records which holder types
// its ciphertext was written under. There is no after-the-fact place to ask the
// question, so the only moment the operator can answer it is this one.
var ErrUndeclaredSecureLensErasure = errors.New("pkgmgr: uninstall refused — a live Secure Lens's key-custody record would leave the destruction-readiness oracle's view unattested")

// ErrInvalidUninstallOptions is the sentinel every malformed-attestation
// refusal wraps — an attestation naming no Lens, carrying no Note, or naming a
// Lens twice. cmd/loupe maps it to 400, deliberately NOT to the 409 the
// unattested-erasure refusal gets: this is a badly formed REQUEST, not a
// conflict with the kernel's package state, and the console can reach it
// (a package declaring two erasures for one lens name would have the confirm
// modal submit two attestations under it).
var ErrInvalidUninstallOptions = errors.New("pkgmgr: invalid uninstall options — a secure-lens retirement attestation is malformed")

// RetiredSecureLens is one operator attestation that a Secure Lens's
// key-custody record may stop being visible to the destruction-readiness
// oracle (retention-class-key-custody-design.md §31).
//
// It is deliberately NOT Definition.RetiredSecureColumns: the attestation is
// the OPERATOR's word at the call site, never the package author's inside the
// package file. A package that could pre-declare its own uninstall retirement
// would carry the excuse for its own erasure, which is the disarm this guard
// exists to prevent — and reusing the author's type is exactly what would
// invite someone to wire the two together. A distinct type makes that a
// compile error.
//
// The erasure an uninstall causes is always whole-spec (the tombstone takes
// every secure column the lens declared at once), so there is no per-column
// selector here: attesting the lens attests all of it.
type RetiredSecureLens struct {
	// Lens is the lens's canonicalName AS THE INSTALLED PACKAGE DECLARED IT —
	// the string the installer salted the lens NanoID from, which is how this
	// attestation resolves to the spec key it excuses.
	Lens string
	// Note is why the ciphertext those columns encrypted is safe to stop
	// tracking. It is the only record of who decided that and why; the platform
	// verifies nothing about the ciphertext itself.
	Note string
}

// UninstallOptions carries the operator-supplied inputs to one uninstall,
// mirroring ApplyOptions: a value assembled at the call site (a CLI flag, an
// HTTP request field), never read out of the package being uninstalled.
//
// The zero value is the REFUSING state, not the permissive one: an uninstall
// that would newly erase a live Secure Lens's key-custody record and carries no
// attestation for it is refused. A caller that forgets to thread the operator's
// input through therefore fails closed and says so, instead of silently
// waving through the erasure this guard exists to catch.
type UninstallOptions struct {
	// RetiredSecureLenses attests, per lens, that this uninstall may erase that
	// lens's secure-column key-custody record from the destruction-readiness
	// oracle's view.
	RetiredSecureLenses []RetiredSecureLens
}

// validate is the pure, pre-read half of the uninstall secure-lens retirement
// guard: every attestation must name a Lens and carry a Note, and no Lens may
// appear twice.
//
// "Twice" is judged on the RAW Lens, not the trimmed one, because that is the
// string LensID salts: "l1" and " l1 " address two different keyspaces, so a
// package can hold both as genuinely separate lenses with separate custody
// records. Rejecting them as a duplicate pair would leave the refusal over the
// second one printing a remedy that, pasted beside the first attestation, is
// refused here as a duplicate — a different error and no way out. Trim-tolerant
// MATCHING is unaffected: the enforcer registers both spellings of every
// attestation as candidate keys.
//
// Uninstall runs it before reading anything, so a malformed attestation is
// refused on EVERY run rather than only on the runs whose erasures happen to
// reach the enforcement half — an operator who mistyped a flag learns it
// immediately, not on the one uninstall that also had a real erasure in it.
// The enforcer repeats these checks rather than trusting this one; it is the
// fail-closed floor for any path that reaches it unvalidated.
func (opts UninstallOptions) validate() error {
	seen := make(map[string]struct{}, len(opts.RetiredSecureLenses))
	for i, att := range opts.RetiredSecureLenses {
		lens := strings.TrimSpace(att.Lens)
		if lens == "" {
			return fmt.Errorf(
				"%w: RetiredSecureLenses[%d] names no Lens — set it to the lens's canonicalName as the INSTALLED package declares it "+
					"(the lens NanoID is salted by that name, so it is the only spelling that resolves to the key losing its secure columns)",
				ErrInvalidUninstallOptions, i)
		}
		if strings.TrimSpace(att.Note) == "" {
			return fmt.Errorf(
				"%w: RetiredSecureLenses[%d] (lens %q) has an empty Note — a retirement attests that the ciphertext those holder types "+
					"encrypted is safe to stop tracking, and the Note is the only record of who decided that and why; state it",
				ErrInvalidUninstallOptions, i, lens)
		}
		if _, dup := seen[att.Lens]; dup {
			return fmt.Errorf(
				"%w: RetiredSecureLenses names lens %q twice — one erasure, one attestation; two Notes for the same retirement "+
					"leave no answer to which one is the reason", ErrInvalidUninstallOptions, lens)
		}
		seen[att.Lens] = struct{}{}
	}
	return nil
}

// lensDeclaredToDestructionOracle reports whether Refractor's
// destruction-readiness oracle can currently see the lens whose spec aspect is
// specKey. It is the predicate that decides whether THIS uninstall's tombstone
// is what erases that lens's key-custody record, or whether the oracle was
// already blind to it before the run started.
//
// It mirrors registry_probe.declaredLensIDs, because the two have to answer the
// same question. The oracle enumerates vertex ROOTS: it drops a lens whose root
// key is absent, whose envelope class is not "meta.lens", or whose root is
// soft-deleted, and it separately skips a spec that CLEANLY DECODES as an
// eventStream source (Chronicler owns those). It does NOT consult the spec
// aspect's own isDeleted — a soft-deleted spec still decodes and still declares
// its secureColumns, so the oracle keeps reading that lens's custody record off
// it.
//
// Keying the classification on the spec's own isDeleted instead fails in both
// directions. A spec tombstoned out of band with its root left live is still
// fully visible to the oracle, so the root tombstone this uninstall is about to
// commit IS the erasure — filing it as "already erased" walks it straight past
// the guard. And a lens whose root is already tombstoned is already invisible,
// so gating it would refuse an uninstall over damage it neither caused nor can
// undo.
//
// specRaw is the spec aspect's committed bytes, not the parsed document,
// because the eventStream skip turns on whether the bytes decode into the
// oracle's own typed probe — see uninstallSpecProbe. The vertex root is read
// here, keyed off the spec key with its `.spec` suffix trimmed.
//
// Two states are handled deliberately more conservatively than the oracle
// handles them, both in the refusing direction:
//
//   - A root that fails to read, or whose envelope does not decode as JSON at
//     all, returns an error and stops the whole uninstall, where the oracle
//     skips a malformed envelope. A state that cannot be read is not a state a
//     custody guard may guess at, and the surrounding uninstall loops take the
//     same stance for the same reason.
//   - A root whose `isDeleted` is present but not a bool (say the string
//     "true") reads as NOT deleted here, so the lens counts as visible and its
//     erasure needs attesting. The oracle's typed decode errors on that shape
//     and drops the lens, so it would already be blind — this over-refuses,
//     which costs an operator one attestation and loses no custody record.
//
// An absent root reads as not visible, matching the oracle exactly.
func (i *Installer) lensDeclaredToDestructionOracle(ctx context.Context, specKey string, specRaw []byte) (bool, error) {
	var probe uninstallSpecProbe
	if json.Unmarshal(specRaw, &probe) == nil && probe.isEventStream() {
		return false, nil
	}
	root, _, err := i.getCommitted(ctx, strings.TrimSuffix(specKey, ".spec"))
	if err != nil {
		return false, err
	}
	if root == nil {
		return false, nil
	}
	if class, _ := root["class"].(string); class != "meta.lens" {
		return false, nil
	}
	deleted, _ := root["isDeleted"].(bool)
	return !deleted, nil
}

// uninstallSpecProbe is a local duplicate of the destruction-readiness oracle's
// own registryProbeSpecProbe (internal/refractor/health/registry_probe.go) —
// the health package is not importable from here, the same package boundary
// that made the oracle's copy a duplicate of CoreKVSource's envelopeProbe.
//
// The duplication is structural, field for field, and that is the whole point:
// the oracle skips an eventStream spec only when the WHOLE probe decodes
// cleanly, so what this type does and does not tolerate has to match. Its
// strictest field is the one that looks least load-bearing here —
// TargetConfig.SecureColumns[].HolderTypes is []string, so a holderTypes list
// carrying a non-string (`["identity", 5]`) fails the decode. On that document
// the oracle's own skip does not apply and the lens stays DECLARED, so an
// eventStream test that read the source kind out of a map would call the lens
// invisible and wave its erasure through unattested. A spec that does not
// decode is declared; here that means visible, and visible means attest it.
type uninstallSpecProbe struct {
	Source *struct {
		Kind string `json:"kind"`
	} `json:"source"`
	TargetConfig *uninstallTargetProbe `json:"targetConfig"`
	Data         *struct {
		Source *struct {
			Kind string `json:"kind"`
		} `json:"source"`
		TargetConfig *uninstallTargetProbe `json:"targetConfig"`
	} `json:"data"`
}

// uninstallTargetProbe mirrors the oracle's registryProbeTargetProbe: the
// Secure-Lens holder-type declarations, whose []string typing is what makes the
// enclosing probe's decode strict.
type uninstallTargetProbe struct {
	SecureColumns []struct {
		HolderTypes []string `json:"holderTypes"`
	} `json:"secureColumns"`
}

// isEventStream reports whether this probe declares a Chronicler-owned
// eventStream source at either the bare-body or the stored-envelope (`data`)
// level. Both levels are consulted, never one in preference to the other,
// because the oracle's sibling method does the same.
func (p uninstallSpecProbe) isEventStream() bool {
	if p.Source != nil && p.Source.Kind == "eventStream" {
		return true
	}
	return p.Data != nil && p.Data.Source != nil && p.Data.Source.Kind == "eventStream"
}

// secureLensAttestation is one UninstallOptions.RetiredSecureLenses entry
// resolved against the keyspace it excuses.
//
// keys is plural for one reason: Lens is validated after trimming but a lens
// NanoID is salted by the RAW canonicalName the installer minted it from, so an
// attestation carrying stray whitespace would validate and then match nothing —
// and the refusal would then instruct the operator to pass a flag visually
// identical to the one they just passed. Registering both readings of the
// operator's own string closes that dead end without letting an attestation
// reach a lens it does not name: LensID is a hash, and the trimmed and
// untrimmed spellings are the only two candidates for what was meant.
type secureLensAttestation struct {
	lens    string
	keys    []string
	matched bool
}

// enforceUninstallSecureLensRetirement is the uninstall half of the Secure-Lens
// key-custody retirement guard (retention-class-key-custody-design.md §31): an
// uninstall that would newly erase a committed `targetConfig.secureColumns`
// record from the destruction-readiness oracle's view must carry the operator's
// attestation for that lens.
//
// The erasure it covers is the one Upgrade/Apply's RetiredSecureColumns guard
// cannot reach: an uninstall takes a package NAME, so no package diff exists to
// declare against. The tombstone lands in the same place either way —
// Refractor's destruction-readiness oracle (internal/refractor/health/
// registry_probe.go) answers "which lenses hold ciphertext for this holder
// type?" from the current registry alone, and a lens whose vertex root it can
// no longer see reads as a genuine no. Meanwhile the uninstall has not touched
// the target store, so every row those columns encrypted is still sitting
// there and the platform now attests destruction coverage over rows it can no
// longer see.
//
// Only erasures the oracle is not already blind to are gated; see Uninstall's
// classification of erased vs already-erased, which mirrors the oracle's own
// visibility predicate. Refusing on a population this uninstall neither caused
// nor can undo would make pre-existing damage un-uninstallable, the opposite of
// what RetentionHoldersAlreadyStranded is for.
//
// It is pure and side-effect-free, which is why Uninstall runs it before both
// of its return paths rather than after — an uninstall whose only keys are
// already gone still refuses an unattested erasure, so the answer does not
// depend on how much of the package happens to survive.
//
// Matching: an attestation resolves to the key it excuses through
// LensID(packageName, att.Lens) — the same derivation the installer used to
// mint that key — never by string-comparing a canonicalName against a key.
//
// Returns the number of secure COLUMNS whose custody record an attestation
// excused (the same unit UpgradeResult.SecureColumnsRetired counts, so the two
// are comparable) and the lenses whose attestation matched nothing — an
// attestation for a lens this package no longer holds reads as load-bearing
// until something says otherwise.
//
// A refusal states the WHOLE bill: every unattested erasure is collected and
// named in one *UndeclaredSecureLensErasureError, never one per run. An
// uninstall is all-or-nothing over a whole package, so a refusal that stopped
// at the first lens would make a two-lens package a game of whack-a-mole.
func enforceUninstallSecureLensRetirement(packageName string, opts UninstallOptions, erased []UninstallSecureColumnErasure) (int, []string, error) {
	atts := make([]secureLensAttestation, 0, len(opts.RetiredSecureLenses))
	seen := make(map[string]struct{}, len(opts.RetiredSecureLenses))
	for i, raw := range opts.RetiredSecureLenses {
		lens := strings.TrimSpace(raw.Lens)
		if lens == "" {
			return 0, nil, fmt.Errorf(
				"%w: RetiredSecureLenses[%d] names no Lens — set it to the lens's canonicalName as the INSTALLED package declares it",
				ErrInvalidUninstallOptions, i)
		}
		if strings.TrimSpace(raw.Note) == "" {
			return 0, nil, fmt.Errorf(
				"%w: RetiredSecureLenses[%d] (lens %q) has an empty Note — a retirement attests that the ciphertext those holder types "+
					"encrypted is safe to stop tracking, and the Note is the only record of who decided that and why; state it",
				ErrInvalidUninstallOptions, i, lens)
		}
		if _, dup := seen[raw.Lens]; dup {
			return 0, nil, fmt.Errorf(
				"%w: RetiredSecureLenses names lens %q twice — one erasure, one attestation; two Notes for the same retirement "+
					"leave no answer to which one is the reason", ErrInvalidUninstallOptions, lens)
		}
		seen[raw.Lens] = struct{}{}
		atts = append(atts, secureLensAttestation{
			lens: lens,
			keys: spellings(metaVertexPrefix+LensID(packageName, lens)+".spec", metaVertexPrefix+LensID(packageName, raw.Lens)+".spec"),
		})
	}

	attested := 0
	var unattested []UninstallSecureColumnErasure
	for _, erasure := range erased {
		excused := false
		for idx := range atts {
			if !slices.Contains(atts[idx].keys, erasure.Key) {
				continue
			}
			// Pin the attestation to the spelling that actually matched. Both
			// readings of the operator's string were registered so a stray space
			// still resolves, but a lens named "foo" and one named " foo" are two
			// different lenses holding two different custody records: leaving both
			// keys live would let one Note excuse both, which is exactly the "one
			// erasure, one attestation" invariant the duplicate-Lens check exists
			// to hold.
			atts[idx].keys = []string{erasure.Key}
			atts[idx].matched = true
			excused = true
			break
		}
		if !excused {
			// Collected, not returned. One uninstall takes the whole package at
			// once, so refusing at the first unexcused erasure would hand a
			// two-Secure-Lens package back one lens at a time: attest, re-run,
			// get refused again over the lens the first refusal never mentioned.
			// The operator needs the entire bill before they write any of it.
			unattested = append(unattested, erasure)
			continue
		}
		// The unit is the secure columns the committed spec DECLARED, not erasure
		// events and not the nameable subset: a removed lens that took twenty
		// secure columns with it retired twenty custody records, and reporting
		// that as 1 would make the number incomparable with
		// UpgradeResult.SecureColumnsRetired beside it. Declared counts every
		// entry, including one whose `column` field is unnameable — that entry
		// still recorded holder types, so its custody record is still lost.
		if n := erasure.Declared; n > 0 {
			attested += n
		} else {
			attested++
		}
	}
	if len(unattested) > 0 {
		return 0, nil, undeclaredSecureLensErasureError(packageName, unattested)
	}

	var unused []string
	for _, att := range atts {
		if !att.matched {
			unused = append(unused, att.lens)
		}
	}
	return attested, unused, nil
}

// UndeclaredSecureLensErasureError is the typed unattested-erasure refusal.
// Error() renders the operator-facing message, and Unattested carries the same
// answer structurally so a caller (or a test, or Loupe's confirm modal) reads
// which lenses need attesting from DATA rather than by scraping prose — the
// modal that re-prompts for a note per lens must not be a regex over this
// sentence.
//
// Unwrap yields ErrUndeclaredSecureLensErasure, so errors.Is keeps working for
// callers that only need the class.
type UndeclaredSecureLensErasureError struct {
	// PackageName is the package whose uninstall was refused.
	PackageName string
	// Unattested names every erasure this uninstall would newly make that no
	// RetiredSecureLens attested — all of them, in the order the uninstall
	// found them, so one refusal states the whole bill.
	Unattested []UninstallSecureColumnErasure

	message string
}

// Error renders the operator-facing refusal.
func (e *UndeclaredSecureLensErasureError) Error() string { return e.message }

// Unwrap yields the sentinel every unattested-erasure refusal wraps.
func (e *UndeclaredSecureLensErasureError) Unwrap() error { return ErrUndeclaredSecureLensErasure }

// undeclaredSecureLensErasureError builds the typed refusal for every erasure
// that reached the gate unattested. It names each one precisely — the lens, its
// spec key, the columns going, and the holder types they recorded — and then
// the exact attestation to pass, one flag per lens, in a single runnable
// command line so the operator's next run is their last. The flags precede the
// package name because Go's flag package stops parsing at the first non-flag
// argument: a command line with the name first parses as a bare positional
// followed by junk and exits 2, which is a remedy that does not run.
//
// The remedy is never "tombstone the spec (or the root) out of band first":
// that IS the erasure being refused, dressed as preparation, and an error whose
// suggested fix defeats the guard is worse than no guard, because it launders
// the erasure through an apparent instruction. It is also always a runnable
// command line, including when a lens's own canonicalName could not be read —
// a remedy that has to be repaired before it can be pasted is a remedy an
// operator edits their way around.
func undeclaredSecureLensErasureError(packageName string, unattested []UninstallSecureColumnErasure) *UndeclaredSecureLensErasureError {
	var bill, flags, unresolved strings.Builder
	unnamed := 0
	for _, erasure := range unattested {
		lens := erasure.Lens
		if lens == "" {
			// The sibling .canonicalName aspect was unreadable. The command keeps
			// a valid placeholder string and a sentence below says what to put
			// there, rather than embedding prose where the flag expects a name.
			// The placeholder is NUMBERED because two unreadable names in one
			// refusal would otherwise print the same literal twice, and pasting
			// that command trips the duplicate-Lens check — handing the operator
			// a different error than the one they set out to fix.
			unnamed++
			lens = fmt.Sprintf("<lens canonicalName #%d>", unnamed)
			fmt.Fprintf(&unresolved,
				" This package's canonicalName for spec key %s could not be read from Core KV; substitute the name it declares for %s.",
				erasure.Key, lens)
		}
		// The DECLARED count leads, and the names follow it: an erasure whose
		// columns are all unnameable would otherwise print an empty list beside
		// an empty holder list and name nothing the operator could attest about.
		fmt.Fprintf(&bill, "\n  - lens %q (%s): %d declared secure column(s), named %v, holding key custody for %v",
			lens, erasure.Key, erasure.Declared, erasure.Columns, erasure.Holders)
		fmt.Fprintf(&flags, " --retire-secure-lens '%s=<why this history is safe to stop carrying>'", lens)
	}
	return &UndeclaredSecureLensErasureError{
		PackageName: packageName,
		Unattested:  slices.Clone(unattested),
		message: fmt.Sprintf(
			"%s: uninstalling %q tombstones %d lens(es) whose committed spec still declares secure columns:%s\n"+
				"Every row those columns encrypted stays in the target store, untouched, while registry_probe.declaredLensIDs stops seeing the "+
				"lens entirely, so the destruction-readiness oracle would attest coverage it no longer has. "+
				"Attest the retirement: re-run with `lattice-pkg uninstall%s %s`.%s",
			ErrUndeclaredSecureLensErasure, packageName, len(unattested), bill.String(), flags.String(), packageName, unresolved.String()),
	}
}
