// Package pkgmgr defines the Capability Package format and provides the
// install / uninstall / list machinery used by `cmd/lattice-pkg`.
//
// See `docs/components/_packages.md` for the canonical spec.
//
// Shape:
//   - Manifest is YAML (`manifest.yaml`); package definitions are Go
//     (each package exports a `Package = pkgmgr.Definition{...}`).
//   - Install/upgrade/uninstall submit an op (InstallPackage / UpgradePackage /
//     UninstallPackage) to the Processor over ops.meta; the Processor is the
//     sole writer of the `core-kv` bucket.
//   - Operator credential is the admin actor NanoID read from
//     `lattice.bootstrap.json`.
package pkgmgr

import (
	"encoding/json"
	"fmt"
)

// validateAll runs every field-level package validator in a fixed order. It is
// the shared pre-flight for Install / Upgrade / Apply so all three reject a
// malformed Definition identically, before any KV operation. Pure (no I/O):
// each constituent validator is a pure function over the Definition.
//
// It validates the EXPANDED Definition: read-grant walks compile first (which
// is also where the walk grammar is enforced), so every validator downstream —
// canonical-name uniqueness, adapters, read-path posture — sees the generated
// producer lenses exactly as the installer will install them.
func (def Definition) validateAll() error {
	def, err := def.ExpandReadGrantWalks()
	if err != nil {
		return err
	}
	for _, check := range []func() error{
		def.validatePackageName,
		def.validateLensBuckets,
		def.validateLensAdapters,
		def.validateLensReadPath,
		def.validateWeaverTargets,
		def.validateLoomPatterns,
		def.validateOpMetas,
		def.ValidateOpDispatchTemplates,
		def.validateEffects,
		def.validateSensitiveClassScope,
		def.validateRetentionClasses,
		def.validateCustodyScope,
		def.validateAbstractDDLScope,
		def.validateCanonicalNameUniqueness,
		def.validateNoReservedRoleName,
		def.validatePermissionIdentityUniqueness,
		def.validateRetiredSecureColumns,
	} {
		if err := check(); err != nil {
			return err
		}
	}
	return nil
}

// validatePackageName refuses a Definition.Name that is not already equal to
// its own normalizePackageName fold (trimmed, then lowercased).
// Installer.findInstalledPackage matches byte-exactly (a package name is a
// destructive resolution target, never folded to decide a match — see
// normalizePackageName's doc comment), so a Name installed with stray
// whitespace or uppercase would be findable ONLY by that exact spelling
// forever after: every later probe using the obviously-correct normalized
// form (Install's idempotency check, Upgrade's/Apply's existing-base lookup,
// Uninstall, IsPackageInstalled) would hit the fold-equal near-miss refusal
// instead of ever resolving it — an unbreakable loop whose only exit is
// deleting the record the guard protects. Refusing the denormalized Name at
// declaration time is what keeps that landmine from ever being installed;
// this is a fail-closed authoring guard, not a migration, since every
// package's declared Name is already its own normalized form.
func (def Definition) validatePackageName() error {
	normalized := normalizePackageName(def.Name)
	if def.Name != normalized {
		return fmt.Errorf(
			"pkgmgr: Definition.Name %q is not normalized (package names are matched trimmed + lowercased) — use %q",
			def.Name, normalized)
	}
	return nil
}

// validateCanonicalNameUniqueness rejects a package that declares the same
// meta-vertex canonicalName twice across the union of its DDLs, Lenses, and
// op-metas — the namespace the Processor's DDL cache serves through one byName
// map (vtx.meta.<NanoID> keyed by canonicalName). A contested name there costs
// one of the two definitions its lookup entirely: the cache serves the
// lowest-keyed meta-vertex and drops the other from both its name and key
// indexes, logging a WARN, so the package must fail closed here instead of
// letting NanoID ordering pick a winner. It is a pure function (no I/O) so it
// runs before any KV operation and is unit-testable without a live substrate.
// Roles (vtx.role.*) are intentionally excluded — they are a separate,
// deliberately shared namespace.
//
// An op-meta's canonicalName is its OperationType: an op-meta vertex is keyed
// vtx.meta.<NanoID> and is the only meta-vertex kind whose identifying name is
// its operation type, so it shares the collision namespace with DDL and Lens
// canonicalNames.
func (def Definition) validateCanonicalNameUniqueness() error {
	seen := make(map[string]string,
		len(def.DDLs)+len(def.Lenses)+len(def.OpMetas))
	check := func(name, kind string) error {
		if prev, dup := seen[name]; dup {
			return fmt.Errorf(
				"pkgmgr: duplicate meta canonicalName %q declared by both a %s and a %s",
				name, prev, kind)
		}
		seen[name] = kind
		return nil
	}
	for _, d := range def.DDLs {
		if err := check(d.CanonicalName, "DDL"); err != nil {
			return err
		}
	}
	for _, l := range def.Lenses {
		if err := check(l.CanonicalName, "lens"); err != nil {
			return err
		}
	}
	for _, o := range def.OpMetas {
		if err := check(o.OperationType, "op-meta"); err != nil {
			return err
		}
	}
	return nil
}

// validateNoReservedRoleName rejects a package that declares a Roles entry
// canonically named "operator" — the second, package-plane mint path a cold
// adversarial review of primordial-epoch-stranded-authority-design.md found
// packages/rbac-domain's runtime CreateRole guard does not cover.
// validateCanonicalNameUniqueness deliberately excludes roles from its own
// check ("a separate, deliberately shared namespace"), and this repo's kernel
// seeds the ONE primordial "operator" role outside any package's install
// batch entirely (Contract #7 §7.2: "the only primordial role is operator").
// A package minting a second live role of that name is root-equivalent
// through both name-matching capability lenses (lenses.go:135,358-365, which
// match by canonicalName, not by id) — and, worse, resolveGrants's
// installer.go:603-628 i.RoleIDs map keys on canonical name, so that SAME
// install's own GrantsTo: ["operator"] permissions would resolve onto the
// package's freshly-minted role instead of the kernel's, silently hijacking
// grant resolution for that install. Checked here, pre-flight and pure (no
// I/O), so it runs before any KV operation on Install AND Upgrade AND Apply
// alike, exactly like validateCanonicalNameUniqueness.
func (def Definition) validateNoReservedRoleName() error {
	for _, r := range def.Roles {
		if r.CanonicalName == "operator" {
			return fmt.Errorf(
				"pkgmgr: package %q declares a Roles entry named %q — reserved for the kernel's primordial"+
					" operator role; no package may mint a role of that name",
				def.Name, r.CanonicalName)
		}
	}
	return nil
}

// validatePermissionIdentityUniqueness rejects a package that declares two
// permissions with the same (operationType, scope). A permission's entity key
// is derived from its operationType + scope (Contract #8 §8.1, permTag) — its
// logical identity, not its position in the Permissions slice — so two
// permissions sharing both would collapse onto one vtx.permission.<id> key,
// silently dropping one of the grants. It is a pure function (no I/O) so it
// runs before any KV operation and is unit-testable without a live substrate.
func (def Definition) validatePermissionIdentityUniqueness() error {
	seen := make(map[string]struct{}, len(def.Permissions))
	for idx, p := range def.Permissions {
		id := p.OperationType + ":" + p.Scope
		if _, dup := seen[id]; dup {
			return fmt.Errorf(
				"pkgmgr: Permission[%d]: duplicate (operationType=%q, scope=%q) — "+
					"permission identity must be unique within a package",
				idx, p.OperationType, p.Scope)
		}
		seen[id] = struct{}{}
	}
	return nil
}

