// The load-bearing assertion for independent-branch decomposition: no
// projected row moves. The same PARSED rule is executed twice over one corpus —
// once with the analysis as Parse computed it, once with it forced nil (the
// path every evaluation took before it existed) — and the two
// []ProjectionResult must be equal in ORDER and CONTENT, not merely as sets.
// §4.5 claims the collected lists are identical element for element and in the
// same order, so a set comparison would hide exactly the failure the claim is
// about. The staging primitive's own proof next door compares anchor SETS; this
// one deliberately does not.
//
// The two runs must also certify the same read-surface FOOTPRINT. A decomposed
// branch is re-walked from the base row instead of from every product row, and
// every read is memoized for the evaluation's life, so the same keys at the same
// revisions are expected — but pipeline.footprintValid re-reads only the keys
// the RECORDED footprint names, so an evaluation that stopped reading something
// would validate fewer keys and pass. Nothing downstream would notice the
// drift-detection coverage it lost, which is why the equality happens here.
package full

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/pkgregistry"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// withoutBranchDecomposition returns a shallow copy of cr with the branch
// analysis cleared — the path every evaluation took before it existed.
//
// A COPY, never a write to cr. ast.go's contract is that a compiled rule is
// immutable after Parse precisely so a reader can never observe a
// half-rewritten rule. Mirrors withoutGroupingAnalysis and WithLabelExpansion,
// including its aliased read-only Query.
func withoutBranchDecomposition(cr *CompiledRule) *CompiledRule {
	if cr == nil {
		return nil
	}
	next := *cr
	next.branchStages = nil
	next.branchDeferred = nil
	return &next
}

// corpusSpec returns the shipped cypher of one installed lens, by canonical
// name, read out of the package registry rather than copied into this file — a
// copy would drift and this differential would then prove nothing about what
// runs.
func corpusSpec(t testing.TB, canonicalName string) string {
	t.Helper()
	for _, pkgName := range pkgregistry.Names() {
		def, ok := pkgregistry.Lookup(pkgName)
		require.Truef(t, ok, "registered package %q must resolve", pkgName)
		for _, l := range def.Lenses {
			if l.CanonicalName == canonicalName {
				require.NotEmptyf(t, l.Spec, "lens %q ships no single cypher", canonicalName)
				return l.Spec
			}
		}
	}
	t.Fatalf("no installed lens is named %q", canonicalName)
	return ""
}

// branchCorpusShape sizes one corpus. Every count may be zero — a
// randomly-empty branch is a shape the decomposition must still handle
// identically, and the randomized differential draws zeros deliberately.
type branchCorpusShape struct {
	Prefix string

	// The orchestration shapes: tasks assigned directly, tasks assigned to the
	// actor's reports, and tasks queued to roles the actor holds. SharedTasks
	// are assigned to the actor AND queued to a held role, so one task reaches
	// the projection through two different branches — the adversarial sharing
	// that makes a mis-routed fold visible.
	Tasks, Reports, TasksPerReport, Roles, QueuedPerRole, SharedTasks int

	// The identity anchors. WorkplaceIsResidence points worksAt at the SAME
	// location vertex residesIn reaches, so two branches walk one shared node
	// and its memoized reads.
	Containers, ExtraParents, Managed, Hats int
	WorkplaceIsResidence                    bool

	// The lease application: the corpus's widest stage, where the pinned
	// frontier does the work. Instances hang below the PINNED applicant, and
	// Managers below the PINNED unit.
	Instances, DocInstances, LeaseDocs, SigTasks, OnbTasks, Managers int

	// The clause: the live lens §4.2 must refuse.
	Conditions, Inspectors, Transactions int

	// The clinic roster's workplace fan-out: the providers the corpus's patient
	// has appointments with, and how many appointments with each. Every provider
	// practises at a SHARED site as well as its own, so the collect(DISTINCT …)
	// behind the roster's authz anchors has real duplicates to fold rather than a
	// list DISTINCT happens not to shorten.
	Providers, AppointmentsPerProvider int

	// The remaining decomposing lenses of the corpus census: rbac permissions
	// off held roles, residence-reachable service templates, the erasure-residue
	// fan-outs, augur proposals against the application, and the rent clause.
	PermsPerRole, Templates, OpsPerTemplate   int
	BoundIn, BoundOut, Indexes, DuplicatesOut int
	DuplicatesIn, Proposals, RentClauses      int
}

// fullBranchShape populates every branch, so a differential over it compares
// non-empty lists rather than a row of empty ones.
func fullBranchShape(prefix string) branchCorpusShape {
	return branchCorpusShape{
		Prefix:                  prefix,
		Tasks:                   3,
		Reports:                 2,
		TasksPerReport:          2,
		Roles:                   2,
		QueuedPerRole:           2,
		SharedTasks:             1,
		Containers:              2,
		ExtraParents:            1,
		Managed:                 2,
		Hats:                    1,
		WorkplaceIsResidence:    true,
		Instances:               3,
		DocInstances:            2,
		LeaseDocs:               1,
		SigTasks:                2,
		OnbTasks:                2,
		Managers:                2,
		Conditions:              1,
		Inspectors:              1,
		Transactions:            2,
		Providers:               2,
		AppointmentsPerProvider: 2,
		PermsPerRole:            2,
		Templates:               2,
		OpsPerTemplate:          2,
		BoundIn:                 2,
		BoundOut:                1,
		Indexes:                 2,
		DuplicatesOut:           1,
		DuplicatesIn:            1,
		Proposals:               2,
		RentClauses:             1,
	}
}

// randomBranchShape draws every count from a deterministic source, zeros
// included.
func randomBranchShape(prefix string, r *rand.Rand) branchCorpusShape {
	n := func(max int) int { return r.Intn(max + 1) }
	return branchCorpusShape{
		Prefix:                  prefix,
		Tasks:                   n(3),
		Reports:                 n(2),
		TasksPerReport:          n(2),
		Roles:                   n(2),
		QueuedPerRole:           n(3),
		SharedTasks:             n(2),
		Containers:              n(3),
		ExtraParents:            n(2),
		Managed:                 n(2),
		Hats:                    n(1),
		WorkplaceIsResidence:    r.Intn(2) == 0,
		Instances:               n(3),
		DocInstances:            n(2),
		LeaseDocs:               n(1),
		SigTasks:                n(2),
		OnbTasks:                n(2),
		Managers:                n(2),
		Conditions:              n(1),
		Inspectors:              n(1),
		Transactions:            n(2),
		Providers:               n(2),
		AppointmentsPerProvider: n(2),
		PermsPerRole:            n(2),
		Templates:               n(2),
		OpsPerTemplate:          n(2),
		BoundIn:                 n(2),
		BoundOut:                n(1),
		Indexes:                 n(2),
		DuplicatesOut:           n(1),
		DuplicatesIn:            n(1),
		Proposals:               n(2),
		RentClauses:             n(1),
	}
}

// branchCorpus names the anchors the differential's specs are evaluated
// against. An unanchored lens takes "" and binds every vertex of its type in
// the KV, which is the multi-base-row half of the coverage.
type branchCorpus struct {
	actorKey, leaseAppKey, renewalKey, clauseKey, objectKey string

	// onboardingActorKey is a SECOND applicant, deliberately not onboarded.
	// applicantOnboarding's application branch reaches its RETURN only through
	// `(ssnVal = null) AND (onboardingApps > 0)`, and the primary actor carries
	// an .ssn (the lease block seeds it so leaseApplicationComplete's own
	// booleans expose their subtrees) — which masks that conjunct at every
	// value of the count. This anchor exposes it.
	onboardingActorKey string

	// clinicNoShowApptKey anchors clinicNoShowSettlement on an appointment
	// already corrected off noShow with its charge already reversed.
	clinicNoShowApptKey string
}

