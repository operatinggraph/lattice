package bootstrap

// LensDefinition holds the data payload for a Capability Lens meta-vertex.
// The Lens vertex has class "meta.lens" per Contract #6 §6.13.
// Aspects: canonicalName, cypherRule, targetBucket, outputSchema.
type LensDefinition struct {
	CanonicalName string
	CypherRule    string
	TargetBucket  string
	OutputSchema  string
}

// CapabilityLensDefinition returns the primary Capability Lens definition.
// Contract #7 §7.2 item 5 — vtx.meta.<NanoID> with class "meta.lens".
// cypherRule body: Contract #6 §6.10 required behaviors.
// Rule is stored as TEXT; openCypher parsing arrives in Story 3.1.
// The rule body here is structurally valid cypher per the handoff brief decision #8.
func CapabilityLensDefinition() LensDefinition {
	return LensDefinition{
		CanonicalName: "capability",
		TargetBucket:  "capability",
		// Cypher rule per Contract #6 §6.10 and brief decision #8.
		// Produces three sections: platformPermissions, serviceAccess, ephemeralGrants.
		// Story 3.1 connects the openCypher engine; Story 3.2 activates live projection.
		CypherRule: `
MATCH (identity:identity {key: $actorKey})

// --- platformPermissions ---
// Walk: identity → holdsRole → role <-grantedBy- permission
// Story 4.7 rename: grantsPermission(role→permission) became
// grantedBy(permission→role); the topology is identical, the traversal
// direction reverses.
OPTIONAL MATCH (identity)-[:holdsRole]->(role:role)<-[:grantedBy]-(perm:permission)

// --- serviceAccess ---
// Walk: identity → containedIn* → location → availableAt → service
// Exclusion: identity path → unavailableAt → service wins over availableAt
OPTIONAL MATCH (identity)-[:containedIn*0..]->(loc)
  -[:availableAt]->(svc)
WHERE NOT (identity)-[:containedIn*0..]->(loc)-[:unavailableAt]->(svc)

// --- ephemeralGrants ---
// Walk: task → assignedTo → identity (direct or via reportsTo chain, max 2 hops).
// Anchored on the already-bound identity (not the unbound task label) so the
// engine traverses adjacency from the actor instead of scanning the entire Core
// KV bucket for task seeds on every reprojection.
OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
  WHERE task.data.expiresAt > $now

OPTIONAL MATCH (identity)-[:reportsTo]->(report:identity)<-[:assignedTo]-(task2:task)
  WHERE task2.data.expiresAt > $now

RETURN
  identity.key AS actorKey,
  collect(DISTINCT {
    operationType: perm.data.operationType,
    scope: perm.data.scope
  }) AS platformPermissions,
  collect(DISTINCT {
    service: svc.key,
    serviceClass: svc.class,
    resolvedVia: [loc.key],
    allowedOperations: [(svc)-[:permitsOperation]->(op) | {operationType: op.data.operationType}]
  }) AS serviceAccess,
  collect(DISTINCT {
    source: "task",
    taskKey: task.key,
    operationType: task.data.grantedOperationType,
    target: task.data.targetKey,
    expiresAt: task.data.expiresAt
  }) + collect(DISTINCT {
    source: "task",
    taskKey: task2.key,
    operationType: task2.data.grantedOperationType,
    target: task2.data.targetKey,
    expiresAt: task2.data.expiresAt
  }) AS ephemeralGrants,
  collect(DISTINCT role.key) AS roles
`,
		// outputSchema: JSON Schema for the Capability KV document per Contract #6 §6.2.
		OutputSchema: `{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "type": "object",
  "required": ["key","actor","version","projectedAt","projectedFromRevisions","lanes",
               "platformPermissions","serviceAccess","ephemeralGrants","roles"],
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
    "ephemeralGrants":{"type": "array"},
    "roles":          {"type": "array", "items": {"type": "string"}}
  }
}`,
	}
}

// CapabilityRoleIndexLensDefinition returns the secondary role-coverage index Lens.
// Contract #6 §6.1 — produces cap.role-by-operation.<operationType> keys.
// Story 3.2 activates live projection; Story 1.3 just seeds the definition.
func CapabilityRoleIndexLensDefinition() LensDefinition {
	return LensDefinition{
		CanonicalName: "capabilityRoleIndex",
		TargetBucket:  "capability",
		// Produces one entry per operationType listing roles that grant it.
		// Used by Processor denial-response (Story 3.4) to build FR22 rolesCarryingPermission.
		CypherRule: `
MATCH (role:role)<-[:grantedBy]-(perm:permission)
RETURN
  perm.data.operationType AS operationType,
  collect(DISTINCT role.canonicalName.data.value) AS roles,
  $projectedAt AS projectedAt
`,
		OutputSchema: `{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "type": "object",
  "required": ["roles","projectedAt"],
  "properties": {
    "roles":       {"type": "array", "items": {"type": "string"}},
    "projectedAt": {"type": "string", "format": "date-time"}
  }
}`,
	}
}