// Definition is the static, install-time bundle for one package. Package
// authors construct one of these in their package's top-level Go file and
// export it as `var Package = pkgmgr.Definition{...}`.
type Definition struct {
	// Name is the package's canonical name (matches the directory name
	// and the `name` field in manifest.yaml).
	Name string

	// Version is the simple string version (Phase 1: equality compared
	// against the installed package vertex's manifest aspect).
	Version string

	// Description is a one-line human-facing summary mirroring the
	// manifest field.
	Description string

	// Depends lists prerequisite package names. The installer logs a
	// warning and proceeds when a dependency is not verified.
	Depends []string

	// DDLs lists the DDL meta-vertices this package declares.
	DDLs []DDLSpec

	// Lenses lists the Lens meta-vertices this package declares.
	Lenses []LensSpec

	// Permissions lists the permission vertices + grants this package
	// declares.
	Permissions []PermissionSpec

	// Roles lists the user-facing roles this package declares. They are
	// created in the SAME install batch as everything else (Story 1.5.5 —
	// no substrate-direct PreInstall), with deterministic NanoIDs, and are
	// captured in the manifest's declaredKeys so uninstall reclaims them
	// (closes F-001 orphans). A package's own Permissions may reference a
	// declared role by canonical name in GrantsTo; the installer resolves
	// it to the role's deterministic NanoID.
	Roles []RoleSpec

	// RetentionClasses lists the retention-class key holders this package
	// declares (retention-class-key-custody-design.md §3.1). Each mints a
	// vtx.retentionclass.<NanoID> root + a `.retentionPolicy` aspect on a
	// deterministic, version-independent NanoID, and is nameable by this same
	// package's aspect-type DDLs through Custody.RetentionClass. A class is a
	// data controller's own declaration, so only the declaring package may
	// name it.
	RetentionClasses []RetentionClassSpec

	// WeaverTargets lists the meta.weaverTarget meta-vertices this package
	// declares. Each binds a violation Lens's weaver-targets row prefix
	// (TargetID) to a gap → remediation playbook (Contract #10 §10.8). The
	// installer emits a vtx.meta.<NanoID> vertex + a `.spec` aspect the
	// Weaver registry CDC source loads.
	WeaverTargets []WeaverTargetSpec

	// LoomPatterns lists the meta.loomPattern meta-vertices this package
	// declares (Contract #10 §10.5). Each is a linear orchestration flow over
	// one subjectType. The installer emits a vtx.meta.<NanoID> vertex + a
	// `.spec` aspect the Loom pattern source CDC-loads.
	LoomPatterns []LoomPatternSpec

	// OpMetas lists the op-meta vertices this package declares. Each carries a
	// single OperationType on the vertex `data`, making that op discoverable
	// by both engines' op-meta index — the contract that lets a Weaver
	// `assignTask`/Loom `userTask` step resolve `forOperation` to the op's
	// meta-vertex. A package declaring an op as the target of `assignTask`
	// (or a `userTask` step) must declare a matching OpMetaSpec.
	OpMetas []OpMetaSpec

	// Panes lists the server-pane descriptors this package declares
	// (facet-discovery-restoration-design.md §2.1). Each is a meta.pane
	// meta-vertex carrying a `.paneDescriptor` aspect — the data-driven
	// replacement for hardcoding a Protected read-model pane (its tables,
	// columns, and dispatch targets) into an edge client's host. The installer
	// emits the vertex, the aspect, and one `pane offeredTo role` link per
	// OfferedToRoles entry; an identity-anchored lens walks
	// holdsRole → offeredTo to deliver the descriptor to exactly the
	// identities whose grant topology earns the pane.
	Panes []PaneSpec

	// ReadGrantDomains declares the cap-read producer slices this package owns
	// (Contract #6 §6.14 `cap-read.<domain>.<actorSuffix>`). Every Personal
	// lens's Walks[i].GrantDomain must name one, and every declared domain
	// must be named by at least one walk. ExpandReadGrantWalks generates
	// exactly one actorAggregate producer lens per domain — a Path-B producer
	// is compiled from the walks it grants, never hand-authored.
	ReadGrantDomains []ReadGrantDomainSpec

	// RetireCancelsOpenTasks lists operationTypes whose op-meta this package
	// VERSION drops, with the disposition "cancel every open task that still
	// references it" (opmeta-retirement-open-task-guard-design.md §2).
	// Upgrade/Apply refuse a version that tombstones an op-meta whose
	// operationType is declared in neither this nor MovedOps — even when this
	// environment happens to hold zero open referents right now, since the
	// declaration is authorship-time policy, not an apply-time convenience.
	// A declared operationType that survives the version (not actually
	// dropped) is inert.
	RetireCancelsOpenTasks []string

	// MovedOps declares operationType -> destination package name for an
	// op-meta this version drops in favor of a successor elsewhere, reserving
	// the vocabulary so the declaration surface does not churn when
	// work-preserving moves land. Not implemented: Upgrade/Apply always
	// refuse a dropped operationType found here today ("work-preserving
	// moves are not yet supported — cancel or wait"); see the design doc §3.
	MovedOps map[string]string

	// RetiredSecureColumns declares, per lens, that this package VERSION
	// deliberately stops carrying a Secure-Lens column's key-custody history
	// (retention-class-key-custody-design.md §30). Upgrade/Apply refuse any
	// edit that erases a committed `targetConfig.secureColumns` entry — the
	// column dropped from a lens the package still declares, or the lens
	// itself removed (or renamed, which mints a new key and tombstones the old
	// one) with its secure columns still standing — unless an entry here names
	// it.
	//
	// The declaration is an author's ATTESTATION, exactly as
	// RetireCancelsOpenTasks is: the platform does not verify that the
	// ciphertext those holder types encrypted has been swept, re-keyed, or
	// destroyed. What it does guarantee is that a package cannot silently
	// blind the destruction-readiness oracle, which answers "which lenses hold
	// ciphertext for this holder type?" from the CURRENT spec alone
	// (internal/refractor/health/registry_probe.go): a spec that has forgotten
	// a column attests coverage over rows it can no longer see.
	//
	// Every entry is checked whether or not it matches a real erasure: a
	// missing Lens or Note fails the upgrade on its own, and a duplicate
	// (Lens, Column) pair is refused. An entry that matches no erasure applies
	// to nothing and is reported back as unused — a retirement that has
	// outlived the edit it was written for is the shape that would otherwise
	// sit in a package file looking load-bearing.
	RetiredSecureColumns []RetiredSecureColumn

	// readGrantWalksExpanded marks a Definition that already ran through
	// ExpandReadGrantWalks and is what makes the pass idempotent. A composed
	// lens still carries the Walks it was compiled from (only its Spec is
	// rewritten), so without this flag a second pass would prepend the head and
	// chain again and append a duplicate producer. The flag travels with the
	// value, so the composed Definition must be the one every downstream step
	// uses — assembling a fresh Definition from a composed one's Lenses loses it.
	readGrantWalksExpanded bool
}

// WeaverTargetSpec is one meta.weaverTarget meta-vertex a package declares
// (Contract #10 §10.8). The installer emits its body so the Weaver registry
// deserializes it into a runtime Target.
type WeaverTargetSpec struct {
	// TargetID is the weaver-targets row prefix (the <targetId>.<entityId>
	// key) and a durable-name segment, so it must be a single KV-key token.
	TargetID string

	// LensRef names the violation Lens whose rows this target dispatches over.
	// A package author writes the lens's CanonicalName; the installer resolves
	// it to that lens's in-batch NanoID (a literal NanoID is passed through to
	// support a lens in an already-installed package). It is surfaced on
	// Weaver's control API; lane-1 dispatch watches weaver-targets directly,
	// not via LensRef.
	LensRef string

	// Gaps maps each `missing_<gap>` violation column to the remediation
	// action the engine runs when that column is set.
	Gaps map[string]GapActionSpec

	// Description is the target's optional operator- and AI-facing prose: the
	// invariant this target keeps true, in the domain's own nouns, and what it
	// does when a candidate violates it. It is emitted as a SIBLING
	// `vtx.meta.<id>.description` aspect (the §10.8 spec body carries no prose
	// field), so the Weaver registry — which reads only `spec`/`effects` —
	// never sees it. Empty emits no aspect at all.
	Description string

	// Augur is the optional, default-absent AI-reasoning escalation policy
	// (Contract #10 §10.8 "Augur escalation"). When set, the installer emits it
	// into the meta.weaverTarget body so the Weaver registry parses it into a
	// runtime AugurPolicy; nil emits no `augur` key (the frozen-contract shape).
	Augur *AugurSpec

	// Mode selects the planner-extension posture (Contract #10 §10.8 Planner
	// extension, mirrors the engine's Target.Mode): "" (the default — omitted
	// from the emitted body) is frozen table-only behavior, byte-identical to
	// every target installed before the planner mandate; "shadow" computes the
	// planner's pick per gap but never dispatches it; "planned" dispatches the
	// planner's pick for real (a gap needs Goal + Actions, or Candidates, to
	// have anything for the planner to pick from).
	Mode string

	// Admission is the optional Fire-8 dispatch-pacing policy (Contract #10
	// §10.8 "Admission control", mirrors the engine's Target.Admission): nil
	// (the default) omits the `admission` key from the emitted body entirely,
	// so the installed target dispatches unbounded, with no pacing applied;
	// a declared policy paces WHEN an already-resolved gap fires, never
	// gating correctness (the §10.3 mark CAS-create remains the sole
	// anti-storm/idempotency guard).
	Admission *AdmissionSpec
}

