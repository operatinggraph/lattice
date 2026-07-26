package identitydomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// OpMetas declares the descriptor vocabulary (edge-showcase-app-design.md §3.3)
// for the identity ops a descriptor-driven client can actually SUBMIT.
// identity-domain is the idiom source every other package copies, so the
// Vertical Package Standard §3.4 has it conform LAST — its descriptors are
// written once the vocabulary has settled against the corpus rather than ahead
// of it.
//
// Two ops carry a descriptor; the other five carry a stated `[no-op-meta: …]`
// exemption in their permission Note (permissions.go) instead. That split is
// the point of this increment, and it is not a shortcut: a descriptor is a
// machine-readable PROMISE that a client holding only these fields can build a
// valid Contract #2 envelope. Five identity ops cannot honour that promise,
// because their submission is a client-side CEREMONY rather than a filled form
// — the client must mint a secret and keep the plaintext, or submit as a
// different actor than the one it authenticated as, or name a key nothing
// projects. Shipping a form for those would not make them discoverable; it
// would make them fail in ways the descriptor itself claims are impossible.
// The exemption reasons are per-op and specific, so the escape hatch stays
// checkable rather than becoming a place to park anything inconvenient.
//
// Dispatch.Class is "identity" on both — the owning vertexType DDL's own
// CanonicalName, never a vertical name.
//
// Each Reads list is the live dispatcher's, verified against the script branch
// it feeds: cmd/facet/claim.go for ClaimIdentity, and the onboarding userTask
// (packages/lease-signing/patterns.go) for RecordIdentityPII.
func OpMetas() []pkgmgr.OpMetaSpec {
	return []pkgmgr.OpMetaSpec{
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
			// targetIdentityKey is deliberately NOT a targetField. It arrives
			// with the invitation, not from an entity the client projects, and
			// declaring TargetType "identity" would be worse than declaring
			// nothing: the client's resolver falls back to the SUBMITTER's own
			// identity for that type rather than degrading, so a wrong target
			// would be substituted silently instead of the op being withheld.
			InputSchema: `{"type":"object","properties":` +
				`{"targetIdentityKey":{"type":"string","description":"vtx.identity.<NanoID> of the identity being claimed — carried by the claim invitation."},` +
				`"claimKey":{"type":"string","description":"The one-time claim secret you were given."}},` +
				`"required":["targetIdentityKey","claimKey"]}`,
			FieldDescriptions: map[string]string{
				"targetIdentityKey": "The identity waiting to be claimed. Comes from the claim invitation, not from anything you are looking at.",
				"claimKey":          "The one-time secret you were handed. Its sha256 is compared against the stored hash; it is spent by a successful claim and cannot be reused. A wrong or spent secret and an already-bound credential fail identically, so a rejection tells an attacker nothing about which one it was.",
			},
			// NOT Sensitive, deliberately. The flag is per-OP and masks every
			// field a client renders — here that is claimKey (a secret, worth
			// masking) AND targetIdentityKey (a key transcribed from the
			// invitation, which masking would make unenterable, since the
			// masked input also drops any prefilled value). Over-masking breaks
			// the flow; echoing a secret the person is reading off their own
			// invitation does not. See the README note on the missing per-field
			// granularity.
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "identity",
				AuthContext: "self",
				// The dedup probe vtx.credentialindex.<sha256(actor)> is NOT
				// declared: it is sha256-derived and the template vocabulary
				// substitutes rather than computes. Harmless HERE, and only
				// here — a claim is submitted by a FRESH credential, which by
				// construction has no prior index entry to revive, and the
				// load-bearing stop is the CreateOnly create that
				// RevisionConflicts on an already-bound credential regardless
				// of what the caller declared.
				Reads: []string{
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
				SubmitLabel: "Submit",
				Group:       "Identity",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"identityKey":{"type":"string","description":"vtx.identity.<NanoID> the details are recorded onto — the task's own subject."},` +
				`"ssn":{"type":"string","description":"Social Security Number: 9 digits. Hyphens are accepted and stripped."},` +
				`"dob":{"type":"string","description":"Date of birth, ISO YYYY-MM-DD."}},` +
				`"required":["identityKey","ssn","dob"]}`,
			FieldDescriptions: map[string]string{
				"identityKey": "The identity these details belong to — filled from the task's own scopedTo subject, never typed.",
				"ssn":         "Social Security Number. 9 digits; hyphens are accepted and stripped, and it is stored normalized in a sensitive, identity-anchored aspect — the crypto-shred unit.",
				"dob":         "Date of birth, ISO YYYY-MM-DD. Validated as a real calendar date (leap years included) and stored in a sensitive aspect alongside the SSN.",
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
	}
}
