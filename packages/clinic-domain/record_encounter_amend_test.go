package clinicdomain_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	"github.com/operatinggraph/lattice/internal/vault"
)

// RecordEncounter's record-or-amend arm split over the shapes the corpus
// already holds, and the bound on the history it keeps:
//
//  1. TestClinic_RecordEncounter_LegacyEncounterWithoutDocumentationIsAFirstRecord
//     — "a record exists" is a live .documentation carrying documentedAt, never
//     .encounter's presence: a pre-split .encounter with no .documentation
//     sibling takes the first-record arm (superseded: [], no amendedAt).
//  2. TestClinic_RecordEncounter_TombstonedDocumentationIsAFirstRecord — a
//     tombstoned .documentation is no record either, and neither is a live
//     one carrying no documentedAt (the lens presence rule, exactly).
//  3. TestClinic_RecordEncounter_PreFireRecordAmendsFromAnEmptyHistory — a
//     record whose plaintext predates the superseded key reads as an empty
//     history; the amendment appends exactly the text it replaced, recorded
//     at the prior documentedAt.
//  4. TestClinic_RecordEncounter_AmendmentLimit — past MAX_ENCOUNTER_AMENDMENTS
//     (40) superseded versions the op refuses AmendmentLimit and writes
//     nothing; at 39 the amendment lands and fills the bound.
//  5. TestClinic_RecordEncounter_TextBound — a text past 4000 BYTES is
//     InvalidArgument before any read (a multi-byte character spends more than
//     one byte); exactly 4000 lands.
//  6. TestClinic_RecordEncounter_FollowUpOnlyChangeIsNotAnAmendment — the same
//     three texts with a changed follow-up rewrite .documentation alone:
//     .encounter is not written, superseded and amendedAt stand.
//
// The prior record is seeded straight into Core KV under the package's own
// clinicalRecord retention-class holder — encrypted exactly as step 6.5
// commits it — because the shapes here are ones the op itself can no longer
// produce (a pre-fire plaintext, a 50-entry history without 50 submits).

// clClinicalRecordHolder is the vertex whose .piiKey custodies every
// .encounter DEK (CustodyKindRetentionClass on the clinicalRecord class).
func clClinicalRecordHolder() string {
	return pkgmgr.RetentionClassKey("clinic-domain", "clinicalRecord")
}

// clSeedEncryptedEncounter writes an appointment's SENSITIVE .encounter aspect
// directly, ciphertext-encoded as the Processor's step 6.5 would commit it —
// minting the retention-class holder's piiKey through the shared TestVault
// when no RecordEncounter has minted it yet (every TestVault shares one KEK,
// so the pipeline's own instance opens what this one wraps). Mirrors
// identity-domain's seedSensitiveAspect, generalized to a non-identity holder.
func clSeedEncryptedEncounter(t *testing.T, ctx context.Context, conn *substrate.Conn, apptKey string, plaintext map[string]any) {
	t.Helper()
	v := testutil.TestVault(t)
	holderKey := clClinicalRecordHolder()
	env := clEnsureHolderPiiKey(t, ctx, conn, v, holderKey)
	pt, err := json.Marshal(plaintext)
	if err != nil {
		t.Fatalf("marshal encounter plaintext: %v", err)
	}
	ct, err := v.Encrypt(ctx, holderKey, env, pt)
	if err != nil {
		t.Fatalf("encrypt %s.encounter: %v", apptKey, err)
	}
	doc := map[string]any{"class": "appointmentEncounter", "isDeleted": false,
		"vertexKey": apptKey, "localName": "encounter", "data": ct}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, apptKey+".encounter", b); err != nil {
		t.Fatalf("seed %s.encounter: %v", apptKey, err)
	}
}

func clEnsureHolderPiiKey(t *testing.T, ctx context.Context, conn *substrate.Conn, v vault.Vault, holderKey string) vault.Envelope {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, holderKey+".piiKey")
	if err == nil {
		var doc struct {
			Data vault.Envelope `json:"data"`
		}
		if uerr := json.Unmarshal(entry.Value, &doc); uerr != nil {
			t.Fatalf("unmarshal piiKey for %s: %v", holderKey, uerr)
		}
		return doc.Data
	}
	if !errors.Is(err, substrate.ErrKeyNotFound) {
		t.Fatalf("read piiKey for %s: %v", holderKey, err)
	}
	env, err := v.CreateIdentityKey(ctx, holderKey)
	if err != nil {
		t.Fatalf("CreateIdentityKey for %s: %v", holderKey, err)
	}
	doc := map[string]any{"class": "piiKey", "vertexKey": holderKey, "localName": "piiKey", "isDeleted": false, "data": env}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, holderKey+".piiKey", b); err != nil {
		t.Fatalf("seed piiKey for %s: %v", holderKey, err)
	}
	return env
}

