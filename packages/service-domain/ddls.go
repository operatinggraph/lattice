package servicedomain

import (
	"strings"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
)

// serviceProviderRoleKey is identity-domain's "provider" role key, computed
// deterministically (pkgmgr.RoleID mirrors what the installer mints at
// install time — no KV read required). BindServiceProviderIdentity's script
// pins its holdsRole grant against this literal rather than trusting any
// live vtx.role.* the caller supplies — mirrors clinic-domain's
// providerRoleKey pin (packages/clinic-domain/ddls.go).
var serviceProviderRoleKey = "vtx.role." + pkgmgr.RoleID("identity-domain", "provider")

// Canonical names for the serviceprovider vertexType DDL + its guard
// aspects (the provider-archetype binding, persona-worlds-design.md Fire
// W0) — a lean, generic entity template-attached vendors (e.g. a laundry
// operator) bind their login to, distinct from the `service` DDL above.
const (
	serviceProviderVertexDDL              = "serviceprovider"
	serviceProviderProfileAspectDDL       = "serviceProviderProfile"
	serviceProviderIdentityClaimAspectDDL = "serviceProviderIdentityClaim"
	identityServiceProviderClaimAspectDDL = "identityServiceProviderClaim"
)

// aspectDeclarationOnlyScript is the shared no-op script for every
// declaration-only aspect-type DDL — its op handler lives on the owning
// vertexType DDL, so this script never executes as a dispatch target (the
// operationType→script index always resolves to the vertexType DDL first).
// Mirrors clinic-domain's / wellness-domain's identical helper.
const aspectDeclarationOnlyScript = `
def execute(state, op):
    fail("InvalidState: this aspect-type DDL is declaration-only; its op is owned by a vertexType DDL")
`

// DDLs returns the package's DDL meta-vertex declarations.
//
// The `service` DDL (vertex-type class) handles all lifecycle + wiring ops
// for both the template and instance classes of every service family. The
// `serviceprovider` DDL + its two guard aspect-type DDLs are the
// provider-archetype binding (persona-worlds-design.md Fire W0).
//
// Architectural rules (binding — same known-key discipline as
// orchestration-base / identity-domain):
//
//   - The script reads ONLY by known key. No prefix scans, no adjacency
//     lookups, no lens-output reads. Each op validates its link endpoints
//     (the template + identity an instance links to, the optional location +
//     provider a template links to) by reading each by the key the caller
//     lists in ContextHint.Reads. RecordServiceOutcome reads the instance
//     root (its envelope class is the discriminator) + its .outcome aspect by
//     their known keys.
//   - No-orphan invariant (FR29 / P4): CreateServiceInstance REQUIRES a live
//     template (instanceOf) and a live applicant identity (providedTo) and
//     rejects (structured ScriptError) if either is absent. The optional
//     template providedBy endpoint is validated likewise before its link is
//     written.
//
// Service shape (Contract #1 §1.1 + D5 — root data minimal, business data in
// aspects):
//
//	vtx.service.<id>            root data = {}; ENVELOPE class is the discriminator:
//	                            "service.<x>.template" | "service.<x>.instance" (P7 — no .class aspect)
//	vtx.service.<id>.outcome    aspect (INSTANCE only, written by RecordServiceOutcome):
//	                            { status ("completed"|"failed"), completedAt (canonical-UTC RFC3339) }
//	lnk.service.<tplId>.providedBy.<provType>.<provId>     # template providedBy a provider
//	lnk.service.<tplId>.instanceOf.meta.<serviceDDLId>     # template instanceOf the service DDL meta (type authority)
//	lnk.service.<instId>.instanceOf.service.<tplId>        # instance instanceOf its template (chains to the DDL meta)
//	lnk.service.<instId>.providedTo.identity.<applicantId> # instance providedTo the applicant
//
// Every link: the service vertex (template or instance) is the later-arriving
// SOURCE, the other vertex pre-exists = the TARGET (Contract #1 §1.1). The
// instanceOf links carry the write-gate type authority: a fine-grained envelope
// class misses the exact class->DDL lookup, so the step-6 resolver walks
// instance -> template -> the service DDL meta (Contract #1 §1.5; each vertex
// carries exactly one instanceOf, so the chain is unambiguous). The availableAt
// availability assertion (template→location) is owned
// by service-location. The instance→identity providedTo link is the convergence
// link a downstream actorAggregate lens fans out across to read the instance's
// outcome aspect.
//
// The outcome (status + completedAt) lives in the `.outcome` ASPECT, never on
// the vertex root (D5): the read-path / cap.svc auth plane is deferred, so
// the root-placement exception ("any field the Capability Lens reads → root
// data") does not fire here; root data is {}.
//
// Caller's ContextHint.Reads MUST include:
//   - CreateServiceTemplate: any providedBy endpoint supplied.
//   - CreateServiceInstance: the template (vtx.service.<tplId>) + the
//     applicant (vtx.identity.<id>).
//   - RecordServiceOutcome: the instance (vtx.service.<id>) — its envelope
//     class is the discriminator. The vtx.service.<id>.outcome aspect is
//     listed ONLY when it already exists (a retry against an already-recorded
//     instance) — listing a not-yet-written key is a hydration miss. The
//     once-only guarantee does NOT depend on the caller listing it: the
//     .outcome aspect is written CreateOnly, so a second record with a
//     different requestId conflicts and is rejected regardless. Listing it
//     when present upgrades that rejection to a structured
//     OutcomeAlreadyRecorded ScriptError.
func DDLs() []pkgmgr.DDLSpec {
	return []pkgmgr.DDLSpec{
		serviceDDL(),
		serviceProviderVertexTypeDDL(),
		serviceProviderProfileAspectTypeDDL(),
		serviceProviderIdentityClaimAspectTypeDDL(),
		identityServiceProviderClaimAspectTypeDDL(),
	}
}

// OpMetas declares the ops a downstream story (14.4's externalTask path)
// references via `forOperation`: CreateServiceInstance (the instanceOp shape
// that mints the claim vertex) and RecordServiceOutcome (the replyOp shape
// that records the outcome). CreateServiceTemplate is install-time / admin
// provisioning and is not bound by a downstream step, so it gets no op-meta.
//
// RequestService (edge-manifest Fire 1, G8) is the platform's first
// service-path consumer op — its op-meta carries the descriptor-vocabulary
// aspects (edge-showcase-app-design.md §3.3) so an edge client can render +
// submit it with no hardcoded knowledge of this package. Dispatch.Class is
// "service" — the service DDL's own CanonicalName, the Contract #2 §2.1
// envelope `class` DDL-hint — not the created instance's fine-grained
// envelope class (service.<fam>.instance), which the script derives
// server-side from the template.
func OpMetas() []pkgmgr.OpMetaSpec {
	return []pkgmgr.OpMetaSpec{
		{OperationType: "CreateServiceInstance"},
		{
			OperationType: "RecordServiceOutcome",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Record outcome",
				Description: "Record the outcome of this service run.",
				Icon:        "check",
				Tone:        "primary",
				SubmitLabel: "Record",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"instanceKey":{"type":"string","description":"vtx.service.<NanoID> of the instance to record an outcome for — auto-filled from the instance being viewed."},` +
				`"status":{"type":"string","enum":["completed","failed"],"description":"The terminal outcome: completed or failed."},` +
				`"completedAt":{"type":"string","description":"RFC3339 instant the run completed."},` +
				`"template":{"type":"string","description":"vtx.service.<NanoID> of the instance's own template — required for a provider (non-operator) caller."},` +
				`"serviceprovider":{"type":"string","description":"vtx.serviceprovider.<NanoID> of your own service-provider record — required for a provider (non-operator) caller."}},` +
				`"required":["instanceKey","status","completedAt"]}`,
			FieldDescriptions: map[string]string{
				"instanceKey":     "The instance this outcome is for — auto-filled by the client from the instance being viewed (dispatch.targetField), not user-entered.",
				"status":          "completed or failed.",
				"completedAt":     "RFC3339 instant the run completed.",
				"template":        "The instance's own template — required for a provider caller (the script's standing guard walks instance→template→serviceprovider→identity).",
				"serviceprovider": "Your own service-provider record — required for a provider caller. Must be providedBy the instance's template and identifiedBy-bound to you.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "service",
				AuthContext: "standing",
				TargetField: "instanceKey",
				TargetType:  "service",
				// The instanceOf/providedBy/identifiedBy ownership-chain
				// probes. Absence of any is a meaningful rejection the
				// script renders (AuthDenied), not a correctness error —
				// the same shape wellness-domain's CancelBooking uses.
				OptionalReads: []string{
					"lnk.service.{payload.instanceKey:id}.instanceOf.service.{payload.template:id}",
					"lnk.service.{payload.template:id}.providedBy.serviceprovider.{payload.serviceprovider:id}",
					"lnk.serviceprovider.{payload.serviceprovider:id}.identifiedBy.identity.{actor:id}",
				},
			},
		},
		{
			OperationType: "RequestService",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Request service",
				Description: "Start a run of this service for yourself.",
				Icon:        "service",
				Tone:        "primary",
				SubmitLabel: "Request",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"service":{"type":"string","description":"vtx.service.<NanoID> of the service template being requested."}},` +
				`"required":["service"]}`,
			FieldDescriptions: map[string]string{
				"service": "The service template this run is of — auto-filled by the client from the service being viewed (dispatch.targetField), not user-entered.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "service",
				AuthContext: "service",
				TargetField: "service",
				TargetType:  "service",
			},
		},
	}
}

func serviceDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "service",
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"CreateServiceTemplate", "CreateServiceInstance", "RecordServiceOutcome", "RequestService", "RetireServiceTemplate", "WireProvidedBy"},
		Description: "Service domain DDL. Vertex shape: vtx.service.<NanoID>, root data = {} " +
			"(minimal, D5). A service vertex is a TEMPLATE (an offering) or an INSTANCE (a run of an " +
			"offering), discriminated by the vertex ENVELOPE class (service.<x>.template / " +
			"service.<x>.instance — P7, no .class shadow aspect); the service family <x> is one of " +
			"{backgroundCheck, payment, laundry, fitness, clinic, wellness, cafe} (edge-showcase-app-design.md §7.3 — widened honestly " +
			"for the showcase dataset rather than branding a closed-enum member via presentation). Relationships are LINKS: providedBy (template→provider: who " +
			"provides it), instanceOf (template→the service DDL meta, and instance→template: the " +
			"write-gate type-authority chain + the offering this run is of), providedTo (instance→identity: " +
			"the applicant this run is for). All links: the service vertex is the later-arriving source, the " +
			"other vertex is the pre-existing target (Contract #1 §1.1). The fine-grained envelope class " +
			"misses the exact class→DDL lookup, so the step-6 write-gate resolver walks the instanceOf chain " +
			"(instance→template→meta) to this DDL (Contract #1 §1.5; one instanceOf per vertex). The " +
			"availableAt availability assertion (template→location) is owned by service-location, not this " +
			"DDL. CreateServiceTemplate mints a template (envelope class service.<x>.template) + its " +
			"instanceOf→service-DDL-meta link, and writes the providedBy link only when the endpoint is " +
			"supplied (validated alive); an optional presentation payload object {name, description?, icon, " +
			"category?} is written as the template's .presentation aspect (edge-manifest Fire 1 — the " +
			"client-facing display metadata a personal-lens catalog projects). " +
			"CreateServiceInstance mints an instance (envelope class service.<x>.instance), requires + " +
			"validates a live template (instanceOf) and a live applicant identity (providedTo), and accepts " +
			"an optional caller-supplied bare-NanoID instanceId (a write-ahead seam: absent → minted). " +
			"RequestService (edge-manifest Fire 1, G8) is the consumer-invocable service-path counterpart: " +
			"the caller supplies only the template key (service); the applicant is the verified op actor " +
			"(never caller-supplied), and the instance family is derived from the template's own envelope " +
			"class. No scope=self/operator grant is needed — step 3 already authorized this exact actor for " +
			"this exact service via authContext.service against the cap.svc availability grant " +
			"(service-location's lens); the script re-asserts payload.service == authContextService so a " +
			"forged payload naming a different, unauthorized template cannot slip through. " +
			"RecordServiceOutcome records the external-call result as the .outcome aspect {status " +
			"(completed|failed), completedAt (canonical-UTC RFC3339)} on the instance; the outcome lives " +
			"in the aspect, never on root data (D5). It rejects a non-existent / template (not instance) / " +
			"already-recorded target and asserts the instance root revision (OCC, Contract #2 §2.6). Its standing " +
			"binder (persona-worlds-design.md Fire W0): the operator passes unconditionally; otherwise the caller " +
			"must supply template + serviceprovider and the script requires ALL THREE known-key links to be alive " +
			"— instanceOf (this instance → the named template), providedBy (that template → the named " +
			"serviceprovider), and identifiedBy (that serviceprovider → the caller's own identity) — rejecting " +
			"AuthDenied otherwise, so a bound serviceprovider records outcomes only for instances of templates " +
			"they themselves provide. " +
			"RetireServiceTemplate (§7.3, admin-only cleanup) soft-deletes (tombstones) a live template that no " +
			"longer belongs — e.g. one presenting a family its envelope class contradicts; it rejects a " +
			"non-existent or already-retired template and a target that is an instance, not a template. " +
			"WireProvidedBy wires an EXISTING live template to a provider entity of any type (validated alive " +
			"only — type-open, mirroring CreateServiceTemplate's own create-time providedBy mint): it mints " +
			"lnk.service.<tplId>.providedBy.<provType>.<provId> (service providedBy provider, Contract #1 §1.1) " +
			"idempotently — a link already alive is left untouched, never re-created. The caller's ContextHint " +
			"MUST declare this deterministic link key in OptionalReads (Contract #2 §2.5) so a repeat wire reads " +
			"it as already-alive rather than attempting a fresh create against a live key.",
		Script: serviceDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"family":{"type":"string","enum":["backgroundCheck","payment","laundry","fitness","clinic","wellness","cafe"],"description":"The service family <x> (backgroundCheck|payment|laundry|fitness|clinic|wellness|cafe). Sets the vertex envelope class service.<x>.template|instance."},` +
			`"templateId":{"type":"string","description":"Optional bare NanoID for the template vertex (CreateServiceTemplate); absent → minted."},` +
			`"providedBy":{"type":"string","description":"Optional vtx.<provType>.<NanoID> that provides the template; the providedBy link is written only when supplied (CreateServiceTemplate)."},` +
			`"presentation":{"type":"object","description":"Optional client-facing display metadata (CreateServiceTemplate): {name, description?, icon, category?}. Written as the template's .presentation aspect.",` +
			`"properties":{"name":{"type":"string"},"description":{"type":"string"},"icon":{"type":"string"},"category":{"type":"string"}}},` +
			`"instanceId":{"type":"string","description":"Optional bare NanoID for the instance vertex (CreateServiceInstance/RequestService); supplied by a caller that must know the key before commit (e.g. Loom's write-ahead handle). Absent → minted."},` +
			`"template":{"type":"string","description":"vtx.service.<NanoID> of the template this instance is of (CreateServiceInstance; required, validated alive + is a template)."},` +
			`"providedTo":{"type":"string","description":"vtx.identity.<NanoID> of the applicant this instance is for (CreateServiceInstance; required, validated alive)."},` +
			`"service":{"type":"string","description":"vtx.service.<NanoID> of the template being requested (RequestService; required, must equal the authorized authContext.service)."},` +
			`"instanceKey":{"type":"string","description":"vtx.service.<NanoID> of the instance to record an outcome for (RecordServiceOutcome; required, validated alive + is an instance + not already recorded)."},` +
			`"status":{"type":"string","enum":["completed","failed"],"description":"The terminal outcome (RecordServiceOutcome): completed = the external call succeeded with a satisfying result; failed = the call failed or returned a non-satisfying result."},` +
			`"completedAt":{"type":"string","description":"RFC3339 instant the external call completed (RecordServiceOutcome; normalized to canonical UTC whole-second RFC3339). The freshness-predicate input."},` +
			`"expectedRevision":{"type":"integer","description":"Optional OCC guard (RecordServiceOutcome): the instance root revision the caller read; the outcome write is rejected if the instance changed concurrently."},` +
			`"serviceprovider":{"type":"string","description":"Optional vtx.serviceprovider.<NanoID> (RecordServiceOutcome). Required, alongside template, for a non-operator provider-role caller — the script requires it to be providedBy the named template AND identifiedBy-bound to the caller's own identity."}},` +
			`"required":[]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.service.<NanoID> of the created/updated service vertex (the operation's principal key), or the providedBy link key for WireProvidedBy."}}}`,
		FieldDescription: map[string]string{
			"family":           "The service family <x>, one of {backgroundCheck, payment, laundry, fitness, clinic, wellness, cafe}. Determines the vertex envelope class (service.<x>.template for CreateServiceTemplate, service.<x>.instance for CreateServiceInstance). Required for the create ops.",
			"templateId":       "Optional bare NanoID (no dots / key segments) for the template vertex (vtx.service.<templateId>) created by CreateServiceTemplate. Absent → minted with nanoid.new().",
			"providedBy":       "Optional full vtx.<provType>.<NanoID> key of the provider of the template offering (CreateServiceTemplate, validated alive, link written only when supplied). Required for WireProvidedBy (validated alive; type-open — any vertex type).",
			"presentation":     "Optional client-facing display metadata {name, description?, icon, category?} (CreateServiceTemplate). Written verbatim as the template's .presentation aspect; absent → no aspect written (a template stays undescribed, per edge-showcase-app-design.md §3.3's degrade-gracefully rule).",
			"instanceId":       "Optional bare NanoID (no dots / key segments) for the instance vertex (vtx.service.<instanceId>) created by CreateServiceInstance or RequestService. Supplied by a caller that must know the instance key before the op commits — e.g. a Loom externalTask step write-aheading its token.<instanceKey> handle. Absent → minted with nanoid.new(). A crash-retry with the same id collapses on the Contract #4 tracker.",
			"template":         "Full vtx.service.<NanoID> key of the template this instance is a run of (CreateServiceInstance), the template to retire (RetireServiceTemplate), the LIVE template to wire providedBy onto (WireProvidedBy, required, validated alive + a template), or the instance's own template (RecordServiceOutcome's provider standing-guard path, required alongside serviceprovider for a non-operator caller).",
			"providedTo":       "Full vtx.identity.<NanoID> key of the applicant this instance is provided to. CreateServiceInstance requires it, validates the identity is alive, and writes the providedTo link (the convergence link a downstream lens reads across).",
			"service":          "Full vtx.service.<NanoID> key of the template being requested (RequestService). Required, must be alive + a template, and must equal the authorized authContext.service — a mismatch is rejected (the payload cannot name a different template than the one step 3 authorized).",
			"instanceKey":      "Full vtx.service.<NanoID> key of the instance to record an outcome for. RecordServiceOutcome validates it is alive, is an instance (not a template), and has no outcome yet.",
			"status":           "The terminal outcome value: completed (the external call succeeded with a satisfying result) or failed (the call failed or returned a non-satisfying result). Stored on the .outcome aspect.",
			"completedAt":      "RFC3339 timestamp the external call completed. Normalized to canonical UTC whole-second RFC3339 and stored on the .outcome aspect — the freshness-predicate input a downstream lens compares lexically.",
			"expectedRevision": "Optional OCC guard for RecordServiceOutcome: the revision the caller read for the instance root. When supplied, the outcome write asserts it (Contract #2 §2.6) so a concurrent change on the same instance cannot be clobbered.",
			"serviceprovider":  "Full vtx.serviceprovider.<NanoID> key (RecordServiceOutcome). Required, alongside template, for a non-operator provider-role caller: must be providedBy the named template AND identifiedBy-bound to the caller's own identity, or the op is rejected AuthDenied.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name: "CreateServiceTemplate — declare a background-check offering",
				Payload: map[string]any{
					"family": "backgroundCheck",
				},
				ExpectedOutcome: "Mints vtx.service.<NanoID> (root data {}, envelope class service.backgroundCheck.template) + " +
					"the instanceOf→service-DDL-meta link (the write-gate type authority). " +
					"The providedBy link is written only when that endpoint is supplied (and validated alive); the availableAt " +
					"availability assertion is owned by service-location. Returns primaryKey (the template key).",
			},
			{
				Name: "CreateServiceInstance — start a background-check run for an applicant",
				Payload: map[string]any{
					"family":     "backgroundCheck",
					"template":   "vtx.service.<templateNanoID>",
					"providedTo": "vtx.identity.<applicantNanoID>",
				},
				ExpectedOutcome: "Validates the template (alive + a template) and the applicant identity (alive). Atomically commits " +
					"vtx.service.<NanoID> (root data {}, envelope class service.backgroundCheck.instance) + the instanceOf link" +
					"(instance→template) + the providedTo link (instance→identity). NO outcome aspect yet (absence = not-yet-complete). " +
					"Accepts an optional caller-supplied bare-NanoID instanceId. Returns primaryKey (the instance key). " +
					"Rejects with ScriptError if the template or applicant is absent.",
			},
			{
				Name: "RequestService — a consumer self-invokes a service they're authorized for",
				Payload: map[string]any{
					"service": "vtx.service.<templateNanoID>",
				},
				ExpectedOutcome: "Requires authContext.service == payload.service (step 3 already authorized this actor for this " +
					"exact template via the cap.svc availability grant). Validates the template (alive + a template). Atomically " +
					"commits vtx.service.<NanoID> (root data {}, envelope class derived from the template's own family) + the " +
					"instanceOf link (instance→template) + the providedTo link (instance→the verified op actor, never a payload " +
					"field). Returns primaryKey (the instance key). Rejects with ScriptError if the template is absent, is not a " +
					"template, or payload.service does not match the authorized authContext.service.",
			},
			{
				Name: "RecordServiceOutcome — record a passing background check (operator)",
				Payload: map[string]any{
					"instanceKey": "vtx.service.<instanceNanoID>",
					"status":      "completed",
					"completedAt": "2026-06-18T14:00:00Z",
				},
				ExpectedOutcome: "Reads the instance (alive + an instance, no outcome yet). Writes the .outcome aspect {status: completed, " +
					"completedAt: 2026-06-18T14:00:00Z (canonical UTC)} on the instance and touches the instance root under an OCC " +
					"revision guard. Returns primaryKey (the instance key). Rejects with ScriptError if the instance is absent, is a " +
					"template, or already has an outcome.",
			},
			{
				Name: "RecordServiceOutcome — a bound serviceprovider records their own run's outcome",
				Payload: map[string]any{
					"instanceKey":     "vtx.service.<instanceNanoID>",
					"status":          "completed",
					"completedAt":     "2026-06-18T14:00:00Z",
					"template":        "vtx.service.<templateNanoID>",
					"serviceprovider": "vtx.serviceprovider.<spNanoID>",
				},
				ExpectedOutcome: "As above, plus (for a non-operator caller) requires instanceOf(instance,template) AND " +
					"providedBy(template,serviceprovider) AND identifiedBy(serviceprovider,caller) to all be alive — " +
					"rejects AuthDenied if the caller is not the template's own bound provider.",
			},
			{
				Name: "RetireServiceTemplate — soft-delete a template that no longer belongs",
				Payload: map[string]any{
					"template": "vtx.service.<templateNanoID>",
				},
				ExpectedOutcome: "Validates the template (alive + a template) and tombstones it (isDeleted: true). Returns " +
					"primaryKey (the template key). Rejects with ScriptError if the template is absent, already retired, or is " +
					"an instance, not a template.",
			},
			{
				Name:    "WireProvidedBy — wire an existing serviceprovider onto a live template",
				Payload: map[string]any{"template": "vtx.service.<templateNanoID>", "providedBy": "vtx.serviceprovider.<spNanoID>"},
				ExpectedOutcome: "Validates the template is alive + a template and the providedBy endpoint is alive (type-open — " +
					"any vertex type), then writes lnk.service.<templateNanoID>.providedBy.serviceprovider.<spNanoID> " +
					"(idempotent: a replay where the link is already alive commits nothing). Returns primaryKey (the link key).",
			},
		},
	}
}

