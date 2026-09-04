package processor_test

// The kernel-link arm admits an update whose written body is "the seeded
// shape", and the predicate that decides so is written from the key's own
// segments — it consults no stored document and imports nothing from
// internal/bootstrap. That independence is what keeps the Processor free of a
// bootstrap dependency, and it is also the failure mode: if the seeder's link
// envelope and the predicate ever describe different bodies, the arm refuses
// the one mutation it exists to admit, and an already-bricked deployment loses
// its only heal path with no test saying why.
//
// So the predicate is run here against what the seeder ACTUALLY emits for the
// twelve protected keys, with the fields the committer injects on its own
// authority set aside — those are on a stored document and never on the script
// document the guard adjudicates.

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// scriptBodyOf strips the fields buildMutationValue supplies from a stored
// envelope, leaving the body a script would have to emit to write it. It fails
// the test if a field the committer is supposed to inject is absent from the
// seeder's own output, since that would silently widen what the residue holds.
func scriptBodyOf(t *testing.T, key string, raw []byte) map[string]interface{} {
	t.Helper()
	var doc map[string]interface{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("%s does not decode as a document: %v", key, err)
	}
	for _, f := range processor.CommitterInjectedEnvelopeFieldsForTest {
		if _, present := doc[f]; !present {
			t.Fatalf("%s: the seeder emits no %q — the committer injects it, so its absence here means the two no longer describe the same envelope", key, f)
		}
		delete(doc, f)
	}
	return doc
}

func TestKernelLinkSeededShape_MatchesWhatTheSeederEmits(t *testing.T) {
	testutil.EnsurePrimordials(t)

	keys, err := bootstrap.KernelTopologyLinkKeys()
	if err != nil {
		t.Fatalf("KernelTopologyLinkKeys: %v", err)
	}
	if len(keys) != 12 {
		t.Fatalf("the protected set holds %d keys, want the kernel's twelve", len(keys))
	}

	entries, err := bootstrap.PrimordialEntries()
	if err != nil {
		t.Fatalf("PrimordialEntries: %v", err)
	}

	for _, key := range keys {
		raw, seeded := entries[key]
		if !seeded {
			t.Errorf("%s is in the protected set but the seeder writes no entry at it", key)
			continue
		}
		body := scriptBodyOf(t, key, raw)

		// The residue must be exactly the six fields the whitelist admits. A
		// seventh would mean the seeder started writing something the arm now
		// refuses — the whitelist has to widen deliberately, not by a test
		// tolerating it.
		var fields []string
		for f := range body {
			fields = append(fields, f)
		}
		sort.Strings(fields)
		if want := "class,data,isDeleted,localName,sourceVertex,targetVertex"; strings.Join(fields, ",") != want {
			t.Errorf("%s: the seeder's body, less the injected envelope fields, is [%s]; want [%s]",
				key, strings.Join(fields, ","), want)
		}

		if !processor.IsSeededLinkShapeForTest(key, body) {
			t.Errorf("%s: the guard does not recognise the body the seeder writes as the seeded shape: %v", key, body)
		}

		// The mirror, over the same real body: the arm's whole purpose is to
		// tell a faithful rewrite from a soft delete, and a predicate that
		// said yes to both would admit every revocation as a revive.
		revoked := make(map[string]interface{}, len(body))
		for k, v := range body {
			revoked[k] = v
		}
		revoked["isDeleted"] = true
		if processor.IsSeededLinkShapeForTest(key, revoked) {
			t.Errorf("%s: the guard admits the seeder's own body soft-deleted — a RevokeRole would pass as a revive", key)
		}
	}
}