// clBookStartedVisit books one appointment at startsAt and returns its key;
// every RecordEncounter below submits at or after that instant.
func clBookStartedVisit(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, patientKey, providerKey, startsAt, endsAt string) string {
	t.Helper()
	apptID := clSubmit(t, ctx, conn, cp, cons, label, "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"`+startsAt+`","endsAt":"`+endsAt+`"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	return "vtx.appointment." + apptID
}

func TestClinic_RecordEncounter_LegacyEncounterWithoutDocumentationIsAFirstRecord(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "encounter-legacy")

	patientKey := createPatient(t, ctx, conn, cp, cons, "amdpat0001", "Legacy Patient")
	providerKey := createProvider(t, ctx, conn, cp, cons, "amdprv0001", "Dr. Legacy", "Family")
	apptKey := clBookStartedVisit(t, ctx, conn, cp, cons, "amdappt0001", patientKey, providerKey, "2026-07-10T09:00:00Z", "2026-07-10T09:30:00Z")

	// The pre-split shape: the operational signals INSIDE .encounter, no
	// .documentation sibling.
	clSeedEncryptedEncounter(t, ctx, conn, apptKey, map[string]any{
		"documentedAt": "2026-07-10T09:20:00Z", "followUpRequested": false, "summary": "Pre-split note."})
	if !clMissing(t, ctx, conn, apptKey+".documentation") {
		t.Fatalf("precondition: no .documentation sibling")
	}

	reads, optionalReads := clRecordEncounterReads(apptKey)
	const at = "2026-07-10T09:40:00Z"
	clSubmitAt(t, ctx, conn, cp, cons, "amdrec0001", "RecordEncounter", "appointment",
		`{"appointmentKey":"`+apptKey+`","summary":"Documented after the split.","followUpRequested":false}`,
		at, reads, optionalReads, processor.OutcomeAccepted)

	data := clDecryptEncounter(t, ctx, conn, apptKey)
	if data["summary"] != "Documented after the split." {
		t.Fatalf("summary = %v", data["summary"])
	}
	if got := clSuperseded(t, data); len(got) != 0 {
		t.Fatalf("a first record carries no history; superseded = %v", got)
	}
	if _, present := data["documentedAt"]; present {
		t.Fatalf("the first record replaces the legacy plaintext whole; documentedAt still inside .encounter: %v", data)
	}
	docData, _ := clReadDoc(t, ctx, conn, apptKey+".documentation")["data"].(map[string]any)
	if docData["documentedAt"] != at {
		t.Fatalf("documentedAt = %v, want %s (this op's submittedAt — the first record)", docData["documentedAt"], at)
	}
	if v, present := docData["amendedAt"]; present {
		t.Fatalf("a first record carries no amendedAt, got %v", v)
	}
}

func TestClinic_RecordEncounter_TombstonedDocumentationIsAFirstRecord(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "encounter-tombstoned-doc")

	patientKey := createPatient(t, ctx, conn, cp, cons, "amdpat0002", "Tombstoned Doc")
	providerKey := createProvider(t, ctx, conn, cp, cons, "amdprv0002", "Dr. Tomb", "Family")
	apptKey := clBookStartedVisit(t, ctx, conn, cp, cons, "amdappt0002", patientKey, providerKey, "2026-07-10T09:00:00Z", "2026-07-10T09:30:00Z")

	clSeedEncryptedEncounter(t, ctx, conn, apptKey, map[string]any{"summary": "Old.", "assessment": "", "plan": "", "superseded": []any{}})
	deleted := map[string]any{"class": "appointmentDocumentation", "isDeleted": true,
		"vertexKey": apptKey, "localName": "documentation",
		"data": map[string]any{"documentedAt": "2026-07-10T09:20:00Z", "followUpRequested": false}}
	b, _ := json.Marshal(deleted)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, apptKey+".documentation", b); err != nil {
		t.Fatalf("seed tombstoned documentation: %v", err)
	}

	reads, optionalReads := clRecordEncounterReads(apptKey)
	const at = "2026-07-10T09:40:00Z"
	clSubmitAt(t, ctx, conn, cp, cons, "amdrec0002", "RecordEncounter", "appointment",
		`{"appointmentKey":"`+apptKey+`","summary":"Fresh.","followUpRequested":false}`,
		at, reads, optionalReads, processor.OutcomeAccepted)
	data := clDecryptEncounter(t, ctx, conn, apptKey)
	if got := clSuperseded(t, data); len(got) != 0 {
		t.Fatalf("a tombstoned .documentation is no record; superseded = %v, want []", got)
	}
	docData, _ := clReadDoc(t, ctx, conn, apptKey+".documentation")["data"].(map[string]any)
	if docData["documentedAt"] != at {
		t.Fatalf("documentedAt = %v, want %s (a first record over a tombstoned one)", docData["documentedAt"], at)
	}
	if v, present := docData["amendedAt"]; present {
		t.Fatalf("a first record carries no amendedAt, got %v", v)
	}
}