// seedBranchCorpus writes one corpus of shape s. Every logical name is
// prefixed, so several corpora share one pair of KVs without any of their walks
// crossing.
func seedBranchCorpus(t testing.TB, reg *fixtureRegistry, adjKV, coreKV *substrate.KV, s branchCorpusShape) branchCorpus {
	t.Helper()
	p := s.Prefix
	name := func(format string, args ...any) string { return p + fmt.Sprintf(format, args...) }
	future := time.Now().Add(72 * time.Hour).UTC().Format(time.RFC3339)

	actor := name("actor")
	putVertex(t, reg, coreKV, actor, "identity", map[string]any{
		"data": map[string]any{"state": "claimed"},
	})
	putVertex(t, reg, coreKV, acctName(p), "account", nil)

	// An operation meta and a scope target per task, so forOperation/scopedTo
	// are real branches rather than two null columns.
	op := name("op")
	putVertex(t, reg, coreKV, op, "meta", map[string]any{
		"data": map[string]any{"operationType": "ApproveLeaseApplication"},
	})
	signOp := name("signop")
	putVertex(t, reg, coreKV, signOp, "meta", map[string]any{
		"data": map[string]any{"operationType": "SignLease"},
	})
	onbOp := name("onbop")
	putVertex(t, reg, coreKV, onbOp, "meta", map[string]any{
		"data": map[string]any{"operationType": "RecordIdentityPII"},
	})
	scope := name("scope")
	putVertex(t, reg, coreKV, scope, "leaseapp", nil)

	// A role that grants EVERY op meta the corpus seeds, through the pair of
	// install-time edges pkgmgr mints together (internal/pkgmgr/build.go):
	// `permission grantedBy role` and `permission forOperation meta`. Without
	// the second one a permission dead-ends at its operationType STRING, which
	// no cypher can join to a vertex, and opCatalog's role collect folds empty
	// over the whole corpus.
	//
	// The actor deliberately does NOT hold this role, so nothing anchored on the
	// actor can reach it: the role feeds the op-anchored catalog and moves no
	// other lens's rows.
	catalogRole := name("catalogrole")
	putVertex(t, reg, coreKV, catalogRole, "role", nil)
	putAspect(t, reg, coreKV, catalogRole, "canonicalName", map[string]any{"value": "catalog"})
	grantOp := func(suffix, opLogical, operationType string) {
		perm := name("catalogperm_%s", suffix)
		putVertex(t, reg, coreKV, perm, "permission", map[string]any{
			"data": map[string]any{"operationType": operationType, "scope": "any"},
		})
		putEdge(t, reg, adjKV, "grantedBy", perm, catalogRole)
		putEdge(t, reg, adjKV, "forOperation", perm, opLogical)
	}
	grantOp("op", op, "ApproveLeaseApplication")
	grantOp("signop", signOp, "SignLease")
	grantOp("onbop", onbOp, "RecordIdentityPII")

	task := func(logical string, extra map[string]any) {
		props := map[string]any{"status": "open", "expiresAt": future}
		for k, v := range extra {
			props[k] = v
		}
		putVertex(t, reg, coreKV, logical, "task", map[string]any{"data": props})
		putEdge(t, reg, adjKV, "forOperation", logical, op)
		putEdge(t, reg, adjKV, "scopedTo", logical, scope)
	}

	for i := 0; i < s.Tasks; i++ {
		tk := name("task%d", i)
		task(tk, nil)
		putEdge(t, reg, adjKV, "assignedTo", tk, actor)
	}
	for i := 0; i < s.Reports; i++ {
		rep := name("report%d", i)
		putVertex(t, reg, coreKV, rep, "identity", nil)
		putEdge(t, reg, adjKV, "reportsTo", rep, actor)
		for j := 0; j < s.TasksPerReport; j++ {
			tk := name("rtask_%d_%d", i, j)
			task(tk, nil)
			putEdge(t, reg, adjKV, "assignedTo", tk, rep)
		}
	}
	for i := 0; i < s.Roles; i++ {
		role := name("role%d", i)
		putVertex(t, reg, coreKV, role, "role", nil)
		putAspect(t, reg, coreKV, role, "canonicalName", map[string]any{"value": "role" + fmt.Sprint(i)})
		putEdge(t, reg, adjKV, "holdsRole", actor, role)
		for j := 0; j < s.PermsPerRole; j++ {
			perm := name("perm_%d_%d", i, j)
			putVertex(t, reg, coreKV, perm, "permission", map[string]any{
				"data": map[string]any{
					"operationType": fmt.Sprintf("Op%d_%d", i, j),
					"scope":         "self",
					"lanes":         []any{"meta"},
				},
			})
			putEdge(t, reg, adjKV, "grantedBy", perm, role)
		}
		for j := 0; j < s.QueuedPerRole; j++ {
			tk := name("qtask_%d_%d", i, j)
			task(tk, nil)
			putEdge(t, reg, adjKV, "queuedFor", tk, role)
		}
		// A report who is also a holder of the role the actor holds: the same
		// task then reaches the projection down two different branches.
		if i < s.Reports {
			putEdge(t, reg, adjKV, "holdsRole", name("report%d", i), role)
		}
	}
	for i := 0; i < s.SharedTasks && s.Roles > 0; i++ {
		tk := name("shared%d", i)
		task(tk, nil)
		putEdge(t, reg, adjKV, "assignedTo", tk, actor)
		putEdge(t, reg, adjKV, "queuedFor", tk, name("role0"))
	}

	// The residence chain, with the extra parents that make containedIn a
	// multi-parent walk rather than a line.
	home := name("home")
	putVertex(t, reg, coreKV, home, "location", nil)
	putAspect(t, reg, coreKV, home, "presentation", map[string]any{"name": "home"})
	putEdge(t, reg, adjKV, "residesIn", actor, home)
	for i := 0; i < s.Containers; i++ {
		c := name("container%d", i)
		putVertex(t, reg, coreKV, c, "location", nil)
		putAspect(t, reg, coreKV, c, "presentation", map[string]any{"name": "c" + fmt.Sprint(i)})
		putEdge(t, reg, adjKV, "containedIn", home, c)
	}
	for i := 0; i < s.ExtraParents; i++ {
		x := name("parent%d", i)
		putVertex(t, reg, coreKV, x, "location", nil)
		putEdge(t, reg, adjKV, "containedIn", home, x)
	}
	work := home
	if !s.WorkplaceIsResidence {
		work = name("work")
		putVertex(t, reg, coreKV, work, "location", nil)
		putAspect(t, reg, coreKV, work, "presentation", map[string]any{"name": "work"})
	}
	putEdge(t, reg, adjKV, "worksAt", actor, work)
	for i := 0; i < s.Managed; i++ {
		m := name("managed%d", i)
		putVertex(t, reg, coreKV, m, "location", nil)
		putEdge(t, reg, adjKV, "manages", actor, m)
		putEdge(t, reg, adjKV, "containedIn", m, home)
	}
	for i := 0; i < s.Hats; i++ {
		for _, hat := range []string{"provider", "instructor", "serviceprovider", "patient"} {
			h := name("%s%d", hat, i)
			putVertex(t, reg, coreKV, h, hat, nil)
			putAspect(t, reg, coreKV, h, "profile", map[string]any{
				"fullName": hat, "displayName": hat,
			})
			putEdge(t, reg, adjKV, "identifiedBy", h, actor)
		}
	}

	// Residence-reachable service templates: the availableAt fan-out
	// capabilityServiceAccess walks off the residence chain. Deliberately with no
	// instanceOf and no unavailableAt, so its two NOT-pattern guards admit them.
	for i := 0; i < s.Templates; i++ {
		tpl := name("tpl%d", i)
		putVertex(t, reg, coreKV, tpl, "service", map[string]any{"class": "service.cleaning.template"})
		putEdge(t, reg, adjKV, "availableAt", tpl, home)
		for j := 0; j < s.OpsPerTemplate; j++ {
			op := name("tplop_%d_%d", i, j)
			putVertex(t, reg, coreKV, op, "meta", map[string]any{
				"data": map[string]any{"operationType": fmt.Sprintf("Svc%d_%d", i, j)},
			})
			putEdge(t, reg, adjKV, "permitsOperation", tpl, op)
			grantOp(fmt.Sprintf("tplop_%d_%d", i, j), op, fmt.Sprintf("Svc%d_%d", i, j))
		}
	}

	// The erasure-residue fan-outs. The requestedAt aspect is what admits the
	// actor to that lens at all (its head carries a filtering WHERE).
	putAspect(t, reg, coreKV, actor, "erasureRequested", map[string]any{"requestedAt": future})
	for i := 0; i < s.BoundIn; i++ {
		c := name("cred%d", i)
		putVertex(t, reg, coreKV, c, "credential", nil)
		putEdge(t, reg, adjKV, "boundTo", c, actor)
	}
	for i := 0; i < s.BoundOut; i++ {
		o := name("boundout%d", i)
		putVertex(t, reg, coreKV, o, "credential", nil)
		putEdge(t, reg, adjKV, "boundTo", actor, o)
	}
	for i := 0; i < s.Indexes; i++ {
		x := name("idxhint%d", i)
		putVertex(t, reg, coreKV, x, "meta", nil)
		putEdge(t, reg, adjKV, "indexes", x, actor)
	}
	for i := 0; i < s.DuplicatesOut; i++ {
		d := name("dupout%d", i)
		putVertex(t, reg, coreKV, d, "identity", nil)
		putEdge(t, reg, adjKV, "duplicateOf", actor, d)
	}
	for i := 0; i < s.DuplicatesIn; i++ {
		d := name("dupin%d", i)
		putVertex(t, reg, coreKV, d, "identity", nil)
		putEdge(t, reg, adjKV, "duplicateOf", d, actor)
	}

	// The lease application and its renewal.
	app := name("app")
	putVertex(t, reg, coreKV, app, "leaseapp", nil)
	// The applicant's own readiness aspects, and an approved landlord decision:
	// leaseApplicationComplete projects each candidate subtree's count only
	// through a boolean gated on these, so without them the branches fold real
	// content into columns no assertion can reach.
	putAspect(t, reg, coreKV, actor, "ssn", map[string]any{"value": "redacted"})
	putAspect(t, reg, coreKV, app, "signature", map[string]any{"signedAt": future})
	putAspect(t, reg, coreKV, app, "decision", map[string]any{"value": "approved"})
	putAspect(t, reg, coreKV, app, "terms", map[string]any{
		"moveInDate": future, "leaseTermMonths": 12, "requestedRent": 2500,
	})
	putAspect(t, reg, coreKV, app, "applicationSignals", map[string]any{
		"submittedAt": future, "incomeToRentMet": true, "employmentVerified": true,
		"referenceCount": 2, "hasCoApplicant": false, "hasGuarantor": false,
	})
	putEdge(t, reg, adjKV, "applicationFor", app, actor)
	unit := name("unit")
	putVertex(t, reg, coreKV, unit, "unit", nil)
	putAspect(t, reg, coreKV, unit, "listing", map[string]any{"rentAmount": 2500, "status": "listed"})
	putEdge(t, reg, adjKV, "appliesToUnit", app, unit)
	for i := 0; i < s.Managers; i++ {
		mgr := name("mgr%d", i)
		putVertex(t, reg, coreKV, mgr, "identity", nil)
		putEdge(t, reg, adjKV, "manages", mgr, unit)
	}
	instClass := []string{"service.backgroundCheck.instance", "service.payment.instance"}
	for i := 0; i < s.Instances; i++ {
		inst := name("inst%d", i)
		putVertex(t, reg, coreKV, inst, "service", map[string]any{"class": instClass[i%len(instClass)]})
		putAspect(t, reg, coreKV, inst, "outcome", map[string]any{
			"status": "completed", "validUntil": future,
		})
		putEdge(t, reg, adjKV, "providedTo", inst, actor)
	}
	for i := 0; i < s.DocInstances; i++ {
		di := name("docinst%d", i)
		putVertex(t, reg, coreKV, di, "service", map[string]any{"class": "service.docGen.instance"})
		putAspect(t, reg, coreKV, di, "outcome", map[string]any{
			"status": "completed", "storeName": "store", "filename": "lease.pdf",
			"contentType": "application/pdf", "digest": "sha256:" + fmt.Sprint(i), "size": 100 + i,
		})
		putEdge(t, reg, adjKV, "providedTo", di, app)
	}
	for i := 0; i < s.LeaseDocs; i++ {
		obj := name("leasedoc%d", i)
		putVertex(t, reg, coreKV, obj, "object", nil)
		putEdge(t, reg, adjKV, "signedLease", obj, app)
	}
	for i := 0; i < s.SigTasks; i++ {
		tk := name("sigtask%d", i)
		putVertex(t, reg, coreKV, tk, "task", map[string]any{
			"data": map[string]any{"status": "open", "expiresAt": future},
		})
		putEdge(t, reg, adjKV, "forOperation", tk, signOp)
		putEdge(t, reg, adjKV, "scopedTo", tk, app)
	}
	for i := 0; i < s.OnbTasks; i++ {
		tk := name("onbtask%d", i)
		putVertex(t, reg, coreKV, tk, "task", map[string]any{
			"data": map[string]any{"status": "open", "expiresAt": future},
		})
		putEdge(t, reg, adjKV, "forOperation", tk, onbOp)
		putEdge(t, reg, adjKV, "scopedTo", tk, actor)
	}

	// The un-onboarded applicant (branchCorpus.onboardingActorKey): their own
	// application, its listed unit, and OnbTasks open RecordIdentityPII tasks,
	// all disjoint from the primary actor's subgraph so no other lens's anchor
	// can reach any of it. Both of applicantOnboarding's sibling branch groups
	// — the application/unit walk and the task/operation walk — fold real
	// content here, and neither is masked, because this identity carries no
	// .ssn aspect.
	onbActor := name("onbactor")
	putVertex(t, reg, coreKV, onbActor, "identity", map[string]any{
		"data": map[string]any{"state": "claimed"},
	})
	onbApp := name("onbapp")
	putVertex(t, reg, coreKV, onbApp, "leaseapp", nil)
	putEdge(t, reg, adjKV, "applicationFor", onbApp, onbActor)
	onbUnit := name("onbunit")
	putVertex(t, reg, coreKV, onbUnit, "unit", nil)
	putAspect(t, reg, coreKV, onbUnit, "listing", map[string]any{"rentAmount": 2100, "status": "listed"})
	putEdge(t, reg, adjKV, "appliesToUnit", onbApp, onbUnit)
	for i := 0; i < s.OnbTasks; i++ {
		tk := name("onbactortask%d", i)
		putVertex(t, reg, coreKV, tk, "task", map[string]any{
			"data": map[string]any{"status": "open", "expiresAt": future},
		})
		putEdge(t, reg, adjKV, "forOperation", tk, onbOp)
		putEdge(t, reg, adjKV, "scopedTo", tk, onbActor)
	}
	// leaseExpiry reads the tenancy aspect (no backfill — an application without
	// one never enters that lens) and leaseRentSettlement the ledger account.
	// Its cycle gate is a recorded lapse, not a clock: the freshnessExpiry marker
	// carries the instant the leaseExpiry target's own @at fired, and without an
	// entry at or after renewalOpensAt the gap column stays false and this lens's
	// differential witness would compare two folded-empty branches.
	putAspect(t, reg, coreKV, app, "tenancy", map[string]any{
		"leaseEnd": "2020-01-01T00:00:00Z", "renewalOpensAt": "2019-12-01T00:00:00Z",
	})
	putAspect(t, reg, coreKV, app, "freshnessExpiry", map[string]any{
		"expiredAt": "2019-12-01T00:00:00Z",
		"byTarget":  map[string]any{"leaseExpiry": "2019-12-01T00:00:00Z"},
	})
	putAspect(t, reg, coreKV, app, "ledgerAccount", map[string]any{"accountKey": vtxKey(reg, acctName(p))})
	for i := 0; i < s.Proposals; i++ {
		prop := name("prop%d", i)
		putVertex(t, reg, coreKV, prop, "augurproposal", nil)
		putAspect(t, reg, coreKV, prop, "gap", map[string]any{"gapColumn": "missing_bgcheck"})
		putEdge(t, reg, adjKV, "forCandidate", prop, app)
	}
	for i := 0; i < s.RentClauses; i++ {
		rc := name("rentclause%d", i)
		putVertex(t, reg, coreKV, rc, "clause", nil)
		putAspect(t, reg, coreKV, rc, "terms", map[string]any{"period": "monthly", "conditioned": false})
		putEdge(t, reg, adjKV, "governs", rc, app)
	}

	renewal := name("renewal")
	putVertex(t, reg, coreKV, renewal, "renewal", map[string]any{
		"data": map[string]any{"status": "open"},
	})
	putEdge(t, reg, adjKV, "renews", renewal, app)

	// The clause: four sibling groups and a non-DISTINCT count() over one of
	// them, which is the live shape §4.2 refuses wholesale.
	clause := name("clause")
	putVertex(t, reg, coreKV, clause, "clause", nil)
	putAspect(t, reg, coreKV, clause, "terms", map[string]any{
		"amountCents": 1000, "conditioned": true, "period": "monthly",
	})
	putEdge(t, reg, adjKV, "chargesTo", clause, acctName(p))
	for i := 0; i < s.Conditions; i++ {
		cond := name("cond%d", i)
		putVertex(t, reg, coreKV, cond, "clause", nil)
		putEdge(t, reg, adjKV, "conditionedOn", clause, cond)
	}
	for i := 0; i < s.Inspectors; i++ {
		insp := name("insp%d", i)
		putVertex(t, reg, coreKV, insp, "identity", nil)
		putEdge(t, reg, adjKV, "requiresInspectionBy", clause, insp)
	}
	for i := 0; i < s.Transactions; i++ {
		tx := name("tx%d", i)
		putVertex(t, reg, coreKV, tx, "transaction", nil)
		putEdge(t, reg, adjKV, "authorizedBy", tx, clause)
	}

	// The clinic roster. clinicPatientsRead is UNANCHORED — it binds every
	// patient the KV holds — and the subtree it defers is the
	// patient <- appointment -> provider -> building walk behind each row's
	// workplace authz anchors. The patient, its identity and the shared site are
	// seeded whatever the random shape drew, for the same reason the attachment
	// object below is: a corpus that drew this block away entirely would leave
	// that lens's differential comparing two empty projections over an EMPTY
	// certified read surface, which executeBothBranchWaysExpanded refuses.
	//
	// The .demographics aspect is what the lens's WHERE guard admits the patient
	// by, and fullName is the unlinked_name column's source — its two fields are
	// patientDemographics's whole shape (packages/clinic-domain/ddls.go). The
	// patient's identity hop is a SECOND identity rather than the corpus actor,
	// so nothing here reaches an actor-anchored lens and no other spec's rows
	// move.
	patient := name("roster")
	putVertex(t, reg, coreKV, patient, "patient", nil)
	putAspect(t, reg, coreKV, patient, "demographics", map[string]any{
		"registeredAt": future, "fullName": "roster",
	})
	patientIdentity := name("rosteridentity")
	putVertex(t, reg, coreKV, patientIdentity, "identity", nil)
	putEdge(t, reg, adjKV, "identifiedBy", patient, patientIdentity)
	sharedSite := name("sharedsite")
	putVertex(t, reg, coreKV, sharedSite, "building", nil)
	for i := 0; i < s.Providers; i++ {
		clinician := name("clinician%d", i)
		putVertex(t, reg, coreKV, clinician, "provider", nil)
		// Two sites per provider, one of them shared with every other provider:
		// the shared one is reached once per (appointment, provider) pair, so the
		// DISTINCT really collapses arms that would otherwise each contribute an
		// entry — the dedup the roster's authz_anchors is written for.
		ownSite := name("site%d", i)
		putVertex(t, reg, coreKV, ownSite, "building", nil)
		putEdge(t, reg, adjKV, "practicesAt", clinician, sharedSite)
		putEdge(t, reg, adjKV, "practicesAt", clinician, ownSite)
		for j := 0; j < s.AppointmentsPerProvider; j++ {
			appt := name("visit_%d_%d", i, j)
			putVertex(t, reg, coreKV, appt, "appointment", nil)
			putEdge(t, reg, adjKV, "forPatient", appt, patient)
			putEdge(t, reg, adjKV, "withProvider", appt, clinician)
		}
	}

	// An appointment whose provider practises NOWHERE, booked at a building
	// nothing else in the corpus reaches. Every appointment above hangs off a
	// provider with practicesAt links, so the roster's atSite arm would bind
	// null on every row the corpus draws and its half of the deferred subtree
	// would fold empty in BOTH execution orders — a differential comparing two
	// empty folds, which is the reading this file's own evidence guards refuse
	// elsewhere. This row makes that arm carry a value, and retiredSite being
	// unreachable any other way is what makes the value observable: were the arm
	// dropped, authz_anchors would lose an entry rather than stay identical.
	// (A live provider at zero sites is the same null-`b` shape a tombstoned one
	// produces — Contract #1 filters the dead vertex out of the walk — and is
	// the shape RemoveProviderSite leaves behind, so it needs no tombstone to
	// reach the branch.)
	retired := name("retired")
	putVertex(t, reg, coreKV, retired, "provider", nil)
	retiredSite := name("retiredsite")
	putVertex(t, reg, coreKV, retiredSite, "building", nil)
	retiredVisit := name("retiredvisit")
	putVertex(t, reg, coreKV, retiredVisit, "appointment", nil)
	putEdge(t, reg, adjKV, "forPatient", retiredVisit, patient)
	putEdge(t, reg, adjKV, "withProvider", retiredVisit, retired)
	putEdge(t, reg, adjKV, "atSite", retiredVisit, retiredSite)

	// objectAttachments anchors on ONE object, so the corpus always carries one
	// with an owner link whatever the random shape drew.
	obj := name("attachment")
	putVertex(t, reg, coreKV, obj, "object", nil)
	putAspect(t, reg, coreKV, obj, "content", map[string]any{
		"storeName": "store", "contentType": "application/pdf", "size": 42,
		"digest": "sha256:0", "sensitive": false, "governingIdentity": vtxKey(reg, actor),
	})
	putEdge(t, reg, adjKV, "signedLease", obj, app)

	// clinicNoShowSettlement anchors on ONE appointment already corrected off
	// noShow, with its charge already posted AND already reversed — the shape
	// that exercises the new [credit,tx] deferred subtree (missing_reversal)
	// on both sides of the fold: a null credit would leave the reverses arm
	// folding empty in both execution orders, the same vacuous-differential
	// risk objectAttachments's comment above names. Always seeded, whatever
	// the random shape drew, like the object above.
	noShowAppt := name("noshowappt")
	putVertex(t, reg, coreKV, noShowAppt, "appointment", nil)
	putAspect(t, reg, coreKV, noShowAppt, "status", map[string]any{
		"value": "completed", "note": "corrected", "noShowFeeCents": 2500.0, "correctedFrom": "noShow",
	})
	noShowPatient := name("noshowpatient")
	putVertex(t, reg, coreKV, noShowPatient, "patient", nil)
	putEdge(t, reg, adjKV, "forPatient", noShowAppt, noShowPatient)
	noShowAcct := name("noshowacct")
	putVertex(t, reg, coreKV, noShowAcct, "clinicaccount", nil)
	putEdge(t, reg, adjKV, "heldFor", noShowAcct, noShowPatient)
	noShowTx := name("noshowtx")
	putVertex(t, reg, coreKV, noShowTx, "clinictransaction", nil)
	putAspect(t, reg, coreKV, noShowTx, "entry", map[string]any{
		"type": "debit", "amountCents": 2500.0, "memo": "No-show fee", "postedAt": future,
	})
	putEdge(t, reg, adjKV, "settles", noShowTx, noShowAppt)
	noShowCredit := name("noshowcredit")
	putVertex(t, reg, coreKV, noShowCredit, "clinictransaction", nil)
	putAspect(t, reg, coreKV, noShowCredit, "entry", map[string]any{
		"type": "credit", "amountCents": 2500.0, "reason": "waiver",
		"memo": "No-show fee reversal (corrected)", "postedAt": future,
	})
	putEdge(t, reg, adjKV, "reverses", noShowCredit, noShowTx)

	return branchCorpus{
		actorKey:            vtxKey(reg, actor),
		leaseAppKey:         vtxKey(reg, app),
		renewalKey:          vtxKey(reg, renewal),
		clauseKey:           vtxKey(reg, clause),
		objectKey:           vtxKey(reg, obj),
		onboardingActorKey:  vtxKey(reg, onbActor),
		clinicNoShowApptKey: vtxKey(reg, noShowAppt),
	}
}

