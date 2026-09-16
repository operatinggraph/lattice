package clinicledger

// Rule-engine proof of the clinicArrearsReminders convergence lens, driven
// through the `full` engine against an embedded NATS Core/Adjacency KV — the
// same harness lens_cypher_test.go's clinicNoShowSettlement pins use.
//
// The arrears convergence lens reads NO clock: every comparison is between two
// stored facts (the recorded due date, the recorded reminder, and the instant a
// fired @at recorded on this account's own freshnessExpiry marker). Every
// conjunct of freshUntil and of the single gap therefore gets a vector here,
// because a mis-compiled lens FALLS BACK silently on the full engine — a shape
// the engine cannot compile projects nothing rather than erroring, and only a
// pin that runs the real engine can tell the difference.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// projectArrears runs the anchored clinicArrearsReminders spec for one
// account. NO clock parameter is supplied: the cypher references none, and
// passing one would let a clock-reading regression pass unnoticed here.
func (f *clFixture) projectArrears(t *testing.T, acctName string) []ruleengine.ProjectionResult {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(arrearsRemindersSpec)
	require.NoError(t, err, "clinicArrearsReminders cypher must parse on the full engine")
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey": "vtx.clinicaccount." + f.ids[acctName],
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// mkArrearsAccount seeds a clinicaccount held for a patient, optionally
// carrying a .arrears aspect. A nil arrears map seeds the account with no
// aspect at all — the never-evaluated shape every account alive at install
// has.
func (f *clFixture) mkArrearsAccount(t *testing.T, prefix string, arrears map[string]any) {
	t.Helper()
	f.vtx(t, prefix+"_patient", "patient")
	f.vtx(t, prefix+"_acct", "clinicaccount")
	f.edge(t, "heldFor", prefix+"_acct", prefix+"_patient")
	if arrears != nil {
		f.aspect(t, prefix+"_acct", "arrears", "clinicAccountArrears", arrears)
	}
}

// recordLapse writes the freshnessExpiry marker MarkExpired commits onto the
// ACCOUNT when this target's @at fires: the instant the timer fired for,
// recorded under the target's own key in byTarget, with expiredAt carrying
// the entity-wide maximum. The marker shape is orchestration-base's, not this
// package's.
func (f *clFixture) recordLapse(t *testing.T, name string, byTarget map[string]string) {
	t.Helper()
	entries := map[string]any{}
	maxAt := ""
	for target, at := range byTarget {
		entries[target] = at
		if at > maxAt {
			maxAt = at
		}
	}
	f.aspect(t, name, "freshnessExpiry", "freshnessExpiry", map[string]any{
		"expiredAt": maxAt,
		"byTarget":  entries,
	})
}

// TestClinicArrears_NeverEvaluated — an account carrying no .arrears at all
// (every account alive before this shipped). evaluatedAt is null, so the gap is
// open from its first projection: that is how each standing account gets its
// one evaluation. freshUntil is null — there is no recorded due date to arm a
// timer at.
func TestClinicArrears_NeverEvaluated(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newClFixture(t)
	f.mkArrearsAccount(t, "virgin", nil)

	rows := f.projectArrears(t, "virgin_acct")
	require.Len(t, rows, 1, "exactly one row per account even with the patient linked")
	v := rows[0].Values
	require.Equal(t, "vtx.clinicaccount."+f.ids["virgin_acct"], v["entityKey"])
	require.Equal(t, "vtx.clinicaccount."+f.ids["virgin_acct"], v["actorKey"])
	require.Equal(t, "vtx.patient."+f.ids["virgin_patient"], v["patientKey"], "the patient is projected for the operator's read model, never routed as a param")
	require.Equal(t, true, v["missing_evaluation"], "an account nothing has ever aged is violating on sight")
	require.Equal(t, true, v["violating"])
	require.Nil(t, v["freshUntil"], "no recorded due date, so no timer arms")
}

// TestClinicArrears_Pending — a recorded due date still ahead of any fired
// timer: NOT violating, and freshUntil = dueAt arms the @at.
func TestClinicArrears_Pending(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newClFixture(t)
	f.mkArrearsAccount(t, "pending", map[string]any{
		"dueAt":       "2026-09-11T10:00:00Z",
		"evaluatedAt": "2026-08-27T10:00:00Z",
	})

	rows := f.projectArrears(t, "pending_acct")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, false, v["missing_evaluation"], "no timer has fired on this account — not due")
	require.Equal(t, false, v["violating"])
	require.Equal(t, "2026-09-11T10:00:00Z", v["freshUntil"], "freshUntil = dueAt arms the @at timer")
	_, isString := v["freshUntil"].(string)
	require.True(t, isString, "freshUntil must be a scalar string so scheduleFreshness can parse it as RFC3339")
	require.Equal(t, "2026-09-11T10:00:00Z", v["dueAt"])
	require.Nil(t, v["reminderSentAt"], "nothing has gone out yet")
}