// AdmissionSpec mirrors the engine's AdmissionPolicy (internal/weaver/admission.go,
// Contract #10 §10.8 "Admission control") field-for-field so the emitted body
// deserializes cleanly into the runtime policy.
type AdmissionSpec struct {
	// GlobalRate bounds the target's TOTAL dispatch rate (tokens/sec, burst
	// capacity == the rate). 0/absent = unbounded on this axis.
	GlobalRate float64
	// AdapterRates bounds the dispatch rate for gaps whose resolved action
	// declares a matching GapActionSpec.Adapter — a rate here takes precedence
	// over GlobalRate for a gap declaring that adapter. A gap with no declared
	// Adapter, or one absent from this map, is governed by GlobalRate alone.
	AdapterRates map[string]float64
}

// AugurSpec mirrors the engine's AugurPolicy (Contract #10 §10.8 "Augur
// escalation") so the emitted body deserializes cleanly. Escalate lists the
// stuck-gap triggers redirected to AI reasoning; Op/Adapter/ReplyOp are optional
// overrides naming the reasoning op / bridge adapter / replyOp Weaver dispatches
// directly as a directOp (Option F — no Loom pattern; they default to
// CreateAugurReasoningClaim / augur / RecordProposal at dispatch when omitted);
// Model is an optional adapter override. AutoApply is DESIGNED, not enabled.
type AugurSpec struct {
	Escalate  []string
	Op        string
	Adapter   string
	ReplyOp   string
	Model     string
	AutoApply *AugurAutoApplySpec
}

// AugurAutoApplySpec mirrors the engine's AugurAutoApply (Contract #10 §10.8):
// the OPTIONAL auto-apply allow-list. Validated fail-closed at install, but no
// escalation path consumes it until Andrew ratifies the autonomy boundary.
type AugurAutoApplySpec struct {
	Actions       []string
	MinConfidence float64
}

// GapActionSpec mirrors the engine's GapAction (Contract #10 §10.8 action
// table) field-for-field so the emitted body deserializes cleanly into the
// runtime target. Action selects the contract; the remaining fields carry the
// per-action params, each a literal or a `row.<column>` template token.
// `Pattern` (triggerLoom) and `Operation` (assignTask/directOp) are
// shipped verbatim and resolve live in the engine registry — the installer
// does not rewrite them to NanoIDs.
type GapActionSpec struct {
	Action    string
	Pattern   string
	Subject   string
	Adapter   string
	Operation string
	Assignee  string
	Target    string
	Params    map[string]string
	// Class pins the dispatched op's DDL canonical name (Contract #2 §2.1
	// operationType→class reverse index). Required whenever Operation is
	// admitted by more than one installed vertexType DDL — the Processor's
	// reverse index deliberately excludes an ambiguous operationType rather
	// than guess, so an unpinned directOp against it fails closed
	// (MissingClass) forever. A literal DDL CanonicalName, never a
	// row.<column> template — the author, not the row, knows which DDL a
	// directOp targets.
	Class string
	// Reads are the dispatched op's ContextHint.Reads — the bare vertex keys
	// its DDL hydrates + validates. Each is a literal or a row.<column> template
	// resolved from the violation row (e.g. `row.entityKey` to hand a directOp
	// the candidate vertex it must read). Used by directOp; the candidate id is
	// already in the target lens row, so this just routes it into the op's reads.
	Reads []string
	// Enumerations are the dispatched op's ContextHint.Enumerations — the
	// Contract #2 §2.5 class-(e) kv.Links walks its script runs, declared onto
	// the envelope as metadata. Used by directOp. Each Hub is a literal or a
	// row.<column> template resolved from the violation row exactly like a
	// Reads entry; Relation/Direction are literals. Declaring a walk does not
	// change how the script runs it (bounded, paged, live) — it puts the walk
	// on the envelope rather than leaving it knowable only by reading the
	// script.
	Enumerations []EnumerationSpec
	// IssueCode/IssueSeverity are consulted only when Action == "surface" (FR29's
	// "surface, never dispatch" gap, Contract #10 §10.8) — the Health-KV issue
	// code/severity raised while the gap is open, cleared on close. No op is
	// ever dispatched for this action. IssueSeverity defaults to "warning" when
	// omitted.
	IssueCode     string
	IssueSeverity string

	// Goal is the Fire-6 goal-regression synthesis target (Contract #10 §10.8
	// Planner extension, the loftspace-lease-renewal-goal-authored-target-design
	// R1): a §10.5 guard-grammar predicate over the gap's row (goalColumns-
	// bridged aspect facts included) the planner searches Actions to satisfy.
	// Required alongside Actions in both directions — install rejects a goal
	// with an empty catalog, and a catalog with no goal to synthesize toward.
	// Mutually exclusive in practice with Action/Candidates (a target picks one
	// remediation shape per gap), though the installer does not enforce that —
	// the engine's dispatch order (explicit Action wins, then goal) makes a
	// combination merely redundant, not unsafe.
	Goal json.RawMessage
	// GoalColumns bridges an ASPECT-qualified fact Goal addresses (e.g.
	// `subject.signature.data.signedAt`) to the lens's flattened row column
	// name — a §10.2 row has no aspect tags, so without this map Goal could
	// never see an Effect's aspect path as satisfied. Map key = the lens
	// BodyColumn name; value = its guard-grammar path string
	// ("subject.<aspect>.data.<field>"). A root-shaped column needs no entry
	// (it already addresses subject.data.<column> by default).
	GoalColumns map[string]string
	// Actions is the gap's planning catalog — a per-gap, package-authored set
	// of dispatchable actions (the same action-contract shape as GapActionSpec)
	// each coupled with the planner-facing Pre/Effects/Cost triple. The
	// installer requires every Pre/Effects path to be row-reachable (a root
	// column, or an aspect path this gap's GoalColumns bridges) so no entry is
	// permanently ineligible or un-satisfiable.
	Actions []ActionCatalogEntrySpec
}

// EnumerationSpec is one declared kv.Links link-enumeration (Contract #2 §2.5
// — `contextHint.enumerations`): the hub vertex the walk starts from, the link
// relation walked, and the direction the hub sits in the link ("out" = hub is
// the link source, "in" = hub is the target). Relation and Direction are always
// literals; Hub's template grammar belongs to the surface carrying it — a gap's
// enumeration resolves it against the violation row (`row.<column>`), a loom
// step's against the instance subject (`subject`, `subject.<aspect>`), each the
// same grammar that surface's Reads use.
type EnumerationSpec struct {
	Hub       string `json:"hub"`
	Relation  string `json:"relation"`
	Direction string `json:"direction"`
}

// ActionCatalogEntrySpec mirrors the engine's ActionCatalogEntry (Contract #10
// §10.8 Planner extension, R1) field-for-field: one entry in a goal gap's
// Actions catalog. Ref identifies the entry for the synthesized plan's steps
// and the canonical tie-break (cost ascending, then Ref lexicographically);
// the Action/Pattern/.../Reads fields are the same dispatch-binding shape as
// GapActionSpec. Pre optionally gates this entry's eligibility in the search;
// Effects are the atoms it entails once dispatched (required — an entry with
// nothing it entails can never advance a plan; each must be a concrete
// present/absent/equals assertion, never anyOf/not); Cost ranks the search
// (ascending, ties break on Ref; omitted/zero defaults to 1 at the engine).
type ActionCatalogEntrySpec struct {
	Ref       string
	Action    string
	Pattern   string
	Subject   string
	Adapter   string
	Operation string
	Assignee  string
	Target    string
	Params    map[string]string
	Reads     []string
	// Enumerations are the entry's declared kv.Links walks, same grammar and
	// same purpose as GapActionSpec.Enumerations: a chosen entry dispatches
	// through the engine's ordinary action contract, so a walk it cannot
	// declare here is a walk silently dropped from the envelope.
	Enumerations []EnumerationSpec
	Pre          json.RawMessage
	Effects      []json.RawMessage
	Cost         int
}