// acctName is the ledger account's logical fixture name, shared by the seeder
// and the aspect that points at it.
func acctName(prefix string) string { return prefix + "account" }

// branchSpec is one lens of the differential: its shipped cypher, the anchor
// its cypher is written against, and the columns that must carry real
// aggregate content over a populated corpus.
//
// evidence is what stops the differential from passing on two empty
// projections. Each lens states it in its OWN columns rather than through a
// shared "collected something" heuristic, because the shapes differ: the
// orchestration and identity lenses concatenate collect(DISTINCT …) lists,
// while the lease lenses project counts and extremes and would read as empty to
// any list-counting rule.
type branchSpec struct {
	name, spec, anchor string
	// evidence asserts, column by column, that every candidate subtree of this
	// lens folded real content.
	evidence func(t *testing.T, row map[string]any)
	// content is the same question as a number, so the randomized differential
	// can report how many of its corpora compared something rather than two
	// empty projections.
	content func(row map[string]any) int
	// labelExpansion is the taxonomy resolution activation threads onto a rule
	// whose pattern carries the `*` sigil. A nil map binds NOTHING for such a
	// pattern (the fail-closed arm in nodeMatches), so a lens written with `*`
	// projects an empty row without it and its differential proves nothing.
	labelExpansion map[string]map[string]struct{}
	// rows is how many rows the lens projects over one corpus; an unanchored
	// lens binds every vertex of its type in the KV, so it takes 0 for "do not
	// assert a count".
	rows int
}