func TestClinic_RecordEncounter_DocumentationWithoutDocumentedAtIsAFirstRecord(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "encounter-undated-doc")

	patientKey := createPatient(t, ctx, conn, cp, cons, "amdpat0006", "Undated Doc")
	providerKey := createProvider(t, ctx, conn, cp, cons, "amdprv0006", "Dr. Undated", "Family")
	apptKey := clBookStartedVisit(t, ctx, conn, cp, cons, "amdappt0006", patientKey, providerKey, "2026-07-10T09:00:00Z", "2026-07-10T09:30:00Z")

	// A live .documentation with no documentedAt is not a record: the lenses'
	// presence signal is documentedAt itself, so the op takes the first-record
	// arm and the encounter beside it is replaced whole rather than amended.
	clSeedEncryptedEncounter(t, ctx, conn, apptKey, map[string]any{"summary": "Old.", "assessment": "", "plan": "", "superseded": []any{}})
	clSeedAspect(t, ctx, conn, apptKey, "documentation", "appointmentDocumentation", map[string]any{"followUpRequested": false})

	reads, optionalReads := clRecordEncounterReads(apptKey)
	const at = "2026-07-10T09:40:00Z"
	clSubmitAt(t, ctx, conn, cp, cons, "amdrec0007", "RecordEncounter", "appointment",
		`{"appointmentKey":"`+apptKey+`","summary":"Fresh.","followUpRequested":false}`,
		at, reads, optionalReads, processor.OutcomeAccepted)
	data := clDecryptEncounter(t, ctx, conn, apptKey)
	if got := clSuperseded(t, data); len(got) != 0 {
		t.Fatalf("an undated .documentation is no record; superseded = %v, want []", got)
	}
	docData, _ := clReadDoc(t, ctx, conn, apptKey+".documentation")["data"].(map[string]any)
	if docData["documentedAt"] != at {
		t.Fatalf("documentedAt = %v, want %s (a first record over an undated one)", docData["documentedAt"], at)
	}
	if v, present := docData["amendedAt"]; present {
		t.Fatalf("a first record carries no amendedAt, got %v", v)
	}
}

func TestClinic_RecordEncounter_PreFireRecordAmendsFromAnEmptyHistory(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "encounter-prefire")

	patientKey := createPatient(t, ctx, conn, cp, cons, "amdpat0003", "Prefire Patient")
	providerKey := createProvider(t, ctx, conn, cp, cons, "amdprv0003", "Dr. Prefire", "Family")
	apptKey := clBookStartedVisit(t, ctx, conn, cp, cons, "amdappt0003", patientKey, providerKey, "2026-07-10T09:00:00Z", "2026-07-10T09:30:00Z")

	// The post-split, pre-history shape: the three texts and nothing else,
	// with the .documentation sibling the split writes.
	clSeedEncryptedEncounter(t, ctx, conn, apptKey, map[string]any{"summary": "Seen, stable.", "assessment": "Stable.", "plan": "None."})
	clSeedAspect(t, ctx, conn, apptKey, "documentation", "appointmentDocumentation",
		map[string]any{"documentedAt": "2026-07-10T09:20:00Z", "followUpRequested": false})

	reads, optionalReads := clRecordEncounterReads(apptKey)
	const at = "2026-07-11T09:00:00Z"
	clSubmitAt(t, ctx, conn, cp, cons, "amdrec0003", "RecordEncounter", "appointment",
		`{"appointmentKey":"`+apptKey+`","summary":"Seen, stable; BP re-taken.","assessment":"Stable.","plan":"None.","followUpRequested":false}`,
		at, reads, optionalReads, processor.OutcomeAccepted)
	data := clDecryptEncounter(t, ctx, conn, apptKey)
	if data["summary"] != "Seen, stable; BP re-taken." {
		t.Fatalf("summary = %v", data["summary"])
	}
	superseded := clSuperseded(t, data)
	if len(superseded) != 1 {
		t.Fatalf("a missing superseded key reads as []; after one amendment want one entry, got %v", superseded)
	}
	clAssertSupersededEntry(t, superseded[0], "Seen, stable.", "Stable.", "None.", "2026-07-10T09:20:00Z", "pre-fire text")
	docData, _ := clReadDoc(t, ctx, conn, apptKey+".documentation")["data"].(map[string]any)
	if docData["documentedAt"] != "2026-07-10T09:20:00Z" || docData["amendedAt"] != at {
		t.Fatalf("documentedAt = %v amendedAt = %v, want 2026-07-10T09:20:00Z / %s", docData["documentedAt"], docData["amendedAt"], at)
	}
}