// LoomPatternSpec is one meta.loomPattern meta-vertex a package declares
// (Contract #10 §10.5). The installer emits its body so the Loom pattern
// source deserializes it into a runtime Pattern.
type LoomPatternSpec struct {
	// PatternID is the pattern's canonical id; a playbook references a pattern
	// by this string (resolved live at dispatch).
	PatternID string

	// SubjectType is the vertex type an instance of this pattern runs for.
	SubjectType string

	// CompletionDomains is the explicit set of event domains the engine
	// reconciles completion consumers for. Empty defaults to {SubjectType}.
	CompletionDomains []string

	// Steps is the linear list of pattern steps.
	Steps []StepSpec
}

// StepSpec is one entry in a pattern's linear step list (Contract #10 §10.5).
// systemOp/userTask carry `{kind, operation, guard?}`; externalTask carries
// `{kind, adapter, params, replyOp, instanceOp, guard?}` and leaves Operation
// unused.
type StepSpec struct {
	// Kind is `systemOp` (submit the bound op directly), `userTask` (CreateTask
	// and wait for the user to perform the bound op), or `externalTask` (submit
	// the instanceOp and wait for the bridge's replyOp).
	Kind string

	// Operation names the bound op for a systemOp/userTask step (unused by
	// externalTask).
	Operation string

	// Guard is the §10.5 declarative predicate the step is gated on. Carried
	// as a Go map so authors write a map literal; marshaled into the step's
	// `guard` field and omitted when nil.
	Guard map[string]any

	// Adapter is the external adapter name an externalTask dispatches to
	// (required for externalTask, unused otherwise).
	Adapter string

	// Params are an externalTask's adapter parameters — author-friendly map,
	// emitted into the step's `params` field and omitted when nil. Opaque to the
	// engine (passed through to the instanceOp payload).
	Params map[string]any

	// ReplyOp is the result-op type the bridge posts back for an externalTask
	// (required for externalTask, unused otherwise).
	ReplyOp string

	// InstanceOp is the op an externalTask step submits — its DDL mints the claim
	// vertex and emits the external.<adapter> event (required for externalTask,
	// unused otherwise).
	InstanceOp string

	// Reads and OptionalReads are the Contract #2 §2.5 declared read-sets a
	// systemOp step's bound op needs hydrated, written as templates against the
	// instance subject: the bare token `subject`, or `subject.<aspect>` for one
	// of its aspects. The engine resolves each against the instance's subjectKey
	// at submit time — a pattern author never writes a concrete key, because the
	// subject is not known until an instance runs.
	//
	// systemOp-only. A userTask's read-set is derived by the engine from the
	// CreateTask invariant, and an externalTask's from its declared params, so a
	// declared set on either kind rejects the whole pattern.
	Reads         []string
	OptionalReads []string

	// Enumerations are the Contract #2 §2.5 class-(e) kv.Links walks a systemOp
	// step's bound op runs, declared onto the envelope as metadata. Each Hub is
	// a subject-relative template resolved against the instance's subjectKey at
	// submit time, exactly like a Reads entry; Relation/Direction are literals.
	//
	// systemOp-only, on the same grounds as Reads: a userTask's and an
	// externalTask's op are engine-chosen, so the engine — not the pattern —
	// knows what they walk.
	Enumerations []EnumerationSpec
}

// OpMetaSpec is one op-meta vertex a package declares so an op is discoverable
// by `forOperation` resolution. The installer emits a vtx.meta.<NanoID> vertex
// carrying `data.operationType`; both engines index it identically.
//
// A future ergonomic could auto-emit one of these per DDL PermittedCommand so
// authors never hand-list them; the explicit field keeps the author in control
// of exactly which ops are resolvable.
//
// Presentation/InputSchema/FieldDescriptions/Dispatch/Sensitive (edge-manifest
// Fire 1, edge-showcase-app-design.md §3.3) are the descriptor vocabulary: an
// edge client renders a form + submits an op from these fields alone, with no
// hardcoded per-op knowledge. All five are optional — an op meta that omits
// them installs only the bare `vtx.meta.<NanoID>` vertex, with no descriptor
// aspects attached; an op meta that supplies none of them still resolves
// normally, it just isn't Facet-renderable (edge-showcase-app-design.md
// §3.3: "ops without descriptors still render, degraded").
type OpMetaSpec struct {
	// OperationType is the op this vertex makes `forOperation`-resolvable.
	OperationType string

	// Presentation is the client-facing display metadata for this op
	// (title/icon/tone/etc). Nil emits no `.presentation` aspect.
	Presentation *OpPresentationSpec

	// InputSchema is a per-op JSON Schema string for this op's payload —
	// finer-grained than the owning DDL's merged InputSchema (which today
	// unions every PermittedCommand's fields with `"required":[]`, unusable
	// for driving a single-op form). Empty emits no `.inputSchema` aspect.
	InputSchema string

	// FieldDescriptions maps InputSchema field names to help text. Empty
	// emits no `.fieldDescriptions` aspect.
	FieldDescriptions map[string]string

	// Dispatch is the machine-readable submission recipe (class/authContext/
	// contextParams/reads) a client uses to author a Contract #2 envelope
	// from this op meta alone. Nil emits no `.dispatch` aspect.
	Dispatch *OpDispatchSpec

	// Sensitive marks this op's payload as carrying fields the sensitive-
	// param-egress mechanism must guard (masked entry, no local echo).
	// Defaults false; emits `.sensitive` only when true (mirrors DDLSpec's
	// own Sensitive field).
	Sensitive bool

	// Ceremony declares the one thing a descriptor-driven client must DO
	// rather than ask — mint a secret, submit only its hash, reveal the
	// plaintext once. Nil emits no `.ceremony` aspect.
	Ceremony *OpCeremonySpec
}

// OpCeremonySpec declares a mint-and-reveal ceremony
// (client-ceremony-op-descriptors-design.md §4.3): the client mints a secret
// the platform must never learn, submits only its hash, and shows the
// plaintext to the person exactly once.
//
// It exists because a form cannot express this. The alternative a descriptor
// without it degrades into is a text input asking a human to type a 64-char
// hash whose preimage nobody holds — an accepted submission that arms a
// secret no one can ever present.
//
// The client contract is three rules, all fail-closed. A client that does not
// implement ceremonies MUST NOT offer an op whose descriptor carries one — it
// degrades exactly as it degrades an unresolvable TargetType, and never falls
// back to rendering the hash field. MintedSecretHashField is removed from the
// rendered form and filled by the client from the platform CSPRNG. The
// plaintext is displayed once on acceptance and dropped without display on
// any other outcome: a secret for a write that did not land is not a secret
// anybody should be handed.
type OpCeremonySpec struct {
	// MintedSecretHashField names the InputSchema field carrying the
	// lowercase-hex sha256 of a client-minted 256-bit secret.
	MintedSecretHashField string

	// RevealTitle and RevealHelp are the copy for the one-time
	// post-acceptance display of the plaintext.
	RevealTitle string
	RevealHelp  string
}

// OpPresentationSpec is an op meta's client-facing display metadata
// (edge-showcase-app-design.md §3.3). Icons/tones are semantic tokens from a
// small fixed set the client interprets; the client owns all pixels.
type OpPresentationSpec struct {
	Title       string
	ShortLabel  string
	Description string
	Icon        string
	Tone        string // "primary" | "neutral" | "destructive"
	SubmitLabel string
	Group       string
}

