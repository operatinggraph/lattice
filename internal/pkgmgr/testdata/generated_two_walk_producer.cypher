
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:residesIn]->(home)-[:containedIn*0..]->(container)
OPTIONAL MATCH (container)<-[:availableAt]-(tpl:service)
WITH identity,
  collect(DISTINCT {anchorType: 'service', anchorId: nanoIdFromKey(tpl.key), via: ['residesIn', 'containedIn', 'availableAt']}) AS grantSlice0
OPTIONAL MATCH (identity)-[:residesIn]->(home)-[:containedIn*0..]->(container)
OPTIONAL MATCH (container)<-[:practicesAt]-(prov:provider)
WITH identity, grantSlice0,
  collect(DISTINCT {anchorType: 'provider', anchorId: nanoIdFromKey(prov.key), via: ['residesIn', 'containedIn', 'practicesAt']}) AS grantSlice1
RETURN
  identity.key AS actorKey,
  grantSlice0 + grantSlice1 AS readableAnchors