// serviceProviderVertexTypeDDL declares the lean `serviceprovider` vertex
// type — the provider-archetype binding for a template-attached vendor
// (e.g. a laundry operator), distinct from clinic-domain's rich `provider`
// (hours, time-off, practicesAt) and wellness-domain's `instructor` (teaches
// sessions, teachesAt) — persona-worlds-design.md Fire W0 F3: per-domain
// provider entities, not one shared type. BindServiceProviderIdentity
// mirrors clinic-domain's BindProviderIdentity verbatim.
func serviceProviderVertexTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     serviceProviderVertexDDL,
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"CreateServiceProvider", "BindServiceProviderIdentity"},
		Description: "Generic template-attached service provider DDL (e.g. a laundry operator). Vertex shape: " +
			"vtx.serviceprovider.<NanoID>, class=serviceprovider, root data = {} (minimal, D5 — the data lives " +
			"in the .profile aspect). CreateServiceProvider mints the serviceprovider + writes the .profile " +
			"aspect {displayName (required)} atomically. BindServiceProviderIdentity binds an existing " +
			"serviceprovider to a pre-minted vtx.identity (both validated alive + typed): it mints " +
			"lnk.serviceprovider.<id>.identifiedBy.identity.<id> (serviceprovider identifiedBy identity, " +
			"Contract #1 §1.1), claims a CreateOnly guard aspect on EACH side (.identityClaim on the " +
			"serviceprovider, .serviceProviderClaim on the identity — mutually exclusive: one identity per " +
			"serviceprovider, one serviceprovider per identity), and idempotently grants the identity-domain " +
			"`provider` role via holdsRole (mirrors clinic-domain's BindProviderIdentity verbatim — " +
			"persona-worlds-design.md Fire W0; a link already alive is left untouched rather than re-created). " +
			"A template's providedBy link to a serviceprovider is wired separately, by the `service` DDL's " +
			"WireProvidedBy op (above) or at CreateServiceTemplate time.",
		Script: serviceProviderDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"displayName":{"type":"string","description":"The service provider's display name (CreateServiceProvider; required)."},` +
			`"serviceProviderId":{"type":"string","description":"Optional bare NanoID for the new serviceprovider vertex (CreateServiceProvider); absent → minted."},` +
			`"serviceProviderKey":{"type":"string","description":"vtx.serviceprovider.<NanoID> of an existing serviceprovider (BindServiceProviderIdentity; required, validated alive)."},` +
			`"identityKey":{"type":"string","description":"vtx.identity.<NanoID> of a pre-minted identity to bind to the serviceprovider (BindServiceProviderIdentity; required, validated alive + class=identity)."}},` +
			`"required":[]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.serviceprovider.<NanoID> the operation wrote (BindServiceProviderIdentity returns the identifiedBy link key instead)."}}}`,
		FieldDescription: map[string]string{
			"displayName":        "The service provider's display name. Stored on the .profile aspect (CreateServiceProvider; required).",
			"serviceProviderId":  "Optional bare NanoID (no dots / key segments) for the new serviceprovider vertex. Absent → minted with nanoid.new().",
			"serviceProviderKey": "Full vtx.serviceprovider.<NanoID> key of an existing serviceprovider vertex (BindServiceProviderIdentity binds it to a login identity).",
			"identityKey":        "Full vtx.identity.<NanoID> key of a pre-minted identity to bind (BindServiceProviderIdentity; required). Must be alive + class=identity; wires the identifiedBy link, claims CreateOnly guard aspects on BOTH sides (rejected if either side is already bound), and idempotently grants the identity the provider role.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "CreateServiceProvider — register a service provider",
				Payload: map[string]any{"displayName": "Kai's Laundry"},
				ExpectedOutcome: "Mints vtx.serviceprovider.<NanoID> (class=serviceprovider, root {}) + the .profile " +
					"aspect {displayName}. Returns primaryKey (the serviceprovider key).",
			},
			{
				Name:    "BindServiceProviderIdentity — bind a service provider to its login identity",
				Payload: map[string]any{"serviceProviderKey": "vtx.serviceprovider.<NanoID>", "identityKey": "vtx.identity.<NanoID>"},
				ExpectedOutcome: "Validates both endpoints alive + typed, mints lnk.serviceprovider.<id>.identifiedBy.identity.<id>, " +
					"claims CreateOnly guard aspects on both sides (rejected if either side is already bound), and " +
					"idempotently grants the identity the provider role via holdsRole. Returns primaryKey (the identifiedBy link key).",
			},
		},
	}
}