// isTrue reports whether a projected boolean column is true.
func isTrue(row map[string]any, col string) bool {
	b, _ := row[col].(bool)
	return b
}

// boolEvidence asserts a projected boolean column carries want, naming the
// branch whose emptiness is the other reading.
func boolEvidence(t *testing.T, row map[string]any, col string, want bool, branch string) {
	t.Helper()
	got, isBool := row[col].(bool)
	require.Truef(t, isBool, "column %q must be a boolean, got %T", col, row[col])
	require.Equalf(t, want, got, "column %q says the %s branch folded empty", col, branch)
}

func boolsTrue(row map[string]any, cols ...string) int {
	n := 0
	for _, c := range cols {
		if isTrue(row, c) {
			n++
		}
	}
	return n
}

// listLen counts the REAL entries of a collected column: those whose identity
// field is populated. A branch that bound nothing still contributes its null
// placeholder entry, which every one of these lenses filters at the adapter, and
// counting those would let an all-empty projection read as evidence.
//
// idFields are the entry keys that carry that identity — each lens names it
// differently (`key`, `taskKey`, `ownerKey`, `service`), so the caller states
// which one applies rather than this helper guessing across all of them.
func listLen(row map[string]any, col string, idFields ...string) int {
	if len(idFields) == 0 {
		idFields = []string{"key", "taskKey"}
	}
	list, _ := row[col].([]any)
	n := 0
	for _, el := range list {
		m, isMap := el.(map[string]any)
		if !isMap {
			n++
			continue
		}
		for _, f := range idFields {
			if k, _ := m[f].(string); k != "" {
				n++
				break
			}
		}
	}
	return n
}

