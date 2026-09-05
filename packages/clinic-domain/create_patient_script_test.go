// Script-level proof of CreatePatient's registration-site branch — the one
// branch whose input is the ACTOR's own topology rather than the payload, so
// the pipeline tests (which submit as one fixed staff identity) cannot vary it.
// The runner is driven directly with a fake kv.Links seam, the
// orchestration-base claim_task_script_test.go idiom.
package clinicdomain_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/processor"
	clinicdomain "github.com/operatinggraph/lattice/packages/clinic-domain"
)

const (
	cpScriptPatientID  = "CPscrPatientHJKMNPQR"
	cpScriptActorID    = "CPscrActorxHJKMNPQRS"
	cpScriptSiteAID    = "CPscrSiteAxHJKMNPQRS"
	cpScriptSiteBID    = "CPscrSiteBxHJKMNPQRS"
	cpScriptActorKey   = "vtx.identity." + cpScriptActorID
	cpScriptPatientKey = "vtx.patient." + cpScriptPatientID
)

// cpPatientScript returns the patient DDL's Starlark source.
func cpPatientScript(t *testing.T) string {
	t.Helper()
	for _, d := range clinicdomain.Package.DDLs {
		if d.CanonicalName == "patient" {
			return d.Script
		}
	}
	t.Fatal("patient vertexType DDL not found in clinic-domain")
	return ""
}

// cpLinkLister is an in-memory processor.ScriptLinkLister returning one canned
// page regardless of the filter — each test configures exactly the worksAt
// links CreatePatient's kv.Links(op.actor, "worksAt", "out") call should see.
type cpLinkLister struct {
	links []processor.LinkDoc
}

func (l cpLinkLister) ListLinks(_ context.Context, _, _ string, _ int) ([]processor.LinkDoc, string, error) {
	return l.links, "", nil
}

// cpWorksAt builds one worksAt link doc from the fixed actor to a building.
func cpWorksAt(buildingID string, deleted bool) processor.LinkDoc {
	return processor.LinkDoc{
		Key:          "lnk.identity." + cpScriptActorID + ".worksAt.building." + buildingID,
		Class:        "worksAt",
		IsDeleted:    deleted,
		SourceVertex: cpScriptActorKey,
		TargetVertex: "vtx.building." + buildingID,
	}
}

// runCreatePatientAs executes CreatePatient under an arbitrary actor key with
// the given worksAt enumeration, and returns the mutations it wrote.
func runCreatePatientAs(t *testing.T, actorKey string, links []processor.LinkDoc) []processor.MutationOp {
	t.Helper()
	result, err := processor.NewStarlarkRunner(0, 0).Run(context.Background(), processor.ScriptContext{
		Operation: &processor.OperationEnvelope{
			RequestID:     "Hj4kPmRtw9nbCxz5vQ2y",
			Lane:          processor.LaneDefault,
			OperationType: "CreatePatient",
			Actor:         actorKey,
			SubmittedAt:   "2026-05-22T10:00:00Z",
			Payload:       json.RawMessage(`{"fullName":"Alice Rivera","patientId":"` + cpScriptPatientID + `"}`),
			// The class-(e) enumeration the descriptor declares as metadata
			// (opmetas.go — hub {actor}, relation worksAt, direction out).
			ContextHint: &processor.ContextHint{
				Enumerations: []processor.EnumerationHint{
					{Hub: actorKey, Relation: "worksAt", Direction: "out"},
				},
			},
		},
		Hydrated:     map[string]processor.VertexDoc{},
		DDLLookup:    map[string]processor.MetaVertex{},
		ScriptSource: cpPatientScript(t),
		ScriptClass:  "patient",
		LinkLister:   cpLinkLister{links: links},
	})
	if err != nil {
		t.Fatalf("CreatePatient as %s: %v", actorKey, err)
	}
	return result.Mutations
}

// cpSiteLinks narrows a mutation set to the registeredAtSite links.
func cpSiteLinks(muts []processor.MutationOp) []processor.MutationOp {
	var out []processor.MutationOp
	for _, m := range muts {
		if strings.Contains(m.Key, ".registeredAtSite.") {
			out = append(out, m)
		}
	}
	return out
}

