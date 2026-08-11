package identitydomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// OpMetas declares the descriptor vocabulary (edge-showcase-app-design.md §3.3)
// for the identity ops a descriptor-driven client can actually SUBMIT.
// identity-domain is the idiom source every other package copies, so the
// Vertical Package Standard §3.4 has it conform LAST — its descriptors are
// written once the vocabulary has settled against the corpus rather than ahead
// of it.
//
// Five ops carry a FULL descriptor; two carry a stated `[no-op-meta: <code> — …]`
// exemption in their permission Note (permissions.go) instead. That split is
// not a shortcut: a full descriptor is a machine-readable PROMISE that a client
// holding only these fields can build a valid Contract #2 envelope, and an op
// that cannot honour it must decline rather than ship a form that fails in
// ways the descriptor itself claims are impossible.
//
// CompleteCredentialLink is a THIRD case, and it exists because the descriptor
// stopped being purely client-facing. Its Dispatch.optionalReads are now read
// Processor-side as the Contract #2 §2.5 floor an envelope cannot raise, so an
// op with no op-meta at all has no floor — and this one is the sharpest
// anti-enumeration path in the package: its scope=self gate binds
// authContext.target to the RAW NEW CREDENTIAL, so nothing upstream of the
// script constrains which identity a submitter names in the payload, and
// `fail_link` reuses the "ClaimKeyInvalid: " prefix precisely so NFR-S6's one
// generic answer covers it. It therefore carries a DISPATCH-ONLY op-meta: the
// read declaration and nothing else. Its permission exemption stands
// unchanged, because the reason for it is untouched — see the entry itself.
//
// Three of the ops that could not honour it now can, and the reason each was
// exempt is the reason it no longer is. CreateUnclaimedIdentity and
// RotateClaimKey were client-side CEREMONIES — the caller mints a secret,
// submits only its sha256, and keeps the plaintext to hand over — plus, for
// Create, a set of dedup probes the caller cannot compute. The Ceremony spec
// makes the minting part of the descriptor rather than a reason to abandon
// it, and derive_reads (Contract #2 §2.5 class (g)) has the DDL declare the
// probes from the same normalizers the main script uses. Neither op ever
// needed a form for those; it needed a client that knew to DO them.
// UnlinkCredential's one input named a credential nothing projected as a
// client-resolvable row; the boundTo link and its per-credential Protected
// lens now project exactly that row, and the op's target is filled from it.
//
// The two that remain exempt are exempt for reasons a ceremony field does not
// touch: both submit as a different actor than the client authenticated as.
//
// Dispatch.Class is "identity" on all five submittable ops — the owning
// vertexType DDL's own CanonicalName, never a vertical name. The dispatch-only
// entry deliberately omits it; that omission is what keeps a client from
// offering the op, and it is load-bearing rather than an oversight.
//
// Each Reads list is the live dispatcher's, verified against the script branch
// it feeds: cmd/facet/claim.go for ClaimIdentity, the onboarding userTask
// (packages/lease-signing/patterns.go) for RecordIdentityPII, and the DDL's
// own RotateClaimKey branch for the re-issue path.
func OpMetas() []pkgmgr.OpMetaSpec {
	return []pkgmgr.OpMetaSpec{
		{
			OperationType: "CreateUnclaimedIdentity",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Create an identity",
				ShortLabel:  "New identity",
				Description: "Register someone who does not have an account yet, and hand them the one-time secret that claims it.",
				Icon:        "user-plus",
				Tone:        "primary",
				SubmitLabel: "Create identity",
				Group:       "Identity",
			},
			// claimKeyHash is in the schema because the payload carries it,
			// and is removed from the RENDERED form by the ceremony below —
			// the client fills it from the secret it mints. It is deliberately
			// not x-sensitive: masking an input nobody types buys nothing, and
			// the value that must not leak is the preimage, which never
			// reaches this schema at all.
			//
			// claimKeyAlgo is omitted rather than rendered as an enum-of-one:
			// the script defaults it to sha256, the only accepted value, so a
			// control for it can only be set right or set wrong.
			InputSchema: `{"type":"object","properties":` +
				`{"name":{"type":"string","maxLength":200,"title":"Full name","description":"The person's display name."},` +
				`"email":{"type":"string","title":"Email","description":"Email address. At least one of email or phone is required."},` +
				`"phone":{"type":"string","title":"Phone","description":"Phone number. At least one of email or phone is required."},` +
				`"claimKeyHash":{"type":"string","title":"Claim secret hash","description":"sha256 of the claim secret the client mints. Never typed."}},` +
				`"required":["name","claimKeyHash"]}`,
			FieldDescriptions: map[string]string{
				"name":         "How this person's name should appear. Also used to spot an identity that already exists.",
				"email":        "Used to reach them, and to spot a duplicate registration. At least one of email or phone.",
				"phone":        "Used to reach them, and to spot a duplicate registration. At least one of email or phone.",
				"claimKeyHash": "Filled by this device. Lattice only ever stores the hash — the secret itself is shown to you once and never sent.",
			},
			Ceremony: &pkgmgr.OpCeremonySpec{
				MintedSecretHashField: "claimKeyHash",
				RevealTitle:           "Their claim secret — shown once",
				RevealHelp: "Give this to them now. Lattice stored only its hash, so this screen is " +
					"the one and only time the secret exists; if it is lost, the identity needs a " +
					"fresh one issued.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class: "identity",
				// "standing": a scope=any grant to frontOfHouse/backOfHouse/
				// operator with no relationship to any target, so the client
				// sends no authContext at all.
				AuthContext: "standing",
				// No Reads. The three vtx.identityindex.<hash> dedup probes
				// are declared by the DDL's own derive_reads (Contract #2 §2.5
				// class (g)), computed from the same normalizers the main
				// script uses. That is the half a template vocabulary could
				// never express — it substitutes, it does not hash — and the
				// reason this op is descriptor-drivable at all.
			},
		},
		{
			OperationType: "RotateClaimKey",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Re-issue a claim secret",
				ShortLabel:  "Re-issue secret",
				Description: "Issue a fresh one-time secret for someone whose original was lost before they could claim their identity.",
				Icon:        "key",
				Tone:        "neutral",
				SubmitLabel: "Re-issue secret",
				Group:       "Identity",
			},
			// identityKey is BOTH the dispatch target and an x-entityRef
			// picker, and the pair is what makes this op degrade honestly.
			//
			// Nothing projects an `identity` entity today: the edge manifest
			// stamps eight entityTypes and six selfAnchor types, none of them
			// `identity` (edge-manifest/lenses.go). So `resolveTargetKey`
			// yields nothing AND the picker has no candidates — which is
			// exactly the state `opButton`'s target gate exists for, and it
			// withholds the op behind a card saying so. Declaring only the
			// picker would skip that gate and OFFER a form whose one required
			// field can never be filled.
			//
			// The picker is not decoration: the moment a lens projects
			// unclaimed identities for staff, `pickerFillsTargetField` starts
			// answering true and this op becomes completable with no change
			// here. That projection is the filed consumer of this descriptor.
			InputSchema: `{"type":"object","properties":` +
				`{"identityKey":{"type":"string","x-entityRef":"identity","title":"Identity","description":"The unclaimed identity whose secret is being re-issued."},` +
				`"claimKeyHash":{"type":"string","title":"New claim secret hash","description":"sha256 of the new claim secret the client mints. Never typed."}},` +
				`"required":["identityKey","claimKeyHash"]}`,
			FieldDescriptions: map[string]string{
				"identityKey":  "Whose secret to replace. Only an identity that is still unclaimed can be re-issued.",
				"claimKeyHash": "Filled by this device. The lost secret stops working the moment this lands.",
			},
			Ceremony: &pkgmgr.OpCeremonySpec{
				MintedSecretHashField: "claimKeyHash",
				RevealTitle:           "The new claim secret — shown once",
				RevealHelp: "Give this to them now. It replaces the lost one, and like the original " +
					"it exists only on this screen.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "identity",
				AuthContext: "standing",
				TargetField: "identityKey",
				TargetType:  "identity",
				// Withheld until a row says it is an UNCLAIMED identity.
				// Without this the op is offered against any identity-typed
				// row by type alone — including a credential-binding row,
				// where "re-issue this person's claim secret" is offered
				// against one of their own sign-in methods and denies at the
				// script's unclaimed-only guard. Fail-closed on every row in
				// the corpus today, and live the moment the filed
				// identity-entity projection carries the field: the same
				// projection the comment above names as this descriptor's
				// consumer, now depended on rather than merely mentioned.
				VisibleWhen: &pkgmgr.OpVisibleWhenSpec{
					Field:  "unclaimed",
					Equals: true,
				},
				// The script branch's own reads: the target vertex, its state
				// (the unclaimed-only guard), and the .claimKey aspect it
				// rewrites. .mergedInto is absent for the reason
				// RecordIdentityPII records — testing membership of a
				// required-but-absent key faults HydrationMiss, which would
				// reject every ordinary call.
				Reads: []string{
					"{payload.identityKey}",
					"{payload.identityKey}.state",
					"{payload.identityKey}.claimKey",
				},
			},
		},
		{
			OperationType: "ClaimIdentity",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Claim your identity",
				ShortLabel:  "Claim",
				Description: "Prove the one-time secret you were given and bind this sign-in to the identity waiting for you.",
				Icon:        "key",
				Tone:        "primary",
				SubmitLabel: "Claim",
				Group:       "Identity",
			},
			// Both fields are transcribed from the claim invitation, which is
			// why this op IS descriptor-drivable where its siblings are not:
			// nothing has to be minted or derived client-side.
			//
			// targetIdentityKey is deliberately NOT a targetField, and the
			// reason is the opposite of what it might look like. Nothing
			// projects an `identity` entity or self-anchor, so a declared
			// TargetType "identity" resolves to nothing and offers no picker
			// — which means `opButton`'s target gate would WITHHOLD this op
			// permanently. The key arrives with the invitation and is meant to
			// be transcribed, so it has to stay an ordinary, prefillable
			// field.
			//
			// (An earlier version of this comment claimed the resolver falls
			// back to the submitter's own identity for this type. It does not:
			// `selfAnchorKey` answers only the six anchor types the edge
			// manifest stamps, and `identity` is not among them. The
			// conclusion was right; the stated mechanism was not, and it
			// propagated to other descriptors before being caught.)
			InputSchema: `{"type":"object","properties":` +
				`{"targetIdentityKey":{"type":"string","title":"Identity to claim","description":"vtx.identity.<NanoID> of the identity being claimed — carried by the claim invitation."},` +
				`"claimKey":{"type":"string","title":"Claim key","description":"The one-time claim secret you were given.","x-sensitive":true}},` +
				`"required":["targetIdentityKey","claimKey"]}`,
			FieldDescriptions: map[string]string{
				"targetIdentityKey": "The identity waiting to be claimed. Comes from the claim invitation, not from anything you are looking at.",
				"claimKey":          "The one-time secret you were handed. A successful claim spends it, so it cannot be used twice.",
			},
			// NOT Sensitive (the op-level flag): it masks every field a client
			// renders, which would ALSO catch targetIdentityKey (a key
			// transcribed from the invitation, which masking would make
			// unenterable, since the masked input also drops any prefilled
			// value). claimKey instead carries the per-field
			// InputSchema `"x-sensitive":true` extension (renderField, app.js)
			// — masked, no-echo entry for that one field, targetIdentityKey
			// stays a plain, prefillable input.
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "identity",
				AuthContext: "self",
				// The dedup probe vtx.credentialindex.<sha256(actor)> is absent
				// from this list because no dispatcher can compute it — the
				// template vocabulary substitutes, it does not hash. It IS
				// declared, by the DDL's own derive_reads (Contract #2 §2.5
				// class (g)), which resolves it from op.actor at the head of
				// step 4. So the script's `credential-already-bound` guard and
				// the tombstoned-index revive branch are LIVE on this path,
				// where they were previously dormant: with the key undeclared
				// the probe always read absent, so re-binding a credential
				// whose index UnlinkCredential had tombstoned died on the
				// CreateOnly create's revision-0 assertion instead of
				// reviving. The CreateOnly create is still the backstop on the
				// absent branch, so the concurrent-bind race is still caught.
				//
				// All three are optionalReads, and that is an anti-enumeration
				// requirement (NFR-S6), not a style choice. This op does not
				// DEPEND on the target's presence, it ADJUDICATES it:
				// `no-target` is one of its own named outcomes, collapsed with
				// every other one into the single generic ClaimKeyInvalid.
				// Under `reads` an absent target is recorded required-absent
				// and the script's first `target in state` faults
				// HydrationMiss, which reaches the caller as HydrationFailed
				// with the probed key echoed back in `details.missingKey` — a
				// different wire code AND the key itself, one guess at a time.
				// Under optionalReads the absence lands in KnownAbsent, the
				// script reads None, and its own generic refusal renders.
				// Same hazard the DDL's derive_reads records for its
				// class-(g) keys, and the same disposition
				// CompleteCredentialLink's dispatchers give the whole of their
				// target's read set.
				//
				// THIS LIST IS AN ENFORCEMENT POINT. It is not advisory and
				// it is not only for descriptor-driven clients: the Processor
				// reads it at the head of step 4 and demotes any of these keys
				// a submitter's envelope declared under `reads`
				// (internal/processor/descriptor_floor.go, the Contract #2
				// §2.5 clause "the descriptor's disposition is a floor the
				// envelope cannot raise"). Since contextHint is client-supplied
				// and step 3 never inspects it, these four lines are the only
				// thing standing between a hand-rolled envelope and an
				// identity-keyspace oracle on this op.
				//
				// So: removing an entry, or moving one to Dispatch.Reads,
				// re-opens that oracle for every caller — not just for the
				// clients that read the descriptor. What the floor does NOT
				// cover is a template it cannot resolve server-side
				// (`{me.<type>}`, `{entity.<column>}` name a vertex out of the
				// caller's projected view); these are all `{payload.*}`, which
				// resolves.
				OptionalReads: []string{
					"{payload.targetIdentityKey}",
					"{payload.targetIdentityKey}.state",
					"{payload.targetIdentityKey}.claimKey",
				},
			},
		},
		{
			OperationType: "RecordIdentityPII",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Confirm your details",
				ShortLabel:  "Confirm details",
				Description: "Provide the SSN and date of birth your application's background check needs.",
				Icon:        "shield",
				Tone:        "neutral",
				SubmitLabel: "Save details",
				Group:       "Identity",
			},
			// dob carries format "date" so it reaches the date widget: the
			// renderer routes a declared date format BEFORE the op-level
			// Sensitive masking, which otherwise renders every field as a
			// password box — a masked date is unenterable, and a birth date is
			// not a secret from the person typing their own. The op stays
			// Sensitive, so ssn keeps its masked, no-echo entry.
			InputSchema: `{"type":"object","properties":` +
				`{"identityKey":{"type":"string","description":"vtx.identity.<NanoID> the details are recorded onto — the task's own subject."},` +
				`"ssn":{"type":"string","title":"Social security number","description":"Social Security Number: 9 digits. Hyphens are accepted and stripped."},` +
				`"dob":{"type":"string","format":"date","title":"Date of birth","description":"Date of birth."}},` +
				`"required":["identityKey","ssn","dob"]}`,
			FieldDescriptions: map[string]string{
				"identityKey": "The identity these details belong to — filled from the task's own scopedTo subject, never typed.",
				"ssn":         "9 digits — hyphens are fine. Stored encrypted under your own key, and erased for good if your data is ever shredded.",
				"dob":         "Your date of birth. Stored encrypted alongside the SSN.",
			},
			// ssn/dob are the platform's sensitive aspect-types: masked entry,
			// no local echo. Safe to set here precisely because the only two
			// fields a client RENDERS are those two — identityKey is the
			// targetField and is filtered out of the rendered set.
			Sensitive: true,
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class: "identity",
				// "task", NOT "standing" — the descriptor names the path a
				// descriptor-driven client actually walks. This op has two
				// audiences: standing front-desk staff (frontOfHouse /
				// backOfHouse, scope=any) and the APPLICANT recording their own
				// PII under lease-signing's onboarding userTask. A staff FE
				// hardcodes its own dispatch; the applicant's client cannot
				// infer the task path, so the descriptor is written in its
				// voice — the same rule that settled clinic's dual-grant ops.
				//
				// It is also the only value that WORKS for that caller: a
				// consumer holds no standing grant for this op, so authority
				// comes from the task's ephemeral grant, which step 3 matches
				// on {task, target}. A "standing" descriptor sends no
				// authContext at all and is refused every time. The script
				// agrees — its unclaimed-only confinement is exempted only when
				// op.authTargetValidated is set, which the task path is what
				// sets.
				AuthContext: "task",
				TargetField: "identityKey",
				TargetType:  "identity",
				// Resolves from the task's scopedTo, which IS the applicant's
				// identity key — matched by type before the client's
				// submitter-identity fallback is ever reached.
				//
				// .mergedInto is NOT declared: the merged guard keys off
				// state == "merged" (MergeIdentity writes both together), the
				// aspect is absent pre-merge, and merely TESTING membership of
				// a required-but-absent key faults HydrationMiss — so
				// declaring it would reject every ordinary call.
				Reads: []string{
					"{payload.identityKey}",
					"{payload.identityKey}.state",
				},
			},
		},
		{
			OperationType: "UnlinkCredential",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Remove this sign-in method",
				ShortLabel:  "Remove",
				Description: "Stop this way of signing in from working. Your other sign-in methods keep working, and you always keep at least one.",
				Icon:        "key",
				Tone:        "danger",
				SubmitLabel: "Remove",
				Group:       "Identity",
			},
			// One input, and it is never asked for: the dispatch resolves it
			// from the row the person acted on. That is the whole reason this
			// op was exempt as `unprojected-input` until now — a form asking
			// someone to hand-type a vtx.identity.<NanoID> is not a form, and
			// the descriptor would have been promising something it could not
			// keep.
			InputSchema: `{"type":"object","properties":` +
				`{"credentialActorKey":{"type":"string","title":"Sign-in method","description":"The bound credential to remove. Filled from the row you chose."}},` +
				`"required":["credentialActorKey"]}`,
			FieldDescriptions: map[string]string{
				"credentialActorKey": "Which sign-in method to remove. Filled from the row you chose; you never type it.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class: "identity",
				// "self", and the two values genuinely differ: the envelope's
				// authContext target is the SESSION identity (the person doing
				// the removing, which is what scope=self is checked against),
				// while the payload's credentialActorKey is the credential
				// named by the row. A client that conflated them would submit
				// its own identity as the credential to remove and take the
				// script's not-found branch every time.
				AuthContext: "self",
				TargetField: "credentialActorKey",
				TargetType:  "identity",
				// A credential actor IS a vtx.identity vertex, so targetType
				// alone would offer this op against any identity-typed row —
				// a person's, not a credential's. The section's constant
				// row_kind is what narrows it to the rows it means, by
				// declaration rather than by the corpus happening to project
				// no other identity-typed rows today.
				VisibleWhen: &pkgmgr.OpVisibleWhenSpec{
					Field:  "row_kind",
					Equals: "credentialBinding",
				},
				// All three absence-TOLERANT, verified against the script
				// branch: {actor}, {actor}.state and {actor}.credentialBinding
				// each have a named outcome of their own when absent
				// (CredentialUnlinkRejected: no-target, or the implicit
				// self-credential not-found case), and a required read faults
				// HydrationMiss before any of them can render.
				//
				// The subject here is op.actor, pinned by step 3's scope=self
				// gate, so this is not the cross-identity probe ClaimIdentity's
				// payload target is. The ceremony still owes its caller its own
				// answer rather than a hydration wire code.
				//
				// The index vertex and the boundTo link this op tombstones
				// are NOT here: they are class-(g) script-derived keys, so
				// the DDL's own derive_reads declares them and no submitter
				// can or should.
				OptionalReads: []string{
					"{actor}",
					"{actor}.state",
					"{actor}.credentialBinding",
				},
			},
		},
		{
			// DISPATCH-ONLY, and every omission below is deliberate.
			//
			// This op cannot be submitted from a descriptor: the Gateway's
			// raw-credential carve-out resolves op.actor to the NEW credential,
			// so a declared `self` authContext (which names the resolved
			// business identity) is denied at step 3. That is why the
			// permission Note carries `[no-op-meta: raw-credential-actor — …]`
			// and why it still does — §4.4's credential authContext value is
			// the mechanism that would retire it, and it has not landed.
			//
			// What changed is that the descriptor is no longer only for
			// clients. Dispatch.optionalReads is read at step 4 as the
			// Contract #2 §2.5 floor a submitter's envelope cannot raise
			// (internal/processor/descriptor_floor.go), and without an op-meta
			// there is nothing for that floor to read. This op needs one more
			// than any other in the package: its scope=self gate binds
			// authContext.target to the raw credential, so — unlike
			// ClaimIdentity, whose actor at least self-matches — NOTHING
			// upstream of the script constrains which identity the payload
			// names. Its script touches that submitter-named key, and
			// `fail_link` reuses the "ClaimKeyInvalid: " prefix so every
			// rejection collapses to one generic answer. Declared `reads`, the
			// absent-target case answers HydrationFailed with the probed key
			// in details.missingKey instead: an identity-keyspace oracle, one
			// guess per request, on a consumer credential.
			//
			// No Presentation, InputSchema, FieldDescriptions, Class or
			// AuthContext. That is not an incomplete descriptor, it is the
			// whole mechanism by which this entry changes nothing
			// client-side:
			//   - No Dispatch.Class means opButton short-circuits before it
			//     can build an envelope (cmd/facet/web/app.js), so no client
			//     submits from this descriptor and the step-3 denial the
			//     exemption avoids is never reachable.
			//   - The three missing self-description fields keep
			//     lint-package-standard's S1 descriptorGaps non-empty, so the
			//     permission's exemption remains VALID rather than becoming
			//     the "exemption plus full descriptor" drift S1 exists to
			//     catch.
			// Authorization is untouched: permissions.go is unchanged, and the
			// only new graph edge is the install-time `forOperation` link
			// pkgmgr mints from the permission that already existed.
			OperationType: "CompleteCredentialLink",
			// The one field the read templates below build on, declared
			// REQUIRED so the guarantee is stated rather than inferred. Without
			// it the templates wrap a payload field nothing promises: an
			// omitted targetIdentityKey substitutes empty and leaves a
			// malformed key, which NATS rejects outright instead of reporting
			// absent (lint-package-standard's read-template rule). The
			// Processor's own resolver already declines to floor an unresolved
			// template, but that safety is a property of one call site — this
			// is the property being true of the descriptor itself, which is
			// what the next author reads.
			//
			// It is deliberately NOT a form schema: no titles, no help text, no
			// FieldDescriptions. Two S1 gaps remain (Presentation.Title and
			// FieldDescriptions), which is what keeps the permission's
			// exemption valid.
			InputSchema: `{"type":"object","properties":` +
				`{"targetIdentityKey":{"type":"string","description":"vtx.identity.<NanoID> of the identity the new credential is binding to."}},` +
				`"required":["targetIdentityKey"]}`,
			Dispatch: &pkgmgr.OpDispatchSpec{
				// The live dispatchers' own list, verified against the script
				// branch: cmd/facet/credentials.go and
				// cmd/loftspace-app/credentials_link.go, both of which build it
				// from identityceremony.CompleteCredentialLinkContextHint.
				OptionalReads: []string{
					"{payload.targetIdentityKey}",
					"{payload.targetIdentityKey}.state",
					"{payload.targetIdentityKey}.linkKey",
					"{payload.targetIdentityKey}.credentialBinding",
				},
			},
		},
	}
}