// branchDifferentialSpecs are the §2 lenses this differential covers.
// clauseSatisfaction is absent: it must report NO decomposition, which
// TestBranchDecomposition_ClauseSatisfactionStaysRefused judges on its own
// terms.
func branchDifferentialSpecs(t testing.TB, c branchCorpus) []branchSpec {
	t.Helper()
	// The `location*` taxonomy resolution activation would thread onto
	// capabilityServiceAccess. Without it the `*` patterns bind nothing and that
	// lens's differential compares two empty rows.
	locationTaxonomy := map[string]map[string]struct{}{"location": {"location": {}}}

	return []branchSpec{
		{name: "capabilityEphemeral", spec: corpusSpec(t, "capabilityEphemeral"), anchor: c.actorKey, rows: 1,
			evidence: func(t *testing.T, row map[string]any) {
				require.Positive(t, listLen(row, "ephemeralGrants"),
					"no ephemeral grant survived — all three task branches folded empty")
			},
			content: func(row map[string]any) int { return listLen(row, "ephemeralGrants") }},
		{name: "myTasks", spec: corpusSpec(t, "myTasks"), anchor: c.actorKey, rows: 1,
			evidence: func(t *testing.T, row map[string]any) {
				require.Positive(t, listLen(row, "openTasks"),
					"no open task survived — both task branches folded empty")
			},
			content: func(row map[string]any) int { return listLen(row, "openTasks") }},
		{name: "identityAnchors", spec: corpusSpec(t, "identityAnchors"), anchor: c.actorKey, rows: 1,
			evidence: func(t *testing.T, row map[string]any) {
				require.Positive(t, listLen(row, "anchors"),
					"no anchor survived — all four anchor branches folded empty")
			},
			content: func(row map[string]any) int { return listLen(row, "anchors") }},
		{name: "edgeIdentity", spec: corpusSpec(t, "edgeIdentity"), anchor: c.actorKey, rows: 1,
			evidence: func(t *testing.T, row map[string]any) {
				require.Positive(t, listLen(row, "roles"), "the role branch folded empty")
				require.Positive(t, listLen(row, "anchors"), "every anchor branch folded empty")
				require.Positive(t, listLen(row, "selfAnchors"), "every self-anchor branch folded empty")
			},
			content: func(row map[string]any) int {
				return listLen(row, "roles") + listLen(row, "anchors") + listLen(row, "selfAnchors")
			}},
		{name: "leaseApplicationComplete", spec: corpusSpec(t, "leaseApplicationComplete"), anchor: c.leaseAppKey, rows: 1,
			evidence: func(t *testing.T, row map[string]any) {
				// One assertion per candidate subtree of the corpus's widest stage:
				// two hang BELOW the pinned applicant and unit (inst, onbTask→onbOp,
				// and mgr), three are rooted on the base (docInst, leaseDocObj,
				// sigTask→sigOp). Each subtree's count reaches the RETURN only
				// through a boolean, so the boolean is where the evidence lives.
				boolEvidence(t, row, "missing_bgcheck", false, "background-check instance")
				boolEvidence(t, row, "missing_payment", false, "payment instance")
				boolEvidence(t, row, "inflight_onboarding", true, "onboarding-task")
				boolEvidence(t, row, "inflight_signature", true, "signature-task")
				boolEvidence(t, row, "leaseDocAttached", true, "signed-lease object")
				boolEvidence(t, row, "missing_manager", false, "unit-manager")
				require.NotNil(t, row["docStoreName"], "the max() over the docGen branch folded empty")
			},
			content: func(row map[string]any) int {
				n := boolsTrue(row, "inflight_onboarding", "inflight_signature", "leaseDocAttached")
				if row["docStoreName"] != nil {
					n++
				}
				if !isTrue(row, "missing_bgcheck") && !isTrue(row, "missing_payment") {
					n++
				}
				return n
			}},
		{name: "applicantOnboarding", spec: corpusSpec(t, "applicantOnboarding"), anchor: c.onboardingActorKey, rows: 1,
			evidence: func(t *testing.T, row map[string]any) {
				// One assertion per sibling branch group. missing_onboarding
				// carries the application/unit walk's count (this anchor has no
				// .ssn, so the conjunct that would mask it is true), and
				// inflight_onboarding carries the task/operation walk's.
				boolEvidence(t, row, "missing_onboarding", true, "application")
				boolEvidence(t, row, "inflight_onboarding", true, "onboarding-task")
			},
			content: func(row map[string]any) int {
				return boolsTrue(row, "missing_onboarding", "inflight_onboarding")
			}},
		{name: "renewalComplete", spec: corpusSpec(t, "renewalComplete"), anchor: c.renewalKey, rows: 1,
			evidence: func(t *testing.T, row map[string]any) {
				require.NotNil(t, row["landlord"],
					"min() over the landlord subtree below the pinned unit folded empty")
				require.NotNil(t, row["bgcheckValidUntil"],
					"max() over the instance subtree below the pinned applicant folded empty")
			},
			content: func(row map[string]any) int {
				n := 0
				if row["landlord"] != nil {
					n++
				}
				if row["bgcheckValidUntil"] != nil {
					n++
				}
				return n
			}},

		// The rest of the corpus census's decomposing population. Every name in
		// decomposingCorpusLenses has to reach a differential, or the census's own
		// claim that each is covered by an equivalence proof is not true.
		{name: "capabilityRoles", spec: corpusSpec(t, "capabilityRoles"), anchor: c.actorKey, rows: 1,
			evidence: func(t *testing.T, row map[string]any) {
				require.Positive(t, listLen(row, "roles"), "the role branch folded empty")
				perms, _ := row["platformPermissions"].([]any)
				require.NotEmpty(t, perms)
				require.NotNil(t, perms[0].(map[string]any)["operationType"],
					"the permission hop below the role folded empty")
			},
			content: func(row map[string]any) int { return listLen(row, "roles") }},
		{name: "capabilityServiceAccess", spec: corpusSpec(t, "capabilityServiceAccess"), anchor: c.actorKey, rows: 1,
			labelExpansion: locationTaxonomy,
			evidence: func(t *testing.T, row map[string]any) {
				svcs, _ := row["serviceAccess"].([]any)
				require.NotEmpty(t, svcs)
				require.NotNil(t, svcs[0].(map[string]any)["service"],
					"the availableAt branch off the residence chain folded empty")
			},
			content: func(row map[string]any) int {
				svcs, _ := row["serviceAccess"].([]any)
				n := 0
				for _, sv := range svcs {
					if m, ok := sv.(map[string]any); ok && m["service"] != nil {
						n++
					}
				}
				return n
			}},
		{name: "identityErasureResidue", spec: corpusSpec(t, "identityErasureResidue"), anchor: c.actorKey, rows: 1,
			evidence: func(t *testing.T, row map[string]any) {
				// One per stage: this lens is five single-branch stages chained by
				// WITH, so each residue count witnesses its own stage's fold.
				for _, col := range []string{"boundInResidue", "boundOutResidue", "indexResidue",
					"duplicateOutResidue", "duplicateInResidue"} {
					v, isInt := row[col].(int64)
					require.Truef(t, isInt, "column %q must be a count, got %T", col, row[col])
					require.Positivef(t, v, "column %q counted nothing — its stage's branch folded empty", col)
				}
			},
			content: func(row map[string]any) int {
				n := 0
				for _, col := range []string{"boundInResidue", "boundOutResidue", "indexResidue",
					"duplicateOutResidue", "duplicateInResidue"} {
					if v, ok := row[col].(int64); ok {
						n += int(v)
					}
				}
				return n
			}},
		{name: "leaseExpiry", spec: corpusSpec(t, "leaseExpiry"), anchor: c.leaseAppKey,
			evidence: func(t *testing.T, row map[string]any) {
				// missing_renewalCycle can only be true when landlordCount > 0, so
				// it is the landlord subtree hanging below the deferred unit.
				boolEvidence(t, row, "missing_renewalCycle", true, "unit-manager")
			},
			content: func(row map[string]any) int { return boolsTrue(row, "missing_renewalCycle") }},
		{name: "leaseRentSettlement", spec: corpusSpec(t, "leaseRentSettlement"), anchor: c.leaseAppKey,
			evidence: func(t *testing.T, row map[string]any) {
				// missing_clause is false only when rentClauseCount > 0, which is
				// the governs branch.
				boolEvidence(t, row, "missing_clause", false, "rent-clause")
			},
			content: func(row map[string]any) int {
				if !isTrue(row, "missing_clause") {
					return 1
				}
				return 0
			}},
		{name: "objectAttachments", spec: corpusSpec(t, "objectAttachments"), anchor: c.objectKey, rows: 1,
			evidence: func(t *testing.T, row map[string]any) {
				require.Positive(t, listLen(row, "owners", "ownerKey"), "the owner-link branch folded empty")
			},
			content: func(row map[string]any) int { return listLen(row, "owners", "ownerKey") }},
		// clinicNoShowSettlement's new deferred subtree is [credit,tx] — the
		// settles hop off the appointment and the reverses hop off that
		// transaction. chargeTxKey (max(tx.key)) witnesses the settles half;
		// missing_reversal false (the reverses hop found a live credit)
		// witnesses the other, since a folded-empty reverses arm would leave
		// reversalCount at 0 and the gap reading unconverged.
		{name: "clinicNoShowSettlement", spec: corpusSpec(t, "clinicNoShowSettlement"), anchor: c.clinicNoShowApptKey, rows: 1,
			evidence: func(t *testing.T, row map[string]any) {
				require.NotNilf(t, row["chargeTxKey"], "the settles half of the deferred subtree folded empty")
				boolEvidence(t, row, "missing_reversal", false, "reverses")
			},
			content: func(row map[string]any) int {
				n := 0
				if row["chargeTxKey"] != nil {
					n++
				}
				if !isTrue(row, "missing_reversal") {
					n++
				}
				return n
			}},

		// The UNANCHORED read lenses: they bind every vertex of their head's type
		// in the KV rather than one, which is the multi-base-row shape the
		// anchored lenses above cannot reach.
		{name: "leaseApplicationsRead", spec: corpusSpec(t, "leaseApplicationsRead"),
			evidence: func(t *testing.T, row map[string]any) {
				// missing_bgcheck false witnesses the readiness-instance subtree
				// (freshBgComplete > 0); escalated_bgcheck true witnesses the
				// augur-proposal branch, the other deferred subtree of the stage.
				boolEvidence(t, row, "missing_bgcheck", false, "readiness instance")
				boolEvidence(t, row, "escalated_bgcheck", true, "augur proposal")
			},
			content: func(row map[string]any) int { return boolsTrue(row, "escalated_bgcheck") }},
		// opCatalog is UNANCHORED and anchored on the OP META rather than on any
		// actor — one row per op the corpus seeds. Every one of them is granted
		// (seedBranchCorpus's catalogRole), so the role collect is non-empty on
		// whichever row comes first, and an empty grantedToRoles anywhere means
		// the permission→role subtree folded empty rather than that this corpus
		// happened to seed an ungranted op.
		{name: "opCatalog", spec: corpusSpec(t, "opCatalog"),
			evidence: func(t *testing.T, row map[string]any) {
				require.Positive(t, listLen(row, "grantedToRoles"),
					"the permission→role branch folded empty")
			},
			content: func(row map[string]any) int { return listLen(row, "grantedToRoles") }},
		{name: "landlordLeaseApplicationsRead", spec: corpusSpec(t, "landlordLeaseApplicationsRead"),
			evidence: func(t *testing.T, row map[string]any) {
				// This lens's only deferred subtree is the readiness instance walk,
				// and `qualified` is the column its counts reach.
				boolEvidence(t, row, "qualified", true, "readiness instance")
			},
			content: func(row map[string]any) int { return boolsTrue(row, "qualified") }},
		// clinicPatientsRead binds every patient. Its identity hop is PINNED (the
		// `id` node is a non-aggregating item of the WITH), so the one subtree it
		// defers is the appointment -> provider -> building walk, and
		// authz_anchors is the only column that walk reaches: the row's own
		// patient NanoID is a list literal, present whether or not the walk folded
		// anything, and every entry PAST it is a building the deferred subtree
		// collected.
		{name: "clinicPatientsRead", spec: corpusSpec(t, "clinicPatientsRead"),
			evidence: func(t *testing.T, row map[string]any) {
				require.Greaterf(t, listLen(row, "authz_anchors"), 1,
					"authz_anchors carries only the row's own patient anchor — the workplace branch folded empty")
			},
			// Counting the whole column would report every corpus as productive on
			// the self anchor alone, which is the reading the randomized
			// differential's own guard exists to refuse.
			content: func(row map[string]any) int {
				if n := listLen(row, "authz_anchors"); n > 1 {
					return n - 1
				}
				return 0
			}},
	}
}

// generatedProducerDifferentialSpecs are the three generated read-grant
// producers as branchSpecs, so the census's coverage claim reaches them through
// the same enumeration the hand-authored lenses do.
func generatedProducerDifferentialSpecs(t *testing.T, actorKey string) []branchSpec {
	t.Helper()
	specs := generatedReadGrantProducers(t)
	out := make([]branchSpec, 0, len(specs))
	for _, name := range sortedNames(namesOf(specs)) {
		out = append(out, branchSpec{name: name, spec: specs[name], anchor: actorKey, rows: 1})
	}
	return out
}

// executeBothBranchWays runs one parsed rule twice over the same corpus —
// decomposed, then as the flat product — and fails unless the two projections
// are identical in order and content AND the two evaluations certified the same
// read-surface footprint.
//
// It also fails when the rule decomposed nothing, since two identical code
// paths prove nothing. clauseSatisfaction goes through
// executeBothBranchWaysRefused instead.
func executeBothBranchWays(t *testing.T, spec, actorKey string, adjKV, coreKV *substrate.KV) []ruleengine.ProjectionResult {
	t.Helper()
	return executeBothBranchWaysExpanded(t, spec, actorKey, nil, adjKV, coreKV)
}

