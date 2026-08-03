package identitydomain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/substrate"
)

func TestPackage_ManifestMatchesDefinition(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	m, err := pkgmgr.ParseManifest(filepath.Join(wd, "manifest.yaml"))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if err := m.VerifyAgainstDefinition(Package); err != nil {
		t.Fatalf("manifest <-> Definition drift: %v", err)
	}
}

func TestPackage_DeclaresUserFacingRoles(t *testing.T) {
	want := map[string]bool{
		"consumer": true, "frontOfHouse": true, "backOfHouse": true, "provider": true,
		// Not user-facing (system role for the Gateway's own identity), but
		// declared in the same Roles slice as the others.
		"identityProvisioner": true,
	}
	if got := len(Package.Roles); got != len(want) {
		t.Fatalf("expected %d declared roles, got %d", len(want), got)
	}
	for _, r := range Package.Roles {
		if !want[r.CanonicalName] {
			t.Errorf("unexpected role %q", r.CanonicalName)
		}
		if r.Description == "" {
			t.Errorf("role %q missing description", r.CanonicalName)
		}
	}
}

func TestPackage_DDLsAndOps(t *testing.T) {
	if got := len(Package.DDLs); got != 15 {
		t.Fatalf("expected 15 DDLs (identity + ssn, dob, name, email, phone, claimKey, linkKey, credentialBinding, idpBinding + "+
			"indexes, duplicateOf + actorRevocation, gateway.actorRevoked, gateway.actorUnrevoked), got %d", got)
	}
	identity := ddlByCanonicalName(t, "identity")
	if identity.Class != "meta.ddl.vertexType" {
		t.Fatalf("identity DDL class = %q, want meta.ddl.vertexType", identity.Class)
	}
	if got := len(identity.PermittedCommands); got != 9 {
		t.Fatalf("identity permittedCommands: got %d, want 9 "+
			"(CreateUnclaimedIdentity, UpdateIdentityState, ClaimIdentity, RotateClaimKey, RecordIdentityPII, ProvisionConsumerIdentity, "+
			"InitiateCredentialLink, CompleteCredentialLink, UnlinkCredential)", got)
	}
}

// TestPackage_SensitivePIIAspectTypes pins the ssn/dob aspect-type DDLs as
// sensitive=true — the structural declaration that makes the step-6 validator
// anchor them to identity vertices (lattice-architecture Item 6 / NFR-S3).
// ssn/dob are written only by RecordIdentityPII, so they pin
// permittedCommands:[RecordIdentityPII].
func TestPackage_SensitivePIIAspectTypes(t *testing.T) {
	for _, name := range []string{"ssn", "dob"} {
		d := ddlByCanonicalName(t, name)
		if d.Class != "meta.ddl.aspectType" {
			t.Errorf("%s DDL class = %q, want meta.ddl.aspectType", name, d.Class)
		}
		if !d.Sensitive {
			t.Errorf("%s DDL Sensitive = false, want true", name)
		}
		if got := d.PermittedCommands; len(got) != 1 || got[0] != "RecordIdentityPII" {
			t.Errorf("%s DDL permittedCommands = %v, want [RecordIdentityPII]", name, got)
		}
	}
}

// TestPackage_LifecyclePIIAspectTypesSensitive pins the name/email/phone/
// claimKey/linkKey/credentialBinding aspect-type DDLs as sensitive=true with
// EMPTY permittedCommands. They are written by multiple ops across packages
// (CreateUnclaimedIdentity, ClaimIdentity, InitiateCredentialLink/
// CompleteCredentialLink, and identity-hygiene's MergeIdentity), so a
// non-empty permittedCommands would make step-6 reject a legitimate writer
// (e.g. MergeIdentity writing name) — identity-anchoring is their only
// enforcement.
func TestPackage_LifecyclePIIAspectTypesSensitive(t *testing.T) {
	for _, name := range []string{"name", "email", "phone", "claimKey", "linkKey", "credentialBinding"} {
		d := ddlByCanonicalName(t, name)
		if d.Class != "meta.ddl.aspectType" {
			t.Errorf("%s DDL class = %q, want meta.ddl.aspectType", name, d.Class)
		}
		if !d.Sensitive {
			t.Errorf("%s DDL Sensitive = false, want true", name)
		}
		if got := len(d.PermittedCommands); got != 0 {
			t.Errorf("%s DDL permittedCommands = %v, want empty (multiple writers across packages)", name, d.PermittedCommands)
		}
	}
}