// serviceProviderProfileAspectTypeDDL declares the .profile aspect (class
// serviceProviderProfile) — the step-6 write gate for CreateServiceProvider.
// Declaration-only; NON-sensitive.
func serviceProviderProfileAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     serviceProviderProfileAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"CreateServiceProvider"},
		Description: "Service provider profile aspect. Stored as vtx.serviceprovider.<NanoID>.profile (class " +
			"serviceProviderProfile) = {displayName}. Non-sensitive. Written by CreateServiceProvider (whose " +
			"serviceprovider vertexType DDL owns the script); this aspect-type DDL is the step-6 write gate. " +
			"Declaration-only: no op handler.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"displayName":{"type":"string"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"displayName": "The service provider's display name.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "service provider profile aspect",
				Payload:         map[string]any{"displayName": "Kai's Laundry"},
				ExpectedOutcome: "Stored as vtx.serviceprovider.<NanoID>.profile; written by CreateServiceProvider.",
			},
		},
	}
}

// serviceProviderIdentityClaimAspectTypeDDL declares the .identityClaim
// guard aspect on the SERVICEPROVIDER side of a BindServiceProviderIdentity
// pair — the entity-keyed half of the bind's mutual-exclusivity guard
// (identityServiceProviderClaimAspectTypeDDL below is the identity-keyed
// half). Mirrors clinic-domain's providerIdentityClaimAspectTypeDDL exactly.
// Declaration-only; NON-sensitive.
func serviceProviderIdentityClaimAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     serviceProviderIdentityClaimAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"BindServiceProviderIdentity"},
		Description: "Service provider identity-claim guard aspect. Stored as " +
			"vtx.serviceprovider.<NanoID>.identityClaim (class serviceProviderIdentityClaim) = {} — a pure " +
			"existence marker, no relationship field. BindServiceProviderIdentity writes ONE per claimed " +
			"serviceProviderKey, CreateOnly: the key ITSELF is the lock — a second, different identity binding " +
			"the SAME serviceprovider collides at commit (RevisionConflict), never a silent double-bind. " +
			"Declaration-only: no op handler (BindServiceProviderIdentity's script, owned by the serviceprovider " +
			"vertexType DDL, writes it).",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"data": "Always {} — a pure existence marker. Exclusivity is enforced by the KEY (the serviceprovider), never by a field in data.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "service provider identity-claim guard aspect",
				Payload:         map[string]any{},
				ExpectedOutcome: "Stored as vtx.serviceprovider.<NanoID>.identityClaim; claimed once by BindServiceProviderIdentity. A second, different identity binding the same serviceprovider is rejected.",
			},
		},
	}
}