func executeBothBranchWaysExpanded(t *testing.T, spec, actorKey string,
	expansion map[string]map[string]struct{}, adjKV, coreKV *substrate.KV) []ruleengine.ProjectionResult {
	t.Helper()
	eng := New()
	cr, err := eng.Parse(spec)
	require.NoError(t, err, "spec must parse:\n%s", spec)
	compiled := cr.(*CompiledRule)
	require.NotEmpty(t, compiled.branchStages,
		"this rule decomposes no branch, so running it twice compares one path with itself:\n%s", spec)
	if expansion != nil {
		compiled = WithLabelExpansion(compiled, expansion)
	}

	params := ruleengine.EventContext{Parameters: map[string]any{
		"actorKey": actorKey,
		"now":      time.Now().UTC().Format(time.RFC3339),
	}}
	decomposed, decomposedPrint, err := eng.ExecuteWithFootprint(context.Background(), compiled, params, adjKV, coreKV)
	require.NoError(t, err)

	flat, flatPrint, err := eng.ExecuteWithFootprint(
		context.Background(), withoutBranchDecomposition(compiled), params, adjKV, coreKV)
	require.NoError(t, err)

	require.Equal(t, flat, decomposed,
		"decomposition moved a projected row — order and content must be identical:\n%s", spec)
	require.Equal(t, flatPrint, decomposedPrint,
		"decomposition changed the evaluation's read-surface footprint:\n%s", spec)
	// Two empty footprints are equal, and would read as agreement while proving
	// that neither evaluation read anything. The footprint is the certificate an
	// auth-plane caller re-validates against, so an equality over it is only
	// worth what it covers.
	require.NotEmptyf(t, decomposedPrint.NodeRevisions,
		"the evaluation certified an EMPTY read surface — the footprint equality above compared nothing:\n%s", spec)
	return decomposed
}

// TestBranchDecomposition_ShippedLensesProjectIdenticalRows is §7's differential
// half: the real §2 lens cyphers, over a corpus that populates every branch,
// project byte-identical rows with decomposition on and off.
func TestBranchDecomposition_ShippedLensesProjectIdenticalRows(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	corpus := seedBranchCorpus(t, reg, adjKV, coreKV, fullBranchShape("full_"))

	for _, s := range branchDifferentialSpecs(t, corpus) {
		t.Run(s.name, func(t *testing.T) {
			rows := executeBothBranchWaysExpanded(t, s.spec, s.anchor, s.labelExpansion, adjKV, coreKV)
			if s.rows > 0 {
				require.Lenf(t, rows, s.rows, "%s is anchored on one vertex and projects one row", s.name)
			}
			require.NotEmptyf(t, rows, "%s projected nothing — its differential compared two empty sets", s.name)
			s.evidence(t, rows[0].Values)
		})
	}
}

// TestBranchDecomposition_EveryDecomposingCorpusLensReachesADifferential is the
// census's coverage claim, stated where it can be checked. Every lens the
// corpus census records as decomposing must be executed under both
// configurations by a test in this package — a claim in a comment next to a
// list is not one, and eight of the seventeen were not covered when it was
// first written.
func TestBranchDecomposition_EveryDecomposingCorpusLensReachesADifferential(t *testing.T) {
	covered := map[string]bool{}
	for _, s := range branchDifferentialSpecs(t, branchCorpus{}) {
		covered[s.name] = true
	}
	for _, s := range generatedProducerDifferentialSpecs(t, "") {
		covered[s.name] = true
	}
	// The population this list mirrors is pinned by
	// TestCorpusBranchDecomposition_DecomposingLensesAreTheKnownPopulation in
	// package refractor, which sees the whole installed registry; this package
	// cannot enumerate it, so the names are restated and the two must agree.
	for _, name := range []string{
		"applicantOnboarding",
		"capabilityEphemeral", "capabilityRoles", "capabilityServiceAccess",
		"clinicNoShowSettlement", "clinicPatientsRead",
		"edgeIdentity", "edgeManifestProviderReadGrants", "edgeManifestReadGrants",
		"edgeManifestStaffReadGrants", "identityAnchors", "identityErasureResidue",
		"landlordLeaseApplicationsRead", "leaseApplicationComplete", "leaseApplicationsRead",
		"leaseExpiry", "leaseRentSettlement", "myTasks", "objectAttachments", "opCatalog",
		"renewalComplete",
	} {
		require.Truef(t, covered[name],
			"%s decomposes in the shipped corpus but no differential in this package executes it "+
				"under both configurations", name)
	}
}

// TestBranchDecomposition_RandomizedCorporaDifferential is §7's randomized
// half: the same on/off comparison over independently randomized corpora, each
// with its own shared workplace/residence, multi-parent containedIn arms,
// randomly-empty branches, a report who is also a role-holder, and tasks that
// reach the projection down two branches at once. Seeding is deterministic, so
// a failure reproduces exactly.
func TestBranchDecomposition_RandomizedCorporaDifferential(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()

	const corpora = 60
	productive := 0
	for i := 0; i < corpora; i++ {
		shape := randomBranchShape(fmt.Sprintf("rnd%d_", i), rand.New(rand.NewSource(int64(i)+1)))
		corpus := seedBranchCorpus(t, reg, adjKV, coreKV, shape)
		content := 0
		for _, s := range branchDifferentialSpecs(t, corpus) {
			t.Run(fmt.Sprintf("corpus%d/%s", i, s.name), func(t *testing.T) {
				rows := executeBothBranchWaysExpanded(t, s.spec, s.anchor, s.labelExpansion, adjKV, coreKV)
				for _, row := range rows {
					content += s.content(row.Values)
				}
			})
		}
		// A corpus whose branches all drew empty compares two empty projections
		// and proves nothing on its own; the guard is that most do not.
		if content > 0 {
			productive++
		}
	}
	require.Greaterf(t, productive, corpora*3/4,
		"only %d of %d randomized corpora folded any branch content — the rest compared empty aggregates",
		productive, corpora)
}

// TestBranchDecomposition_GeneratedProducersProjectIdenticalRows runs the same
// differential over the GENERATED read-grant producers, whose walks carry the
// zero-hop `*0..` and multi-parent `containedIn` arms the hand-authored §2
// lenses do not. Those producers are already hand-staged (§12), so each of
// their stages holds ONE branch group — the shape that must decompose to
// exactly itself.
func TestBranchDecomposition_GeneratedProducersProjectIdenticalRows(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	specs := generatedReadGrantProducers(t)
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()

	const corpora = 6
	for i := 0; i < corpora; i++ {
		shape := randomCorpusShape(fmt.Sprintf("gen%d_", i), rand.New(rand.NewSource(int64(i)+1)))
		actorKey := seedReadGrantCorpus(t, reg, adjKV, coreKV, shape)
		for _, name := range sortedNames(namesOf(specs)) {
			t.Run(fmt.Sprintf("corpus%d/%s", i, name), func(t *testing.T) {
				executeBothBranchWays(t, specs[name], actorKey, adjKV, coreKV)
			})
		}
	}

	// Unanchored, so the producers project one row per actor and the grouping
	// key has to keep them apart: a decomposition that fed one actor's branch
	// rows into another actor's fold shows up here and nowhere above.
	for _, name := range sortedNames(namesOf(specs)) {
		t.Run("multi-actor/"+name, func(t *testing.T) {
			rows := executeBothBranchWays(t, unanchoredProducer(t, specs[name]), "", adjKV, coreKV)
			require.Lenf(t, rows, corpora, "%s must project one row per seeded actor", name)
		})
	}
}

// TestBranchDecomposition_CapRefusesTheProductAndAdmitsTheBranches is §7's cap
// behaviour: capabilityEphemeral's three-branch shape over a corpus whose
// product exceeds a 50,000-row cap. The flat product is refused; decomposed,
// the same evaluation succeeds under the same cap and projects exactly the rows
// an uncapped flat run does.
//
// This is the design's headline claim stated as an outcome — peak rows fall
// from the product of the branches to the largest single branch — and the peak
// each configuration actually reached is read off the engine's own gauge rather
// than extrapolated.
func TestBranchDecomposition_CapRefusesTheProductAndAdmitsTheBranches(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	// 50 direct × 30 reports' tasks × 50 queued: 75,000 product rows against
	// 50 rows for the largest single branch.
	corpus := seedBranchCorpus(t, reg, adjKV, coreKV, branchCorpusShape{
		Prefix: "cap_", Tasks: 50, Reports: 30, TasksPerReport: 1, Roles: 1, QueuedPerRole: 50,
	})
	spec := corpusSpec(t, "capabilityEphemeral")
	params := ruleengine.EventContext{Parameters: map[string]any{
		"actorKey": corpus.actorKey,
		"now":      time.Now().UTC().Format(time.RFC3339),
	}}

	const lowCap = 50_000
	capped := New().WithMaxBindings(lowCap)
	cr, err := capped.Parse(spec)
	require.NoError(t, err)
	compiled := cr.(*CompiledRule)
	require.Len(t, compiled.branchStages, 1, "capabilityEphemeral has one stage and it must decompose")

	_, _, flatStats, err := capped.ExecuteWithStats(
		context.Background(), withoutBranchDecomposition(compiled), params, adjKV, coreKV)
	require.Error(t, err, "the flat product must overrun a %d-row cap", lowCap)
	require.Contains(t, err.Error(), "over the cap of",
		"the refusal must be the binding-set cap, not some other error: %v", err)

	decomposed, _, branchStats, err := capped.ExecuteWithStats(context.Background(), compiled, params, adjKV, coreKV)
	require.NoError(t, err, "decomposed, the same evaluation must fit under the same cap")

	uncapped := New().WithMaxBindings(0)
	uncappedCR, err := uncapped.Parse(spec)
	require.NoError(t, err)
	flat, _, uncappedStats, err := uncapped.ExecuteWithStats(
		context.Background(), withoutBranchDecomposition(uncappedCR.(*CompiledRule)), params, adjKV, coreKV)
	require.NoError(t, err, "uncapped, the flat product must complete so its rows can be compared")
	require.Equal(t, flat, decomposed,
		"the decomposed evaluation must project exactly the rows the uncapped flat product does")

	require.Greater(t, uncappedStats.PeakBindingRows, lowCap,
		"the flat product must exceed the cap by the engine's own gauge, not by arithmetic in this comment")
	require.Less(t, branchStats.PeakBindingRows, uncappedStats.PeakBindingRows/100,
		"decomposed peak %d vs flat peak %d — the fall must be the product collapsing to the largest branch",
		branchStats.PeakBindingRows, uncappedStats.PeakBindingRows)
	t.Logf("capabilityEphemeral peak binding rows: flat=%d refused-at-cap=%d decomposed=%d",
		uncappedStats.PeakBindingRows, flatStats.PeakBindingRows, branchStats.PeakBindingRows)
}