// OpDispatchSpec is an op meta's machine-readable submission recipe
// (edge-showcase-app-design.md §3.3 — "the machine-readable version of the
// loftspace COMPLETIONS registry"). A client builds a Contract #2 envelope
// from this alone: OperationType + Class from Dispatch, AuthContext selects
// which of the wire envelope's `self|service|task` fields the client
// populates (`self` -> {target: actorId}; `service` -> {service: serviceKey};
// `task` -> {task, target: scopedTo}) — NOT itself a processor.AuthContext
// value.
//
// ContextParams, Reads, and OptionalReads are all template strings, but they
// draw from two different vocabularies, and ValidateOpDispatchTemplates is
// the install-time gate that holds Reads/OptionalReads to the narrower one:
//
//   - ContextParams is the full vocabulary: `{actor}`, `{scopedTo}`,
//     `{service}`, `{payload.<field>}`, `{me.<type>}`, and
//     `{entity.<column>}` — the last naming a column off the row the client
//     resolved TargetType from (e.g. `{entity.studioKey}`) — each optionally
//     suffixed `:id`, and any of them may also close with the `?` OPTIONAL
//     marker described below.
//   - Reads and OptionalReads draw from a CLOSED subset of that vocabulary:
//     `{actor}`, `{scopedTo}`, `{service}`, `{payload.<field>}`, and
//     `{me.<type>}`, each optionally suffixed `:id` — nothing else.
//     `{entity.<column>}` and the `?` marker are ContextParams-only: a read
//     template is resolved server-side by the Processor's descriptor floor,
//     whose client wholeKey silently drops either one, so an entry using
//     them is dead weight the gate refuses rather than ships. `{me.<type>}`
//     in a read template carries two further gated rules: it is
//     OptionalReads-only (a required-side `{me.<type>}` cannot resolve
//     server-side and would force a required PATTERN that blankets a whole
//     key class out of demotion — descriptor-floor-template-coverage-design.md
//     §3.2), and it must occupy a WHOLE dot-delimited segment of the
//     template, because the Processor's matcher only wildcards whole
//     segments: a mid-segment client-only fragment (`bkr{me.instructor:id}`)
//     is refused at install rather than silently carrying no floor. An
//     unrecognized placeholder in either read list is refused the same way —
//     the read-template vocabulary is closed by default-deny, not
//     open-ended.
//
// `{me.<type>}` is the submitting identity's own vertex of that Contract #1
// type, taken from the `selfAnchors` set the client's identity projection
// carries (edge-manifest's edgeIdentity lens projects each as {type, key}).
// It is how a self-scope entity-key param is declared rather than
// asked of the visitor as a raw vertex key: the client fills it and never
// renders the field. It resolves only when the identity holds exactly one
// vertex of that type — zero or several is not a value to guess at, and a
// client that cannot resolve a declared `{me.<type>}` has no business
// offering the op (the same rule TargetType states below).
//
// A ContextParams placeholder may also close with a `?` OPTIONAL marker
// ({me.leaseapp?}, {me.leaseapp:id?}): the client fills the param silently
// when it resolves and OMITS the param silently when it doesn't — the field
// is never rendered and the op stays offered either way. It exists for
// rate/eligibility params whose ABSENCE is a designed script branch; a
// required key never carries it. Reads/OptionalReads have no marker
// equivalent — filing a key under OptionalReads is that vocabulary's own
// way of saying its absence is expected.
//
// Any placeholder accepts a trailing `:id` modifier, which substitutes the
// Contract #1 BARE id rather than the full `vtx.<type>.<id>` key. That is what
// makes a LINK key expressible as a read declaration — a 6-segment
// `lnk.<typeA>.<idA>.<relation>.<typeB>.<idB>` is built from bare ids, so
// without `:id` an ownership link could not be declared at all and the script
// would be left doing an undeclared live read. Unlike a vertex/aspect
// template, a placeholder carrying `:id` is a key FRAGMENT: it may appear
// mid-entry (e.g.
// `lnk.leaseapp.{payload.leaseAppKey:id}.applicationFor.identity.{actor:id}`),
// which is exactly why the AI-authored validator's stricter anchored
// vocabulary rejects that shape — see sensitiveReadAspect. In a read
// template a mid-entry fragment is legal only when the embedded placeholder
// is server-resolvable (`{payload.*}`, `{actor}`, `{service}`, or a
// validated `{scopedTo}`); a client-only `{me.<type>}` fragment is exactly
// the shape the whole-segment rule above refuses.
type OpDispatchSpec struct {
	Class string

	// ClassChoices names the DDLs this op is legitimately declared on when no
	// single Class pins the dispatched op's DDL — a client must let the caller
	// pick one of these canonical names and submit it as the envelope's class
	// field. Mutually exclusive with Class: an op sets exactly one of the two.
	ClassChoices []string

	// AuthContext is "self" | "service" | "task" | "standing".
	//
	// The first three name which of the wire envelope's authContext fields the
	// client populates. "standing" names the fourth case: populate NONE of them
	// and send no authContext object at all, because the caller's authority is a
	// standing role grant (cap.roles) rather than a relationship to the target.
	// That is how every operator / staff FE has always submitted; naming it lets
	// a data-driven client render and dispatch such an op from the descriptor
	// alone, instead of having to special-case the absence of an authContext.
	AuthContext string
	TargetField string

	// TargetType is the Contract #1 vertex type TargetField's value must
	// hold ("session" for a field named `session`, "appointment" for one
	// named `appointmentKey`). It is what lets a client resolve the field
	// from context by TYPE — the entity in view, the task's scopedTo
	// target, the service — rather than inferring its source from
	// AuthContext. The two answer different questions: AuthContext selects
	// which wire-envelope field the client populates, TargetType says where
	// TargetField's own value comes from. A client that cannot resolve a
	// declared TargetType from its context has no business offering the op.
	// Empty leaves the client its AuthContext-keyed fallback.
	TargetType string

	ContextParams map[string]string
	Reads         []string

	// OptionalReads are the dispatched op's ContextHint.OptionalReads — the
	// absence-tolerant half of Contract #2 §2.5's declared read posture, and
	// the only way a descriptor-driven client can express a class-(d)
	// read-before-create/dedup or a fail-closed ownership probe. Same template
	// vocabulary as Reads (including the `:id` modifier and `{me.<type>}`), so
	// a composite key whose absence is the NORMAL case — a per-entity
	// uniqueness guard whose prior claim was released, an ownership link that
	// simply may not exist for this caller — is declarable rather than left to
	// a live undeclared read.
	//
	// The split is semantic, not stylistic, and mis-filing it breaks the op in
	// opposite directions: a key the script REQUIRES belongs in Reads (absence
	// is a correctness error), while a key whose absence the script branches on
	// belongs here — declaring such a key as a required Read fails the whole
	// submission the first time it is legitimately absent.
	OptionalReads []string

	// VisibleWhen gates OFFERING this op against the state of the target row
	// the client resolved TargetType from: the op is offered only when the
	// row's Field column equals Equals. It exists for state-machine op pairs
	// (pause/resume, open/settle) where offering both halves at once forces
	// the visitor to know the state the row already carries. Visibility only —
	// the script remains the enforcer, exactly as it is for every descriptor
	// affordance. Nil offers the op unconditionally, and a client evaluating
	// VisibleWhen against a row that LACKS the named column must treat the
	// condition as unmet (fail-closed: no state, no offer).
	VisibleWhen *OpVisibleWhenSpec
}

// OpVisibleWhenSpec is OpDispatchSpec.VisibleWhen's single-condition form:
// offer the op iff the resolved target row's Field column strictly equals
// Equals (JSON-value equality — bool, string, or number).
type OpVisibleWhenSpec struct {
	Field  string
	Equals any
}

// PaneSpec is one server-pane descriptor. The pane's SECTIONS — which
// Protected read-model table each reads, which columns it projects (a PHI
// decision made here, in reviewable package data, never in an app), how rows
// filter/order, and which column is a dispatch target of which vertex type —
// travel as a JSON string exactly as OpMetaSpec.InputSchema does, and the
// client + host both consume them from the projected `manifest.pane.*` row.
// The host's generic pane executor validates every identifier against its own
// strict grammar before compiling SQL; a descriptor is package data with the
// same trust shape as the lens DDL that projects the table itself.
type PaneSpec struct {
	// CanonicalName is the pane's stable id (e.g. "staffWorklist") — the
	// value clients pass back to the host's pane executor.
	CanonicalName string

	// OfferedToRoles lists role canonical names (resolved to role NanoIDs at
	// install exactly as PermissionSpec.GrantsTo is); one
	// `pane offeredTo role` link is emitted per entry.
	OfferedToRoles []string

	// Title / Icon are the pane's client-facing presentation (icon is a
	// semantic token, same vocabulary as OpPresentationSpec.Icon).
	Title string
	Icon  string

	// Surface names WHICH screen of a client the pane belongs on. A renderer
	// names no table, no column and no operation; where a pane lives is the
	// same class of fact, and declaring it is what lets a pane be offered to
	// an audience that has no work surface at all.
	//
	// "" and "work" are the work surface — the staff screen whose visibility
	// derives from a workplace anchor. "account" is the signed-in identity's
	// own settings screen, which every claimed identity has. A client that
	// does not implement a named surface renders the pane NOWHERE; it never
	// falls back to another screen, because a pane placed by guess is a
	// Protected read drawn somewhere its reader never asked for it.
	Surface string

	// Sections is the pane's section-descriptor array as a JSON string.
	Sections string
}

// The surfaces a PaneSpec may name. The set is closed and validated at
// install: an unknown value would install a descriptor every client draws
// nowhere, which reads on the board as "the pane shipped" and to its audience
// as "the pane is missing."
const (
	// PaneSurfaceWork is the staff work screen, whose own visibility derives
	// from a workplace anchor. It is the zero value, so every pane predating
	// this vocabulary keeps its placement.
	PaneSurfaceWork = "work"

	// PaneSurfaceAccount is the signed-in identity's own settings screen,
	// which every claimed identity has regardless of what it holds.
	PaneSurfaceAccount = "account"
)