func TestClinic_RecordEncounter_AmendmentLimit(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "encounter-limit")

	patientKey := createPatient(t, ctx, conn, cp, cons, "amdpat0004", "Bounded Patient")
	providerKey := createProvider(t, ctx, conn, cp, cons, "amdprv0004", "Dr. Bound", "Family")
	history := func(n int) []any {
		out := make([]any, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, map[string]any{"summary": fmt.Sprintf("Version %d.", i), "assessment": "", "plan": "",
				"recordedAt": fmt.Sprintf("2026-07-10T%02d:%02d:00Z", 10+i/60, i%60)})
		}
		return out
	}
	seed := func(label, startsAt, endsAt string, n int) string {
		apptKey := clBookStartedVisit(t, ctx, conn, cp, cons, label, patientKey, providerKey, startsAt, endsAt)
		clSeedEncryptedEncounter(t, ctx, conn, apptKey, map[string]any{"summary": "Current.", "assessment": "", "plan": "", "superseded": history(n)})
		clSeedAspect(t, ctx, conn, apptKey, "documentation", "appointmentDocumentation",
			map[string]any{"documentedAt": "2026-07-10T09:20:00Z", "amendedAt": "2026-07-12T09:00:00Z", "followUpRequested": false})
		return apptKey
	}
	amend := `,"summary":"One more.","followUpRequested":false}`

	// AT the bound: refused, and neither aspect moves.
	full := seed("amdappt0004", "2026-07-10T09:00:00Z", "2026-07-10T09:30:00Z", 40)
	encRev, docRev := clRevision(t, ctx, conn, full+".encounter"), clRevision(t, ctx, conn, full+".documentation")
	reads, optionalReads := clRecordEncounterReads(full)
	reason := clStaffReason(t, ctx, conn, cp, cons, "amdrec0004", "RecordEncounter",
		`{"appointmentKey":"`+full+`"`+amend, "2026-07-13T09:00:00Z", reads, optionalReads)
	if !strings.HasPrefix(reason, "AmendmentLimit:") {
		t.Fatalf("amendment past the bound: reason = %q, want AmendmentLimit", reason)
	}
	if got := clRevision(t, ctx, conn, full+".encounter"); got != encRev {
		t.Fatalf("a refused amendment moved .encounter %d → %d", encRev, got)
	}
	if got := clRevision(t, ctx, conn, full+".documentation"); got != docRev {
		t.Fatalf("a refused amendment moved .documentation %d → %d", docRev, got)
	}
	// An IDENTICAL re-submit at the bound is still the no-op, not the refusal:
	// nothing changes, so nothing is amended.
	clSubmitAt(t, ctx, conn, cp, cons, "amdrec0005", "RecordEncounter", "appointment",
		`{"appointmentKey":"`+full+`","summary":"Current.","followUpRequested":false}`,
		"2026-07-13T09:00:00Z", reads, optionalReads, processor.OutcomeAccepted)
	if got := clRevision(t, ctx, conn, full+".encounter"); got != encRev {
		t.Fatalf("an identical re-submit at the bound moved .encounter %d → %d", encRev, got)
	}

	// ONE BELOW the bound: the amendment lands and fills it — the entry it
	// appends carries the prior amendedAt as its recordedAt.
	almost := seed("amdappt0005", "2026-07-10T10:00:00Z", "2026-07-10T10:30:00Z", 39)
	reads, optionalReads = clRecordEncounterReads(almost)
	clSubmitAt(t, ctx, conn, cp, cons, "amdrec0006", "RecordEncounter", "appointment",
		`{"appointmentKey":"`+almost+`"`+amend, "2026-07-13T09:00:00Z", reads, optionalReads, processor.OutcomeAccepted)
	data := clDecryptEncounter(t, ctx, conn, almost)
	superseded := clSuperseded(t, data)
	if len(superseded) != 40 {
		t.Fatalf("after the 40th amendment superseded has %d entries, want 40", len(superseded))
	}
	clAssertSupersededEntry(t, superseded[0], "Version 0.", "", "", "2026-07-10T10:00:00Z", "oldest entry (unchanged)")
	clAssertSupersededEntry(t, superseded[39], "Current.", "", "", "2026-07-12T09:00:00Z", "newest entry")
	if data["summary"] != "One more." {
		t.Fatalf("summary = %v, want One more.", data["summary"])
	}
}