// TestBranchDecomposition_ClauseSatisfactionStaysRefused is §4.4: the one live
// lens whose flat multiplicity is load-bearing must behave byte-identically,
// and the reason must be the §4.2 precondition rather than an accident of its
// shape.
func TestBranchDecomposition_ClauseSatisfactionStaysRefused(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	spec := corpusSpec(t, "clauseSatisfaction")
	eng := New()
	cr, err := eng.Parse(spec)
	require.NoError(t, err)
	compiled := cr.(*CompiledRule)
	require.Empty(t, compiled.branchStages,
		"clauseSatisfaction must decompose nothing: its count(t.key) is non-DISTINCT and today counts the product")

	stages := compiled.BranchDecomposition()
	require.NotEmpty(t, stages)
	require.Equal(t, 4, stages[0].Groups,
		"the refusal must be about a stage that really does hold four sibling groups")
	require.Truef(t, strings.HasPrefix(stages[0].Refusal, refuseMultiplicitySensitive),
		"the refusal must be the §4.2 precondition, got %q", stages[0].Refusal)

	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	corpus := seedBranchCorpus(t, reg, adjKV, coreKV, fullBranchShape("cs_"))
	params := ruleengine.EventContext{Parameters: map[string]any{
		"actorKey": corpus.clauseKey,
		"now":      time.Now().UTC().Format(time.RFC3339),
	}}
	got, err := eng.ExecuteWith(context.Background(), compiled, params, adjKV, coreKV)
	require.NoError(t, err)
	want, err := eng.ExecuteWith(
		context.Background(), withoutBranchDecomposition(compiled), params, adjKV, coreKV)
	require.NoError(t, err)
	require.Equal(t, want, got)
	require.NotEmpty(t, got, "the corpus must actually bind the clause, or this proves nothing")
}

// TestBranchDecomposition_SeededEvaluationProjectsIdenticalRows runs the
// differential on the one shape the §2 lenses cannot reach: an evaluation whose
// anchor pattern is a labeled node with no `key` property, so the per-event seed
// anchor is really armed and the executor narrows its first scan to the event
// vertex. Every installed lens anchors on `{key: $actorKey}`, which
// seedAnchorBinds refuses, so nothing in the corpus exercises the seed and
// decomposition together.
func TestBranchDecomposition_SeededEvaluationProjectsIdenticalRows(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	seedBranchCorpus(t, reg, adjKV, coreKV, fullBranchShape("seed_"))

	const spec = `
MATCH (t:task)
OPTIONAL MATCH (t)-[:forOperation]->(op)
OPTIONAL MATCH (t)-[:scopedTo]->(tgt)
RETURN t.key AS taskKey,
  collect(DISTINCT op.key) AS ops,
  collect(DISTINCT tgt.key) AS tgts`

	eng := New()
	cr, err := eng.Parse(spec)
	require.NoError(t, err)
	compiled := cr.(*CompiledRule)
	require.NotEmpty(t, compiled.branchStages, "both optional branches must fold")

	seed := vtxKey(reg, "seed_task0")
	require.NotEmpty(t, seed)
	params := ruleengine.EventContext{SeedAnchor: seed}

	decomposed, err := eng.ExecuteWith(context.Background(), compiled, params, adjKV, coreKV)
	require.NoError(t, err)
	flat, err := eng.ExecuteWith(
		context.Background(), withoutBranchDecomposition(compiled), params, adjKV, coreKV)
	require.NoError(t, err)

	require.Equal(t, flat, decomposed)
	require.Lenf(t, decomposed, 1,
		"the armed seed must narrow the anchor scan to the one seeded task, got %d rows", len(decomposed))
	require.Equal(t, seed, decomposed[0].Values["taskKey"])
	require.NotEmpty(t, decomposed[0].Values["ops"], "the forOperation branch folded empty")
	require.NotEmpty(t, decomposed[0].Values["tgts"], "the scopedTo branch folded empty")
}

// seedMultiActorBranchCorpus writes actors identities, each carrying tasks
// tasks and bookings bookings, and nothing else — so an UNANCHORED head binds
// exactly `actors` base rows.
//
// Every cap and gauge assertion elsewhere in this package runs against a lens
// anchored on `{key: $actorKey}`, i.e. ONE base row, where a branch's running
// total and its widest single expansion are the same number. That is the shape
// under which a per-base-row cap and a cumulative one are indistinguishable, and
// it is why this corpus exists.
func seedMultiActorBranchCorpus(t testing.TB, reg *fixtureRegistry, adjKV, coreKV *substrate.KV,
	prefix string, actors, tasks, bookings int) {
	t.Helper()
	for a := 0; a < actors; a++ {
		actor := fmt.Sprintf("%sactor%d", prefix, a)
		putVertex(t, reg, coreKV, actor, "identity", nil)
		for i := 0; i < tasks; i++ {
			task := fmt.Sprintf("%stask_%d_%d", prefix, a, i)
			putVertex(t, reg, coreKV, task, "task", nil)
			putEdge(t, reg, adjKV, "assignedTo", task, actor)
		}
		for i := 0; i < bookings; i++ {
			bk := fmt.Sprintf("%sbk_%d_%d", prefix, a, i)
			putVertex(t, reg, coreKV, bk, "booking", nil)
			putEdge(t, reg, adjKV, "bookedBy", bk, actor)
		}
	}
}

// multiActorTwoBranchSpec is the unanchored two-branch shape: `actors` base
// rows, each expanding independently through a task branch and a booking branch.
const multiActorTwoBranchSpec = `
MATCH (identity:identity)
OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
OPTIONAL MATCH (identity)<-[:bookedBy]-(bk:booking)
RETURN identity.key AS actorKey,
  collect(DISTINCT task.key) AS tasks,
  collect(DISTINCT bk.key) AS bookings`

// TestBranchDecomposition_CapCountsTheWholeBranchNotOneBaseRow pins the cap's
// semantics over MANY base rows, which is the only shape that can see it.
//
// A branch walked once per base row really does cost the whole total, even
// though each expansion is discarded before the next is built, so the cap is
// enforced against that total. Capping one expansion instead — or dropping the
// check altogether — silently admits an evaluation the product path refuses,
// which is the cap weakening in the one direction it must never weaken.
func TestBranchDecomposition_CapCountsTheWholeBranchNotOneBaseRow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	const actors, tasks, bookings = 10, 20, 3
	seedMultiActorBranchCorpus(t, reg, adjKV, coreKV, "cap_", actors, tasks, bookings)

	// The three quantities the assertions below separate: what one base row's
	// widest expansion holds, what the whole branch costs, and what the product
	// materializes.
	const widestExpansion = tasks + 1 // the source row plus its expansion
	const branchTotal = actors * tasks
	const flatProduct = actors * tasks * bookings

	params := ruleengine.EventContext{}
	parse := func(t *testing.T, eng *Engine) *CompiledRule {
		t.Helper()
		cr, err := eng.Parse(multiActorTwoBranchSpec)
		require.NoError(t, err)
		compiled := cr.(*CompiledRule)
		require.NotEmpty(t, compiled.branchStages, "both branches must fold")
		return compiled
	}

	// A cap ABOVE one base row's widest expansion but BELOW the branch's total.
	// A per-base-row cap, and a missing check, both let this through.
	t.Run("cap between one expansion and the whole branch", func(t *testing.T) {
		eng := New().WithMaxBindings(widestExpansion * 4)
		require.Less(t, widestExpansion*4, branchTotal,
			"the cap must sit between the two quantities or this test cannot tell them apart")
		_, err := eng.ExecuteWith(context.Background(), parse(t, eng), params, adjKV, coreKV)
		require.Error(t, err,
			"the cap must count the whole branch: %d rows are walked through it across %d base rows",
			branchTotal, actors)
		require.Contains(t, err.Error(), "over the cap of")
	})

	// A cap ABOVE the branch total but BELOW the product: this is the relief
	// decomposition buys, and the flat path must still refuse at the same cap.
	t.Run("cap between the whole branch and the product", func(t *testing.T) {
		eng := New().WithMaxBindings(branchTotal * 3 / 2)
		require.Less(t, branchTotal*3/2, flatProduct)
		compiled := parse(t, eng)

		decomposed, _, stats, err := eng.ExecuteWithStats(context.Background(), compiled, params, adjKV, coreKV)
		require.NoError(t, err, "decomposed, the branches must fit under a cap the product does not")
		require.Len(t, decomposed, actors)

		_, _, _, err = eng.ExecuteWithStats(
			context.Background(), withoutBranchDecomposition(compiled), params, adjKV, coreKV)
		require.Error(t, err, "the product must still overrun the same cap")

		// The gauge is the CO-RESIDENT high-water mark, not the branch's running
		// total: a summed gauge reports the same number for the product and for
		// the branches that replaced it, and Inc 2 could not then see Inc 1 at
		// all (docs/observability/health-kv-schema.md's per-lens entry says
		// "materialized at one time", "never co-resident").
		require.LessOrEqualf(t, stats.PeakBindingRows, widestExpansion*2,
			"peak %d must be one base row's widest expansion (%d), not the branch total (%d)",
			stats.PeakBindingRows, widestExpansion, branchTotal)
		require.Positive(t, stats.PeakBindingRows)
	})

	// The measurement, uncapped, for the record: the product against the widest
	// thing decomposition ever holds.
	t.Run("peak measurement", func(t *testing.T) {
		eng := New().WithMaxBindings(0)
		compiled := parse(t, eng)
		decomposed, _, branchStats, err := eng.ExecuteWithStats(context.Background(), compiled, params, adjKV, coreKV)
		require.NoError(t, err)
		flat, _, flatStats, err := eng.ExecuteWithStats(
			context.Background(), withoutBranchDecomposition(compiled), params, adjKV, coreKV)
		require.NoError(t, err)
		require.Equal(t, flat, decomposed)
		require.Equal(t, flatProduct, flatStats.PeakBindingRows,
			"the product path's peak is the product itself")
		require.Lessf(t, branchStats.PeakBindingRows, flatStats.PeakBindingRows/10,
			"decomposed peak %d vs product peak %d", branchStats.PeakBindingRows, flatStats.PeakBindingRows)
		t.Logf("%d actors × %d tasks × %d bookings: product peak=%d decomposed peak=%d (branch total walked=%d)",
			actors, tasks, bookings, flatStats.PeakBindingRows, branchStats.PeakBindingRows, branchTotal)
	})
}