// TestCreatePatientScript_RecordsOneSitePerLiveWorkplace — the registration
// site is a FACT recorded on the patient, one link per building the submitting
// staffer worked at when they typed the patient in. The patient (later-
// arriving) is the source, the pre-existing building the target, and the
// relation reads as the sentence "patient registeredAtSite building".
// clinicPatientsRead reads exactly these links as the roster row's
// pre-appointment workplace anchor.
func TestCreatePatientScript_RecordsOneSitePerLiveWorkplace(t *testing.T) {
	muts := runCreatePatientAs(t, cpScriptActorKey, []processor.LinkDoc{
		cpWorksAt(cpScriptSiteAID, false),
		cpWorksAt(cpScriptSiteBID, false),
	})

	sites := cpSiteLinks(muts)
	if len(sites) != 2 {
		t.Fatalf("wrote %d registeredAtSite links, want one per live worksAt building: %v", len(sites), muts)
	}
	want := map[string]string{
		"lnk.patient." + cpScriptPatientID + ".registeredAtSite.building." + cpScriptSiteAID: "vtx.building." + cpScriptSiteAID,
		"lnk.patient." + cpScriptPatientID + ".registeredAtSite.building." + cpScriptSiteBID: "vtx.building." + cpScriptSiteBID,
	}
	for _, m := range sites {
		target, ok := want[m.Key]
		if !ok {
			t.Fatalf("unexpected registeredAtSite key %s, want one of %v", m.Key, want)
		}
		delete(want, m.Key)
		if m.Op != "create" {
			t.Errorf("%s: op = %q, want create", m.Key, m.Op)
		}
		if m.Document["class"] != "registeredAtSite" {
			t.Errorf("%s: class = %v, want registeredAtSite", m.Key, m.Document["class"])
		}
		if m.Document["localName"] != "registeredAtSite" {
			t.Errorf("%s: localName = %v, want registeredAtSite", m.Key, m.Document["localName"])
		}
		if m.Document["sourceVertex"] != cpScriptPatientKey {
			t.Errorf("%s: sourceVertex = %v, want the PATIENT (later-arriving source)", m.Key, m.Document["sourceVertex"])
		}
		if m.Document["targetVertex"] != target {
			t.Errorf("%s: targetVertex = %v, want %s", m.Key, m.Document["targetVertex"], target)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing registeredAtSite links: %v", want)
	}
}

// TestCreatePatientScript_SkipsTombstonedWorkplace — UnwireWorksAt tombstones
// rather than deletes, and kv.Links returns a tombstoned link with isDeleted
// set rather than omitting it (internal/processor's
// TestKVLinks_TombstonedReturned). A staffer who has already moved on from a
// building therefore records no site there, while the building they still work
// at IS recorded — the live half is what stops this passing for the wrong
// reason.
func TestCreatePatientScript_SkipsTombstonedWorkplace(t *testing.T) {
	muts := runCreatePatientAs(t, cpScriptActorKey, []processor.LinkDoc{
		cpWorksAt(cpScriptSiteAID, true),
		cpWorksAt(cpScriptSiteBID, false),
	})

	sites := cpSiteLinks(muts)
	if len(sites) != 1 {
		t.Fatalf("wrote %d registeredAtSite links, want only the LIVE worksAt building: %v", len(sites), sites)
	}
	if got, want := sites[0].Key, "lnk.patient."+cpScriptPatientID+".registeredAtSite.building."+cpScriptSiteBID; got != want {
		t.Fatalf("recorded %s, want %s — a tombstoned worksAt link records no site", got, want)
	}
}

// TestCreatePatientScript_NoWorkplaceRecordsNoSite — an actor that works
// nowhere (an operator, a console, a provider who only practicesAt) records no
// site and the registration still commits. The site is a convenience for the
// front desk, never a precondition on who may register a patient, so this
// branch must never fail.
func TestCreatePatientScript_NoWorkplaceRecordsNoSite(t *testing.T) {
	muts := runCreatePatientAs(t, cpScriptActorKey, nil)

	if sites := cpSiteLinks(muts); len(sites) != 0 {
		t.Fatalf("wrote %v, want no registeredAtSite link for an actor with no worksAt link", sites)
	}
	if len(muts) == 0 {
		t.Fatal("the registration itself must still commit")
	}
}

// TestCreatePatientScript_UnparseableActorRecordsNoSite — kv.Links REJECTS a
// hub that is not a 3-segment vtx.<type>.<id> key, and op.actor is a
// platform-owned value that is not always a person. Refusing a registration
// over the shape of who submitted it would break every service/console dispatch
// path, so an unparseable actor enumerates nothing and still registers. The
// canned lister hands back links for any filter, so a shape guard that failed
// to short-circuit would surface here as a written link rather than as the
// kv.Links hub error.
func TestCreatePatientScript_UnparseableActorRecordsNoSite(t *testing.T) {
	for _, actor := range []string{
		"vtx.identity." + cpScriptActorID + ".extra",
		"vtx.identity.",
		"weaver",
	} {
		muts := runCreatePatientAs(t, actor, []processor.LinkDoc{cpWorksAt(cpScriptSiteAID, false)})
		if sites := cpSiteLinks(muts); len(sites) != 0 {
			t.Errorf("actor %q: wrote %v, want no registeredAtSite link", actor, sites)
		}
		if len(muts) == 0 {
			t.Errorf("actor %q: the registration itself must still commit", actor)
		}
	}
}

// TestCreatePatientScript_NonIdentityActorStillRecordsItsWorkplaces — the guard
// is on the actor key's SHAPE, not its type. A well-formed vtx.<type>.<id>
// actor of some other type enumerates normally: kv.Links scopes its filter to
// that hub, so a type holding no worksAt link simply yields nothing, and one
// that does is recorded like any other. Pinning this keeps a later reader from
// re-narrowing the guard to class=identity on the assumption that it already is.
func TestCreatePatientScript_NonIdentityActorStillRecordsItsWorkplaces(t *testing.T) {
	muts := runCreatePatientAs(t, "vtx.serviceaccount."+cpScriptActorID, []processor.LinkDoc{
		cpWorksAt(cpScriptSiteAID, false),
	})

	sites := cpSiteLinks(muts)
	if len(sites) != 1 {
		t.Fatalf("wrote %d registeredAtSite links, want 1: %v", len(sites), muts)
	}
	if got, want := sites[0].Key, "lnk.patient."+cpScriptPatientID+".registeredAtSite.building."+cpScriptSiteAID; got != want {
		t.Fatalf("recorded %s, want %s", got, want)
	}
}
