package health_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/health/healthwire"
)

// TestReporter_WholesaleWriters_CarryEveryEntryFieldForward gates the three
// WHOLESALE writers of a health Entry — SetActive, SetPaused, SetRebuilding —
// against silently dropping a field. Each builds a fresh Entry and enumerates
// every field it carries forward, so a field nobody wrote a line for is reset to
// its zero value on the next status transition, with nothing failing anywhere.
//
// The universe of fields is discovered by REFLECTION over healthwire.Entry
// rather than a maintained list, so a newly added field fails this test by name
// until its author decides which of the two things it is: carried forward, or
// owned by the writer. There is no third state, and "nobody thought about it"
// is not one of them.
//
// It exists because the class has now bitten twice. The consumer-filter
// footprint (filterMode/filterLabelCount/filterBroadReason) was added to Entry
// with no carry-forward line and was erased by the very rebuild that derives it;
// the structural-auto-recovery record (structuralAutoRecovered*) was added the
// same way and was erased by the re-pause of the flapping lens it exists to
// report. Each fix minted a hand-written test for its own fields. A third would
// have been a third blind spot: those tests pin BEHAVIOUR for the fields that
// have already burned us, this one pins COMPLETENESS for every field there is.
// Keep all three.
func TestReporter_WholesaleWriters_CarryEveryEntryFieldForward(t *testing.T) {
	ctx := context.Background()
	kv := startHealthKV(t)

	writers := []struct {
		name   string
		invoke func(*health.Reporter) error
	}{
		{"SetActive", func(r *health.Reporter) error { return r.SetActive(ctx) }},
		{"SetPaused", func(r *health.Reporter) error { return r.SetPaused(ctx, "structural", "a diagnosis") }},
		{"SetRebuilding", func(r *health.Reporter) error { return r.SetRebuilding(ctx) }},
	}

	t.Run("AllowListIsNotStale", func(t *testing.T) {
		declared := map[string]bool{}
		typ := reflect.TypeOf(healthwire.Entry{})
		for i := range typ.NumField() {
			declared[typ.Field(i).Name] = true
		}
		var stale []string
		for name := range writerOwnedEntryFields {
			if !declared[name] {
				stale = append(stale, name)
			}
		}
		sort.Strings(stale)
		if len(stale) > 0 {
			t.Fatalf("writerOwnedEntryFields names fields that healthwire.Entry no longer declares: %v.\n"+
				"The field was renamed or removed; delete the stale entry, or the allow-list will keep "+
				"excusing a field that does not exist while its replacement goes ungated.", stale)
		}
	})

	for _, w := range writers {
		t.Run(w.name, func(t *testing.T) {
			ruleID := "carry-" + w.name
			seeded := seedEntry(t, ruleID)
			data, err := json.Marshal(seeded)
			require.NoError(t, err)
			_, err = kv.Put(ctx, ruleID, data)
			require.NoError(t, err)

			r := health.New(kv, ruleID)
			require.NoError(t, w.invoke(r))

			got, err := r.GetStatus(ctx)
			require.NoError(t, err)

			seededVal := reflect.ValueOf(seeded)
			gotVal := reflect.ValueOf(got)
			typ := reflect.TypeOf(healthwire.Entry{})
			for i := range typ.NumField() {
				field := typ.Field(i).Name
				if _, owned := writerOwnedEntryFields[field]; owned {
					continue
				}
				want := seededVal.Field(i).Interface()
				have := gotVal.Field(i).Interface()
				if !reflect.DeepEqual(want, have) {
					t.Errorf("%s", carryForwardFailure(field, w.name, want, have))
				}
			}
		})
	}
}