func TestPackage_ScriptUsesOnlyKnownKeyReads(t *testing.T) {
	for _, d := range Package.DDLs {
		for _, forbidden := range []string{"KVListKeys", "list_keys", "keys_with_prefix"} {
			if strings.Contains(d.Script, forbidden) {
				t.Errorf("DDL %q script must not reference prefix-scan helper %q", d.CanonicalName, forbidden)
			}
		}
	}
}

// ddlByCanonicalName returns the DDLSpec with the given canonicalName, failing
// the test if absent.
func ddlByCanonicalName(t *testing.T, name string) pkgmgr.DDLSpec {
	t.Helper()
	for _, d := range Package.DDLs {
		if d.CanonicalName == name {
			return d
		}
	}
	t.Fatalf("no DDL with canonicalName %q", name)
	return pkgmgr.DDLSpec{}
}

// TestActorRevocationScript_NanoIDAlphabetMatchesSubstrate pins the Starlark
// NANOID_ALPHABET literal in actorRevocationScript (required_actor's charset
// guard, the gateway-revocation-poison-pill fix) to internal/substrate/
// nanoid.go's canonical Alphabet — Starlark has no cross-language import, so
// the two are hand-duplicated; without this test a future alphabet change
// (e.g. rotating out another ambiguous character) would silently desync them,
// either rejecting live NanoIDs or admitting an id shape the real generator
// never produces.
func TestActorRevocationScript_NanoIDAlphabetMatchesSubstrate(t *testing.T) {
	if !strings.Contains(actorRevocationScript, `NANOID_ALPHABET = "`+substrate.Alphabet+`"`) {
		t.Errorf("actorRevocationScript's NANOID_ALPHABET literal does not match substrate.Alphabet (%q) verbatim", substrate.Alphabet)
	}
}

func TestPackage_DependsOnRbacDomain(t *testing.T) {
	found := false
	for _, d := range Package.Depends {
		if d == "rbac-domain" {
			found = true
		}
	}
	if !found {
		t.Error("identity-domain must declare rbac-domain as a dependency")
	}
}