// IsPaneSurface reports whether s names a known surface. The empty string is
// PaneSurfaceWork.
func IsPaneSurface(s string) bool {
	return s == "" || s == PaneSurfaceWork || s == PaneSurfaceAccount
}

// RoleSpec is one user-facing role a package declares. The installer
// creates a role vertex (`vtx.role.<id>`), its canonicalName +
// description aspects, and a canonical-name index vertex
// (`vtx.roleindex.<sha256(canonical)>` → roleId) — all in the install
// batch with deterministic NanoIDs.
type RoleSpec struct {
	// CanonicalName is the role's canonical name (e.g. "consumer").
	CanonicalName string

	// Description is the role's plain-language description aspect.
	Description string
}

// DDLSpec is one DDL meta-vertex declaration.
type DDLSpec struct {
	// CanonicalName is the DDL's canonical name (used by the Processor's
	// DDL cache for class lookup).
	CanonicalName string

	// Class is the meta-vertex class — typically `meta.ddl.vertexType`.
	Class string

	// PermittedCommands is the list of operationTypes the DDL admits.
	// The Starlark script in Script handles each of these.
	PermittedCommands []string

	// Description is a plain-language description aspect.
	Description string

	// Script is the Starlark source. Each permittedCommand should have
	// a branch; the runner returns ScriptError for unrecognized ops.
	Script string

	// Sensitive marks an aspect-type DDL as carrying sensitive data
	// (lattice-architecture Item 6 — the aspect-level sensitivity
	// boundary). The Processor's step-6 validator anchors sensitive
	// aspects to identity vertices (NFR-S3). Meaningful only for
	// Class == "meta.ddl.aspectType" — validateSensitiveClassScope rejects
	// the install otherwise; defaults false (non-sensitive), so a DDL that
	// omits it installs exactly as before (no `.sensitive` aspect emitted).
	Sensitive bool

	// Custody declares WHICH key holder custodies this aspect's DEK
	// (retention-class-key-custody-design.md §3.2). The zero value means
	// custody kind `identity` — the aspect's own anchoring identity — and a
	// DDL that omits it emits no `.custody` aspect. Meaningful only alongside
	// Sensitive on an aspect-type DDL; validateCustodyScope rejects every
	// other combination.
	Custody CustodySpec

	// Abstract marks this DDL as a type that names no instance — usable only
	// as a lens pattern label or a subtypeOf ancestor (dynamic-type-taxonomy-
	// design.md §3.2), never as the class of a written document or a key's
	// type segment. Meaningful only for Class == "meta.ddl.vertexType" (the
	// same empty-Class default buildInstallBatch applies); mutually exclusive
	// with Script and PermittedCommands, which an abstract type declares
	// neither of. validateAbstractDDLScope rejects every other combination.
	// Defaults false, the ordinary case for every DDL that does not declare it.
	Abstract bool

	// SubtypeOfRef names, by canonicalName, the abstract type this DDL is a
	// subtype of (§3.3/§3.5). The installer resolves it — batch-local first,
	// then against an already-installed abstract meta-vertex — and emits the
	// `subtypeOf` link (leaf → abstract, Contract #1 §1.1) into the same
	// atomic batch. Resolution fails the install closed when the name does
	// not resolve, resolves to a non-abstract type, or resolves to a
	// tombstoned meta-vertex. Meaningful only alongside Class ==
	// "meta.ddl.vertexType"; empty declares no taxonomy membership.
	SubtypeOfRef string

	// LeafBudget bounds how many concrete subtypeOf leaves this abstract type
	// is expected to accept before a dependent lens's narrowed-filter label
	// cap is at risk (§10.2). Meaningful only when Abstract is true; zero
	// defaults to 8 (maxNarrowedFilterLabels) at the point a consumer reads
	// it. Exceeding it is a WARNING surfaced on InstallResult, never an
	// install rejection — rejecting a leaf install would let one package's
	// lens narrowing veto another package's type declaration.
	LeafBudget int

	// Self-description aspects. Required for all DDL classes.

	// InputSchema is the JSON Schema string for this DDL's operation payload.
	InputSchema string

	// OutputSchema is the JSON Schema string for this DDL's operation response.
	OutputSchema string

	// FieldDescription maps payload field paths to plain-language descriptions.
	FieldDescription map[string]string

	// Examples is an ordered list of named usage examples for this DDL.
	Examples []ExampleSpec

	// Effects maps a PermittedCommands operationType to the §10.5 guard-grammar
	// predicates (Contract #10 §10.8 Planner extension, ratified 2026-07-04) its
	// commit entails on its target subject — declared self-description the
	// Weaver planner consumes for candidate ranking (Fire 5) and goal-regression
	// synthesis (Fire 6). Additive/optional: an operationType absent from
	// Effects declares none (still fully dispatchable via an explicit
	// action/candidates gap entry today; only unplannable via goal regression).
	// Install-time validated: every key must be one of this DDL's
	// PermittedCommands, and every guard must parse (same grammar as a Loom
	// step Guard, §10.5) — a malformed effect rejects the whole install.
	Effects map[string][]json.RawMessage
}

// Custody kinds (retention-class-key-custody-design.md §3.1). These are the
// DECLARED kind strings, which are camelCase; the retention-class holder's
// VERTEX TYPE segment is the all-lowercase `retentionclass`
// (RetentionClassVertexType) because a Contract #1 type segment is
// [a-z][a-z0-9]*. The two strings are deliberately different things.
const (
	// CustodyKindIdentity custodies the DEK on the aspect's own anchoring
	// identity. Policy erase-on-request; destroyed by ShredIdentityKey. This
	// is the default when a DDL declares no custody at all.
	CustodyKindIdentity = "identity"

	// CustodyKindRetentionClass custodies the DEK on a package-declared
	// retention-class holder. Policy erase-on-expiry; destroyed by
	// ShredRetentionClassKey on the controller's schedule, NOT on a data
	// subject's erasure request — which is the whole point: a record in a
	// retention class survives its subject's erasure, pseudonymized.
	CustodyKindRetentionClass = "retentionClass"

	// RetentionClassVertexType is the Contract #1 vertex type segment of a
	// retention-class holder: vtx.retentionclass.<NanoID>.
	RetentionClassVertexType = "retentionclass"

	// RetentionPolicyEraseOnExpiry is the only retention policy the
	// platform implements. The period is declarative — nothing expires a
	// class key automatically yet (design §7.2).
	RetentionPolicyEraseOnExpiry = "eraseOnExpiry"
)

// CustodySpec declares which key holder custodies a sensitive aspect's DEK.
// Custody is a function of the resolved aspect-type DDL and nothing else —
// never supplied by the caller, never discovered by graph traversal (design
// §3.3), so it is known at install time and reaches the Processor's commit
// path as an already-resolved field with no extra read.
type CustodySpec struct {
	// Kind is "" (== identity), CustodyKindIdentity, or
	// CustodyKindRetentionClass.
	Kind string

	// RetentionClass is the canonicalName of a RetentionClassSpec THIS SAME
	// package declares. Required iff Kind is CustodyKindRetentionClass, empty
	// otherwise. Cross-package references are refused: a retention class is a
	// data controller's own declaration, not a shared handle.
	RetentionClass string
}

// RetentionClassSpec declares a controller-owned retention class — the key
// holder for records whose retention obligation outlives any one data
// subject's erasure request (design §3.1). Each declared class mints a
// vtx.retentionclass.<NanoID> root plus a .retentionPolicy aspect at install,
// on the same deterministic-NanoID mechanism roles and lenses already use.
type RetentionClassSpec struct {
	// CanonicalName identifies the class within its declaring package, e.g.
	// "clinicalRecord". It is what a DDL's Custody.RetentionClass names.
	CanonicalName string

	// Description is what this class is and why it is retained — the place
	// the controller records the obligation the retention answers to.
	Description string

	// Policy is RetentionPolicyEraseOnExpiry, the only kind implemented.
	Policy string

	// RetentionPeriod is an ISO-8601 duration. DECLARATIVE: no automatic
	// expiry exists yet, so this states the controller's schedule rather than
	// arming a timer (design §7.2).
	RetentionPeriod string
}