// TestClinicArrears_Due — the @at has FIRED and its lapse is recorded at dueAt,
// and nothing has been reminded for that date: the gap OPENS. freshUntil goes
// null once the lapse is recorded — a one-shot wake-up, not a re-arm; the
// violating row itself drives the dispatch from here.
func TestClinicArrears_Due(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newClFixture(t)
	f.mkArrearsAccount(t, "due", map[string]any{
		"dueAt":       "2026-08-06T14:20:00Z",
		"evaluatedAt": "2026-07-22T14:20:00Z",
	})
	f.recordLapse(t, "due_acct", map[string]string{ArrearsRemindersTarget: "2026-08-06T14:20:00Z"})

	rows := f.projectArrears(t, "due_acct")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, true, v["missing_evaluation"], "the recorded lapse reaches dueAt and nothing was reminded for it")
	require.Equal(t, true, v["violating"])
	require.Nil(t, v["freshUntil"], "the timer fired; it is not re-armed")
}

// TestClinicArrears_Sent — the same account after EvaluateClinicArrears sent
// the reminder: remindedFor = dueAt closes the gap, and freshUntil stays null.
// This is the "no re-dispatch after a send" assertion — a gap that re-opened
// every convergence window would mint a fresh notification each time.
func TestClinicArrears_Sent(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newClFixture(t)
	f.mkArrearsAccount(t, "sent", map[string]any{
		"dueAt":       "2026-08-06T14:20:00Z",
		"remindedFor": "2026-08-06T14:20:00Z",
		"sentAt":      "2026-08-06T14:25:00Z",
		"evaluatedAt": "2026-08-06T14:25:00Z",
	})
	f.recordLapse(t, "sent_acct", map[string]string{ArrearsRemindersTarget: "2026-08-06T14:20:00Z"})

	rows := f.projectArrears(t, "sent_acct")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, false, v["missing_evaluation"], "one reminder per episode: the recorded send closes the gap for good")
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"], "remindedFor = dueAt, so no timer re-arms either")
	require.Equal(t, "2026-08-06T14:25:00Z", v["reminderSentAt"], "the desk and the patient both read this")
}

// TestClinicArrears_Stale — a partial payment moved the FIFO head somewhere
// post_entry cannot compute, so it marked the recorded state stale. The gap
// opens with no timer involved at all, and freshUntil is suppressed even though
// the recorded dueAt is still in the future and unreminded: arming a timer at a
// date already known to be wrong would fire a reminder for the wrong charge.
func TestClinicArrears_Stale(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newClFixture(t)
	f.mkArrearsAccount(t, "stale", map[string]any{
		"dueAt":       "2026-09-11T10:00:00Z",
		"evaluatedAt": "2026-08-27T10:00:00Z",
		"stale":       true,
	})

	rows := f.projectArrears(t, "stale_acct")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, true, v["missing_evaluation"], "stale opens the gap directly — no fired timer needed")
	require.Equal(t, true, v["violating"])
	require.Nil(t, v["freshUntil"], "a due date known to be stale must not arm a timer")
	require.Equal(t, true, v["stale"])
}

// TestClinicArrears_Cleared — the account was paid off, so the aspect was
// rewritten to {evaluatedAt} alone. No dueAt: nothing violating, no timer, and
// nothing left of the finished episode to make the NEXT charge look reminded.
func TestClinicArrears_Cleared(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newClFixture(t)
	f.mkArrearsAccount(t, "cleared", map[string]any{"evaluatedAt": "2026-08-20T09:00:00Z"})
	// The marker from the episode that has just ended stays on the account
	// forever (orchestration-base merges, never clears). It must not make a
	// cleared account violating.
	f.recordLapse(t, "cleared_acct", map[string]string{ArrearsRemindersTarget: "2026-08-06T14:20:00Z"})

	rows := f.projectArrears(t, "cleared_acct")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, false, v["missing_evaluation"], "nothing is owed — the episode is over")
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"])
	require.Nil(t, v["dueAt"])
}