func TestClinic_RecordEncounter_TextBound(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "encounter-textbound")

	patientKey := createPatient(t, ctx, conn, cp, cons, "amdpat0007", "Long Note")
	providerKey := createProvider(t, ctx, conn, cp, cons, "amdprv0007", "Dr. Long", "Family")
	apptKey := clBookStartedVisit(t, ctx, conn, cp, cons, "amdappt0007", patientKey, providerKey, "2026-07-10T09:00:00Z", "2026-07-10T09:30:00Z")
	reads, optionalReads := clRecordEncounterReads(apptKey)

	// 3999 ASCII bytes plus one two-byte character: 4000 characters, 4001
	// BYTES — refused on each of the three fields, before any read, so no
	// record is written.
	over := strings.Repeat("a", 3999) + "é"
	if len(over) != 4001 {
		t.Fatalf("fixture: len = %d, want 4001 bytes", len(over))
	}
	for i, field := range []string{"summary", "assessment", "plan"} {
		payload := map[string]any{"appointmentKey": apptKey, "summary": "ok", field: over}
		b, _ := json.Marshal(payload)
		reason := clStaffReason(t, ctx, conn, cp, cons, fmt.Sprintf("amdlong%03d", i+1), "RecordEncounter", string(b), "2026-07-10T09:00:00Z", reads, optionalReads)
		if !strings.HasPrefix(reason, "InvalidArgument: "+field+" exceeds 4000 bytes") {
			t.Fatalf("%s at 4001 bytes: reason = %q, want InvalidArgument naming the field and the bound", field, reason)
		}
	}
	if !clMissing(t, ctx, conn, apptKey+".documentation") {
		t.Fatalf("a refused over-long note must write no record")
	}

	// Exactly 4000 bytes (3998 ASCII + one two-byte character) lands.
	exact := strings.Repeat("a", 3998) + "é"
	if len(exact) != 4000 {
		t.Fatalf("fixture: len = %d, want 4000 bytes", len(exact))
	}
	b, _ := json.Marshal(map[string]any{"appointmentKey": apptKey, "summary": exact})
	clSubmitAt(t, ctx, conn, cp, cons, "amdlong004", "RecordEncounter", "appointment", string(b),
		"2026-07-10T09:00:00Z", reads, optionalReads, processor.OutcomeAccepted)
	if data := clDecryptEncounter(t, ctx, conn, apptKey); data["summary"] != exact {
		t.Fatalf("a 4000-byte summary must be recorded verbatim")
	}
}

