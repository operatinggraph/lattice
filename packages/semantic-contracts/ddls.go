package semanticcontracts

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// DDLs returns the package's DDL meta-vertex declarations: `clause`
// (CreateClause, InspectPremises, SupersedeClause, BackfillClauseTerm,
// ShortenClauseTerm) plus its four aspect-type declarations (clauseProse,
// clauseTerms, clauseStatus, clauseInspection). SupersedeClause (Fire V4
// self-amendment) writes prose/terms/status on the NEW clause exactly like
// CreateClause, plus a status update on the AMENDED clause — so
// clauseProse/clauseTerms/clauseStatus each permit it too. BackfillClauseTerm
// updates terms (the term) and status (the normalized due date) on an
// existing clause, so clauseTerms/clauseStatus permit it. ShortenClauseTerm
// (LoftSpace "a tenant gives notice" design) shortens an already-termed
// clause's validUntil to a recorded early move-out and updates status the
// same way, so clauseTerms/clauseStatus permit it too.
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
		// BackfillClauseTerm acts on an existing monthly clause (updates its
		// .terms and .status, root untouched) — the leaseRentSettlement
		// playbook's missing_term directOp. ShortenClauseTerm likewise acts
		// on an existing termed monthly clause (updates its .terms and
		// .status, root untouched) — the leaseRentSettlement playbook's
		// missing_termShortened directOp.
		PermittedCommands: []string{"CreateClause", "InspectPremises", "SupersedeClause", "BackfillClauseTerm", "ShortenClauseTerm"},
		Description: "Semantic-contract clause DDL. Vertex shape: vtx.clause.<NanoID>, class=clause, root data = {} " +
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
			"chargeValidUntil (DebitAccount-stamped) — the clauseSatisfaction lens treats it as due again once a " +
			"recorded lapse reaches chargeValidUntil, mirroring the lease-signing bgcheck-freshness pattern " +
			"(validUntil decay, not a stored transaction count). A monthly clause may carry a TERM — validFrom + " +
			"validUntil (both or neither; canonical RFC3339 UTC; validUntil > validFrom at mint — a mint-time " +
			"argument check only, not a standing invariant: ShortenClauseTerm may later collapse validUntil down to " +
			"validFrom, recording that the clause bills nothing for the rest of its life; refused on a oneTime " +
			"clause): it then bills one charge per calendar-month period of [validFrom, validUntil), the first due " +
			"at validFrom and each next due on the anniversary of validFrom (DebitAccount computes it from " +
			"validFrom each time, so Jan 31 → Feb 28 → Mar 31 never drifts), none before validFrom and none for a " +
			"period starting at or after validUntil, after which it completes. A monthly clause minted WITHOUT a " +
			"term keeps the untermed ~30d cadence — unless it governs a lease that has a .tenancy, in which case " +
			"the leaseRentSettlement lens opens missing_term and BackfillClauseTerm{clauseKey, leaseAppKey} stamps " +
			"validFrom = tenancy.leaseStart and validUntil = tenancy.termStart (a renewed lease: the legacy clause " +
			"covers the original term only) else tenancy.leaseEnd, moves the recorded chargeValidUntil onto the " +
			"term's anniversary grid (absent or before validFrom → validFrom; inside the term → the start of the " +
			"period containing it, capped at validUntil), and re-keys a governs link spelled with the legacy " +
			"`governs.lease.` target segment to `governs.leaseapp.` (the Contract #1 vertex type; the same " +
			"document under the new key, the old key tombstoned). AlreadyTermed refuses a clause that already " +
			"carries a term, so the backfill and its link repair run exactly once per clause. " +
			"ShortenClauseTerm{clauseKey, leaseAppKey} (the LoftSpace \"a tenant gives notice\" design) shortens an " +
			"already-termed monthly clause's validUntil to a tenant's recorded early move-out: NotTermed if the " +
			"clause carries no validFrom yet (BackfillClauseTerm runs first); NoNotice if the lease carries no " +
			".notice aspect; InvalidState if the clause's own governs link does not name this lease. A no-op " +
			"(empty mutations, no event) when the term already ends at or before the move-out; otherwise validUntil " +
			"becomes max(moveOutAt, validFrom) — collapsing to validFrom when the move-out precedes the term's " +
			"start, the recorded fact that the clause bills nothing — and .status completes once its recorded due " +
			"date reaches the new validUntil, the same mark BackfillClauseTerm and DebitAccount leave for the " +
			"identical fact. Rent for the period containing the move-out is billed whole (monthly, no proration). " +
			"Either kind may " +
			"carry an optional " +
			"conditionedOnKey (any live vertex, e.g. a " +
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
			`"validFrom":{"type":"string","description":"Optional (monthly only, together with validUntil): RFC3339 start of the clause's term — the first period's due date. Normalized to canonical UTC. Refused on a oneTime clause or without validUntil."},` +
			`"validUntil":{"type":"string","description":"Optional (monthly only, together with validFrom): RFC3339 end of the clause's term, exclusive; must be after validFrom. No period starting at or after it is ever billed."},` +
			`"clauseKey":{"type":"string","description":"SupersedeClause: vtx.clause.<NanoID> of the live clause being amended. BackfillClauseTerm: the live untermed monthly clause to stamp a term onto. ShortenClauseTerm: the live, already-termed monthly clause to shorten to the lease's recorded move-out."},` +
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
			"period":           "computational only: \"oneTime\" (default) or \"monthly\". A monthly clause re-arms via the .status aspect's chargeValidUntil after each debit instead of completing once — on its term's anniversary grid when it carries validFrom/validUntil.",
			"validFrom":        "Optional, monthly only, with validUntil: the term's start (RFC3339, canonicalized to UTC). Stored on .terms; the clauseSatisfaction lens bills the first period once a recorded lapse reaches it, and DebitAccount computes every later due date from it.",
			"validUntil":       "Optional, monthly only, with validFrom: the term's exclusive end (RFC3339, canonicalized to UTC; must be after validFrom). Stored on .terms; no period starting at or after it is billed, and the clause completes once its next due reaches it.",
			"clauseKey":        "SupersedeClause: full vtx.clause.<NanoID> key of the live clause being amended. BackfillClauseTerm: the live, untermed, monthly computational clause to stamp a term onto (AlreadyTermed if it has one). ShortenClauseTerm: the live, already-termed monthly clause to shorten (NotTermed if it carries no term yet).",
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
				Name: "CreateClause — a monthly rent clause with a term",
				Payload: map[string]any{
					"leaseAppKey": "vtx.leaseapp.<NanoID>",
					"accountKey":  "vtx.account.<NanoID>",
					"prose":       "Monthly rent per the signed lease agreement.",
					"amountCents": 240000,
					"period":      "monthly",
					"validFrom":   "2026-09-08T00:00:00Z",
					"validUntil":  "2027-09-08T00:00:00Z",
				},
				ExpectedOutcome: "As the monthly example, plus .terms.validFrom/validUntil (canonical UTC). The " +
					"clauseSatisfaction lens projects freshUntil=validFrom and NOT violating (nothing is due before " +
					"the term starts); the @at Weaver arms fires at validFrom, MarkExpired records the lapse, and " +
					"missing_charge opens for the first period. DebitAccount posts it and stamps chargeValidUntil = " +
					"validFrom + 1 month (the second period's due, on the anniversary grid); the lens re-arms on " +
					"that instant, and so on until the next due would reach validUntil, at which point the final " +
					"charge marks the clause completed and nothing further is billed. Twelve charges in total for " +
					"this one-year term, never thirteen.",
			},
			{
				Name: "BackfillClauseTerm — term a legacy monthly clause from its lease's tenancy",
				Payload: map[string]any{
					"clauseKey":   "vtx.clause.<NanoID>",
					"leaseAppKey": "vtx.leaseapp.<NanoID>",
				},
				ExpectedOutcome: "Validates the clause is alive, monthly, computational and untermed (AlreadyTermed " +
					"otherwise — a second submission is refused, so the backfill runs once) and that the lease's " +
					".tenancy is present with leaseStart/leaseEnd (declared reads; the clause's .status is an " +
					"OptionalRead). Updates .terms in place — every existing key kept — adding validFrom = " +
					"tenancy.leaseStart and validUntil = tenancy.termStart when the lease has been renewed, else " +
					"tenancy.leaseEnd. Moves .status.chargeValidUntil onto the term's grid: absent or before " +
					"validFrom → validFrom; inside the term → the start of the calendar-month period containing " +
					"it, capped at validUntil. Walks the clause's own governs links (one, by construction) and " +
					"re-keys one spelled `governs.lease.` to `governs.leaseapp.` (create under the new key, " +
					"tombstone the old). Emits clause.termed{clauseKey, leaseAppKey, validFrom, validUntil, " +
					"chargeValidUntil}. leaseRentSettlement then closes missing_term, and clauseSatisfaction " +
					"re-arms freshUntil at the normalized due — a past instant fires at once, so a clause already " +
					"inside its term bills its current period immediately.",
			},
			{
				Name: "ShortenClauseTerm — cap a termed rent clause at a tenant's recorded move-out",
				Payload: map[string]any{
					"clauseKey":   "vtx.clause.<NanoID>",
					"leaseAppKey": "vtx.leaseapp.<NanoID>",
				},
				ExpectedOutcome: "Validates the clause is alive and already termed (NotTermed otherwise), the lease " +
					"carries a .notice with moveOutAt (NoNotice otherwise), and the clause's own governs link names " +
					"this lease (InvalidState otherwise). A term already ending at or before moveOutAt is a no-op " +
					"(empty mutations, no event). Otherwise updates .terms in place — validUntil becomes " +
					"max(moveOutAt, validFrom), every other key kept — and .status.state becomes completed (with " +
					"completedAt) once the recorded chargeValidUntil (or validFrom, never charged) reaches the new " +
					"validUntil. Emits clause.termShortened{clauseKey, leaseAppKey, validUntil, chargeValidUntil}. " +
					"leaseRentSettlement then closes missing_termShortened, and clauseSatisfaction stops billing " +
					"past the new validUntil — the last period, the one containing the move-out, is billed whole.",
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
		PermittedCommands: []string{"CreateClause", "SupersedeClause", "BackfillClauseTerm", "ShortenClauseTerm"},
		Description: "The clause's machine terms — what 'fulfillment' means digitally. Stored as " +
			"vtx.clause.<NanoID>.terms (class clauseTerms) = {kind, conditioned, amountCents?, period, validFrom?, " +
			"validUntil?, basis?, rateCents?, periodDays?, daysOccupied?}, kind ∈ {computational, judgment}. " +
			"Non-sensitive. " +
			"computational (Fire V1, default) carries amountCents; judgment (Fire V2) carries no amountCents — its " +
			"gate is the requiresInspectionBy link + the clauseInspection aspect, not a charge. `conditioned` is " +
			"true iff CreateClause received a conditionedOnKey — an explicit flag (not inferred from the " +
			"conditionedOn link's liveness) because a tombstoned condition TARGET makes the lens's optional match " +
			"resolve null exactly like \"never conditioned\" would; only this flag lets the lens tell the two " +
			"apart. `period` (Fire V3) is \"oneTime\" (default) or \"monthly\" (computational only) — the " +
			"clauseSatisfaction lens's recurring gate reads this column, not a stored charge count. A monthly " +
			"clause's TERM is `validFrom`/`validUntil` (both or neither, canonical RFC3339 UTC, validUntil > " +
			"validFrom): the lens bills one period per calendar month of [validFrom, validUntil) and DebitAccount " +
			"computes each next due date from validFrom; absent, the clause is untermed and bills on the ~30d " +
			"cadence — until BackfillClauseTerm stamps the term from the governed lease's .tenancy " +
			"(leaseRentSettlement's missing_term gap), the one write to this aspect after creation, which preserves " +
			"every other key. When " +
			"amountCents was computed by proration (Fire V3), `basis`=\"daysOccupied\" and rateCents/periodDays/ " +
			"daysOccupied carry the inputs verbatim, for audit only (the lens never re-derives amountCents — it " +
			"was computed once, at creation). Written by CreateClause (or SupersedeClause minting the replacement " +
			"clause), atomically alongside the clause vertex it belongs to; updated by BackfillClauseTerm (stamping " +
			"the term) or, later, by ShortenClauseTerm (capping validUntil at a recorded early move-out — collapsing " +
			"it to validFrom is the recorded fact that the clause bills nothing for the rest of its life). " +
			"Declaration-only: no op handler of its own.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":{"kind":{"type":"string"},"conditioned":{"type":"boolean"},` +
			`"amountCents":{"type":"number"},"period":{"type":"string"},"validFrom":{"type":"string"},"validUntil":{"type":"string"},"basis":{"type":"string"},` +
			`"rateCents":{"type":"number"},"periodDays":{"type":"number"},"daysOccupied":{"type":"number"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"kind":         "\"computational\" (auto-debit, default) or \"judgment\" (open-a-Task, Fire V2).",
			"conditioned":  "True iff CreateClause received a conditionedOnKey. The clauseSatisfaction lens's conditioning gate reads this flag, not the link's liveness.",
			"amountCents":  "The charge amount in integer cents — either the flat CreateClause payload value, or (Fire V3) the once-computed prorated result. Absent for kind=judgment.",
			"period":       "\"oneTime\" (default) or \"monthly\" (Fire V3, computational only). The clauseSatisfaction lens's missing_charge gate branches on this column.",
			"validFrom":    "Monthly only, with validUntil: the term's start, canonical RFC3339 UTC — the first period's due date and the origin of every later anniversary. Absent on an untermed clause (leaseRentSettlement's missing_term gap, when the lease has a .tenancy).",
			"validUntil":   "Monthly only, with validFrom: the term's exclusive end, canonical RFC3339 UTC. No period starting at or after it is billed. ShortenClauseTerm may cap it down to a recorded early move-out (never below validFrom).",
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
				Name:            "clause terms aspect — recurring monthly with a term",
				Payload:         map[string]any{"kind": "computational", "conditioned": false, "amountCents": 240000, "period": "monthly", "validFrom": "2026-09-08T00:00:00Z", "validUntil": "2027-09-08T00:00:00Z"},
				ExpectedOutcome: "Stored as vtx.clause.<NanoID>.terms by CreateClause (or added to an untermed clause's existing terms by BackfillClauseTerm); the clauseSatisfaction lens bills one period per calendar month of [validFrom, validUntil), none outside it.",
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
		// amended clause's status superseded. BackfillClauseTerm moves an
		// untermed clause's recorded due date onto its new term's grid.
		// ShortenClauseTerm completes a termed clause whose recorded due
		// reaches its new, shortened validUntil.
		PermittedCommands: []string{"CreateClause", "DebitAccount", "SupersedeClause", "BackfillClauseTerm", "ShortenClauseTerm"},
		Description: "The clause's lifecycle state. Stored as vtx.clause.<NanoID>.status (class clauseStatus) = " +
			"{state, completedAt?, chargeValidUntil?, supersededAt?, supersededBy?}, state ∈ {active, completed, " +
			"superseded}. Non-sensitive. Created active by CreateClause (or SupersedeClause minting the replacement " +
			"clause, Fire V4); updated by loftspace-ledger's DebitAccount when it posts the " +
			"authorizing charge, or by SupersedeClause on the AMENDED clause (state→superseded, supersededAt " +
			"stamped, supersededBy = the new clause's key — audit only, since the convergence retraction is the " +
			"root tombstone, not this state) (an UNCONDITIONED update in every case). For a period=oneTime clause: state → " +
			"completed + completedAt stamped — audit/display bookkeeping only, since the clauseSatisfaction " +
			"lens's convergence gate for that case derives from the authorizedBy transaction link, not this aspect " +
			"(see the design's R3). For a period=monthly clause (Fire V3): state STAYS active and chargeValidUntil " +
			"is (re-)stamped to the NEXT due date — postedAt + ~30d for an untermed clause, or, for a clause whose " +
			".terms carry validFrom/validUntil, the next anniversary of validFrom (computed from validFrom each " +
			"time, never drifting) — here the field IS the convergence gate the lens reads (mirrors the " +
			"lease-signing bgcheck-freshness validUntil pattern): missing_charge re-opens once a recorded lapse " +
			"reaches chargeValidUntil, and the lens projects it back out as freshUntil to arm Weaver's temporal " +
			"lane for the next re-open. A termed clause whose next due reaches validUntil is marked completed by " +
			"that final charge (chargeValidUntil = validUntil, which the lens reads as no period left). " +
			"BackfillClauseTerm moves an untermed clause's chargeValidUntil onto its new term's grid (absent or " +
			"before validFrom → validFrom; inside the term → the start of the period containing it, capped at " +
			"validUntil), keeping every other key. ShortenClauseTerm marks state completed (+ completedAt) when a " +
			"recorded chargeValidUntil (or, never charged, validFrom) has already reached the newly shortened " +
			"validUntil — the same completion fact BackfillClauseTerm's cap-at-validUntil case leaves, keeping " +
			"every other key.",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{"state":{"type":"string"},"completedAt":{"type":"string"},"chargeValidUntil":{"type":"string"},"supersededAt":{"type":"string"},"supersededBy":{"type":"string"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"state":            "active (CreateClause, and every monthly recharge) or completed (DebitAccount, period=oneTime clauses only) or superseded (SupersedeClause, Fire V4, on the amended clause).",
			"completedAt":      "RFC3339 timestamp DebitAccount stamps when it marks a oneTime clause completed. Absent while active or for monthly clauses.",
			"chargeValidUntil": "RFC3339 due date of a period=monthly clause's NEXT charge: DebitAccount (re-)stamps it on every charge — postedAt + ~30d for an untermed clause, the next anniversary of .terms.validFrom for a termed one (= validUntil after the final period's charge); BackfillClauseTerm moves it onto a newly stamped term's grid. The clauseSatisfaction lens's recurring convergence gate and the projected freshUntil column both read this field. Absent for period=oneTime clauses and for a monthly clause never charged.",
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
				Name:            "clause status aspect — due date moved onto a backfilled term's grid",
				Payload:         map[string]any{"state": "active", "chargeValidUntil": "2026-09-08T00:00:00Z"},
				ExpectedOutcome: "Updated by BackfillClauseTerm on an untermed monthly clause whose lease's tenancy starts 2026-09-08: a recorded due before validFrom (or none) becomes validFrom; one inside the term becomes the start of the period containing it. state and every other key are kept.",
			},
			{
				Name:            "clause status aspect — superseded (Fire V4)",
				Payload:         map[string]any{"state": "superseded", "supersededAt": "2026-07-02T14:00:00Z", "supersededBy": "vtx.clause.<newNanoID>"},
				ExpectedOutcome: "Updated (op:update, unconditioned) by SupersedeClause on the amended clause, atomically alongside the root tombstone that retracts its clauseSatisfaction row.",
			},
		},
	}
}
