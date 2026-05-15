package simple

// EdgeDirection represents the direction of a relationship traversal in a pattern.
type EdgeDirection int

const (
	// Outbound is a directed edge: (a)-[:TYPE]->(b)
	Outbound EdgeDirection = iota
	// Inbound is a directed edge: (a)<-[:TYPE]-(b)
	Inbound
	// Both is an undirected edge: (a)-[:TYPE]-(b)
	Both
)

// NodePattern is a node in a graph pattern, e.g. (a:Agreement).
type NodePattern struct {
	Variable string // bound variable name, e.g. "a"; may be empty
	Label    string // node label, e.g. "Agreement"; may be empty
}

// EdgePattern is a relationship in a graph pattern, e.g. -[:HAS_PARTY]->.
type EdgePattern struct {
	Variable  string        // bound variable name; may be empty
	Type      string        // relationship type, e.g. "HAS_PARTY"
	Direction EdgeDirection // direction of the traversal
}

// Pattern is a sequence of alternating nodes and edges forming a path.
// Invariant: len(Nodes) == len(Edges)+1.
type Pattern struct {
	Nodes []NodePattern
	Edges []EdgePattern
}

// MatchClause represents a MATCH or OPTIONAL MATCH clause.
type MatchClause struct {
	Optional bool      // true for OPTIONAL MATCH
	Patterns []Pattern // one or more comma-separated path patterns
}

// ReturnItem is a single projected column in the RETURN clause.
// Example: a.id AS agreement_id → Expression="a.id", Alias="agreement_id"
type ReturnItem struct {
	Expression string // source expression, e.g. "a.id"
	Alias      string // output column name, e.g. "agreement_id"
}

// ReturnClause holds all projected columns.
type ReturnClause struct {
	Items []ReturnItem
}

// Query is the top-level AST produced by Parse for a v1 openCypher query.
type Query struct {
	Matches []MatchClause
	Return  ReturnClause
}