// executeBothWaysRaw runs spec under both configurations and returns
// (flat, decomposed) without asserting that anything decomposed — the shape the
// tests below need, where the point is sometimes that NOTHING decomposes.
func executeBothWaysRaw(t *testing.T, spec string, params ruleengine.EventContext,
	adjKV, coreKV *substrate.KV) (flat, decomposed []ruleengine.ProjectionResult, cr *CompiledRule) {
	t.Helper()
	eng := New()
	parsed, err := eng.Parse(spec)
	require.NoErrorf(t, err, "spec must parse:\n%s", spec)
	cr = parsed.(*CompiledRule)

	decomposed, err = eng.ExecuteWith(context.Background(), cr, params, adjKV, coreKV)
	require.NoError(t, err)
	flat, err = eng.ExecuteWith(context.Background(), withoutBranchDecomposition(cr), params, adjKV, coreKV)
	require.NoError(t, err)
	return flat, decomposed, cr
}

// TestBranchDecomposition_PatternPredicatePropertyIsARealDependency: a variable
// referenced ONLY inside a pattern-predicate's property map is a dependency of
// that predicate, and the reference walk has to see it. It did not — walking a
// pattern's own node/rel variables and reporting unknown=false — so a WHERE
// reaching from one branch into another read as depending on neither, the
// clause was never parented under the branch it reads, and the two evaluated
// apart. Flat granted one instance; decomposed granted two, which on a grant
// document is the over-grant direction.
func TestBranchDecomposition_PatternPredicatePropertyIsARealDependency(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()

	actor := "pp_actor"
	putVertex(t, reg, coreKV, actor, "identity", nil)
	// One task, and two service instances: svc0 is scopedTo that task, svc1 is
	// not. The predicate excludes svc0 exactly when `task` is bound on the row.
	putVertex(t, reg, coreKV, "pp_task", "task", nil)
	putEdge(t, reg, adjKV, "assignedTo", "pp_task", actor)
	for i := 0; i < 2; i++ {
		svc := fmt.Sprintf("pp_svc%d", i)
		putVertex(t, reg, coreKV, svc, "service", nil)
		putEdge(t, reg, adjKV, "providedTo", svc, actor)
	}
	putEdge(t, reg, adjKV, "scopedTo", "pp_svc0", "pp_task")

	const spec = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
OPTIONAL MATCH (identity)<-[:providedTo]-(inst:service)
  WHERE NOT (inst)-[:scopedTo]->(:task {key: task.key})
RETURN identity.key AS actorKey,
  collect(DISTINCT task.key) AS tasks,
  collect(DISTINCT inst.key) AS instances`

	params := ruleengine.EventContext{Parameters: map[string]any{"actorKey": vtxKey(reg, actor)}}
	flat, decomposed, cr := executeBothWaysRaw(t, spec, params, adjKV, coreKV)
	require.Equal(t, flat, decomposed,
		"the instance branch reads `task` through a pattern-predicate property; evaluating the two apart grants an instance the product excludes")

	// And the mechanism, not just the outcome: the reading clause is parented
	// under the branch it reads, so both run in ONE binding stream.
	stages := cr.BranchDecomposition()
	require.Len(t, stages, 1)
	require.Equal(t, []string{"inst,task"}, stages[0].Deferred,
		"the clause reading `task` must join `task`'s own subtree")

	require.Len(t, flat, 1)
	instances, _ := flat[0].Values["instances"].([]any)
	require.Lenf(t, instances, 1,
		"the corpus must exclude exactly one instance, or the two configurations agree for lack of anything to disagree about: %v", instances)
}

// TestBranchDecomposition_ForwardReferenceRefusesTheStage: a clause reading a
// name a LATER clause of the same stage binds. Cypher permits it — Parse runs no
// scope check and evalExpr answers an unbound variable with nil — so the flat
// path evaluates the WHERE against the value the later clause bound, while
// decomposition would evaluate it against nil. The reference walk skipped names
// that were not bound YET, so the read landed on the floor: it neither parented
// the clause nor refused. Flat collected no task; decomposed collected one.
func TestBranchDecomposition_ForwardReferenceRefusesTheStage(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()

	actor := "fw_actor"
	putVertex(t, reg, coreKV, actor, "identity", nil)
	putVertex(t, reg, coreKV, "fw_task", "task", map[string]any{
		"data": map[string]any{"status": "open"},
	})
	putEdge(t, reg, adjKV, "assignedTo", "fw_task", actor)
	putVertex(t, reg, coreKV, "fw_role", "role", map[string]any{
		"data": map[string]any{"status": "active"},
	})
	putEdge(t, reg, adjKV, "holdsRole", actor, "fw_role")

	const spec = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
  WHERE task.data.status = role.data.status
OPTIONAL MATCH (identity)-[:holdsRole]->(role:role)
RETURN identity.key AS actorKey,
  role.key AS roleKey,
  collect(DISTINCT task.key) AS tasks`

	params := ruleengine.EventContext{Parameters: map[string]any{"actorKey": vtxKey(reg, actor)}}
	flat, decomposed, cr := executeBothWaysRaw(t, spec, params, adjKV, coreKV)
	require.Equal(t, flat, decomposed,
		"a forward reference must not be silently re-read against nil")

	stages := cr.BranchDecomposition()
	require.Len(t, stages, 1)
	require.Truef(t, strings.HasPrefix(stages[0].Refusal, refuseForwardReference),
		"the stage must be refused for the forward reference, got %q", stages[0].Refusal)
	require.Empty(t, cr.branchStages, "a refused stage decomposes nothing")

	// The positive vector: the same two clauses in the order that makes the
	// reference backward, which decomposes.
	_, _, ordered := executeBothWaysRaw(t, `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:holdsRole]->(role:role)
OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
  WHERE task.data.status = role.data.status
RETURN identity.key AS actorKey,
  role.key AS roleKey,
  collect(DISTINCT task.key) AS tasks`, params, adjKV, coreKV)
	require.NotEmpty(t, ordered.branchStages,
		"read backwards, the same pair is one parented subtree and decomposes")
}

// TestBranchDecomposition_PinPropagatesToAncestors: a non-aggregating item pins
// the clause that binds its variable AND every clause on the path up to the
// group root, because a clause cannot run in the product while the clause that
// binds its own anchor has been deferred. Pinning only the owning clause leaves
// the parent deferred, the grouping term evaluates against an unbound variable,
// and two rows collapse into one carrying a null column.
func TestBranchDecomposition_PinPropagatesToAncestors(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()

	actor := "anc_actor"
	putVertex(t, reg, coreKV, actor, "identity", nil)
	for i := 0; i < 2; i++ {
		task := fmt.Sprintf("anc_task%d", i)
		op := fmt.Sprintf("anc_op%d", i)
		putVertex(t, reg, coreKV, task, "task", nil)
		putVertex(t, reg, coreKV, op, "meta", nil)
		putEdge(t, reg, adjKV, "assignedTo", task, actor)
		putEdge(t, reg, adjKV, "forOperation", task, op)
		bk := fmt.Sprintf("anc_bk%d", i)
		putVertex(t, reg, coreKV, bk, "booking", nil)
		putEdge(t, reg, adjKV, "bookedBy", bk, actor)
	}

	const spec = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
OPTIONAL MATCH (task)-[:forOperation]->(op)
OPTIONAL MATCH (identity)<-[:bookedBy]-(bk:booking)
RETURN identity.key AS actorKey,
  op.key AS opKey,
  collect(DISTINCT bk.key) AS bookings`

	params := ruleengine.EventContext{Parameters: map[string]any{"actorKey": vtxKey(reg, actor)}}
	flat, decomposed, cr := executeBothWaysRaw(t, spec, params, adjKV, coreKV)
	require.Equal(t, flat, decomposed)

	stages := cr.BranchDecomposition()
	require.Len(t, stages, 1)
	require.Equal(t, []string{"bk"}, stages[0].Deferred,
		"`op` is pinned by the grouping term and `task` is pinned as its ancestor; only the booking branch folds")

	require.Lenf(t, decomposed, 2,
		"one row per op — a deferred ancestor collapses them into one, which is what makes this pin load-bearing")
	for _, row := range decomposed {
		require.NotNilf(t, row.Values["opKey"], "opKey is a grouping term and must be bound on every row")
	}
}
