package cafeledger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
)

// TestPackage_ManifestMatchesDefinition keeps manifest.yaml and the Go
// Definition in lockstep (the loftspace-ledger/clinic-ledger precedent): the
// install reads the Definition, but the manifest is the human-facing
// declaration, and a drift between the two is a silent install hazard.
func TestPackage_ManifestMatchesDefinition(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	m, err := pkgmgr.ParseManifest(filepath.Join(wd, "manifest.yaml"))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if err := m.VerifyAgainstDefinition(Package); err != nil {
		t.Fatalf("manifest <-> Definition drift: %v", err)
	}
}

// TestPackage_StructurePins pins what this package declares, by count and by
// canonical name (Vertical Package Standard S6, loftspace-domain/package_test.go
// idiom). A declaration added or dropped without a deliberate edit here reds
// this test rather than reaching an install, where the same change is a silent
// capability or read-model shift.
func TestPackage_StructurePins(t *testing.T) {
	if got, want := len(Package.DDLs), 4; got != want {
		t.Errorf("DDLs: got %d, want %d", got, want)
	}
	if got, want := len(Package.Permissions), 5; got != want {
		t.Errorf("Permissions: got %d, want %d", got, want)
	}
	if got, want := len(Package.Lenses), 2; got != want {
		t.Errorf("Lenses: got %d, want %d", got, want)
	}
	if got, want := len(Package.WeaverTargets), 0; got != want {
		t.Errorf("WeaverTargets: got %d, want %d", got, want)
	}
	if got, want := len(Package.LoomPatterns), 0; got != want {
		t.Errorf("LoomPatterns: got %d, want %d", got, want)
	}
	if got, want := len(Package.OpMetas), 2; got != want {
		t.Errorf("OpMetas: got %d, want %d", got, want)
	}

	wantDDLs := []string{"cafeaccount", "cafeLedgerAccountGuard", "cafeAccountBalance", "cafetransaction"}
	for i, d := range Package.DDLs {
		if i < len(wantDDLs) && d.CanonicalName != wantDDLs[i] {
			t.Errorf("DDLs[%d]: got %q, want %q", i, d.CanonicalName, wantDDLs[i])
		}
	}

	wantPerms := []struct{ op, scope string }{{"CreateAccount", "any"}, {"DebitAccount", "any"}, {"CreditCafeAccount", "any"}, {"CreditCafeAccount", "self"}, {"RefundCafeCharge", "any"}}
	for i, want := range wantPerms {
		if i >= len(Package.Permissions) {
			break
		}
		got := Package.Permissions[i]
		if got.OperationType != want.op || got.Scope != want.scope {
			t.Errorf("Permissions[%d]: got %s/%s, want %s/%s", i, got.OperationType, got.Scope, want.op, want.scope)
		}
	}

	wantOpMetas := []string{"CreditCafeAccount", "RefundCafeCharge"}
	for i, m := range Package.OpMetas {
		if i < len(wantOpMetas) && m.OperationType != wantOpMetas[i] {
			t.Errorf("OpMetas[%d]: got %q, want %q", i, m.OperationType, wantOpMetas[i])
		}
	}

	wantLenses := []string{"cafeLedgerHistory", "cafeLeaseAccounts"}
	for i, d := range Package.Lenses {
		if i < len(wantLenses) && d.CanonicalName != wantLenses[i] {
			t.Errorf("Lenses[%d]: got %q, want %q", i, d.CanonicalName, wantLenses[i])
		}
	}
}