// TestClinicArrears_NewEpisodeAfterOldLapse is the standing-marker vector: the
// freshnessExpiry entry from a PREVIOUS episode is permanent, and a new episode
// whose dueAt is later than that recorded instant must re-arm rather than open
// on the old lapse. It always is later — a new episode's charge posts after the
// last one ended and both add the same net term — so this pins the ordering the
// design leans on.
func TestClinicArrears_NewEpisodeAfterOldLapse(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newClFixture(t)
	f.mkArrearsAccount(t, "reopened", map[string]any{
		"dueAt":       "2026-09-20T08:00:00Z",
		"evaluatedAt": "2026-09-05T08:00:00Z",
	})
	f.recordLapse(t, "reopened_acct", map[string]string{ArrearsRemindersTarget: "2026-08-06T14:20:00Z"})

	rows := f.projectArrears(t, "reopened_acct")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, false, v["missing_evaluation"], "the old lapse predates this episode's due date")
	require.Equal(t, "2026-09-20T08:00:00Z", v["freshUntil"], "a fresh @at arms for the new episode")
}

// TestClinicArrears_SiblingTargetLapseDoesNotOpen pins the byTarget
// indirection: the marker is shared by every target that arms a timer on this
// anchor, and only THIS target's own entry may open this gap. Reading
// expiredAt (the entity-wide maximum) instead would have any sibling's fired
// timer send an arrears reminder.
func TestClinicArrears_SiblingTargetLapseDoesNotOpen(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newClFixture(t)
	f.mkArrearsAccount(t, "sibling", map[string]any{
		"dueAt":       "2026-09-11T10:00:00Z",
		"evaluatedAt": "2026-08-27T10:00:00Z",
	})
	f.recordLapse(t, "sibling_acct", map[string]string{"someOtherTarget": "2026-09-30T00:00:00Z"})

	rows := f.projectArrears(t, "sibling_acct")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, false, v["missing_evaluation"], "another target's fired timer is not this gap's evidence")
	require.Equal(t, "2026-09-11T10:00:00Z", v["freshUntil"], "and it must not suppress this target's own @at either")
}

// TestClinicArrears_HistoryTooLongGoesQuiet pins the degrade posture, and it
// is the ONE vector where a recorded lapse must NOT open the gap. An account
// whose transaction history outran the evaluation's replay budget carries
// historyTooLong: the op cannot compute a head for it, so leaving the gap open
// would have Weaver re-dispatch an evaluation that can only fail again, every
// window, forever, with nothing sent and nothing said. Both suppressions are
// asserted together — a lens that killed only the gap would still arm an @at
// at a dueAt no evaluation could confirm, and one that killed only the timer
// would still re-dispatch. The row itself stays projected, which is the
// operator's signal in the weaver-targets bucket.
func TestClinicArrears_HistoryTooLongGoesQuiet(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newClFixture(t)
	f.mkArrearsAccount(t, "toolong", map[string]any{
		"dueAt":          "2026-08-06T14:20:00Z",
		"evaluatedAt":    "2026-08-22T09:00:00Z",
		"historyTooLong": true,
	})
	// The lapse IS recorded and nothing was reminded for it: without the
	// historyTooLong conjunct this is TestClinicArrears_Due exactly, so the
	// vector reds the moment either suppression is dropped.
	f.recordLapse(t, "toolong_acct", map[string]string{ArrearsRemindersTarget: "2026-08-06T14:20:00Z"})

	rows := f.projectArrears(t, "toolong_acct")
	require.Len(t, rows, 1, "the row must stay projected — quiet is not invisible")
	v := rows[0].Values
	require.Equal(t, true, v["historyTooLong"], "the operator reads this column off the weaver-targets row")
	require.Equal(t, false, v["missing_evaluation"], "an evaluation that cannot succeed must not be re-dispatched every window")
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"], "and no timer arms at a due date no evaluation could confirm")
}

// TestClinicArrears_HistoryTooLongClearedReopens is the positive vector the
// one above is measured against: the SAME state with the flag absent IS
// violating. Without it a lens that had simply stopped computing
// missing_evaluation would pass the suppression pin.
func TestClinicArrears_HistoryTooLongClearedReopens(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newClFixture(t)
	f.mkArrearsAccount(t, "cleardone", map[string]any{
		"dueAt":       "2026-08-06T14:20:00Z",
		"evaluatedAt": "2026-08-22T09:00:00Z",
	})
	f.recordLapse(t, "cleardone_acct", map[string]string{ArrearsRemindersTarget: "2026-08-06T14:20:00Z"})

	rows := f.projectArrears(t, "cleardone_acct")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Nil(t, v["historyTooLong"])
	require.Equal(t, true, v["missing_evaluation"], "the identical row without the flag opens the gap")
}