// TestPackage_OpMetasAreFullDescriptors pins the S1 surface (Vertical Package
// Standard §2 S1, §3.4). A count alone would miss the defect that matters — an
// entry surviving as a bare {OperationType} still counts while rendering
// nothing — so each is pinned to its full shape and to the authContext its
// caller actually uses.
//
// RecordIdentityPII is "task", not "standing": its descriptor-driven caller is
// the applicant submitting lease-signing's onboarding userTask, who holds no
// standing grant for this op at all. A standing descriptor sends no authContext
// and step 3 refuses it.
//
// The two staff ceremony ops are "standing" for the mirror reason: both are
// scope=any grants to frontOfHouse/backOfHouse/operator with no relationship
// to any target, so there is no authContext for the client to populate.
func TestPackage_OpMetasAreFullDescriptors(t *testing.T) {
	wantAuthContext := map[string]string{
		"ClaimIdentity":           "self",
		"RecordIdentityPII":       "task",
		"CreateUnclaimedIdentity": "standing",
		"RotateClaimKey":          "standing",
	}
	if got, want := len(Package.OpMetas), len(wantAuthContext); got != want {
		t.Fatalf("OpMetas: got %d, want %d", got, want)
	}
	byOp := map[string]pkgmgr.OpMetaSpec{}
	for _, m := range Package.OpMetas {
		byOp[m.OperationType] = m
	}
	for op, wantAC := range wantAuthContext {
		m, ok := byOp[op]
		if !ok {
			t.Fatalf("%s: no op-meta — a granted, user-facing op must be self-describing (S1)", op)
		}
		if m.Presentation == nil || m.Presentation.Title == "" || m.InputSchema == "" ||
			len(m.FieldDescriptions) == 0 || m.Dispatch == nil {
			t.Fatalf("%s: needs a FULL descriptor (presentation+schema+fields+dispatch), got %+v", op, m)
		}
		if m.Dispatch.Class != "identity" {
			t.Errorf("%s: dispatch class = %q, want identity (the owning DDL's canonicalName)", op, m.Dispatch.Class)
		}
		if m.Dispatch.AuthContext != wantAC {
			t.Errorf("%s: authContext = %q, want %q", op, m.Dispatch.AuthContext, wantAC)
		}
	}
	// ClaimIdentity must NOT name a targetType. A client resolving targetType
	// "identity" falls back to the SUBMITTER's own identity rather than
	// degrading, so a target it cannot really resolve would be substituted
	// silently. RecordIdentityPII may, and only because its task context
	// carries a scopedTo of that type, which matches before the fallback.
	if d := byOp["ClaimIdentity"].Dispatch; d.TargetField != "" || d.TargetType != "" {
		t.Errorf("ClaimIdentity: declares targetField %q/targetType %q; the claim target comes from the invitation, and a declared identity targetType silently resolves to the submitter", d.TargetField, d.TargetType)
	}
	// RotateClaimKey inherits that refusal for the same mechanism and a worse
	// outcome: staff re-issuing against their OWN identity is the silent
	// substitution, so identityKey is collected by an x-entityRef picker in the
	// form rather than resolved from context.
	if d := byOp["RotateClaimKey"].Dispatch; d.TargetField != "" || d.TargetType != "" {
		t.Errorf("RotateClaimKey: declares targetField %q/targetType %q; an identity targetType resolving from no context substitutes the submitting staff member's own identity", d.TargetField, d.TargetType)
	}
	if !strings.Contains(byOp["RotateClaimKey"].InputSchema, `"x-entityRef":"identity"`) {
		t.Error("RotateClaimKey: identityKey must declare an x-entityRef picker — without it the field is neither resolvable nor pickable, and the form asks a human to hand-type a vtx.identity.<NanoID>")
	}
	if d := byOp["RecordIdentityPII"].Dispatch; d.TargetField == "" || d.TargetType != "identity" {
		t.Errorf("RecordIdentityPII: targetField %q/targetType %q, want a field typed identity so the task's scopedTo fills it", d.TargetField, d.TargetType)
	}
	// Only RecordIdentityPII masks its payload: the flag is per-OP and a client
	// masks every field it renders, which is safe here only because its
	// targetField is filtered out, leaving exactly ssn+dob.
	for op, m := range byOp {
		if want := op == "RecordIdentityPII"; m.Sensitive != want {
			t.Errorf("%s: Sensitive = %v, want %v", op, m.Sensitive, want)
		}
	}
}

