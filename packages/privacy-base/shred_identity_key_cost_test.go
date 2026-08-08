// ShredIdentityKey cost tests: the op's commit size is a constant, and the
// script carries no enumeration vocabulary at all.
//
// The sibling integration tests each assert a specific relation survives the
// shred, so each is a negative naming one key shape — five between them. A
// cascade over any relation those five do not name would pass all of them.
// These two tests state the property those negatives approximate: exactly one
// mutation and one event, whatever the subject is connected to, and no symbol
// in the script by which connectivity could be reached.
package privacybase_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"

	privacybase "github.com/operatinggraph/lattice/packages/privacy-base"
)

const shredCostIdentityID = "BBshredCostHJKMNPQRS"

// shredIdentityKeyScript returns the installed ShredIdentityKey DDL's Starlark
// source — the same string lattice-pkg writes to the meta-vertex, so an
// assertion over it is an assertion over what the Processor dispatches.
func shredIdentityKeyScript(t *testing.T) string {
	t.Helper()
	for _, d := range privacybase.Package.DDLs {
		if d.CanonicalName == "shredIdentityKey" {
			return d.Script
		}
	}
	t.Fatal("shredIdentityKey DDL not found in the privacy-base package")
	return ""
}

// countingLinkLister answers every enumeration with `links` and records that it
// was asked. A script that never enumerates leaves calls at zero; one that does
// gets a corpus large enough that its mutation count cannot pass for one.
type countingLinkLister struct {
	links []processor.LinkDoc
	calls atomic.Int64
}

func (l *countingLinkLister) ListLinks(_ context.Context, _, _ string, limit int) ([]processor.LinkDoc, string, error) {
	l.calls.Add(1)
	if limit <= 0 || limit >= len(l.links) {
		return l.links, "", nil
	}
	return l.links[:limit], "cursor", nil
}

// shredPiiKeyReader answers the piiKey lookup with a real (unshredded)
// envelope, so the script takes its update branch rather than the placeholder
// one — the branch a connected subject reaches.
type shredPiiKeyReader struct{ doc *processor.VertexDoc }

func (r shredPiiKeyReader) ReadVertex(_ context.Context, _ string) (*processor.VertexDoc, error) {
	return r.doc, nil
}

// costIndexID renders i as a distinct 20-character NanoID from the Lattice
// alphabet (no I/l/O/0), so the fixture's link keys parse as canonical Contract
// #1 keys. Counted out rather than hashed: nothing here is content-addressed,
// and a hash would be a second copy of identity-domain's own derivation.
func costIndexID(i int) string {
	const digits = "abcdefghijkmnpqrstuvwxyz" // 24 characters of the alphabet
	return "BBshredCostidx" + string([]byte{
		digits[(i/(24*24))%24], digits[(i/24)%24], digits[i%24],
	}) + "xyz"
}

// indexLinksFor builds n well-formed `indexes` links pointing at identityKey.
// `indexes` is the relation a walk over this subject would most plausibly take,
// so the fixture is a real corpus rather than an empty one: an enumerating
// script finds something to act on here.
func indexLinksFor(t *testing.T, identityKey string, n int) []processor.LinkDoc {
	t.Helper()
	id := strings.TrimPrefix(identityKey, "vtx.identity.")
	links := make([]processor.LinkDoc, 0, n)
	for i := 0; i < n; i++ {
		idxID := costIndexID(i)
		if !substrate.IsValidNanoID(idxID) {
			t.Fatalf("fixture index id %q is not a canonical NanoID", idxID)
		}
		links = append(links, processor.LinkDoc{
			Key:          "lnk.identityindex." + idxID + ".indexes.identity." + id,
			Class:        "indexes",
			SourceVertex: "vtx.identityindex." + idxID,
			TargetVertex: identityKey,
			Data:         map[string]interface{}{},
		})
	}
	return links
}

