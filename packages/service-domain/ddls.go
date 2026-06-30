package servicedomain

import "github.com/asolgan/lattice/internal/pkgmgr"

// DDLs returns the package's DDL meta-vertex declarations.
//
// Single DDL `service` (vertex-type class) handles all three lifecycle ops
// for both the template and instance classes of every service family.
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
	return []pkgmgr.DDLSpec{serviceDDL()}
}

// OpMetas declares the ops a downstream story (14.4's externalTask path)
// references via `forOperation`: CreateServiceInstance (the instanceOp shape
// that mints the claim vertex) and RecordServiceOutcome (the replyOp shape
// that records the outcome). CreateServiceTemplate is install-time / admin
// provisioning and is not bound by a downstream step, so it gets no op-meta.
func OpMetas() []pkgmgr.OpMetaSpec {
	return []pkgmgr.OpMetaSpec{
		{OperationType: "CreateServiceInstance"},
		{OperationType: "RecordServiceOutcome"},
	}
}

func serviceDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "service",
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"CreateServiceTemplate", "CreateServiceInstance", "RecordServiceOutcome"},
		Description: "Service domain DDL. Vertex shape: vtx.service.<NanoID>, root data = {} " +
			"(minimal, D5). A service vertex is a TEMPLATE (an offering) or an INSTANCE (a run of an " +
			"offering), discriminated by the vertex ENVELOPE class (service.<x>.template / " +
			"service.<x>.instance — P7, no .class shadow aspect); the service family <x> is one of " +
			"{backgroundCheck, payment}. Relationships are LINKS: providedBy (template→provider: who " +
			"provides it), instanceOf (template→the service DDL meta, and instance→template: the " +
			"write-gate type-authority chain + the offering this run is of), providedTo (instance→identity: " +
			"the applicant this run is for). All links: the service vertex is the later-arriving source, the " +
			"other vertex is the pre-existing target (Contract #1 §1.1). The fine-grained envelope class " +
			"misses the exact class→DDL lookup, so the step-6 write-gate resolver walks the instanceOf chain " +
			"(instance→template→meta) to this DDL (Contract #1 §1.5; one instanceOf per vertex). The " +
			"availableAt availability assertion (template→location) is owned by service-location, not this " +
			"DDL. CreateServiceTemplate mints a template (envelope class service.<x>.template) + its " +
			"instanceOf→service-DDL-meta link, and writes the providedBy link only when the endpoint is " +
			"supplied (validated alive). " +
			"CreateServiceInstance mints an instance (envelope class service.<x>.instance), requires + " +
			"validates a live template (instanceOf) and a live applicant identity (providedTo), and accepts " +
			"an optional caller-supplied bare-NanoID instanceId (a write-ahead seam: absent → minted). " +
			"RecordServiceOutcome records the external-call result as the .outcome aspect {status " +
			"(completed|failed), completedAt (canonical-UTC RFC3339)} on the instance; the outcome lives " +
			"in the aspect, never on root data (D5). It rejects a non-existent / template (not instance) / " +
			"already-recorded target and asserts the instance root revision (OCC, Contract #2 §2.6).",
		Script: serviceDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"family":{"type":"string","enum":["backgroundCheck","payment"],"description":"The service family <x> (backgroundCheck|payment). Sets the vertex envelope class service.<x>.template|instance."},` +
			`"templateId":{"type":"string","description":"Optional bare NanoID for the template vertex (CreateServiceTemplate); absent → minted."},` +
			`"providedBy":{"type":"string","description":"Optional vtx.<provType>.<NanoID> that provides the template; the providedBy link is written only when supplied (CreateServiceTemplate)."},` +
			`"instanceId":{"type":"string","description":"Optional bare NanoID for the instance vertex (CreateServiceInstance); supplied by a caller that must know the key before commit (e.g. Loom's write-ahead handle). Absent → minted."},` +
			`"template":{"type":"string","description":"vtx.service.<NanoID> of the template this instance is of (CreateServiceInstance; required, validated alive + is a template)."},` +
			`"providedTo":{"type":"string","description":"vtx.identity.<NanoID> of the applicant this instance is for (CreateServiceInstance; required, validated alive)."},` +
			`"instanceKey":{"type":"string","description":"vtx.service.<NanoID> of the instance to record an outcome for (RecordServiceOutcome; required, validated alive + is an instance + not already recorded)."},` +
			`"status":{"type":"string","enum":["completed","failed"],"description":"The terminal outcome (RecordServiceOutcome): completed = the external call succeeded with a satisfying result; failed = the call failed or returned a non-satisfying result."},` +
			`"completedAt":{"type":"string","description":"RFC3339 instant the external call completed (RecordServiceOutcome; normalized to canonical UTC whole-second RFC3339). The freshness-predicate input."},` +
			`"expectedRevision":{"type":"integer","description":"Optional OCC guard (RecordServiceOutcome): the instance root revision the caller read; the outcome write is rejected if the instance changed concurrently."}},` +
			`"required":["family"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.service.<NanoID> of the created/updated service vertex (the operation's principal key)."}}}`,
		FieldDescription: map[string]string{
			"family":           "The service family <x>, one of {backgroundCheck, payment}. Determines the vertex envelope class (service.<x>.template for CreateServiceTemplate, service.<x>.instance for CreateServiceInstance). Required for the create ops.",
			"templateId":       "Optional bare NanoID (no dots / key segments) for the template vertex (vtx.service.<templateId>) created by CreateServiceTemplate. Absent → minted with nanoid.new().",
			"providedBy":       "Optional full vtx.<provType>.<NanoID> key of the provider of the template offering. CreateServiceTemplate validates it is alive and writes the providedBy link only when supplied.",
			"instanceId":       "Optional bare NanoID (no dots / key segments) for the instance vertex (vtx.service.<instanceId>) created by CreateServiceInstance. Supplied by a caller that must know the instance key before the op commits — e.g. a Loom externalTask step write-aheading its token.<instanceKey> handle. Absent → minted with nanoid.new(). A crash-retry with the same id collapses on the Contract #4 tracker.",
			"template":         "Full vtx.service.<NanoID> key of the template this instance is a run of. CreateServiceInstance requires it, validates it is alive and is a template (its envelope class ends in .template), and writes the instanceOf link.",
			"providedTo":       "Full vtx.identity.<NanoID> key of the applicant this instance is provided to. CreateServiceInstance requires it, validates the identity is alive, and writes the providedTo link (the convergence link a downstream lens reads across).",
			"instanceKey":      "Full vtx.service.<NanoID> key of the instance to record an outcome for. RecordServiceOutcome validates it is alive, is an instance (not a template), and has no outcome yet.",
			"status":           "The terminal outcome value: completed (the external call succeeded with a satisfying result) or failed (the call failed or returned a non-satisfying result). Stored on the .outcome aspect.",
			"completedAt":      "RFC3339 timestamp the external call completed. Normalized to canonical UTC whole-second RFC3339 and stored on the .outcome aspect — the freshness-predicate input a downstream lens compares lexically.",
			"expectedRevision": "Optional OCC guard for RecordServiceOutcome: the revision the caller read for the instance root. When supplied, the outcome write asserts it (Contract #2 §2.6) so a concurrent change on the same instance cannot be clobbered.",
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
				Name: "RecordServiceOutcome — record a passing background check",
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
		},
	}
}

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

# The service families this package admits (<x> in the class string
# service.<x>.template | service.<x>.instance). One service DDL handles all
# families; the family is carried by the vertex envelope class + an op payload field, not
# a DDL per family.
SERVICE_FAMILIES = ["backgroundCheck", "payment"]

def required_family(p):
    fam = required_string(p, "family")
    if fam not in SERVICE_FAMILIES:
        fail("InvalidArgument: family: must be one of backgroundCheck, payment; got " + fam)
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

        events = [{"class": "service.templateCreated",
                   "data": {"serviceKey": tpl_key, "family": fam}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": tpl_key}}

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
        parts_of(inst_key, "instanceKey", "service")
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

    fail("service DDL: unknown operationType: " + ot)
`
