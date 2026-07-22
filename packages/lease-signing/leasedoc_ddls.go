package leasesigning

import (
	"encoding/json"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
)

// LeaseDocDDLs returns the executed-lease document generation DDLs — the docGen
// externalTask triad's package data (Contract #10 §10.5/§10.6):
//
//   - `leaseDocInstance` / CreateLeaseDocInstance — the instanceOp Loom submits
//     for the leaseDocument pattern's externalTask step: validates the subject
//     leaseapp is alive AND signed, mints the claim vertex vtx.service.<handle>
//     (class service.docGen.instance) with its instanceOf + providedTo links,
//     assembles the document fields Processor-side (kv.Read / kv.Links over the
//     applicant identity, the leased unit, and the application's own aspects),
//     and emits external.docGen carrying the resolved fields to the vendor.
//   - `leaseDocReply` / RecordLeaseDocOutcome — the replyOp the bridge submits:
//     records the create-only .outcome aspect (class leaseDocOutcome) carrying
//     the terminal status plus, on completed, the produced artifact's pointer
//     set {digest, size, contentType, storeName, filename}, and emits
//     orchestration.externalTaskCompleted{externalRef}.
//   - `leaseDocOutcome` — the aspect-type write gate for the .outcome write
//     (exact class match; declaration-only).
//
// The async PENDING marker reuses RecordServiceDispatch + the
// leaseServiceDispatchMarker aspect gate unchanged: both reconstruct
// vtx.service.<externalRef> generically from the bare handle and carry no
// family/class knowledge, so a future async docGen vendor rides the shipped
// §10.4 poll/timeout lane with no package change (the reference adapter is
// sync and never triggers it).
//
// Unlike the identity-family triad (CreateLeaseServiceInstance's .outcome keeps
// the free-form result OFF the aspect), the docGen .outcome carries the document
// POINTERS on the aspect: the convergence lens projects them and the §10.8
// AttachObject playbook templates them into the anchor op — they are reference
// metadata for bytes already in the object store, not PII. No validUntil: a
// produced document does not expire.
func LeaseDocDDLs() []pkgmgr.DDLSpec {
	return []pkgmgr.DDLSpec{
		leaseDocInstanceDDL(),
		leaseDocReplyDDL(),
		leaseDocOutcomeAspectDDL(),
	}
}

func leaseDocInstanceDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName: "leaseDocInstance",
		Class:         "meta.ddl.vertexType",
		// CreateLeaseDocInstance creates the instance vertex ROOT (class
		// service.docGen.instance), which misses the exact class→DDL lookup, so
		// the step-6 write-gate resolver walks the instance's instanceOf link to
		// THIS DDL's meta-vertex (the type authority) and enforces this list. The
		// .outcome aspect write resolves by exact class match to the
		// leaseDocOutcome aspect-type DDL; the .dispatch marker to
		// leaseServiceDispatchMarker.
		PermittedCommands: []string{"CreateLeaseDocInstance"},
		Description: "ExternalTask instanceOp DDL for executed-lease document generation (Contract #10 §10.5). The op " +
			"Loom submits for the leaseDocument pattern's externalTask step: payload {instanceKey (the bare handle Loom " +
			"minted), subjectKey (the signed vtx.leaseapp.<NanoID> the document is about), adapter, replyOp, " +
			"params:{family: docGen}}. It validates the subject application is alive AND signed (kv.Read of the " +
			".signature aspect — an unsigned application fails with NO claim and NO dispatch), prepends the " +
			"package-chosen claim-vertex type `service` → vtx.service.<handle>, and mints the claim vertex: root data {} " +
			"(D5), envelope class service.docGen.instance (P7), an instanceOf link to this DDL's own meta-vertex (the " +
			"write-gate type authority) and a providedTo link to the subject LEASEAPP (the document is about the " +
			"application, not the applicant — the convergence lens fans out across this link). It then assembles the " +
			"document fields Processor-side (the §10.5 linked-vertex read: kv.Links walks applicationFor → the applicant " +
			"identity and appliesToUnit → the leased unit; kv.Read loads the identity's .name (decrypt-on-read supplies " +
			"the plaintext value), the unit's .address/.listing, and the application's own .terms/.signature) and emits " +
			"the external.docGen event via its own transactional outbox (body {instanceKey, adapter, replyOp, " +
			"dispatchOp: RecordServiceDispatch, externalRef, idempotencyKey, params:{family, leaseAppKey, doc:{…the " +
			"resolved fields…}}}) — the vendor receives real field values and never touches the graph or a lens. A " +
			"missing OPTIONAL field (an unnamed applicant, an absent listing economics field, no .terms) is omitted from " +
			"doc{} and the vendor's renderer degrades exactly as the display path does.",
		Script: leaseDocInstanceDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"instanceKey":{"type":"string","description":"The BARE instance handle Loom minted (no dots / key segments / wildcards); the op prepends vtx.service. → vtx.service.<handle>. Required."},` +
			`"subjectKey":{"type":"string","description":"vtx.leaseapp.<NanoID> of the signed application the document is generated for (the pattern subject); the providedTo link points at it. Required, validated alive AND signed."},` +
			`"adapter":{"type":"string","description":"The external adapter name (docGen), carried into the external.<adapter> event. Required."},` +
			`"replyOp":{"type":"string","description":"The result-op the bridge posts back (RecordLeaseDocOutcome), carried into the external event. Required."},` +
			`"params":{"type":"object","description":"Opaque pass-through adapter params from the Loom step; params.family must be docGen (the one family this DDL owns)."}},` +
			`"required":["instanceKey","subjectKey","adapter","replyOp"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.service.<handle> of the minted claim vertex (the operation's principal key)."}}}`,
		FieldDescription: map[string]string{
			"instanceKey": "The bare instance handle Loom minted for this externalTask (type-free, no dots / key segments / wildcards). The op prepends vtx.service. to it → vtx.service.<handle>. It is echoed back as the reply op's externalRef and is the bridge's adapter dedup key. Required.",
			"subjectKey":  "Full vtx.leaseapp.<NanoID> key of the signed application the document is generated for (the Loom pattern subject). CreateLeaseDocInstance validates it is alive (hydrated — Loom lists it in ContextHint.Reads) and signed (.signature via kv.Read), and writes the providedTo link (the convergence link the lens fans out across). Required.",
			"adapter":     "The registered bridge adapter name (docGen). Carried into the external.<adapter> event class + body so the bridge selects its adapter. Required.",
			"replyOp":     "The result-op type the bridge posts back (RecordLeaseDocOutcome). Carried into the external event body so the bridge knows which op to submit on a terminal outcome. Required.",
			"params":      "Opaque adapter params passed through from the Loom step. params.family must be docGen — the claim vertex's envelope class is service.docGen.instance.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name: "CreateLeaseDocInstance — claim document generation for a signed application",
				Payload: map[string]any{
					"instanceKey": "<bareHandle>",
					"subjectKey":  "vtx.leaseapp.<NanoID>",
					"adapter":     "docGen",
					"replyOp":     "RecordLeaseDocOutcome",
					"params":      map[string]any{"family": "docGen"},
				},
				ExpectedOutcome: "Validates the application (alive) and its .signature (present — an unsigned application " +
					"is rejected NotSigned with no claim and no dispatch). Atomically commits vtx.service.<handle> with " +
					"envelope class service.docGen.instance (root data {} — D5) + the instanceOf link to the " +
					"leaseDocInstance type-authority meta + the providedTo link (instance→leaseapp). NO outcome aspect yet " +
					"(absence = not-yet-complete). Resolves the document fields (applicant name, unit address/economics, " +
					"terms, signedAt) via kv.Read/kv.Links and emits the external.docGen event (body {instanceKey, adapter, " +
					"replyOp, dispatchOp, params:{family, leaseAppKey, doc}, externalRef, idempotencyKey}) off the op's " +
					"outbox. Returns primaryKey (the claim-vertex key).",
			},
		},
	}
}

func leaseDocReplyDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "leaseDocReply",
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"RecordLeaseDocOutcome"},
		Description: "ExternalTask replyOp DDL for executed-lease document generation (Contract #10 §10.5/§10.6). The op " +
			"the bridge submits as the result op: payload {externalRef (the bare handle), status (completed | failed — " +
			"REQUIRED, copied verbatim from the adapter's Result.Status), result (the adapter's Detail string; on " +
			"completed it is the JSON document-pointer object {digest, size, contentType, storeName, filename} the " +
			"vendor produced, on failed a free-form reason)}. The bridge submits it with no ContextHint.Reads, so the op " +
			"reads NOTHING from state: it reconstructs vtx.service.<externalRef> from the bare handle, derives " +
			"completedAt = time.rfc3339_utc(op.submittedAt), and writes the .outcome aspect (class leaseDocOutcome) as a " +
			"CREATE-ONLY mutation — {status, completedAt} plus, on completed, the pointer set parsed from result " +
			"(json.decode; a completed reply whose result is not the pointer object is rejected). The pointers live ON " +
			"the aspect (unlike the identity-family .outcome, which keeps its free-form result off the projection " +
			"plane): the convergence lens projects them and the §10.8 AttachObject playbook templates them into the " +
			"anchor op — reference metadata for bytes already in the object store, never PII. No validUntil (a produced " +
			"document does not expire). It emits orchestration.externalTaskCompleted{externalRef: <bare handle>} — the " +
			"uniform completion signal Loom correlates on — plus service.outcomeRecorded (provenance, carrying the raw " +
			"result string). The outcome is recorded once: the create-only .outcome conflicts on a redelivered reply " +
			"(the FR58 defense at the DDL layer, atop the bridge's deterministic result-op requestId collapse).",
		Script: leaseDocReplyDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"externalRef":{"type":"string","description":"The BARE instance handle the bridge echoes (no dots / key segments); the op reconstructs vtx.service.<externalRef>. Required."},` +
			`"status":{"type":"string","enum":["completed","failed"],"description":"The adapter's terminal verdict: completed = the vendor produced + stored the document; failed = a definitive render rejection. Copied verbatim by the bridge from the adapter's Result.Status. Required."},` +
			`"result":{"type":"string","description":"The adapter's Detail string. On completed: the JSON document-pointer object {digest,size,contentType,storeName,filename} (required — parsed onto the .outcome aspect). On failed: a free-form reason, kept off the aspect (it rides the service.outcomeRecorded provenance event)."}},` +
			`"required":["externalRef","status"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.service.<handle> of the claim vertex the outcome was recorded on (the operation's principal key)."}}}`,
		FieldDescription: map[string]string{
			"externalRef": "The bare instance handle the bridge echoes back (the same handle CreateLeaseDocInstance received). The op reconstructs vtx.service.<externalRef> and emits orchestration.externalTaskCompleted carrying this bare handle (Loom parks on token.<handle>). Required.",
			"status":      "The adapter's terminal verdict, copied verbatim by the bridge: completed (the vendor rendered the document and stored its bytes) or failed (a definitive render rejection — missing required inputs). Written to the .outcome aspect; the convergence lens reads it. Required (no default).",
			"result":      "The adapter's Detail string. On a completed reply it is REQUIRED and must be the JSON document-pointer object {digest, size, contentType, storeName, filename} — parsed and copied onto the .outcome aspect (the lens/playbook read them there). On a failed reply it is an optional free-form reason, kept off the aspect and carried on the service.outcomeRecorded provenance event.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name: "RecordLeaseDocOutcome — record a produced document",
				Payload: map[string]any{
					"externalRef": "<bareHandle>",
					"status":      "completed",
					"result":      `{"digest":"SHA-256=<base64url>","size":1264,"contentType":"text/plain; charset=utf-8","storeName":"<derivedNanoID>","filename":"signed-lease-leaseapp.abcd1234.txt"}`,
				},
				ExpectedOutcome: "Reads no state (the bridge submits no Reads). Reconstructs vtx.service.<handle> from the " +
					"bare handle. Parses the document-pointer object from result and writes the .outcome aspect {status: " +
					"completed, completedAt: canonical-UTC(op.submittedAt), digest, size, contentType, storeName, filename} " +
					"as a create-only mutation (the instance root, already {}, is untouched — D5). Emits " +
					"orchestration.externalTaskCompleted{externalRef: <handle>} (the Loom completion signal) + " +
					"service.outcomeRecorded (provenance). Returns primaryKey. Rejects a second reply for the same handle " +
					"(the create-only .outcome once-only guard) and a completed reply whose result is not the pointer object.",
			},
			{
				Name: "RecordLeaseDocOutcome — record a failed render",
				Payload: map[string]any{
					"externalRef": "<bareHandle>",
					"status":      "failed",
					"result":      "lease-doc render failed: doc.signedAt is required",
				},
				ExpectedOutcome: "Same shape, but the terminal status is failed — a definitive render rejection (an adapter " +
					"ERROR is Nak+retry, never a reply). Writes the .outcome aspect {status: failed, completedAt} with NO " +
					"pointer fields; the reason rides the service.outcomeRecorded provenance event, off the aspect. The " +
					"convergence lens reads status=failed as the terminal declined_docGen disposition (no auto-retry — a " +
					"re-generation is a fresh manual StartLoomPattern). Emits the same completion + provenance events.",
			},
		},
		Effects: map[string][]json.RawMessage{
			// RecordLeaseDocOutcome unconditionally writes the .outcome aspect on
			// commit, regardless of the completed/failed verdict carried in the
			// param — the same unconditional-commit grammar rationale as
			// RecordLeaseServiceOutcome's declaration.
			"RecordLeaseDocOutcome": {json.RawMessage(`{"present":"subject.outcome.data.status"}`)},
		},
	}
}