// TestPackage_ExemptOpsStateTheirExemption pins the other half of S1. These
// three are user-facing — a person triggers each — but their submission is
// something the descriptor vocabulary still cannot express: a submission as a
// different actor than the client authenticated as, a secret that has to reach
// a SECOND device, or an input nothing projects. S1 admits that only when the
// permission Note SAYS so, naming the missing mechanism by its code, and the
// reason has to survive in the Note or this fails.
//
// The set is what shrinks as mechanisms land. CreateUnclaimedIdentity and
// RotateClaimKey left it when OpCeremonySpec made minting expressible, and
// they are asserted against the opposite half below — an op that gains a
// mechanism must gain a descriptor, not quietly keep its amnesty.
func TestPackage_ExemptOpsStateTheirExemption(t *testing.T) {
	exempt := map[string]string{
		"InitiateCredentialLink": "paired-code",
		"CompleteCredentialLink": "raw-credential-actor",
		"UnlinkCredential":       "lifecycle-op",
	}
	seen := map[string]bool{}
	for _, p := range Package.Permissions {
		code, isExempt := exempt[p.OperationType]
		if !isExempt {
			continue
		}
		seen[p.OperationType] = true
		if !strings.Contains(p.Note, "[no-op-meta: "+code+" — ") {
			t.Errorf("%s: Note must state the exemption as [no-op-meta: %s — <reason>]; S1's closed vocabulary is what lets the exemption expire when the mechanism ships. Got: %s",
				p.OperationType, code, p.Note)
		}
	}
	for op := range exempt {
		if !seen[op] {
			t.Errorf("%s: expected a permission entry to carry its exemption", op)
		}
	}
	// The complement: an exempt op must not ALSO carry a descriptor, which
	// would be the exemption and the promise at once.
	for _, m := range Package.OpMetas {
		if _, isExempt := exempt[m.OperationType]; isExempt {
			t.Errorf("%s: exempt in its Note but also declares an op-meta", m.OperationType)
		}
	}
}

// TestPackage_CeremonyOpsCarryDescriptorNotExemption is the direction that can
// regress. The two minting ops were exempt precisely because a form cannot ask
// a person to type a hash whose preimage nobody holds; OpCeremonySpec removed
// that reason, so each must now carry a descriptor whose Ceremony names the
// field the client fills, AND must have surrendered its exemption. Keeping
// both would leave a client free to render the hash as a text input — the
// accepted-garbage outcome the exemption existed to prevent.
func TestPackage_CeremonyOpsCarryDescriptorNotExemption(t *testing.T) {
	wantCeremonyField := map[string]string{
		"CreateUnclaimedIdentity": "claimKeyHash",
		"RotateClaimKey":          "claimKeyHash",
	}
	seen := map[string]bool{}
	for _, m := range Package.OpMetas {
		field, want := wantCeremonyField[m.OperationType]
		if !want {
			continue
		}
		seen[m.OperationType] = true
		if m.Ceremony == nil {
			t.Errorf("%s: no Ceremony — the client would render %s as a text input", m.OperationType, field)
			continue
		}
		if m.Ceremony.MintedSecretHashField != field {
			t.Errorf("%s: Ceremony.MintedSecretHashField = %q, want %q",
				m.OperationType, m.Ceremony.MintedSecretHashField, field)
		}
		if m.Ceremony.RevealTitle == "" || m.Ceremony.RevealHelp == "" {
			t.Errorf("%s: reveal copy is required — a minted secret shown with no explanation is a string the person discards", m.OperationType)
		}
		if !strings.Contains(m.InputSchema, `"`+field+`"`) {
			t.Errorf("%s: %s must be declared in InputSchema; the client fills it, and a field absent from the schema is absent from the payload",
				m.OperationType, field)
		}
	}
	for op := range wantCeremonyField {
		if !seen[op] {
			t.Errorf("%s: expected an op-meta now that its ceremony is expressible", op)
		}
	}
	for _, p := range Package.Permissions {
		if _, isCeremony := wantCeremonyField[p.OperationType]; !isCeremony {
			continue
		}
		if strings.Contains(p.Note, "[no-op-meta:") {
			t.Errorf("%s: still claims an S1 exemption while carrying a descriptor — the mechanism landed, so the amnesty must go", p.OperationType)
		}
	}
}

// TestPackage_NoDescriptorForOperatorOnlyOps: an op only a trusted tool submits
// gets no descriptor at all, not a bare one. A bare entry would still mint a
// vtx.meta vertex and occupy forOperation's flat operationType index for no
// caller's benefit.
func TestPackage_NoDescriptorForOperatorOnlyOps(t *testing.T) {
	for _, m := range Package.OpMetas {
		switch m.OperationType {
		case "UpdateIdentityState", "ProvisionConsumerIdentity", "RevokeActor", "UnrevokeActor":
			t.Errorf("%s: operator/system-only, no human triggers it — it should carry no op-meta", m.OperationType)
		}
	}
}