// ExampleSpec is a single named usage example for a DDL operation.
type ExampleSpec struct {
	// Name is a short descriptive label for this example.
	Name string

	// Payload is the example operation payload sent by the client.
	Payload map[string]any

	// ExpectedOutcome is plain English describing what the platform does.
	ExpectedOutcome string
}

// LensSpec is one Lens meta-vertex declaration.
type LensSpec struct {
	// CanonicalName is the lens's canonical name (e.g. `duplicateCandidates`).
	CanonicalName string

	// Class is typically `meta.lens`.
	Class string

	// Spec is the cypher source for the lens body. Empty for an eventStream
	// lens (Source non-nil) — an event lens has no Core-KV vertex to MATCH;
	// the event payload is the only data. Also empty when SpecBranches is
	// set (a multi-walk Personal lens compiles to N independent queries
	// instead of one).
	Spec string

	// SpecBranches carries a multi-walk Personal lens's N independently-
	// compiled queries — one per Walks entry, each the walk's own OPTIONAL
	// MATCH chain plus the lens's shared tail
	// (refractor-shared-keyspace-arbitration-design.md §13.2). Refractor
	// evaluates each branch independently per actor and merges the row sets
	// by output key. Empty for every single-walk or walkless lens, which
	// keep using Spec exactly as before.
	SpecBranches []string

	// Source is the optional lens-source descriptor (the Chronicler's
	// `eventStream` primitive, orchestration-history-read-model-design.md
	// §2.2). Nil ⇒ {kind: "coreKv"} — every existing lens is byte-for-byte
	// unchanged, re-executing Spec's cypher over Core-KV CDC. Non-nil with
	// Kind "eventStream" sources a durable core-events subject instead: Spec
	// must be left empty.
	//
	// SourceConfig mirrors internal/chronicler.SourceConfig's JSON shape
	// (NOT the same Go type — pkgmgr must not import internal/chronicler;
	// it depends on internal/refractor/ruleengine/full, which a full-engine
	// test imports packages/orchestration-base from, so importing chronicler
	// here would cycle). Exactly the same mirror-by-JSON-shape convention
	// OutputDescriptorSpec below already uses.
	Source *SourceConfig

	// Adapter is the projection-output adapter — `"nats-kv"`, `"postgres"`,
	// or `"nats-subject"`.
	Adapter string

	// Bucket is the target NATS KV bucket name (nats-kv adapter only). The
	// Refractor's nats-kv adapter auto-creates-or-opens the bucket on first
	// projection. Must not be a reserved short alias (see validateLensBuckets).
	Bucket string

	// SubjectPrefix and Stream configure a "nats-subject" adapter — the
	// Personal Lens transport (personal-secure-lens-design.md), required
	// when Adapter is "nats-subject" and ignored otherwise. Mirrors the
	// Refractor-side lens.IntoConfig.SubjectPrefix/Stream fields (JSON shape
	// only, not the same Go type — see Source's doc comment above). IntoKey
	// doubles as the nats-subject targetConfig.key and must include the
	// reserved actor key field ("__actor", mirroring
	// internal/refractor/adapter.PersonalActorKeyField) regardless of
	// Personal.
	SubjectPrefix string

	// Stream is the JetStream stream backing a "nats-subject" adapter (e.g.
	// "SYNC"). Required when Adapter is "nats-subject".
	Stream string

	// Personal opts a "nats-subject" lens into the cross-vertex fan-out
	// (personal-secure-lens-design.md §3.3): Refractor re-executes the lens
	// cypher once per enumerated identity recipient instead of relying on
	// the cypher's own RETURN to supply the actor. nats-subject only.
	Personal bool

	// Walks declares a non-self-anchored Personal lens's actor→anchor
	// reachability, one entry per independent path. ExpandReadGrantWalks
	// compiles every entry into both this lens's reachability prefix and its
	// own grant domain's producer, so the walk the D1 gate's two enumerations
	// run cannot drift apart. Required (non-empty) on every non-self-anchored
	// Personal lens; empty on a self-anchored one (the platform base cap-read
	// self-grant covers the actor's own key) and on every non-Personal lens.
	// Every entry must resolve to the same AnchorType/AnchorVar (one lens
	// still projects one entity kind, Contract #6's one-RETURN-shape policy)
	// and bind no variable another entry in the same lens already bound (each
	// concatenates verbatim into one prelude, so a shared name would silently
	// join the paths).
	//
	// With Walks declared, Spec carries the presentation TAIL only — the head
	// and every entry's chain OPTIONAL MATCH clauses are compiled in front of
	// it, in declaration order.
	Walks []AnchorWalk

	// DSN is the Postgres connection string (postgres adapter only). A package
	// declares posture + columns, not a deployment connection string, so DSN may
	// be left empty: Refractor resolves it from REFRACTOR_PG_DSN at activation.
	DSN string

	// Table is the Postgres table name (postgres adapter only). A plain (non-
	// protected) table must already exist — the installer and Refractor never
	// issue DDL for it. A Protected table is provisioned from Columns at
	// activation; a GrantTable lens defaults the table to actor_read_grants.
	Table string

	// QueryTimeout is the per-query deadline for the postgres adapter, e.g.
	// "5s". Empty defaults to 30s in Refractor. Ignored by nats-kv.
	QueryTimeout string

	// Engine selects the cypher engine — `full` for the standard rule set.
	Engine string

	// ProjectionKind opts the lens into the declarative actor-aggregate
	// projection plan ("actorAggregate"); empty for a plain projection lens.
	ProjectionKind string

	// Output is the §6.13 Output descriptor for an actor-aggregate lens. It is
	// emitted into the lens spec body so Refractor compiles a ProjectionPlan
	// from it. Nil for a non-actor-aggregate lens.
	Output *OutputDescriptorSpec

	// IntoKey is the lens's primary output-key column list — the RETURN
	// column(s) the adapter keys each projected record under. Empty defaults to
	// ["key"] (the per-row envelope key produced by an actor-aggregate lens),
	// except for a GrantTable lens, whose key defaults to the platform's grant
	// composite (actor_id, anchor_id, grant_source) at activation.
	// An operation-aggregate index keys by its aggregation column instead (e.g.
	// ["operationType"] for the role-by-operation index).
	IntoKey []string

	// Protected marks this lens as a read-path-authorized business read model
	// (Contract #6 §6.14, postgres only). At activation Refractor provisions an
	// RLS table (FORCE ROW LEVEL SECURITY + the set-membership policy) from
	// Columns and projects an authz_anchors column. Mutually exclusive with
	// Public.
	Protected bool

	// Public is the auditable opt-out: a genuinely public postgres read model
	// that declines read-path authorization. Mutually exclusive with Protected.
	Public bool

	// GrantTable marks this lens as a cap-read.* grant projector (postgres only).
	// Its rows are written to the shared actor_read_grants table through the
	// seq-guarded grant writer; Table defaults to actor_read_grants and IntoKey
	// to (actor_id, anchor_id, grant_source). Not a protected business model.
	GrantTable bool

	// GrantSource declares the grant_source this lens owns — the same literal
	// its cypher RETURNs as the grant_source column (e.g. "cap-read.staff").
	// Declaring it lifts that value from row data to lens metadata, which is
	// what lets Refractor confine a whole-table operation to this producer's own
	// rows in the SHARED actor_read_grants table: it scopes the key enumeration
	// DiffRetraction diffs against, and is enforced against every row this lens
	// writes, so the declaration and the cypher cannot drift apart. Required
	// with DiffRetraction; optional otherwise — a producer that retracts through
	// its anchor's tombstone enumerates nothing.
	GrantSource string

	// Columns declares the business columns of a Protected table (name + verbatim
	// Postgres type) so Refractor can provision the table from the lens spec. The
	// platform always adds authz_anchors text[] and projection_seq; key columns
	// are provisioned as text. Ignored for a non-protected lens.
	Columns []PostgresColumn

	// SecureColumns marks this lens as a Secure Lens (Contract #3 §3.10):
	// each entry names a RETURN column carrying a sensitive aspect's
	// ciphertext envelope (`node.<aspect>.data`) that Refractor decrypts
	// under the owning identity's DEK at projection time. Requires Protected
	// (decrypted PII may only land behind RLS) — Refractor fails any other
	// posture closed at activation.
	SecureColumns []SecureColumn

	// DiffRetraction opts a plain (non-actorAggregate) lens into Refractor's
	// Fire 3 target-diff retraction
	// (negative-filter-retraction-projection-design.md §2.4): for a lens whose
	// output key cannot be derived read-free from its own anchor (a composite
	// key with a column bound to a non-anchor variable — e.g. a landlord_id
	// resolved by walking a `manages` link off the matched unit, not the
	// lens's leaseapp anchor, or a pair-keyed dedup output), Refractor diffs
	// the target's live key set against each re-execute instead of relying on
	// the anchor-self presence check, which structurally cannot reach this
	// shape. Postgres or nats-kv (both adapters implement adapter.KeyLister).
	DiffRetraction bool
}