func TestClinic_RecordEncounter_FollowUpOnlyChangeIsNotAnAmendment(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "encounter-followup-only")

	patientKey := createPatient(t, ctx, conn, cp, cons, "amdpat0008", "Follow Up")
	providerKey := createProvider(t, ctx, conn, cp, cons, "amdprv0008", "Dr. Follow", "Family")
	apptKey := clBookStartedVisit(t, ctx, conn, cp, cons, "amdappt0008", patientKey, providerKey, "2026-07-10T09:00:00Z", "2026-07-10T09:30:00Z")
	reads, optionalReads := clRecordEncounterReads(apptKey)

	// First record, then one TEXT amendment so the record carries an amendedAt
	// and one superseded entry.
	clSubmitAt(t, ctx, conn, cp, cons, "amdfu0001", "RecordEncounter", "appointment",
		`{"appointmentKey":"`+apptKey+`","summary":"Seen.","followUpRequested":false}`,
		"2026-07-10T09:20:00Z", reads, optionalReads, processor.OutcomeAccepted)
	clSubmitAt(t, ctx, conn, cp, cons, "amdfu0002", "RecordEncounter", "appointment",
		`{"appointmentKey":"`+apptKey+`","summary":"Seen; stable.","followUpRequested":false}`,
		"2026-07-11T09:00:00Z", reads, optionalReads, processor.OutcomeAccepted)
	encRev := clRevision(t, ctx, conn, apptKey+".encounter")
	docRev := clRevision(t, ctx, conn, apptKey+".documentation")

	// The same text with a follow-up added: .documentation is rewritten (the
	// follow-up fields are the payload's), documentedAt and the prior
	// amendedAt are carried, and .encounter is left out of the batch — its
	// revision and its history do not move.
	clSubmitAt(t, ctx, conn, cp, cons, "amdfu0003", "RecordEncounter", "appointment",
		`{"appointmentKey":"`+apptKey+`","summary":"Seen; stable.","followUpRequested":true,"followUpDate":"2027-01-15"}`,
		"2026-07-12T09:00:00Z", reads, optionalReads, processor.OutcomeAccepted)
	if got := clRevision(t, ctx, conn, apptKey+".encounter"); got != encRev {
		t.Fatalf("a follow-up-only change wrote .encounter (%d → %d); want untouched", encRev, got)
	}
	if got := clRevision(t, ctx, conn, apptKey+".documentation"); got == docRev {
		t.Fatalf("a follow-up-only change must rewrite .documentation (revision stayed %d)", docRev)
	}
	docData, _ := clReadDoc(t, ctx, conn, apptKey+".documentation")["data"].(map[string]any)
	if docData["documentedAt"] != "2026-07-10T09:20:00Z" || docData["amendedAt"] != "2026-07-11T09:00:00Z" {
		t.Fatalf("documentedAt = %v amendedAt = %v, want 2026-07-10T09:20:00Z / 2026-07-11T09:00:00Z (both carried)", docData["documentedAt"], docData["amendedAt"])
	}
	if docData["followUpRequested"] != true || docData["followUpDate"] != "2027-01-15T09:00:00Z" {
		t.Fatalf("follow-up fields = %v / %v, want true / 2027-01-15T09:00:00Z", docData["followUpRequested"], docData["followUpDate"])
	}
	data := clDecryptEncounter(t, ctx, conn, apptKey)
	if data["summary"] != "Seen; stable." {
		t.Fatalf("summary = %v", data["summary"])
	}
	superseded := clSuperseded(t, data)
	if len(superseded) != 1 {
		t.Fatalf("superseded = %v, want the one text amendment's entry only", superseded)
	}
	clAssertSupersededEntry(t, superseded[0], "Seen.", "", "", "2026-07-10T09:20:00Z", "the text amendment's entry (unchanged)")

	// A record never text-amended carries no amendedAt through a follow-up-only
	// change either: absent stays absent.
	appt2 := clBookStartedVisit(t, ctx, conn, cp, cons, "amdappt0009", patientKey, providerKey, "2026-07-10T10:00:00Z", "2026-07-10T10:30:00Z")
	reads2, optionalReads2 := clRecordEncounterReads(appt2)
	clSubmitAt(t, ctx, conn, cp, cons, "amdfu0004", "RecordEncounter", "appointment",
		`{"appointmentKey":"`+appt2+`","summary":"Seen.","followUpRequested":true,"followUpDate":"2027-01-15"}`,
		"2026-07-10T10:20:00Z", reads2, optionalReads2, processor.OutcomeAccepted)
	clSubmitAt(t, ctx, conn, cp, cons, "amdfu0005", "RecordEncounter", "appointment",
		`{"appointmentKey":"`+appt2+`","summary":"Seen.","followUpRequested":false}`,
		"2026-07-11T10:00:00Z", reads2, optionalReads2, processor.OutcomeAccepted)
	docData, _ = clReadDoc(t, ctx, conn, appt2+".documentation")["data"].(map[string]any)
	if v, present := docData["amendedAt"]; present {
		t.Fatalf("a follow-up-only change on a never-amended record must not mint amendedAt, got %v", v)
	}
	if _, present := docData["followUpDate"]; present || docData["followUpRequested"] != false {
		t.Fatalf("follow-up dropped: fields = %v", docData)
	}
}