// TestShredIdentityKey_CommitsExactlyOneMutation_AtEveryConnectivity is §13's
// Inc-1 obligation: the op writes one mutation and emits one event for a
// subject with 0, 1 and 500 links, so its cost cannot grow with the subject's
// connectivity and it can never refuse a well-connected person for size. The
// lister is the only channel by which a link count could reach the script, so
// asserting it was never consulted states the property generically rather than
// per-relation.
func TestShredIdentityKey_CommitsExactlyOneMutation_AtEveryConnectivity(t *testing.T) {
	identityKey := "vtx.identity." + shredCostIdentityID

	for _, linkCount := range []int{0, 1, 500} {
		t.Run(fmt.Sprintf("links=%d", linkCount), func(t *testing.T) {
			lister := &countingLinkLister{links: indexLinksFor(t, identityKey, linkCount)}
			payload, _ := json.Marshal(map[string]any{"identityKey": identityKey})
			sc := processor.ScriptContext{
				Operation: &processor.OperationEnvelope{
					RequestID:     "Hj4kPmRtw9nbCxz5vQ2y",
					Lane:          processor.LaneUrgent,
					OperationType: "ShredIdentityKey",
					Actor:         "vtx.identity." + shredCostIdentityID,
					SubmittedAt:   "2026-08-07T10:00:00Z",
					Payload:       payload,
				},
				Hydrated: map[string]processor.VertexDoc{
					identityKey: {Key: identityKey, Class: "identity", Data: map[string]interface{}{}},
				},
				ScriptSource: shredIdentityKeyScript(t),
				ScriptClass:  "shredIdentityKey",
				KVReader: shredPiiKeyReader{doc: &processor.VertexDoc{
					Key:       identityKey + ".piiKey",
					Class:     "piiKey",
					VertexKey: identityKey,
					LocalName: "piiKey",
					Data: map[string]interface{}{
						"wrappedDEK": "d3JhcHBlZA==", "keyId": identityKey,
						"kekVersion": "v1", "alg": "AES-256-GCM",
						"createdAt": "2026-08-01T00:00:00Z", "shredded": false,
					},
				}},
				LinkLister: lister,
			}

			result, err := processor.NewStarlarkRunner(0, 0).Run(context.Background(), sc)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if len(result.Mutations) == 0 {
				t.Fatalf("no mutation for a subject with %d links; the shred must always write its envelope", linkCount)
			}
			if len(result.Mutations) != 1 {
				t.Fatalf("mutations = %d for a subject with %d links, want exactly 1; the second is %s %s",
					len(result.Mutations), linkCount, result.Mutations[1].Op, result.Mutations[1].Key)
			}
			if got := result.Mutations[0].Key; got != identityKey+".piiKey" {
				t.Fatalf("the single mutation targets %q, want the piiKey envelope %q", got, identityKey+".piiKey")
			}
			if len(result.Events) != 1 || result.Events[0].Class != "privacy.keyShredded" {
				t.Fatalf("events = %+v, want exactly one privacy.keyShredded", result.Events)
			}
			if calls := lister.calls.Load(); calls != 0 {
				t.Fatalf("the script enumerated links %d times; its commit size must not depend on the subject's connectivity", calls)
			}
		})
	}
}

// TestShredIdentityKey_ScriptCarriesNoEnumerationVocabulary asserts the symbols
// are gone, not merely that they do not fire: a size refusal reachable on some
// untested branch is exactly the failure the one-mutation property exists to
// rule out, and a branch nothing reaches is invisible to a behavioural test.
func TestShredIdentityKey_ScriptCarriesNoEnumerationVocabulary(t *testing.T) {
	script := shredIdentityKeyScript(t)
	for _, symbol := range []string{
		"ShredBatchTooLarge", // a commit-size refusal
		"FanoutTooLarge",     // a per-relation enumeration refusal
		"kv.Links",           // the enumeration itself
		"total_muts",         // a running mutation count to refuse on
	} {
		if strings.Contains(script, symbol) {
			t.Errorf("the shredIdentityKey script still carries %q — the op commits one mutation unconditionally, so neither an enumeration nor a size refusal has a reachable meaning", symbol)
		}
	}
}
