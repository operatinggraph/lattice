package bootstrap

// LensDefinition holds the data payload for a Capability Lens meta-vertex.
// The Lens vertex has class "meta.lens" per Contract #6 §6.13.
// Aspects: canonicalName, cypherRule, targetBucket, outputSchema, and — for an
// actor-aggregate lens — projectionKind + the §6.13 Output descriptor.
type LensDefinition struct {
	CanonicalName string
	CypherRule    string
	TargetBucket  string
	OutputSchema  string

	// ProjectionKind opts the lens into the declarative actor-aggregate
	// projection plan ("actorAggregate"); empty for an operation-aggregate lens.
	ProjectionKind string

	// Output is the §6.13 Output descriptor for an actor-aggregate lens; nil
	// otherwise.
	Output *OutputDescriptorSpec
}

// OutputDescriptorSpec mirrors the on-wire §6.13 Output descriptor a primordial
// actor-aggregate lens seeds. It is encoded into the `output` aspect + the spec
// body so Refractor's CoreKVSource compiles a ProjectionPlan from it. Field
// shape matches the Refractor-side lens.OutputDescriptorSpec.
type OutputDescriptorSpec struct {
	AnchorType         string   `json:"anchorType"`
	OutputKeyPattern   string   `json:"outputKeyPattern"`
	BodyColumns        []string `json:"bodyColumns"`
	EmptyBehavior      string   `json:"emptyBehavior"`
	RealnessFilter     string   `json:"realnessFilter,omitempty"`
	Freshness          string   `json:"freshness,omitempty"`
	ActorField         string   `json:"actorField,omitempty"`
	Lanes              []string `json:"lanes,omitempty"`
	StaticEmptyColumns []string `json:"staticEmptyColumns,omitempty"`
}

// CapabilityLensDefinition returns the primary Capability Lens definition —
// the primordial-identity anchor. Contract #7 §7.2 item 5 — vtx.meta.<NanoID>
// with class "meta.lens"; Contract #6 §6.1 decomposition note.
//
// Core projects root-equivalent platform grants for the kernel-seeded system
// identities only — the primordial admin and the Loom + Weaver + Bridge service
// actors (`internal/bootstrap/primordial.go`). These actors ARE core: protected,
// kernel-seeded, and fixed, so their root-grant set is hard-coded here rather
// than derived through the rbac role/permission graph. That keeps the
// kernel authorizable even when no rbac package is installed and removes every
// rbac (role/permission/holdsRole/grantedBy) and service/location
// (containedIn/availableAt/unavailableAt/permitsOperation) reference from
// core's bootstrap cypher — those vocabularies are owned by their packages
// (rbac-domain projects ordinary actors' role-derived grants to the disjoint
// cap.roles.<actor> key; a future service package projects service access).
func CapabilityLensDefinition() LensDefinition {
	return LensDefinition{
		CanonicalName: "capability",
		TargetBucket:  "capability",
		// Actor-aggregate: the compiled ProjectionPlan drives the §6.2 envelope.
		// The cap.<actor> doc carries `lanes` and an always-empty
		// `ephemeralGrants` (live grants live in the disjoint cap.ephemeral.<actor>
		// doc; §6.2/§6.3 require the field present here). emptyBehavior:delete is
		// the actor-disappearance tombstone.
		ProjectionKind: "actorAggregate",
		Output: &OutputDescriptorSpec{
			AnchorType:         "identity",
			OutputKeyPattern:   "cap.{actorSuffix}",
			BodyColumns:        []string{"platformPermissions"},
			EmptyBehavior:      "delete",
			Freshness:          "auto",
			// Per-lane submission grant (Contract #2 §2.3). The protected
			// kernel-seeded system actors (admin + Loom + Weaver + Bridge +
			// object-store-manager) carry the full root-grant set: `meta`
			// (serialized DDL — installs/lens DDL), `system` (engine result/
			// dispatch ops), `urgent`, and `default`. This matches their
			// uniform root platformPermissions above; per-actor lane scoping is
			// a future refinement. Ordinary actors get only `default` from the
			// rbac cap.roles.<actor> lens.
			Lanes:              []string{"default", "meta", "urgent", "system"},
			StaticEmptyColumns: []string{"ephemeralGrants", "serviceAccess", "roles"},
		},
		// The anchor projects only the protected (kernel-seeded) system
		// identities; the WHERE filters out every ordinary actor (zero rows →
		// no cap.<actor> doc for them — they read cap.roles.<actor>). Each
		// system identity receives the fixed kernel root-grant set: the
		// scope:"any" meta + package-install permissions the operator role
		// carries. The grant set is a literal here, NOT a graph walk, so core
		// references no rbac vocabulary.
		CypherRule: `
MATCH (identity:identity {key: $actorKey})
WHERE identity.data.protected = true
RETURN
  identity.key AS actorKey,
  [
    {operationType: 'CreateMetaVertex', scope: 'any'},
    {operationType: 'UpdateMetaVertex', scope: 'any'},
    {operationType: 'TombstoneMetaVertex', scope: 'any'},
    {operationType: 'InstallPackage', scope: 'any'},
    {operationType: 'UninstallPackage', scope: 'any'},
    {operationType: 'UpgradePackage', scope: 'any'}
  ] AS platformPermissions
`,
		// outputSchema: JSON Schema for the Capability KV document per Contract #6 §6.2.
		OutputSchema: `{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "type": "object",
  "required": ["key","actor","version","projectedAt","projectedFromRevisions","lanes",
               "platformPermissions","serviceAccess","roles"],
  "properties": {
    "key":                   {"type": "string"},
    "actor":                 {"type": "string"},
    "version":               {"type": "string"},
    "projectedAt":           {"type": "string", "format": "date-time"},
    "projectedFromRevisions":{"type": "object", "additionalProperties": {"type": "integer"}},
    "lanes":                 {"type": "array",  "items": {"type": "string"}},
    "platformPermissions":   {"type": "array",  "items": {
      "type": "object",
      "required": ["operationType","scope"],
      "properties": {
        "operationType": {"type": "string"},
        "scope":         {"type": "string", "enum": ["any","self","specific","owned"]}
      }
    }},
    "serviceAccess":  {"type": "array"},
    "roles":          {"type": "array", "items": {"type": "string"}}
  }
}`,
	}
}