// identityServiceProviderClaimAspectTypeDDL declares the .serviceProviderClaim
// aspect ATTACHED onto an identity-domain vtx.identity — the identity-keyed
// half of BindServiceProviderIdentity's mutual-exclusivity guard, mirroring
// clinic-domain's identityProviderClaimAspectTypeDDL exactly, just keyed
// "serviceProviderClaim". Declaration-only; NON-sensitive.
func identityServiceProviderClaimAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     identityServiceProviderClaimAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"BindServiceProviderIdentity"},
		Description: "Identity service-provider-claim guard aspect (attached onto an identity-domain vertex). " +
			"Stored as vtx.identity.<NanoID>.serviceProviderClaim (class identityServiceProviderClaim) = {} — a " +
			"pure existence marker, no relationship field. BindServiceProviderIdentity writes ONE per claimed " +
			"identityKey, CreateOnly: the key ITSELF (identical regardless of WHICH serviceprovider is claiming) " +
			"is the lock — a second, different serviceprovider passing the same identityKey collides at commit " +
			"(RevisionConflict), never a silent double-bind. Declaration-only: no op handler " +
			"(BindServiceProviderIdentity's script, owned by the serviceprovider vertexType DDL, writes it).",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"data": "Always {} — a pure existence marker. Exclusivity is enforced by the KEY (the identity), never by a field in data.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "identity service-provider-claim guard aspect",
				Payload:         map[string]any{},
				ExpectedOutcome: "Stored as vtx.identity.<NanoID>.serviceProviderClaim; claimed once by BindServiceProviderIdentity's identityKey wiring. A second, different serviceprovider claiming the same identity is rejected.",
			},
		},
	}
}

// serviceProviderDDLScript handles CreateServiceProvider +
// BindServiceProviderIdentity. BindServiceProviderIdentity mirrors
// clinic-domain's BindProviderIdentity verbatim (identifiedBy mint +
// idempotent holdsRole grant + CreateOnly mutual-exclusivity guards on both
// sides) — serviceProviderDDLScript is derived from
// serviceProviderDDLScriptTemplate by pinning the placeholder —
// identity-domain's own "provider" role key — to its real, deterministic
// value (see serviceProviderRoleKey above).
var serviceProviderDDLScript = strings.ReplaceAll(serviceProviderDDLScriptTemplate, "__EXPECTED_PROVIDER_ROLE_KEY__", serviceProviderRoleKey)

const serviceProviderDDLScriptTemplate = `
def make_vtx(key, cls, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False, "data": data}}

def make_aspect(vtx_key, local_name, cls, data):
    return {"op": "create", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

def make_link(key, source, target, cls, local_name, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False,
                         "sourceVertex": source, "targetVertex": target,
                         "localName": local_name, "data": data}}

def required_string(p, name):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if v == None or type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": required non-empty string")
    return v.strip()

def bare_nanoid_or_mint(p, name):
    if not hasattr(p, name):
        return nanoid.new()
    v = getattr(p, name)
    if v == None:
        return nanoid.new()
    if type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": must be a non-empty id string")
    v = v.strip()
    for bad in [".", "*", ">", " ", "\t", "\n"]:
        if bad in v:
            fail("InvalidArgument: " + name + ": must carry no dots / key segments, wildcards, or whitespace; got " + v)
    return v

def parts_of(key, name, want_type):
    parts = key.split(".")
    if len(parts) != 3 or parts[0] != "vtx":
        fail("InvalidArgument: " + name + ": required vtx.<type>.<NanoID> (exactly 3 segments); got " + key)
    if parts[1] == "":
        fail("InvalidArgument: " + name + ": empty type segment; required vtx.<type>.<NanoID>; got " + key)
    if want_type != "" and parts[1] != want_type:
        fail("InvalidArgument: " + name + ": required vtx." + want_type + ".<NanoID>; got " + key)
    return parts[1], parts[2]

def vertex_alive(state, key):
    if key not in state:
        return False
    doc = state[key]
    if doc == None:
        return False
    if hasattr(doc, "isDeleted") and doc.isDeleted:
        return False
    return True

def class_of(state, key):
    if key not in state:
        return None
    doc = state[key]
    if doc == None or not hasattr(doc, "class"):
        return None
    return getattr(doc, "class")

def require_live_typed(state, key, name, want_class):
    if not vertex_alive(state, key):
        fail("UnknownEndpoint: " + name + ": " + key + " is absent or tombstoned")
    cls = class_of(state, key)
    if cls != want_class:
        fail("WrongClass: " + name + ": " + key + " has class " + str(cls) + ", required " + want_class)

def claim_serviceprovider_identity(sp_key):
    # Entity-keyed guard: at most one identity may ever bind THIS
    # serviceprovider (nothing releases the claim, so it is never
    # tombstoned) — mirrors clinic-domain's claim_provider_identity idiom.
    # read-posture: (d) declared optionalReads by BindServiceProviderIdentity's
    # dispatcher (absence is the common first-bind case).
    existing = kv.Read(sp_key + ".identityClaim")
    if existing != None:
        fail("ServiceProviderAlreadyBound: " + sp_key + " is already bound to another identity")
    return make_aspect(sp_key, "identityClaim", "serviceProviderIdentityClaim", {})

def claim_identity_serviceprovider(identity_key):
    # Identity-keyed guard: at most one serviceprovider may ever bind THIS
    # identity (nothing releases the claim, so it is never tombstoned) —
    # mirrors clinic-domain's claim_identity_provider idiom.
    # read-posture: (d) declared optionalReads by BindServiceProviderIdentity's
    # dispatcher (absence is the common first-bind case).
    existing = kv.Read(identity_key + ".serviceProviderClaim")
    if existing != None:
        fail("IdentityAlreadyBoundToServiceProvider: " + identity_key + " is already bound to another service provider")
    return make_aspect(identity_key, "serviceProviderClaim", "identityServiceProviderClaim", {})

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "CreateServiceProvider":
        display_name = required_string(p, "displayName")
        spid = bare_nanoid_or_mint(p, "serviceProviderId")
        spkey = "vtx.serviceprovider." + spid
        mutations = [
            make_vtx(spkey, "serviceprovider", {}),
            make_aspect(spkey, "profile", "serviceProviderProfile", {"displayName": display_name}),
        ]
        events = [{"class": "service.serviceProviderCreated", "data": {"serviceProviderKey": spkey}}]
        return {"mutations": mutations, "events": events, "response": {"primaryKey": spkey}}

    if ot == "BindServiceProviderIdentity":
        spkey = required_string(p, "serviceProviderKey")
        _, sp_id = parts_of(spkey, "serviceProviderKey", "serviceprovider")
        require_live_typed(state, spkey, "serviceProviderKey", "serviceprovider")

        identity_key = required_string(p, "identityKey")
        _, identity_id = parts_of(identity_key, "identityKey", "identity")
        require_live_typed(state, identity_key, "identityKey", "identity")

        # serviceprovider identifiedBy identity (Contract #1 §1.1: the
        # later-arriving serviceprovider is the source, the pre-existing
        # identity is the target). Sentence: "serviceprovider identifiedBy
        # identity". Mirrors clinic-domain's provider identifiedBy mint.
        identified_by_lnk = "lnk.serviceprovider." + sp_id + ".identifiedBy.identity." + identity_id
        mutations = [make_link(identified_by_lnk, spkey, identity_key, "identifiedBy", "identifiedBy", {})]

        # Mutual exclusivity, both sides.
        mutations.append(claim_serviceprovider_identity(spkey))
        mutations.append(claim_identity_serviceprovider(identity_key))

        # Grant the provider role, exactly as clinic-domain's
        # BindProviderIdentity does — IDEMPOTENT (mirrors rbac AssignRole's
        # state-check branch): a holdsRole link already alive is left
        # untouched rather than re-created.
        provider_role_key = "__EXPECTED_PROVIDER_ROLE_KEY__"
        provider_role_id = provider_role_key[len("vtx.role."):]
        holds_role_lnk = "lnk.identity." + identity_id + ".holdsRole.role." + provider_role_id
        # read-posture: (d) declared optionalReads by BindServiceProviderIdentity's
        # dispatcher (absence is the common first-bind case, mirroring rbac's
        # AssignRole idempotency check).
        existing_role_grant = kv.Read(holds_role_lnk)
        if existing_role_grant == None or existing_role_grant.isDeleted:
            mutations.append(make_link(holds_role_lnk, identity_key, provider_role_key, "holdsRole", "holdsRole", {}))

        events = [{"class": "service.serviceProviderIdentityBound",
                   "data": {"serviceProviderKey": spkey, "identityKey": identity_key}}]
        return {"mutations": mutations, "events": events, "response": {"primaryKey": identified_by_lnk}}

    fail("serviceprovider DDL: unknown operationType: " + ot)
`