// carryForwardFailure is the whole product of this test: an author who has never
// seen this rule has to be able to act on it without reading the test.
func carryForwardFailure(field, writer string, want, have any) string {
	return fmt.Sprintf(`health.Entry.%[1]s is DROPPED by %[2]s — seeded %[3]v, came back %[4]v.

%[2]s builds a fresh Entry and lists every field it carries forward, so a field with no
line of its own is silently reset on every status transition. Nothing else fails when
this happens: the entry stays well-formed, and the value is simply gone.

Fix it one of two ways:

  1. Carry it forward — add "%[1]s: existing.%[1]s," to the Entry literal in %[2]s
     (internal/refractor/health/reporter.go), beside the neighbours that already do, with
     a line saying why the value outlives a status transition. This is the right answer
     for anything OBSERVED or CUMULATIVE: a status transition observes nothing, so writing
     a zero there asserts something no read established.

  2. Add %[1]q to writerOwnedEntryFields in this file, with a one-line reason — but only
     if the writer genuinely DECIDES this field's value, the way it decides Status or
     LastUpdated. "The poller will rewrite it soon" is not a reason: a paused lens polls
     nothing, so the zero can stand indefinitely.

Do not delete or weaken this assertion. It exists because this exact class has shipped
twice already — the consumer-filter footprint and the structural-auto-recovery record were
both added to Entry with no carry-forward line, and both were erased by the very operation
that made them worth reading.`, field, writer, want, have)
}

// writerOwnedEntryFields are the Entry fields the three wholesale writers
// legitimately DECIDE rather than carry forward. Each is owned by all three:
// they are what a status write is FOR, so preserving them would defeat the call.
// Every other field must survive every writer, and the test above fails by name
// until a new field joins one side or the other.
var writerOwnedEntryFields = map[string]string{
	"Status":      "the transition itself — active / paused / rebuilding is what the call exists to write",
	"PauseReason": "set with Status: non-nil only on SetPaused, explicit JSON null on the other two",
	"LastError":   "the pause's diagnosis, from SetPaused's argument; nulled by SetActive/SetRebuilding as part of clearing the pause",
	"LastUpdated": "stamped fresh on every write — a carried-forward timestamp would claim the entry is older than it is",
	"RuleID":      "re-stamped from the Reporter's own ruleID, so a malformed or truncated entry is repaired rather than propagated",
	"ActiveSequence": "sourced from the Reporter's in-memory rule sequence (SetRuleSequence), which is the authority; " +
		"the persisted value is an echo of it",
	"RuleEngine": "sourced from the Reporter's in-memory engine name (SetRuleEngine), same as ActiveSequence",
}

// seedEntry builds an Entry with a distinctive non-zero value in every field, so
// a dropped field is visible as a zero rather than hidden behind a value that
// happened to match. A field whose type this cannot seed fails the test rather
// than being skipped: an unseeded field would pass the comparison trivially and
// so be gated by nothing at all.
func seedEntry(t *testing.T, ruleID string) healthwire.Entry {
	t.Helper()
	var entry healthwire.Entry
	v := reflect.ValueOf(&entry).Elem()
	typ := v.Type()
	for i := range typ.NumField() {
		field := typ.Field(i)
		fv := v.Field(i)
		switch fv.Kind() {
		case reflect.String:
			fv.SetString("seeded-" + field.Name)
		case reflect.Uint64:
			fv.SetUint(uint64(i) + 1)
		case reflect.Int:
			fv.SetInt(int64(i) + 1)
		case reflect.Ptr:
			if fv.Type().Elem().Kind() != reflect.String {
				t.Fatalf("seedEntry cannot seed healthwire.Entry.%s (%s): teach this function that type, "+
					"or the field ships gated by nothing — an unseeded field compares equal to itself and always passes",
					field.Name, field.Type)
			}
			s := "seeded-" + field.Name
			fv.Set(reflect.ValueOf(&s))
		default:
			t.Fatalf("seedEntry cannot seed healthwire.Entry.%s (%s): teach this function that type, "+
				"or the field ships gated by nothing — an unseeded field compares equal to itself and always passes",
				field.Name, field.Type)
		}
	}
	entry.RuleID = ruleID
	return entry
}