// CapabilityReadLensDefinition returns the base read-path authorization lens —
// the core slice of the §6.14 cap-read.* family (D1). Contract #7 §7.2 item 5
// (vtx.meta.<NanoID> with class "meta.lens"); Contract #6 §6.14.
//
// Read auth mirrors write auth (§6.1's contract-contribution model): core owns
// the bucket + the key conventions and projects only the BASE read scope every
// actor carries independent of any package — its **self** anchor (an actor may
// always read its own vertex). Each package ships its own cap-read.<domain>
// actor-aggregate lens for the relationships it owns (rbac-domain →
// cap-read.roles, loftspace → cap-read.residence, …); the actor's effective
// readable set is the union over all cap-read.*.<actor> slices (§6.14). This
// base lens references no package vocabulary, exactly as the write-path base
// capability lens does.
//
// Scope note (D1.1): this increment projects the self anchor for every actor.
// The primordial root-read scope for kernel-seeded identities (the read analog
// of the write base's scope:"any" grant — the privileged all-access anchor)
// lands with the D1 enforcement seam, which defines the wildcard-anchor
// representation the RLS/read boundary matches against (design §3.3, M5).
//
// readableAnchors carries each entry as {anchorType, anchorId, via} (§6.14). The
// anchorId is the full vertex key (vtx.<type>.<id>) — the covered-cypher
// representation, consistent with §6.5 serviceAccess.service; the §6.14 example's
// bare-NanoID rendering is illustrative (see the staged §6.14 representation
// clarification). emptyBehavior:delete is the actor-disappearance tombstone — the
// self anchor is always present, so the key drops only when the identity vertex
// itself disappears.
func CapabilityReadLensDefinition() LensDefinition {
	return LensDefinition{
		CanonicalName:  "capabilityRead",
		TargetBucket:   "capability",
		ProjectionKind: "actorAggregate",
		Output: &OutputDescriptorSpec{
			AnchorType:       "identity",
			OutputKeyPattern: "cap-read.{actorSuffix}",
			BodyColumns:      []string{"readableAnchors"},
			EmptyBehavior:    "delete",
			Freshness:        "auto",
			Lanes:            []string{"default"},
		},
		CypherRule: `
MATCH (identity:identity {key: $actorKey})
RETURN
  identity.key AS actorKey,
  [
    {anchorType: 'identity', anchorId: identity.key, via: ['self']}
  ] AS readableAnchors
`,
		// outputSchema: JSON Schema for the cap-read.<actor> document per
		// Contract #6 §6.14 (read-path mirror of the §6.2 envelope; the body
		// carries readableAnchors instead of the write-path grant columns).
		OutputSchema: `{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "type": "object",
  "required": ["key","actor","version","projectedAt","projectedFromRevisions","lanes",
               "readableAnchors"],
  "properties": {
    "key":                   {"type": "string"},
    "actor":                 {"type": "string"},
    "version":               {"type": "string"},
    "projectedAt":           {"type": "string", "format": "date-time"},
    "projectedFromRevisions":{"type": "object", "additionalProperties": {"type": "integer"}},
    "lanes":                 {"type": "array",  "items": {"type": "string"}},
    "readableAnchors":       {"type": "array",  "items": {
      "type": "object",
      "required": ["anchorType","anchorId","via"],
      "properties": {
        "anchorType": {"type": "string"},
        "anchorId":   {"type": "string"},
        "via":        {"type": "array", "items": {"type": "string"}}
      }
    }}
  }
}`,
	}
}

