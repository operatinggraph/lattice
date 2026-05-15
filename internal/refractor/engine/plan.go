package engine

// Column describes one projected output column from the RETURN clause.
type Column struct {
	Alias      string // output column name (= RETURN alias), e.g. "agreement_id"
	Expression string // source expression, e.g. "a.id"
	Variable   string // node variable parsed from Expression, e.g. "a"
	Property   string // node property parsed from Expression, e.g. "id"
	Nullable   bool   // true if Variable is bound only in an OPTIONAL MATCH clause
	IsKey      bool   // true if Alias is in the rule's into.key list
}

// TraversalStep is one hop in the traversal path: source node → edge → destination node.
// Steps are ordered to follow the path from anchor to leaf nodes, required MATCH first.
type TraversalStep struct {
	FromVariable string        // source node variable, e.g. "a"
	FromLabel    string        // source node label, e.g. "agreement"
	EdgeType     string        // relationship type, e.g. "HAS_PARTY"
	Direction    EdgeDirection // traversal direction (Outbound/Inbound/Both)
	ToVariable   string        // destination node variable, e.g. "i"
	ToLabel      string        // destination node label, e.g. "identity"
	Optional     bool          // true if this step comes from an OPTIONAL MATCH clause
}

// QueryPlan is the compiled, evaluator-ready representation of a parsed openCypher query.
type QueryPlan struct {
	AnchorLabel    string          // label of the anchor node (first node of first required MATCH)
	AnchorVariable string          // query variable bound to the anchor node, e.g. "a"
	Steps          []TraversalStep // ordered traversal hops
	Columns        []Column        // ordered projected output columns
}