// serviceDDLScript handles the three service lifecycle ops. Known-key reads
// only (validates every link endpoint by the keys the caller listed in
// ContextHint.Reads). The outcome (status + completedAt) is written to the
// .outcome ASPECT, never the vertex root (D5).
const serviceDDLScript = `
def make_vtx(key, cls, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False, "data": data}}

def make_aspect(vtx_key, local_name, cls, data):
    return {"op": "create", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

def make_link(key, source, target, cls, local_name, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False,
                         "sourceVertex": source, "targetVertex": target,
                         "localName": local_name, "data": data}}

def split_key(k):
    return k.split(".")

def required_string(p, name):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if v == None or type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": required non-empty string")
    return v.strip()

def optional_string(p, name):
    if not hasattr(p, name):
        return None
    v = getattr(p, name)
    if v == None or type(v) != type("") or len(v.strip()) == 0:
        return None
    return v.strip()

def optional_int(p, name):
    # Returns the caller-supplied integer payload field when present, else
    # None. A present-but-non-integer value is a structured ScriptError.
    if not hasattr(p, name):
        return None
    v = getattr(p, name)
    if v == None:
        return None
    if type(v) != type(0):
        fail("InvalidArgument: " + name + ": must be an integer")
    return v

def optional_presentation(p):
    # Returns CreateServiceTemplate's optional "presentation" payload object
    # {name, description?, icon, category?} as a plain dict ready for
    # make_aspect, or None when absent — every field individually optional
    # (edge-showcase-app-design.md §3.3: an undescribed template still
    # installs; it just isn't Facet-renderable). A present-but-non-object
    # value is a structured ScriptError.
    if not hasattr(p, "presentation"):
        return None
    d = getattr(p, "presentation")
    if d == None:
        return None
    if type(d) != type({}):
        fail("InvalidArgument: presentation: must be an object")
    data = {}
    for field in ["name", "description", "icon", "category"]:
        if field in d and d[field] != None and type(d[field]) == type("") and len(d[field].strip()) > 0:
            data[field] = d[field].strip()
    if len(data) == 0:
        return None
    return data

# The service families this package admits (<x> in the class string
# service.<x>.template | service.<x>.instance). One service DDL handles all
# families; the family is carried by the vertex envelope class + an op payload field, not
# a DDL per family. laundry/fitness/clinic/wellness/cafe (edge-showcase-app-design.md §7.3, §7.4, §7.6) are
# the showcase dataset's own honest families, widened rather than branding a
# closed-enum member (backgroundCheck) via .presentation as something it isn't.
SERVICE_FAMILIES = ["backgroundCheck", "payment", "laundry", "fitness", "clinic", "wellness", "cafe"]

def required_family(p):
    fam = required_string(p, "family")
    if fam not in SERVICE_FAMILIES:
        fail("InvalidArgument: family: must be one of backgroundCheck, payment, laundry, fitness, clinic, wellness, cafe; got " + fam)
    return fam

# The terminal outcome values RecordServiceOutcome admits. completed = the
# external call succeeded with a satisfying result; failed = the call failed
# or returned a non-satisfying result.
OUTCOME_STATUSES = ["completed", "failed"]

def required_status(p):
    st = required_string(p, "status")
    if st not in OUTCOME_STATUSES:
        fail("InvalidArgument: status: must be one of completed, failed; got " + st)
    return st

def bare_nanoid_or_mint(p, name):
    # Returns the caller-supplied id when present, else a freshly minted one.
    # The supplied id is checked for KEY-DELIMITER safety only: it is rejected
    # if it carries a dot, a wildcard ("*"/">"), or whitespace, so
    # "vtx.service." + id is a single well-formed 3-segment vertex key. It is
    # NOT validated as a full canonical NanoID (the alphabet + 20-char length
    # are not reachable in the sandbox, and the caller-supplied id is a
    # write-ahead handle whose exact shape the caller owns) — the only
    # invariant enforced here is that it cannot inject extra key segments.
    if not hasattr(p, name):
        return nanoid.new()
    v = getattr(p, name)
    if v == None:
        return nanoid.new()
    if type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": must be a non-empty id string")
    v = v.strip()
    for bad in [".", "*", ">", " ", "\t", "\n"]:
        if bad in v:
            fail("InvalidArgument: " + name + ": must carry no dots / key segments, wildcards, or whitespace; got " + v)
    return v

def parts_of(key, name, want_type):
    # Parses a VERTEX key: exactly 3 segments vtx.<type>.<NanoID>. A non-3
    # segment key (e.g. an aspect/link key, or a vertex key with a stray tail)
    # is rejected, not silently truncated to its first three segments.
    parts = split_key(key)
    if len(parts) != 3 or parts[0] != "vtx":
        fail("InvalidArgument: " + name + ": required vtx.<type>.<NanoID> (exactly 3 segments); got " + key)
    if parts[1] == "":
        fail("InvalidArgument: " + name + ": empty type segment; required vtx.<type>.<NanoID>; got " + key)
    if want_type != "" and parts[1] != want_type:
        fail("InvalidArgument: " + name + ": required vtx." + want_type + ".<NanoID>; got " + key)
    return parts[1], parts[2]

def vertex_alive(state, key):
    if key not in state:
        return False
    doc = state[key]
    if doc == None:
        return False
    if hasattr(doc, "isDeleted") and doc.isDeleted:
        return False
    return True

def aspect_alive(state, key):
    return vertex_alive(state, key)

def vertex_class(state, key):
    # The vertex's ENVELOPE class (service.<x>.template / service.<x>.instance),
    # or None if absent/dead. The type/subtype discriminator is the envelope
    # class (P7) — there is no .class shadow aspect.
    if not vertex_alive(state, key):
        return None
    doc = state[key]
    if not hasattr(doc, "class"):
        return None
    return getattr(doc, "class")

SERVICE_ROLE_PAGE_LIMIT = 50

def actor_holds_operator(actor_key):
    # Resolved from the GRAPH, not from a compile-time constant: the primordial
    # role ids are loaded at runtime (bootstrap.LoadPrimordialNanoIDs) while a
    # package's Definition -- and so its script text -- is built at package-init,
    # so no substitution can see the operator id. The walk mirrors the kernel's
    # own root-grant lens exactly (internal/bootstrap/lenses.go: MATCH (identity)
    # -[:holdsRole]->(role) WHERE role.canonicalName.data.value = 'operator') --
    # the same idiom clinic-domain's / wellness-domain's standing binders use.
    #
    # read-posture: (e) relation=holdsRole epoch=none -- an identity holds few
    # roles, so this is never a keyspace scan. A role granted concurrently with
    # this write is not a race worth closing: it can only widen authority, and
    # the confined branch is the safe one.
    page, _ = kv.Links(actor_key, "holdsRole", "out", None, SERVICE_ROLE_PAGE_LIMIT)
    for lk in page:
        if lk.isDeleted:
            continue
        # read-posture: (e) per-candidate follow-up read off the enumeration
        # above (data-derived key -- the role is unknown until it resolves).
        cn = kv.Read(lk.targetVertex + ".canonicalName")
        if cn != None and not cn.isDeleted and cn.data.get("value") == "operator":
            return True
    return False

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "CreateServiceTemplate":
        fam = required_family(p)
        tpl_id = bare_nanoid_or_mint(p, "templateId")
        tpl_key = "vtx.service." + tpl_id
        # The type/subtype discriminator is the vertex ENVELOPE class (P7) —
        # service.<fam>.template — NOT a .class shadow aspect. That fine-grained
        # class misses the exact class->DDL lookup, so the step-6 write-gate
        # resolver walks this template's instanceOf link to its type authority
        # (Contract #1 §1.5 terminal #1): the service DDL's own meta-vertex,
        # surfaced to the script as ddl["service"].metaKey. The template is the
        # chain terminal a downstream instance walks through.
        tpl_class = "service." + fam + ".template"
        meta_key = ddl["service"].metaKey
        _, meta_id = parts_of(meta_key, "typeAuthority", "meta")
        instance_of_lnk = "lnk.service." + tpl_id + ".instanceOf.meta." + meta_id

        mutations = [
            make_vtx(tpl_key, tpl_class, {}),
            make_link(instance_of_lnk, tpl_key, meta_key, "instanceOf", "instanceOf", {}),
        ]

        # providedBy is the offering's provider link. The link is written only
        # when the endpoint is supplied; the endpoint must be alive (no-orphan,
        # FR29 / P4). The availableAt availability assertion + the spatial graph
        # are owned by service-location — this op does not build them.
        provided_by = optional_string(p, "providedBy")
        if provided_by != None:
            prov_type, prov_id = parts_of(provided_by, "providedBy", "")
            if not vertex_alive(state, provided_by):
                fail("UnknownProvider: " + provided_by)
            provby_lnk = "lnk.service." + tpl_id + ".providedBy." + prov_type + "." + prov_id
            mutations.append(make_link(provby_lnk, tpl_key, provided_by, "providedBy", "providedBy", {}))

        # Optional client-facing display metadata (edge-manifest Fire 1,
        # edge-showcase-app-design.md §3.3): written once, at creation, as a
        # sibling aspect — never on root data (D5). Absent → no aspect (the
        # template installs exactly as before this fire).
        pres = optional_presentation(p)
        if pres != None:
            mutations.append(make_aspect(tpl_key, "presentation", "presentation", pres))

        events = [{"class": "service.templateCreated",
                   "data": {"serviceKey": tpl_key, "family": fam}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": tpl_key}}

    if ot == "RequestService":
        # The platform's first service-path consumer op (edge-manifest
        # Fire 1, G8): step 3 already authorized this exact actor for this
        # exact template via authContext.service (the cap.svc availability
        # grant service-location's lens projects) — no scope=self/operator
        # PermissionSpec exists or is needed for this operationType. The
        # script's only job is to re-assert payload.service matches the
        # authorized value (a forged payload naming a different template
        # must not slip through) and to derive the applicant from the
        # verified op actor, never from the payload.
        svc_key = required_string(p, "service")
        if op.authContextService == "" or op.authContextService != svc_key:
            fail("AuthContextMismatch: service payload field must match the authorized authContext.service")
        if not vertex_alive(state, svc_key):
            fail("UnknownTemplate: " + svc_key)
        tpl_class = vertex_class(state, svc_key)
        if tpl_class == None or not tpl_class.endswith(".template"):
            fail("NotATemplate: " + svc_key + " is not a service template")
        # tpl_class is "service.<fam>.template" — derive the instance's family
        # from the template's own envelope class rather than trusting a
        # caller-supplied field (mirrors CreateServiceInstance's family
        # validation, minus the redundant payload round-trip).
        fam = tpl_class.split(".")[1]
        _, tpl_id = parts_of(svc_key, "service", "service")

        applicant = op.actor
        if applicant == "":
            fail("AuthContextMismatch: RequestService requires a verified op actor")
        _, applicant_id = parts_of(applicant, "actor", "identity")
        if not vertex_alive(state, applicant):
            fail("UnknownApplicant: " + applicant)

        inst_id = bare_nanoid_or_mint(p, "instanceId")
        inst_key = "vtx.service." + inst_id
        inst_class = "service." + fam + ".instance"

        instance_of_lnk = "lnk.service." + inst_id + ".instanceOf.service." + tpl_id
        provided_to_lnk = "lnk.service." + inst_id + ".providedTo.identity." + applicant_id

        mutations = [
            make_vtx(inst_key, inst_class, {}),
            make_link(instance_of_lnk, inst_key, svc_key, "instanceOf", "instanceOf", {}),
            make_link(provided_to_lnk, inst_key, applicant, "providedTo", "providedTo", {}),
        ]
        events = [{"class": "service.instanceCreated",
                   "data": {"serviceKey": inst_key, "family": fam,
                            "template": svc_key, "providedTo": applicant}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": inst_key}}

    if ot == "CreateServiceInstance":
        fam = required_family(p)
        template = required_string(p, "template")
        provided_to = required_string(p, "providedTo")
        _, tpl_id = parts_of(template, "template", "service")
        _, applicant_id = parts_of(provided_to, "providedTo", "identity")

        # No-orphan invariant (FR29 / P4): the template MUST exist, be alive,
        # and be a template (not another instance). The applicant identity
        # MUST be alive. An instance pointing at a non-existent template or
        # applicant is never committed.
        if not vertex_alive(state, template):
            fail("UnknownTemplate: " + template)
        tpl_class = vertex_class(state, template)
        if tpl_class == None or not tpl_class.endswith(".template"):
            fail("NotATemplate: " + template + " is not a service template")
        if not vertex_alive(state, provided_to):
            fail("UnknownApplicant: " + provided_to)

        # instanceId is a caller-supplied write-ahead seam (Contract #10
        # §10.6): a caller that must know the instance key before the op
        # commits (e.g. a Loom externalTask step write-aheading its
        # token.<instanceKey> handle) supplies a bare NanoID and the minted
        # instance uses it verbatim. Absent → mint internally. CreateOnly
        # semantics make a crash-retry with the same id collapse on the
        # Contract #4 tracker — no duplicate instance.
        inst_id = bare_nanoid_or_mint(p, "instanceId")
        inst_key = "vtx.service." + inst_id
        # The discriminator is the vertex ENVELOPE class (P7) —
        # service.<fam>.instance — NOT a .class shadow aspect. The instance keeps
        # its single instanceOf -> template link (Contract #1 §1.1); the step-6
        # write-gate resolver walks instance -> template -> the service DDL meta
        # to reach the type authority (the template carries instanceOf -> meta).
        inst_class = "service." + fam + ".instance"

        instance_of_lnk = "lnk.service." + inst_id + ".instanceOf.service." + tpl_id
        provided_to_lnk = "lnk.service." + inst_id + ".providedTo.identity." + applicant_id

        # Root data minimal (D5): {} on root, the discriminator on the envelope
        # class. NO outcome aspect yet — absence = not-yet-complete.
        mutations = [
            make_vtx(inst_key, inst_class, {}),
            make_link(instance_of_lnk, inst_key, template, "instanceOf", "instanceOf", {}),
            make_link(provided_to_lnk, inst_key, provided_to, "providedTo", "providedTo", {}),
        ]
        events = [{"class": "service.instanceCreated",
                   "data": {"serviceKey": inst_key, "family": fam,
                            "template": template, "providedTo": provided_to}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": inst_key}}

    if ot == "RecordServiceOutcome":
        inst_key = required_string(p, "instanceKey")
        _, inst_id = parts_of(inst_key, "instanceKey", "service")
        status = required_status(p)
        # Normalize completedAt to canonical UTC whole-second RFC3339 (the same
        # form the Refractor's $now uses) so a downstream lexical freshness
        # compare (completedAt + window > now) is sound regardless of caller
        # formatting. Malformed input is rejected with a structured ScriptError.
        completed_at = time.rfc3339_utc(required_string(p, "completedAt"))

        # The instance MUST exist and be alive.
        if not vertex_alive(state, inst_key):
            fail("UnknownInstance: " + inst_key)
        # It MUST be an instance, not a template (recording an outcome on a
        # template is a category error).
        inst_class = vertex_class(state, inst_key)
        if inst_class == None or not inst_class.endswith(".instance"):
            fail("NotAnInstance: " + inst_key + " is not a service instance")

        # Standing binder (persona-worlds-design.md Fire W0): operator passes
        # unconditionally; otherwise the caller must be the identity bound to
        # the SPECIFIC serviceprovider that provides this instance's
        # template -- the three-hop known-key chain instance->template
        # (instanceOf), template->serviceprovider (providedBy),
        # serviceprovider->identity (identifiedBy). Mirrors clinic-domain's
        # actor_bound_to_provider, extended one hop for the template
        # indirection a bare provider entity doesn't need.
        if not actor_holds_operator(op.actor):
            template = optional_string(p, "template")
            serviceprovider = optional_string(p, "serviceprovider")
            if template == None or serviceprovider == None:
                fail("AuthDenied: " + op.actor + " must supply template + serviceprovider to record an outcome as a provider")
            _, tpl_id = parts_of(template, "template", "service")
            _, sp_id = parts_of(serviceprovider, "serviceprovider", "serviceprovider")
            _, actor_id = parts_of(op.actor, "actor", "identity")
            # read-posture: (d) declared optionalReads by RecordServiceOutcome's
            # dispatcher for the provider-standing path (absence is a
            # meaningful AuthDenied, not a correctness error).
            instance_of = kv.Read("lnk.service." + inst_id + ".instanceOf.service." + tpl_id)
            if instance_of == None or instance_of.isDeleted:
                fail("AuthDenied: " + inst_key + " is not an instance of template " + template)
            # read-posture: (d) declared optionalReads by RecordServiceOutcome's dispatcher.
            provided_by = kv.Read("lnk.service." + tpl_id + ".providedBy.serviceprovider." + sp_id)
            if provided_by == None or provided_by.isDeleted:
                fail("AuthDenied: template " + template + " is not providedBy serviceprovider " + serviceprovider)
            # read-posture: (d) declared optionalReads by RecordServiceOutcome's dispatcher.
            identified_by = kv.Read("lnk.serviceprovider." + sp_id + ".identifiedBy.identity." + actor_id)
            if identified_by == None or identified_by.isDeleted:
                fail("AuthDenied: " + op.actor + " is not identifiedBy-bound to serviceprovider " + serviceprovider)

        # The outcome is recorded once, guarded on two load-bearing paths:
        #   - SEQUENTIAL second-record (the first has committed): the
        #     .outcome aspect is written CreateOnly below, so the second
        #     conflicts on that create and is rejected. When the caller lists
        #     the now-existing .outcome key in ContextHint.Reads, the state is
        #     hydrated and this explicit check fires first, upgrading the
        #     rejection to a structured OutcomeAlreadyRecorded ScriptError.
        #   - CONCURRENT second-record (two records in flight, neither sees the
        #     other's .outcome yet): both pass this check, but the OCC root
        #     update (expectedRevision) below asserts the instance root
        #     revision, so the loser's root assertion conflicts and is
        #     rejected. Exactly one record lands.
        outcome_key = inst_key + ".outcome"
        if aspect_alive(state, outcome_key):
            fail("OutcomeAlreadyRecorded: " + inst_key)

        # Write the outcome aspect + OCC-guard the instance root: assert a
        # revision so a concurrent RecordServiceOutcome cannot clobber. The
        # caller MAY supply the revision it read (expectedRevision) for an
        # explicit compare-and-swap; absent, the hydrated read revision is
        # asserted (the transition_task default). The root vertex is
        # re-asserted (data stays {} — D5) under that revision; the outcome
        # aspect is the new fact.
        #
        # A live instance root is always at revision >= 1, so a supplied
        # expectedRevision <= 0 can never match it: revision 0 carries the
        # substrate "key must not exist" semantics, which against a live
        # instance always conflicts. A caller passing 0 almost certainly meant
        # "no OCC guard" — that intent is expressed by OMITTING the field, so
        # an explicit <= 0 is a structured InvalidArgument rather than a
        # silently-always-conflicting write.
        expected_rev = optional_int(p, "expectedRevision")
        if expected_rev != None and expected_rev <= 0:
            fail("InvalidArgument: expectedRevision: must be a positive instance revision; omit it for no OCC guard; got " + str(expected_rev))
        if expected_rev == None:
            expected_rev = state[inst_key].revision
        # Re-assert the instance root under the OCC guard, PRESERVING its
        # fine-grained envelope class (P7) — re-stamping the bare "service" here
        # would clobber the discriminator and break the write-gate instanceOf
        # chain. data stays {} (D5); the outcome aspect is the new fact.
        mutations = [
            make_aspect(inst_key, "outcome", "outcome",
                        {"status": status, "completedAt": completed_at}),
            {"op": "update", "key": inst_key,
             "expectedRevision": expected_rev,
             "document": {"class": inst_class, "isDeleted": False, "data": {}}},
        ]
        events = [{"class": "service.outcomeRecorded",
                   "data": {"serviceKey": inst_key, "status": status,
                            "completedAt": completed_at}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": inst_key}}

    if ot == "RetireServiceTemplate":
        # Admin-only cleanup (§7.3): soft-deletes a live template that no
        # longer belongs — e.g. one whose .presentation aspect brands a
        # family its envelope class contradicts. A tombstone mutation carries
        # no document (the runtime writes isDeleted:true + the lastModified*
        # stamps only, regardless of what a script's document field supplies
        # — starlark_runner.go's parseMutations parses the document field for
        # create/update only). The write is self-OCC'd on the hydrated
        # revision so a concurrent mutation of the same template aborts
        # instead of racing.
        tpl_key = required_string(p, "template")
        parts_of(tpl_key, "template", "service")
        if not vertex_alive(state, tpl_key):
            fail("UnknownTemplate: " + tpl_key)
        tpl_class = vertex_class(state, tpl_key)
        if tpl_class == None or not tpl_class.endswith(".template"):
            fail("NotATemplate: " + tpl_key + " is not a service template")

        mutations = [
            {"op": "tombstone", "key": tpl_key,
             "expectedRevision": state[tpl_key].revision},
        ]
        events = [{"class": "service.templateRetired", "data": {"serviceKey": tpl_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": tpl_key}}

    if ot == "WireProvidedBy":
        # Wires an EXISTING live template to a provider entity of any type
        # (validated alive only -- type-open, mirroring
        # CreateServiceTemplate's own create-time providedBy mint above).
        # Idempotent: a replay against an already-alive link commits nothing.
        template = required_string(p, "template")
        _, tpl_id = parts_of(template, "template", "service")
        if not vertex_alive(state, template):
            fail("UnknownTemplate: " + template)
        tpl_class = vertex_class(state, template)
        if tpl_class == None or not tpl_class.endswith(".template"):
            fail("NotATemplate: " + template + " is not a service template")

        provided_by = required_string(p, "providedBy")
        prov_type, prov_id = parts_of(provided_by, "providedBy", "")
        if not vertex_alive(state, provided_by):
            fail("UnknownProvider: " + provided_by)

        provby_lnk = "lnk.service." + tpl_id + ".providedBy." + prov_type + "." + prov_id
        # The deterministic link key is a declared optionalReads entry
        # (Contract #2 §2.5) -- a first wire legitimately finds it absent, so
        # this is a STATE lookup (the caller's declared read, already
        # hydrated), never a live kv.Read.
        existing = state[provby_lnk] if provby_lnk in state else None
        if existing != None and not (hasattr(existing, "isDeleted") and existing.isDeleted):
            return {"mutations": [], "events": []}
        mutations = [make_link(provby_lnk, template, provided_by, "providedBy", "providedBy", {})]
        events = [{"class": "service.providedByWired",
                   "data": {"service": template, "providedBy": provided_by, "linkKey": provby_lnk}}]
        return {"mutations": mutations, "events": events, "response": {"primaryKey": provby_lnk}}

    fail("service DDL: unknown operationType: " + ot)
`
