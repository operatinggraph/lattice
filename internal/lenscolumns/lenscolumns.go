// Package lenscolumns is the ONE derivation of "which keys can a row of this
// lens carry", shared by every static reader that must answer it without
// standing up a pipeline: the pkgmgr installer's gap-companion-pair rule and
// the gap-column-declaration lint.
//
// The derivation is a WHITELIST over lens shape, not a blacklist: a shape not
// on the list is UNREADABLE, never empty. Blacklisting ("not actorAggregate
// ⇒ plain") misreads an eventStream lens (no cypher at all), a per-entry
// actor-aggregate lens (a runtime shape, not a static column set), and an
// Output-less actorAggregate lens (never activates) as either empty or a
// legitimate plain-lens parse.
package lenscolumns

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Spec is the subset of an installed lens spec (vtx.meta.<id>.spec's data, or
// a pkgmgr.LensSpec) that decides which keys its rows carry. JSON tags match
// the stored body.
type Spec struct {
	ProjectionKind string   `json:"projectionKind,omitempty"`
	Output         *Output  `json:"output,omitempty"`
	Source         *Source  `json:"source,omitempty"`
	CypherRule     string   `json:"cypherRule,omitempty"`
	CypherBranches []string `json:"cypherBranches,omitempty"`
}

// Output is the §6.13 Output descriptor of an actor-aggregate lens.
type Output struct {
	BodyColumns        []string `json:"bodyColumns"`
	StaticEmptyColumns []string `json:"staticEmptyColumns,omitempty"`
	EntryKeyColumn     string   `json:"entryKeyColumn,omitempty"`
}

// Source is a lens's source descriptor (the Chronicler's eventStream
// primitive). Kind "coreKv" (the default, empty Source) is the ordinary
// cypher-over-Core-KV path and carries no Project.
type Source struct {
	Kind    string   `json:"kind"`
	Project *Project `json:"project,omitempty"`
}

// Project is an eventStream source's pure event→row column mapping. Columns'
// values are opaque here — only the key set (the column names) decides what a
// row carries.
type Project struct {
	Columns map[string]json.RawMessage `json:"columns"`
}

// Result is Projected's answer: every key a row of the lens carries, and
// which top-level declaration produced them.
type Result struct {
	// Columns maps a column name to the declaration it came from — one of
	// the Provenance* constants below.
	Columns map[string]string

	// Source names which of the three whitelist branches produced Columns:
	// "Output", "Source.Project.Columns", or "RETURN".
	Source string
}

// Provenance values a Result.Columns entry carries.
const (
	ProvenanceBodyColumns        = "Output.BodyColumns"
	ProvenanceStaticEmptyColumns = "Output.StaticEmptyColumns"
	ProvenanceProjectColumns     = "Source.Project.Columns"
	ProvenanceReturn             = "RETURN"
)

// resultSourceOutput is Result.Source's value for the actorAggregate branch —
// deliberately coarser than the per-column Provenance* constants above, since
// a Result may carry both BodyColumns and StaticEmptyColumns provenances at
// once.
const resultSourceOutput = "Output"

// Lens-shape constants a caller's own dispatch (pkgmgr's install-time rules,
// the gap-column lint) is pinned against, so "actorAggregate" is spelled in
// exactly one place across the two packages.
const (
	ActorAggregateKind = "actorAggregate"
	EventStreamKind    = "eventStream"
)

// GapColumnPrefix is the §10.8 convention every weaver-target gap column
// carries: missing_<gap>.
const GapColumnPrefix = "missing_"

// ErrUnreadable reports that a lens's row columns are not derivable from its
// declaration — a distinct verdict from "no columns", which errors.Is lets a
// caller test for regardless of the reason text appended to it.
var ErrUnreadable = errors.New("lenscolumns: the lens's row columns are not derivable from its declaration")

// Projected returns every key a row of this lens carries, dispatching on the
// whitelist in order:
//
//   - Source.Kind == "eventStream": the keys of Source.Project.Columns. No
//     cypher is read.
//   - ProjectionKind == "actorAggregate": Output == nil is ErrUnreadable (the
//     lens never activates); Output.EntryKeyColumn != "" is ErrUnreadable (a
//     per-entry list lens projects a runtime shape, not a static column set);
//     otherwise Output.BodyColumns ∪ Output.StaticEmptyColumns, with
//     BodyColumns winning the provenance on overlap.
//   - otherwise (a plain lens — every artifact lens, and every other
//     projectionKind): CypherBranches non-empty parses branch 0; empty parses
//     CypherRule. A nil returnColumns, a parse error, or an empty RETURN are
//     each ErrUnreadable naming the reason.
//
// Every ErrUnreadable is wrapped with fmt.Errorf("%w: …") so errors.Is still
// matches and the message names why.
func Projected(s Spec, returnColumns func(rule string) ([]string, error)) (Result, error) {
	switch {
	case s.Source != nil && s.Source.Kind == EventStreamKind:
		return projectedFromEventStream(s.Source)
	case s.ProjectionKind == ActorAggregateKind:
		return projectedFromActorAggregate(s.Output)
	default:
		return projectedFromPlain(s, returnColumns)
	}
}

func projectedFromEventStream(src *Source) (Result, error) {
	cols := map[string]string{}
	if src.Project != nil {
		for name := range src.Project.Columns {
			cols[name] = ProvenanceProjectColumns
		}
	}
	return Result{Columns: cols, Source: ProvenanceProjectColumns}, nil
}

func projectedFromActorAggregate(out *Output) (Result, error) {
	if out == nil {
		return Result{}, fmt.Errorf("%w: declares no Output descriptor for an actorAggregate lens", ErrUnreadable)
	}
	if out.EntryKeyColumn != "" {
		return Result{}, fmt.Errorf("%w: per-entry list lens: a runtime shape", ErrUnreadable)
	}
	cols := make(map[string]string, len(out.BodyColumns)+len(out.StaticEmptyColumns))
	for _, c := range out.StaticEmptyColumns {
		cols[c] = ProvenanceStaticEmptyColumns
	}
	for _, c := range out.BodyColumns {
		cols[c] = ProvenanceBodyColumns
	}
	return Result{Columns: cols, Source: resultSourceOutput}, nil
}

func projectedFromPlain(s Spec, returnColumns func(rule string) ([]string, error)) (Result, error) {
	if returnColumns == nil {
		return Result{}, fmt.Errorf("%w: no cypher parser supplied", ErrUnreadable)
	}
	rule := s.CypherRule
	if len(s.CypherBranches) > 0 {
		rule = s.CypherBranches[0]
	}
	names, err := returnColumns(rule)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrUnreadable, err)
	}
	if len(names) == 0 {
		return Result{}, fmt.Errorf("%w: RETURN clause is empty or absent", ErrUnreadable)
	}
	cols := make(map[string]string, len(names))
	for _, n := range names {
		cols[n] = ProvenanceReturn
	}
	return Result{Columns: cols, Source: ProvenanceReturn}, nil
}

// Gaps filters r.Columns down to the missing_-prefixed keys — the Strategist's
// gap scan. Never nil.
func Gaps(r Result) map[string]string {
	out := make(map[string]string)
	for k, v := range r.Columns {
		if strings.HasPrefix(k, GapColumnPrefix) {
			out[k] = v
		}
	}
	return out
}
