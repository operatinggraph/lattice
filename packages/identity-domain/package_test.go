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
	if got := len(Package.DDLs); got != 17 {
		t.Fatalf("expected 17 DDLs (identity + ssn, dob, name, email, phone, claimKey, linkKey, credentialBinding, idpBinding + "+
			"indexes, duplicateOf, boundTo + actorRevocation, gateway.actorRevoked, gateway.actorUnrevoked + unbindIdentityCredentials), got %d", got)
	}
	identity := ddlByCanonicalName(t, "identity")
	if identity.Class != "meta.ddl.vertexType" {
		t.Fatalf("identity DDL class = %q, want meta.ddl.vertexType", identity.Class)
	}
	if got := len(identity.PermittedCommands); got != 10 {
		t.Fatalf("identity permittedCommands: got %d, want 10 "+
			"(CreateUnclaimedIdentity, UpdateIdentityState, ClaimIdentity, RotateClaimKey, RecordIdentityPII, ProvisionConsumerIdentity, "+
			"InitiateCredentialLink, CompleteCredentialLink, UnlinkCredential, ReconcileCredentialBinding)", got)
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
//
// UnlinkCredential is "self", and its payload target is a DIFFERENT value from
// its envelope target — the session identity authorizes, the row names the
// credential. Its own shape is pinned in full below
// (TestPackage_UnlinkCredentialIsRowDispatched).
func TestPackage_OpMetasAreFullDescriptors(t *testing.T) {
	wantAuthContext := map[string]string{
		"ClaimIdentity":           "self",
		"RecordIdentityPII":       "task",
		"CreateUnclaimedIdentity": "standing",
		"RotateClaimKey":          "standing",
		"UnlinkCredential":        "self",
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
	// ClaimIdentity must NOT name a targetType. Nothing projects an `identity`
	// entity or self-anchor, so a declared TargetType "identity" resolves to
	// nothing and offers no picker — which makes the client withhold the op
	// entirely. Its target is transcribed from the invitation, so the field
	// has to stay ordinary and prefillable. RecordIdentityPII may declare one,
	// and only because its task context carries a scopedTo of that type.
	if d := byOp["ClaimIdentity"].Dispatch; d.TargetField != "" || d.TargetType != "" {
		t.Errorf("ClaimIdentity: declares targetField %q/targetType %q; the claim target is transcribed from the invitation, and a declared identity targetType would withhold the op instead of letting it be entered", d.TargetField, d.TargetType)
	}
	// RotateClaimKey takes the OPPOSITE call, and the pair is what makes it
	// degrade honestly. Nothing projects an `identity` entity or self-anchor
	// today, so declaring the target is what lets opButton's gate withhold the
	// op behind a card; declaring only the picker would skip that gate and
	// offer a form whose one required field can never be filled. Both halves
	// must hold together — the picker is what lets the op become completable
	// the moment a lens projects unclaimed identities.
	if d := byOp["RotateClaimKey"].Dispatch; d.TargetField != "identityKey" || d.TargetType != "identity" {
		t.Errorf("RotateClaimKey: targetField %q/targetType %q, want identityKey/identity so the offer gate can withhold it while nothing projects an identity", d.TargetField, d.TargetType)
	}
	if !strings.Contains(byOp["RotateClaimKey"].InputSchema, `"x-entityRef":"identity"`) {
		t.Error("RotateClaimKey: identityKey must declare an x-entityRef picker — it is what makes the op completable once unclaimed identities are projected, with no change here")
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

// TestPackage_ExemptOpsStateTheirExemption pins the other half of S1. Both of
// these are user-facing — a person triggers each — but their submission is
// something the descriptor vocabulary still cannot express: each submits as a
// different actor than the client authenticated as, one of them after carrying
// a secret to a SECOND device. S1 admits that only when the permission Note
// SAYS so, naming the missing mechanism by its code, and the reason has to
// survive in the Note or this fails.
//
// The set is what shrinks as mechanisms land. CreateUnclaimedIdentity and
// RotateClaimKey left it when OpCeremonySpec made minting expressible, and
// UnlinkCredential left it when the boundTo link gave its one input a
// projected row to be filled from. Each is asserted against the opposite half
// below — an op that gains a mechanism must gain a descriptor, not quietly
// keep its amnesty.
func TestPackage_ExemptOpsStateTheirExemption(t *testing.T) {
	exempt := map[string]string{
		"InitiateCredentialLink": "paired-code",
		"CompleteCredentialLink": "raw-credential-actor",
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

// TestPackage_UnlinkCredentialIsRowDispatched pins the three declarations that
// make UnlinkCredential's descriptor a promise rather than a form nobody can
// fill, each of which used to be the reason it was exempt.
//
// The payload target and the envelope target are DIFFERENT values, and the
// separation is the whole mechanism: authContext "self" names the session
// identity (what scope=self is checked against) while credentialActorKey is
// resolved from the row. A descriptor that named one for both would submit the
// person's own identity as the credential to remove.
//
// visibleWhen is the other half. A credential actor is itself a vtx.identity
// vertex, so targetType alone offers this op against a person's row as readily
// as a credential's; the section's constant row_kind is what narrows it.
func TestPackage_UnlinkCredentialIsRowDispatched(t *testing.T) {
	var m *pkgmgr.OpMetaSpec
	for i := range Package.OpMetas {
		if Package.OpMetas[i].OperationType == "UnlinkCredential" {
			m = &Package.OpMetas[i]
		}
	}
	if m == nil {
		t.Fatal("UnlinkCredential: no descriptor — its unprojected-input exemption is retired, so it must carry one")
	}
	d := m.Dispatch
	if d == nil {
		t.Fatal("UnlinkCredential: no Dispatch — a descriptor with no dispatch degrades to an un-submittable card")
	}
	if d.AuthContext != "self" {
		t.Errorf("AuthContext = %q, want self — scope=self is checked against the session identity", d.AuthContext)
	}
	if d.TargetField != "credentialActorKey" || d.TargetType != "identity" {
		t.Errorf("target = %q/%q, want credentialActorKey/identity", d.TargetField, d.TargetType)
	}
	if d.VisibleWhen == nil || d.VisibleWhen.Field != "row_kind" || d.VisibleWhen.Equals != "credentialBinding" {
		t.Errorf("VisibleWhen = %+v, want row_kind == credentialBinding — without it the op is offered on any identity-typed row", d.VisibleWhen)
	}
	// The dispatcher's declared reads, which must stay the live envelope's:
	// nothing required, all three absence-tolerant. Each of them has a named
	// outcome of its own when absent — CredentialUnlinkRejected: no-target,
	// or the implicit self-credential not-found case — and a required read
	// faults HydrationMiss before any of those can render, substituting a
	// hydration wire code that also echoes the probed key back.
	if got, want := strings.Join(d.Reads, ","), ""; got != want {
		t.Errorf("Reads = %q, want %q — every key here has a script-rendered absence outcome", got, want)
	}
	if got, want := strings.Join(d.OptionalReads, ","), "{actor},{actor}.state,{actor}.credentialBinding"; got != want {
		t.Errorf("OptionalReads = %q, want %q", got, want)
	}
	// The class-(g) keys stay out of the submitter's declaration: the index
	// vertex and the boundTo link are derived by the DDL, not by any client.
	for _, r := range append(append([]string{}, d.Reads...), d.OptionalReads...) {
		if strings.Contains(r, "credentialindex") || strings.Contains(r, "boundTo") {
			t.Errorf("declared read %q is a script-derived key — derive_reads owns it, no submitter can compute it", r)
		}
	}
}

// TestPackage_RotateClaimKeyWithheldUntilUnclaimedRow: RotateClaimKey targets
// an identity by type, and a credential-binding row is identity-typed too. Its
// visibleWhen is what stops "re-issue this person's claim secret" being offered
// against one of their own sign-in methods — an offer that reaches the script's
// unclaimed-only guard and denies, after the person clicked it.
func TestPackage_RotateClaimKeyWithheldUntilUnclaimedRow(t *testing.T) {
	for _, m := range Package.OpMetas {
		if m.OperationType != "RotateClaimKey" {
			continue
		}
		v := m.Dispatch.VisibleWhen
		if v == nil || v.Field != "unclaimed" || v.Equals != true {
			t.Fatalf("RotateClaimKey VisibleWhen = %+v, want unclaimed == true", v)
		}
		return
	}
	t.Fatal("RotateClaimKey: no descriptor")
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