// TestTransactionScript_RefundIsRefusedOnEveryValidatedPath pins the guard's
// PREDICATE at the source, because no outcome-level test can distinguish its
// two conjuncts. `op.authContextTarget != ""` is the client-supplied hint;
// `op.authTargetValidated` is the platform bit that DISCHARGES the workplace
// walk (workplace_exempt in scripts.go), and today's Processor sets the second
// only on paths that also carry the first — platform scope=self requires
// target == actor, and the task path requires the matched ephemeral grant to
// name a target (internal/processor/operation_context.go). A live vector on
// either path is therefore refused whichever conjunct is present, and dropping
// the second reds nothing that runs.
//
// That subsumption is the PLATFORM's invariant, not this package's, and the
// two diverge under different edits: granting this op scope=self reaches only
// the first, while a task minted forOperation RefundCafeCharge is authorized
// entirely by the second. A refund that reached post_entry on a validated path
// would arrive both exempt from confinement and on the resident-credit branch,
// minting credits against the caller's own charges.
// TestRefundCafeCharge_ValidatedTargetSubmitRejected drives the task path
// end-to-end for the coverage; this is what makes the conjunct unremovable.
func TestTransactionScript_RefundIsRefusedOnEveryValidatedPath(t *testing.T) {
	const guard = `if op.authContextTarget != "" or op.authTargetValidated:`
	if !strings.Contains(transactionDDLScript, guard) {
		t.Errorf("transactionDDLScript must refuse RefundCafeCharge on BOTH the raw target and the validated bit: %s", guard)
	}
	// tabRef is DebitAccount's field, and the refund refuses it rather than
	// ignoring it — symmetric with post_entry's refusal of reversesRef on every
	// op but this one. A silent drop commits a credit unrelated to the tab the
	// caller named.
	if !strings.Contains(transactionDDLScript, `fail("InvalidArgument: tabRef: only valid on DebitAccount, not RefundCafeCharge")`) {
		t.Error("transactionDDLScript must refuse a tabRef sent to RefundCafeCharge")
	}
}

// TestTransactionScript_RefundCeilingIsACASPinnedTally pins the mechanism the
// refund ceiling is made of, because no outcome-level test can distinguish it
// from the platform's own safety net: Contract #3 §3.2 conditions any `update`
// on a key declared in contextHint.reads at the revision it was hydrated at, so
// an unpinned tally upsert on the charge's declared-read .entry is still
// conditioned — but as a DEFAULTED condition, which the commit path treats as a
// benign same-key race and retries. The explicit pin is what makes the loser's
// refusal terminal rather than a re-execution, and it is what keeps the
// guarantee if the read is ever moved out of the descriptor. Only the source
// says which of the two is in force.
//
// The enumeration lines matter for the same reason from the other direction: a
// ceiling summed by walking `reverses` links has no single revision to pin at
// all, so an op that computed one that way would be back to a cap two
// concurrent refunds can both satisfy, with nothing at the outcome level to
// say so.
func TestTransactionScript_RefundCeilingIsACASPinnedTally(t *testing.T) {
	for _, want := range []string{
		`m["expectedRevision"] = expected_revision`,                   // the CAS itself
		`make_aspect_upsert_occ(reverses_key, "entry"`,                // the tally lands on the reversed charge
		`tally_data["refundedCents"] = refunded_cents + amount_cents`, // and accumulates
		`entry.revision`, // pinned to the revision the ceiling was read at
		`refunded_cents = entry.data.get("refundedCents", 0)`, // the ceiling is a read, not a walk
		`RefundExceedsCharge`, // the rejection the ceiling produces
	} {
		if !strings.Contains(transactionDDLScript, want) {
			t.Errorf("transactionDDLScript must contain %q", want)
		}
	}
	for _, unwanted := range []string{
		`kv.Links(reverses_key, "reverses"`, // an enumerated, unpinnable ceiling
		`REVERSAL_PAGE_LIMIT`,
		`REVERSAL_MAX_PAGES`,
	} {
		if strings.Contains(transactionDDLScript, unwanted) {
			t.Errorf("transactionDDLScript must NOT contain %q: an enumerated ceiling cannot be pinned to a revision", unwanted)
		}
	}

	for _, m := range Package.OpMetas {
		if m.OperationType != "RefundCafeCharge" || m.Dispatch == nil {
			continue
		}
		for _, e := range m.Dispatch.Enumerations {
			if e.Relation == "reverses" {
				t.Errorf("RefundCafeCharge declares a %s enumeration: the ceiling is a tally on a declared read, not a walk", e.Relation)
			}
		}
		var hasEntryRead bool
		for _, r := range m.Dispatch.Reads {
			if r == "{payload.reversesRef}.entry" {
				hasEntryRead = true
			}
		}
		if !hasEntryRead {
			t.Error("RefundCafeCharge must declare {payload.reversesRef}.entry: it carries the ceiling and supplies the revision the CAS pins")
		}
	}
}