// PostgresColumn declares one provisioned column of a Protected read-model
// table: Type is the verbatim Postgres column type (e.g. "text", "bigint").
// Mirrors the Refractor-side lens.PostgresColumn on-wire shape.
type PostgresColumn struct {
	Name string
	Type string
}

// SecureColumn declares one decrypt-at-projection column of a Secure Lens:
// Column is the RETURN alias holding the ciphertext envelope, HolderTypes the
// vertex types whose keys may open it (["identity"], ["retentionclass"]), and
// Field optionally one field of the decrypted plaintext object to project
// (empty projects the whole object). Mirrors the Refractor-side
// lens.SecureColumn on-wire shape.
type SecureColumn struct {
	Column      string
	HolderTypes []string
	Field       string
}

// RetiredSecureColumn is one entry of Definition.RetiredSecureColumns: the
// author's attestation that a Secure-Lens column's key-custody history may
// stop being carried by the persisted spec.
//
// Lens is the lens's canonicalName AS THE COMMITTED PACKAGE DECLARED IT. A
// lens's key is a NanoID salted by that name (LensID), so a rename mints a
// wholly new key and tombstones the old one — the retirement names the OLD
// name, the one whose spec is losing its secure columns, never the new one.
//
// Column selects the erasure this entry attests to, and the two shapes do not
// substitute for one another. A named column excuses that column being dropped
// from a lens that survives. Column:"" excuses ONLY the whole spec going at
// once — the removed or renamed lens — and never a single column dropped from
// a surviving one.
//
// The asymmetry is what keeps a retirement from outliving its edit. A blanket
// Column:"" entry left in a package file after the removal it described would
// otherwise wave through every later erasure on that lens, under a Note
// written about a different one; requiring each dropped column to be named
// means a stale entry goes inert instead of silently excusing the next author.
//
// Note is required and free-form: why the history is safe to stop carrying
// (the rows were swept, re-keyed, or never written). Nothing reads it — it is
// the record the next operator asking "who decided this?" needs, and requiring
// it is what keeps a retirement from being a reflex.
type RetiredSecureColumn struct {
	Lens   string
	Column string
	Note   string
}

// SourceConfig mirrors the on-wire lens-source descriptor (the Chronicler's
// `eventStream` primitive, orchestration-history-read-model-design.md §2.2).
// Field shape matches internal/chronicler.SourceConfig — a separate Go type
// by necessity (see LensSpec.Source's doc comment), kept in sync by hand
// like OutputDescriptorSpec below.
type SourceConfig struct {
	Kind     string           `json:"kind"`
	Subjects []string         `json:"subjects,omitempty"`
	Project  *EventProjection `json:"project,omitempty"`
}

// EventProjection mirrors the on-wire internal/chronicler.EventProjection: a
// pure, total `event → row` mapping (no cypher, no Adjacency, no Core-KV
// read — an event lens's only data is the event body).
type EventProjection struct {
	Key     string                   `json:"key"`
	Columns map[string]ColumnMapping `json:"columns"`
}

// ColumnMapping mirrors the on-wire internal/chronicler.ColumnMapping's
// shapes: a bare dot-path string, {from,map}, {when,value}, plus the
// orthogonal ClearOn — see that type's doc comment for the full doctrine.
// MarshalJSON picks the shape by which fields are populated (the mirror
// image of chronicler.ColumnMapping.UnmarshalJSON, which Chronicler applies
// when it reads this back off the installed lens's aspect data).
type ColumnMapping struct {
	// Path is set for a bare dot-path mapping (mutually exclusive with the
	// two structured shapes below).
	Path string

	From string
	Map  map[string]string

	When  []string
	Value string

	// ClearOn lists event types on which this column resets to absent
	// instead of carrying forward — orthogonal to, and may accompany, any
	// of the three shapes above. See internal/chronicler.ColumnMapping's
	// doc comment (the reader of this wire shape) for the full doctrine.
	ClearOn []string
}

// MarshalJSON encodes a bare-path mapping with no ClearOn as a JSON string
// and every other case as an object. Mirrors the mutual-exclusivity guards
// internal/chronicler.ColumnMapping.MarshalJSON enforces on the same three
// shapes — a malformed literal (e.g. Path set alongside From/Map from a
// copy-paste mistake authoring a package's Lenses()) fails loudly here too,
// instead of silently keeping only the first-matched shape.
func (c ColumnMapping) MarshalJSON() ([]byte, error) {
	isFromMap := c.From != "" || len(c.Map) > 0
	isConditional := len(c.When) > 0 || c.Value != ""
	switch {
	case c.Path != "":
		if isFromMap || isConditional {
			return nil, fmt.Errorf("pkgmgr: column mapping: a bare path cannot also carry from/map/when/value")
		}
		if len(c.ClearOn) == 0 {
			return json.Marshal(c.Path)
		}
		return json.Marshal(struct {
			Path    string   `json:"path"`
			ClearOn []string `json:"clearOn"`
		}{Path: c.Path, ClearOn: c.ClearOn})
	case isFromMap:
		if isConditional {
			return nil, fmt.Errorf("pkgmgr: column mapping: from/map and when/value are mutually exclusive")
		}
		return json.Marshal(struct {
			From    string            `json:"from"`
			Map     map[string]string `json:"map"`
			ClearOn []string          `json:"clearOn,omitempty"`
		}{From: c.From, Map: c.Map, ClearOn: c.ClearOn})
	case isConditional:
		return json.Marshal(struct {
			When    []string `json:"when"`
			Value   string   `json:"value"`
			ClearOn []string `json:"clearOn,omitempty"`
		}{When: c.When, Value: c.Value, ClearOn: c.ClearOn})
	default:
		return nil, fmt.Errorf("pkgmgr: column mapping: empty mapping (expected a path, {from,map}, or {when,value})")
	}
}

// OutputDescriptorSpec mirrors the on-wire §6.13 Output descriptor a package
// actor-aggregate lens declares. Field shape matches the Refractor-side
// lens.OutputDescriptorSpec.
type OutputDescriptorSpec struct {
	AnchorType         string   `json:"anchorType"`
	OutputKeyPattern   string   `json:"outputKeyPattern"`
	BodyColumns        []string `json:"bodyColumns"`
	EmptyBehavior      string   `json:"emptyBehavior"`
	RealnessFilter     string   `json:"realnessFilter,omitempty"`
	Freshness          string   `json:"freshness,omitempty"`
	KeyColumn          string   `json:"keyColumn,omitempty"`
	ActorField         string   `json:"actorField,omitempty"`
	Lanes              []string `json:"lanes,omitempty"`
	StaticEmptyColumns []string `json:"staticEmptyColumns,omitempty"`

	// EntryKeyColumn opts an actor-aggregate lens into per-entry key emission
	// (cap-read-per-anchor-grant-keys-design.md §3.3): instead of one document
	// per actor, the descriptor's single list body column is split into one
	// guarded key per real entry, keyed by BuildKey(actorKey) + "." +
	// entry[EntryKeyColumn]. Empty leaves the default one-document-per-actor
	// path unchanged. Field/tag matches the Refractor-side
	// lens.OutputDescriptorSpec.EntryKeyColumn exactly.
	EntryKeyColumn string `json:"entryKeyColumn,omitempty"`
}

// PermissionSpec is one permission vertex + grant set.
type PermissionSpec struct {
	// OperationType is the operationType this permission gates.
	OperationType string

	// Scope is `any` or `self` per Contract #6.
	Scope string

	// GrantsTo lists the role canonical names that receive this
	// permission via a `grantedBy` link at install time.
	GrantsTo []string

	// Note is an optional human-readable note stored in the permission
	// vertex's data.
	Note string

	// Lanes optionally names the privileged lane(s) (a subset of
	// meta/urgent/system) this grant authorizes on the matched op. Absent
	// means default-lane-only (via the doc-level fallback). A privileged
	// entry here is honored by the Processor only if core's allowlist
	// covers {operationType, lane} (scoped-privileged-lane-grants-design.md).
	Lanes []string
}