// leaseDocOutcomeAspectDDL declares the .outcome aspect (class leaseDocOutcome)
// — the step-6 write gate for RecordLeaseDocOutcome. Same rationale as
// leaseServiceOutcomeAspectDDL: the docGen instance carries the fine-grained
// envelope class service.docGen.instance (P7) + an instanceOf link to its type
// authority, so an aspect write that misses the exact class→DDL lookup would
// walk the instanceOf chain to the leaseDocInstance DDL (which permits only
// CreateLeaseDocInstance) and fail closed. This aspect-type DDL makes the
// .outcome write resolve by exact class match to its own gate instead.
// Declaration-only: no op handler (the leaseDocReply vertexType DDL owns the
// writing script).
func leaseDocOutcomeAspectDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "leaseDocOutcome",
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"RecordLeaseDocOutcome"},
		Description: "Executed-lease document-generation outcome aspect. Stored as vtx.service.<handle>.outcome (class " +
			"leaseDocOutcome) = {status (completed|failed), completedAt, and on completed the produced artifact's pointer " +
			"set: digest, size, contentType, storeName, filename}. The terminal external-call verdict the convergence " +
			"lens reads (by local name inst.outcome.data.*) and the §10.8 AttachObject playbook templates its pointer " +
			"columns from. Written ONLY by RecordLeaseDocOutcome (whose leaseDocReply vertexType DDL owns the script); " +
			"this aspect-type DDL is the step-6 write gate (exact class match — the instance's fine-grained envelope " +
			"class + instanceOf type authority would otherwise route the write to the instance DDL and reject it). " +
			"Declaration-only.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"status":{"type":"string","enum":["completed","failed"]},"completedAt":{"type":"string"},"digest":{"type":"string"},"size":{"type":"integer"},"contentType":{"type":"string"},"storeName":{"type":"string"},"filename":{"type":"string"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"status":      "The terminal verdict: completed | failed.",
			"completedAt": "RFC3339 instant the external call completed (canonical UTC).",
			"digest":      "The produced artifact's NATS content digest (SHA-256=<base64url>) — the AttachObject oid seed. Present on completed only.",
			"size":        "The produced artifact's size in bytes. Present on completed only.",
			"contentType": "The produced artifact's MIME type. Present on completed only.",
			"storeName":   "The core-objects store name the vendor streamed the bytes under (deterministic per application — a re-render overwrites). Present on completed only.",
			"filename":    "The artifact's download filename, carried onto the signedLease link by AttachObject. Present on completed only.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "lease document outcome aspect",
				Payload:         map[string]any{"status": "completed", "completedAt": "2026-01-01T00:00:00Z", "digest": "SHA-256=<base64url>", "size": 1264, "contentType": "text/plain; charset=utf-8", "storeName": "<derivedNanoID>", "filename": "signed-lease-leaseapp.abcd1234.txt"},
				ExpectedOutcome: "Stored as vtx.service.<handle>.outcome; written by RecordLeaseDocOutcome.",
			},
		},
	}
}