// TestClinicArrears_NoPatientStillProjects pins the heldFor walk as OPTIONAL.
// An account with no patient is not a shape ClinicCreateAccount produces, but
// a patient tombstoned out from under a live account is — and the row for such
// an account must still project, because it is the row that carries the gap: a
// required MATCH would silently stop aging that account and remind nobody.
// patientKey comes back null, which is why the playbook never routes it as a
// param — the op resolves the patient itself.
func TestClinicArrears_NoPatientStillProjects(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newClFixture(t)
	f.vtx(t, "nopatient_acct", "clinicaccount")
	f.aspect(t, "nopatient_acct", "arrears", "clinicAccountArrears", map[string]any{
		"dueAt":       "2026-09-11T10:00:00Z",
		"evaluatedAt": "2026-08-27T10:00:00Z",
	})

	rows := f.projectArrears(t, "nopatient_acct")
	require.Len(t, rows, 1, "an account with no heldFor patient still projects its arrears row")
	v := rows[0].Values
	require.Equal(t, "vtx.clinicaccount."+f.ids["nopatient_acct"], v["entityKey"])
	require.Nil(t, v["patientKey"], "no patient to walk to")
	require.Equal(t, false, v["missing_evaluation"])
	require.Equal(t, "2026-09-11T10:00:00Z", v["freshUntil"], "and the timer arms exactly as it would with a patient")
}

// TestClinicPatientAccounts_ProjectsArrearsColumns pins the three
// informational columns the desk's arrears grid, the appointment card and the
// patient's statement read for "when did the reminder go out" — they come off
// the ACCOUNT's aspect through the lens's inbound heldFor hop, so a patient
// with no account, or an account nothing has aged, still projects nulls.
func TestClinicPatientAccounts_ProjectsArrearsColumns(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newClFixture(t)
	f.vtx(t, "arr_patient", "patient")
	f.vtx(t, "arr_acct", "clinicaccount")
	f.edge(t, "heldFor", "arr_acct", "arr_patient")
	f.aspect(t, "arr_acct", "arrears", "clinicAccountArrears", map[string]any{
		"dueAt":       "2026-08-06T14:20:00Z",
		"remindedFor": "2026-08-06T14:20:00Z",
		"sentAt":      "2026-08-06T14:25:00Z",
		"evaluatedAt": "2026-08-06T14:25:00Z",
	})
	f.vtx(t, "bare_patient", "patient")
	f.vtx(t, "bare_acct", "clinicaccount")
	f.edge(t, "heldFor", "bare_acct", "bare_patient")
	f.vtx(t, "lone_patient", "patient")

	rows := f.project(t, "clinicPatientAccounts", patientAccountsSpec)
	require.Len(t, rows, 3, "one row per patient, account or not")
	byPatient := map[string]map[string]any{}
	for _, r := range rows {
		byPatient[r.Values["patientKey"].(string)] = r.Values
	}

	aged := byPatient["vtx.patient."+f.ids["arr_patient"]]
	require.Equal(t, "vtx.clinicaccount."+f.ids["arr_acct"], aged["accountKey"])
	require.Equal(t, "2026-08-06T14:20:00Z", aged["arrearsDueAt"])
	require.Equal(t, "2026-08-06T14:20:00Z", aged["arrearsRemindedFor"])
	require.Equal(t, "2026-08-06T14:25:00Z", aged["arrearsReminderSentAt"])

	bare := byPatient["vtx.patient."+f.ids["bare_patient"]]
	require.Equal(t, "vtx.clinicaccount."+f.ids["bare_acct"], bare["accountKey"])
	require.Nil(t, bare["arrearsDueAt"], "an account nothing has aged projects null")
	require.Nil(t, bare["arrearsRemindedFor"])
	require.Nil(t, bare["arrearsReminderSentAt"])

	lone := byPatient["vtx.patient."+f.ids["lone_patient"]]
	require.Nil(t, lone["accountKey"], "no account opened yet")
	require.Nil(t, lone["arrearsDueAt"], "and so no arrears state either")
	require.Nil(t, lone["arrearsReminderSentAt"])
}
