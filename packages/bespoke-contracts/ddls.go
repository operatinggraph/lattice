package bespokecontracts

import "github.com/asolgan/lattice/internal/pkgmgr"

// DDLs returns the package's DDL meta-vertex declarations: `clause`
// (CreateClause, InspectPremises, SupersedeClause) plus its four aspect-type
// declarations (clauseProse, clauseTerms, clauseStatus, clauseInspection).
// SupersedeClause (Fire V4 self-amendment) writes prose/terms/status on the
// NEW clause exactly like CreateClause, plus a status update on the AMENDED
// clause — so clauseProse/clauseTerms/clauseStatus each permit it too.
// clauseStatus also permits DebitAccount — the cross-package write
// loftspace-ledger's DebitAccount makes to mark a fixed/one-time clause
// completed (the objectLiveness → TombstoneObject precedent: a package's
// aspect DDL lists every op, in any package, that legitimately writes it).
func DDLs() []pkgmgr.DDLSpec {
	return []pkgmgr.DDLSpec{
		clauseDDL(),
		clauseProseAspectTypeDDL(),
		clauseTermsAspectTypeDDL(),
		clauseStatusAspectTypeDDL(),
		clauseInspectionAspectTypeDDL(),
	}
}

func clauseDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName: "clause",
		Class:         "meta.ddl.vertexType",
		// InspectPremises acts on an existing clause (writes its .inspection
		// aspect, root untouched) the same way SignLease acts on an existing
		// leaseapp — the op's env.Class routes to this DDL's Script.
		// SupersedeClause (Fire V4) mints a replacement clause (CreateClause's
		// shape, plus clauseKey naming the one it amends) and tombstones the
		// amended clause's root — the anchor-tombstone retraction precedent.
		PermittedCommands: []string{"CreateClause", "InspectPremises", "SupersedeClause"},
		Description: "Bespoke-contract clause DDL. Vertex shape: vtx.clause.<NanoID>, class=clause, root data = {} " +
			"(minimal, D5 — the provision text and terms are aspects). CreateClause{leaseAppKey, kind?, prose, " +
			"accountKey?, amountCents?, period?, rateCents?, periodDays?, daysOccupied?, inspectorKey?, " +
			"conditionedOnKey?} mints the clause under a fresh NanoID, requiring the lease (and, per kind, the " +
			"account or inspector, and the conditionedOn vertex if given) all be live (no-orphan invariant). " +
			"`kind` selects the archetype: \"computational\" (default, Fire V1) requires an account and an amount " +
			"and writes the chargesTo link (clause→account) the DebitAccount directOp charges; \"judgment\" " +
			"(Fire V2) requires inspectorKey and writes the requiresInspectionBy link (clause→identity) the " +
			"assignTask(InspectPremises) gap targets — no charge. A computational clause's amount is either a flat " +
			"amountCents (default) or, when rateCents+periodDays+daysOccupied are supplied instead, a PRORATED " +
			"amount computed once at creation as (rateCents*daysOccupied)/periodDays in exact integer arithmetic " +
			"(Fire V3, §7 — no float division, no platform rounding UDF needed since the divide happens here, " +
			"Processor-side, in Starlark bignum ints); the result is stored as the clause's own amountCents and " +
			"behaves exactly like a flat one-time fee thereafter — proration is a one-time-only archetype (period " +
			"must be oneTime). `period` (computational only) is \"oneTime\" (default, one charge ever) or " +
			"\"monthly\" (Fire V3 recurring): a monthly clause re-arms after each charge via the .status aspect's " +
			"chargeValidUntil (~30d, DebitAccount-stamped) — the clauseSatisfaction lens treats it as due again once " +
			"chargeValidUntil lapses, mirroring the lease-signing bgcheck-freshness pattern (validUntil decay, not a " +
			"stored transaction count). Either kind may carry an optional conditionedOnKey (any live vertex, e.g. a " +
			"pet record): CreateClause writes the conditionedOn link (clause→that vertex) generically from its own " +
			"key-shape (vtx.<type>.<id>); the clauseSatisfaction lens only opens the gap while that link is live, so " +
			"tombstoning the condition stops the fee/inspection without touching the clause. Writes the governs " +
			"link (clause→lease, the state this provision governs) in every case — the clause is the " +
			"later-arriving vertex on every link it writes, so it is the source (Contract #1 §1.1). " +
			"SupersedeClause{clauseKey, <the same fields CreateClause takes>} (Fire V4 self-amendment) mints a " +
			"replacement clause exactly like CreateClause, writes an amends link (new clause to amended clause), " +
			"tombstones the amended clause's root (retracting its clauseSatisfaction row via anchor-tombstone " +
			"retraction), and marks its .status superseded (audit). clauseKey must name a currently-live clause " +
			"(no double-amend).",
		Script: clauseDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"leaseAppKey":{"type":"string","description":"vtx.leaseapp.<NanoID> this clause governs (required, validated alive)."},` +
			`"kind":{"type":"string","description":"\"computational\" (default) or \"judgment\". Selects which of accountKey+amount vs inspectorKey is required."},` +
			`"prose":{"type":"string","description":"The human-readable provision text (the legal paragraph the signer agreed to); required, non-empty."},` +
			`"accountKey":{"type":"string","description":"vtx.account.<NanoID> this clause charges (required + validated alive when kind=computational)."},` +
			`"amountCents":{"type":"number","description":"The flat one-time (or recurring-per-period) charge amount in integer cents, when kind=computational and no rateCents/periodDays/daysOccupied proration trio is supplied (required, must be > 0, in that case)."},` +
			`"period":{"type":"string","description":"computational only: \"oneTime\" (default) or \"monthly\" (Fire V3 recurring fee). A prorated clause (rateCents/periodDays/daysOccupied) must be oneTime."},` +
			`"rateCents":{"type":"number","description":"Fire V3 proration: the full-period rate in integer cents (e.g. a $50/month fee = 5000). Supplied together with periodDays+daysOccupied INSTEAD of amountCents; the clause's amountCents is then computed as (rateCents*daysOccupied)/periodDays, exact integer floor division. computational + period=oneTime only."},` +
			`"periodDays":{"type":"number","description":"Fire V3 proration: the number of days in the full period the rateCents is denominated over (e.g. 30). Required together with rateCents/daysOccupied."},` +
			`"daysOccupied":{"type":"number","description":"Fire V3 proration: the number of days actually occupied this partial period (required <= periodDays, together with rateCents/periodDays)."},` +
			`"inspectorKey":{"type":"string","description":"vtx.identity.<NanoID> assigned the InspectPremises Task (required + validated alive when kind=judgment)."},` +
			`"conditionedOnKey":{"type":"string","description":"Optional vtx.<type>.<NanoID> of any live vertex (e.g. a pet record) this clause is conditioned on; validated alive if given. Absent link ⇒ unconditional."}},` +
			`"required":["leaseAppKey","prose"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.clause.<NanoID> of the created clause (the operation's principal key)."}}}`,
		FieldDescription: map[string]string{
			"leaseAppKey":      "Full vtx.leaseapp.<NanoID> key of the lease this clause governs. CreateClause validates it is alive and writes the governs link (clause→lease).",
			"kind":             "\"computational\" (default) or \"judgment\". computational requires accountKey + an amount (flat or prorated); judgment requires inspectorKey.",
			"prose":            "The legal paragraph a signer agreed to. Stored verbatim on the .prose aspect; never interpreted — the machine terms are the separate .terms aspect.",
			"accountKey":       "Full vtx.account.<NanoID> key of the ledger account this clause charges. CreateClause validates it is alive and writes the chargesTo link (clause→account); the account key also flows into the clauseSatisfaction lens as the directOp target.",
			"amountCents":      "The flat charge amount in integer cents when no proration trio is supplied; required (kind=computational), must be a positive number. Stored on the .terms aspect and flows type-preserved into the DebitAccount directOp's amountCents param when the clause is unsatisfied.",
			"period":           "computational only: \"oneTime\" (default) or \"monthly\". A monthly clause re-arms via the .status aspect's chargeValidUntil after each debit instead of completing once.",
			"rateCents":        "Fire V3 proration input: the full-period rate in integer cents. Combined with periodDays+daysOccupied to compute amountCents once, at creation, in exact Starlark bignum integer arithmetic (no float division). Stored on .terms for audit alongside the computed amountCents.",
			"periodDays":       "Fire V3 proration input: days in the full period rateCents is denominated over. Stored on .terms for audit.",
			"daysOccupied":     "Fire V3 proration input: days actually occupied this partial period; must be positive and at most periodDays. Stored on .terms for audit.",
			"inspectorKey":     "Full vtx.identity.<NanoID> key of the identity assigned to inspect (kind=judgment). CreateClause validates it is alive and writes the requiresInspectionBy link (clause→identity); flows into the clauseSatisfaction lens as the assignTask assignee.",
			"conditionedOnKey": "Full vtx.<type>.<NanoID> key of any live vertex this clause is conditioned on. CreateClause validates it is alive and writes the conditionedOn link (clause→that vertex). Tombstoning the target vertex retracts the link, which the clauseSatisfaction lens reads as the condition no longer holding.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name: "CreateClause — a one-time lockout fee",
				Payload: map[string]any{
					"leaseAppKey": "vtx.leaseapp.<NanoID>",
					"accountKey":  "vtx.account.<NanoID>",
					"prose":       "Tenant agrees to a $45 lockout fee for each after-hours lockout assistance request.",
					"amountCents": 4500,
				},
				ExpectedOutcome: "Validates the lease and account are alive. Atomically commits vtx.clause.<freshNanoID> " +
					"(root data {} — D5) + .prose{text} + .terms{kind:computational, amountCents:4500, period:oneTime} + " +
					".status{state:active} + the governs link (clause→lease) + the chargesTo link (clause→account). " +
					"Emits clause.created{clauseKey, leaseAppKey, kind, accountKey, amountCents}. Returns primaryKey. The " +
					"clauseSatisfaction lens immediately projects the clause as violating (missing_charge=true, no " +
					"authorizedBy transaction yet); Weaver dispatches directOp(DebitAccount) to close the gap.",
			},
			{
				Name: "CreateClause — a conditioned pet fee",
				Payload: map[string]any{
					"leaseAppKey":      "vtx.leaseapp.<NanoID>",
					"accountKey":       "vtx.account.<NanoID>",
					"prose":            "Tenant agrees to a $50 monthly pet fee for each pet on file.",
					"amountCents":      5000,
					"conditionedOnKey": "vtx.pet.<NanoID>",
				},
				ExpectedOutcome: "As the fixed-fee example, plus the conditionedOn link (clause→pet). The " +
					"clauseSatisfaction lens only opens missing_charge while the pet link is live; tombstoning the " +
					"pet vertex retracts the link and the gap stops opening.",
			},
			{
				Name: "CreateClause — a move-in inspection (judgment)",
				Payload: map[string]any{
					"leaseAppKey":  "vtx.leaseapp.<NanoID>",
					"kind":         "judgment",
					"prose":        "Landlord will inspect the premises before move-in and record any pre-existing damage.",
					"inspectorKey": "vtx.identity.<NanoID>",
				},
				ExpectedOutcome: "Validates the lease and inspector identity are alive. Commits the clause (no " +
					"chargesTo link) + .terms{kind:judgment, period:oneTime} + the requiresInspectionBy link " +
					"(clause→identity). Emits clause.created{clauseKey, leaseAppKey, kind, inspectorKey}. The " +
					"clauseSatisfaction lens projects missing_inspection=true; Weaver dispatches " +
					"assignTask(InspectPremises) to the inspector; InspectPremises closes the gap.",
			},
			{
				Name: "CreateClause — a recurring monthly smart-home fee (Fire V3)",
				Payload: map[string]any{
					"leaseAppKey": "vtx.leaseapp.<NanoID>",
					"accountKey":  "vtx.account.<NanoID>",
					"prose":       "Tenant agrees to a $15/month smart-home device fee, billed monthly for the lease term.",
					"amountCents": 1500,
					"period":      "monthly",
				},
				ExpectedOutcome: "As the fixed-fee example, plus .terms.period=\"monthly\". The clauseSatisfaction " +
					"lens projects missing_charge=true immediately (never charged); once DebitAccount posts the " +
					"first monthly charge it stamps .status.chargeValidUntil ~30 days out and the lens goes " +
					"non-violating with freshUntil=chargeValidUntil (arming Weaver's temporal lane) until it lapses, " +
					"at which point missing_charge re-opens and the next period's charge fires — indefinitely, " +
					"never reaching status=completed.",
			},
			{
				Name: "CreateClause — a prorated first-month amenity fee (Fire V3)",
				Payload: map[string]any{
					"leaseAppKey":  "vtx.leaseapp.<NanoID>",
					"accountKey":   "vtx.account.<NanoID>",
					"prose":        "Tenant agrees to a $50/month amenity fee, prorated for a mid-month move-in.",
					"rateCents":    5000,
					"periodDays":   30,
					"daysOccupied": 17,
				},
				ExpectedOutcome: "Computes amountCents = (5000*17)/30 = 2833 (exact Starlark bignum integer floor " +
					"division — no float rounding) at creation time and stores it as a normal flat amountCents " +
					"(period stays oneTime); .terms also carries basis:\"daysOccupied\", rateCents:5000, " +
					"periodDays:30, daysOccupied:17 for audit. Behaves exactly like the fixed-fee example from " +
					"here on — a single directOp(DebitAccount) for 2833 cents, then completed.",
			},
			{
				Name: "SupersedeClause — amend a fee amount (Fire V4)",
				Payload: map[string]any{
					"clauseKey":   "vtx.clause.<oldNanoID>",
					"leaseAppKey": "vtx.leaseapp.<NanoID>",
					"accountKey":  "vtx.account.<NanoID>",
					"prose":       "Tenant agrees to a $55 lockout fee (amended from $45), effective this signing.",
					"amountCents": 5500,
				},
				ExpectedOutcome: "Validates the old clause is alive (not already superseded), then mints a new " +
					"vtx.clause.<freshNanoID> exactly like CreateClause. Additionally writes the amends link " +
					"(new clause→old clause), tombstones the old clause's root (isDeleted=True — its " +
					"clauseSatisfaction row retracts via anchor-tombstone retraction, so it stops dispatching " +
					"further debits), and marks the old clause's .status {state:superseded, supersededAt, " +
					"supersededBy:<newClauseKey>} (audit). Emits clause.superseded{clauseKey:<old>, " +
					"supersededBy:<new>} then clause.created{...}. Returns the new clause's primaryKey. A second " +
					"SupersedeClause naming the same old clauseKey is rejected (UnknownClause — already " +
					"tombstoned).",
			},
		},
	}
}

func clauseProseAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "clauseProse",
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"CreateClause", "SupersedeClause"},
		Description: "The clause's legal-paragraph text. Stored as vtx.clause.<NanoID>.prose (class clauseProse) " +
			"= {text}. Non-sensitive. Written exactly once by CreateClause (or SupersedeClause minting the " +
			"replacement clause, Fire V4), atomically alongside the clause vertex " +
			"it belongs to. Declaration-only: no op handler of its own.",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{"text":{"type":"string"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"text": "The human-readable provision text, verbatim from the CreateClause payload.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "clause prose aspect",
				Payload:         map[string]any{"text": "Tenant agrees to a $45 lockout fee for each after-hours lockout assistance request."},
				ExpectedOutcome: "Stored as vtx.clause.<NanoID>.prose; created once by CreateClause alongside the clause vertex.",
			},
		},
	}
}

func clauseTermsAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "clauseTerms",
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"CreateClause", "SupersedeClause"},
		Description: "The clause's machine terms — what 'fulfillment' means digitally. Stored as " +
			"vtx.clause.<NanoID>.terms (class clauseTerms) = {kind, conditioned, amountCents?, period, basis?, " +
			"rateCents?, periodDays?, daysOccupied?}, kind ∈ {computational, judgment}. Non-sensitive. " +
			"computational (Fire V1, default) carries amountCents; judgment (Fire V2) carries no amountCents — its " +
			"gate is the requiresInspectionBy link + the clauseInspection aspect, not a charge. `conditioned` is " +
			"true iff CreateClause received a conditionedOnKey — an explicit flag (not inferred from the " +
			"conditionedOn link's liveness) because a tombstoned condition TARGET makes the lens's optional match " +
			"resolve null exactly like \"never conditioned\" would; only this flag lets the lens tell the two " +
			"apart. `period` (Fire V3) is \"oneTime\" (default) or \"monthly\" (computational only) — the " +
			"clauseSatisfaction lens's recurring gate reads this column, not a stored charge count. When " +
			"amountCents was computed by proration (Fire V3), `basis`=\"daysOccupied\" and rateCents/periodDays/ " +
			"daysOccupied carry the inputs verbatim, for audit only (the lens never re-derives amountCents — it " +
			"was computed once, at creation). Written exactly once by CreateClause, atomically alongside the " +
			"clause vertex it belongs to. Declaration-only: no op handler of its own.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":{"kind":{"type":"string"},"conditioned":{"type":"boolean"},` +
			`"amountCents":{"type":"number"},"period":{"type":"string"},"basis":{"type":"string"},` +
			`"rateCents":{"type":"number"},"periodDays":{"type":"number"},"daysOccupied":{"type":"number"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"kind":         "\"computational\" (auto-debit, default) or \"judgment\" (open-a-Task, Fire V2).",
			"conditioned":  "True iff CreateClause received a conditionedOnKey. The clauseSatisfaction lens's conditioning gate reads this flag, not the link's liveness.",
			"amountCents":  "The charge amount in integer cents — either the flat CreateClause payload value, or (Fire V3) the once-computed prorated result. Absent for kind=judgment.",
			"period":       "\"oneTime\" (default) or \"monthly\" (Fire V3, computational only). The clauseSatisfaction lens's missing_charge gate branches on this column.",
			"basis":        "Fire V3: \"daysOccupied\" when amountCents was proration-computed; absent for a flat charge.",
			"rateCents":    "Fire V3 proration audit: the full-period rate in cents, verbatim from CreateClause. Absent for a flat charge.",
			"periodDays":   "Fire V3 proration audit: days in the full period. Absent for a flat charge.",
			"daysOccupied": "Fire V3 proration audit: days actually occupied. Absent for a flat charge.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "clause terms aspect — fixed one-time",
				Payload:         map[string]any{"kind": "computational", "conditioned": false, "amountCents": 4500, "period": "oneTime"},
				ExpectedOutcome: "Stored as vtx.clause.<NanoID>.terms; created once by CreateClause alongside the clause vertex.",
			},
			{
				Name:            "clause terms aspect — judgment",
				Payload:         map[string]any{"kind": "judgment", "conditioned": false, "period": "oneTime"},
				ExpectedOutcome: "Stored as vtx.clause.<NanoID>.terms; created once by CreateClause alongside the clause vertex. No amountCents.",
			},
			{
				Name:            "clause terms aspect — recurring monthly (Fire V3)",
				Payload:         map[string]any{"kind": "computational", "conditioned": false, "amountCents": 1500, "period": "monthly"},
				ExpectedOutcome: "Stored as vtx.clause.<NanoID>.terms; the clauseSatisfaction lens re-opens missing_charge each period via .status.chargeValidUntil rather than a one-time chargeCount check.",
			},
			{
				Name:            "clause terms aspect — prorated (Fire V3)",
				Payload:         map[string]any{"kind": "computational", "conditioned": false, "amountCents": 2833, "period": "oneTime", "basis": "daysOccupied", "rateCents": 5000, "periodDays": 30, "daysOccupied": 17},
				ExpectedOutcome: "Stored as vtx.clause.<NanoID>.terms; amountCents=2833 was computed once by CreateClause ((5000*17)/30, exact integer floor division), the rest is audit trail.",
			},
		},
	}
}

func clauseInspectionAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "clauseInspection",
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"InspectPremises"},
		Description: "The judgment clause's inspection record. Stored as vtx.clause.<NanoID>.inspection (class " +
			"clauseInspection) = {completed, completedAt}. Non-sensitive. Absent while the inspection is " +
			"outstanding (missing_inspection=true); written exactly once, CreateOnly, by InspectPremises — the " +
			"assignTask target the §10.8 playbook dispatches to the clause's requiresInspectionBy identity. A " +
			"second InspectPremises against the same clause is rejected (AlreadyInspected), mirroring SignLease's " +
			"once-only .signature write.",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{"completed":{"type":"boolean"},"completedAt":{"type":"string"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"completed":   "Always true once written — the aspect's presence, not this field, is the gate the lens reads.",
			"completedAt": "RFC3339 timestamp InspectPremises stamps when it records the inspection.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "clause inspection aspect — recorded",
				Payload:         map[string]any{"completed": true, "completedAt": "2026-07-02T12:00:00Z"},
				ExpectedOutcome: "Created (op:create, CreateOnly) by InspectPremises; the clauseSatisfaction lens flips missing_inspection false.",
			},
		},
	}
}

func clauseStatusAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName: "clauseStatus",
		Class:         "meta.ddl.aspectType",
		// DebitAccount (loftspace-ledger) marks a fixed/one-time clause completed,
		// or a monthly clause's chargeValidUntil forward, once it posts the
		// authorizing charge — a cross-package write, the objectLiveness →
		// TombstoneObject precedent. SupersedeClause (Fire V4) both creates the
		// new clause's status (active, exactly like CreateClause) and marks the
		// amended clause's status superseded.
		PermittedCommands: []string{"CreateClause", "DebitAccount", "SupersedeClause"},
		Description: "The clause's lifecycle state. Stored as vtx.clause.<NanoID>.status (class clauseStatus) = " +
			"{state, completedAt?, chargeValidUntil?, supersededAt?, supersededBy?}, state ∈ {active, completed, " +
			"superseded}. Non-sensitive. Created active by CreateClause (or SupersedeClause minting the replacement " +
			"clause, Fire V4); updated by loftspace-ledger's DebitAccount when it posts the " +
			"authorizing charge, or by SupersedeClause on the AMENDED clause (state→superseded, supersededAt " +
			"stamped, supersededBy = the new clause's key — audit only, since the convergence retraction is the " +
			"root tombstone, not this state) (an UNCONDITIONED update in every case). For a period=oneTime clause: state → " +
			"completed + completedAt stamped — audit/display bookkeeping only, since the clauseSatisfaction " +
			"lens's convergence gate for that case derives from the authorizedBy transaction link, not this aspect " +
			"(see the design's R3). For a period=monthly clause (Fire V3): state STAYS active (a recurring clause " +
			"never completes) and chargeValidUntil is (re-)stamped to completedAt + ~30d — here the field IS the " +
			"convergence gate the lens reads (mirrors the lease-signing bgcheck-freshness validUntil pattern): " +
			"missing_charge re-opens once chargeValidUntil lapses, and the lens projects it back out as freshUntil " +
			"to arm Weaver's temporal lane for the next re-open.",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{"state":{"type":"string"},"completedAt":{"type":"string"},"chargeValidUntil":{"type":"string"},"supersededAt":{"type":"string"},"supersededBy":{"type":"string"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"state":            "active (CreateClause, and every monthly recharge) or completed (DebitAccount, period=oneTime clauses only) or superseded (SupersedeClause, Fire V4, on the amended clause).",
			"completedAt":      "RFC3339 timestamp DebitAccount stamps when it marks a oneTime clause completed. Absent while active or for monthly clauses.",
			"chargeValidUntil": "Fire V3: RFC3339 timestamp DebitAccount (re-)stamps on every charge of a period=monthly clause (completedAt + ~30d). The clauseSatisfaction lens's recurring convergence gate and the projected freshUntil column both read this field. Absent for period=oneTime clauses.",
			"supersededAt":     "Fire V4: RFC3339 timestamp SupersedeClause stamps on the amended clause's status. Audit only — the row-retraction signal is the root tombstone, not this field.",
			"supersededBy":     "Fire V4: the replacement clause's full vtx.clause.<NanoID> key. Audit only, same caveat as supersededAt.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "clause status aspect — completed by a one-time debit",
				Payload:         map[string]any{"state": "completed", "completedAt": "2026-07-02T12:00:00Z"},
				ExpectedOutcome: "Updated (op:update, unconditioned) by DebitAccount when clauseRef names a period=oneTime clause.",
			},
			{
				Name:            "clause status aspect — recharged (Fire V3 recurring)",
				Payload:         map[string]any{"state": "active", "chargeValidUntil": "2026-08-01T12:00:00Z"},
				ExpectedOutcome: "Updated (op:update, unconditioned) by DebitAccount when clauseRef names a period=monthly clause — stays active, re-arms chargeValidUntil ~30 days out.",
			},
			{
				Name:            "clause status aspect — superseded (Fire V4)",
				Payload:         map[string]any{"state": "superseded", "supersededAt": "2026-07-02T14:00:00Z", "supersededBy": "vtx.clause.<newNanoID>"},
				ExpectedOutcome: "Updated (op:update, unconditioned) by SupersedeClause on the amended clause, atomically alongside the root tombstone that retracts its clauseSatisfaction row.",
			},
		},
	}
}
