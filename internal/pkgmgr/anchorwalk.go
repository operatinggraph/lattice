package pkgmgr

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// A Personal (nats-subject) Lens that anchors on a vertex OTHER than the
// recipient identity is only half of a read path: Refractor's D1 gate
// (internal/refractor/projection/personal.go → capabilityread.IsReadable) drops
// every projected row whose anchor NanoID is absent from the actor's unioned
// `cap-read.<domain>.<actor>` slices, silently and by design (Contract #6
// §6.14). The other half is an actorAggregate producer lens that grants those
// anchors — and until this primitive both halves stated the SAME actor→anchor
// reachability walk, hand-authored twice.
//
// AnchorWalk is that walk, declared once. ExpandReadGrantWalks compiles it into
// both artifacts: the data lens's reachability prefix and the whole grant
// producer. The runtime stays two independent enumerations (separate lens
// vertices, separate CDC re-executions), so D1's gate keeps bounding the
// hand-authored presentation tail — what the declaration removes is the drift
// between two copies of one walk, not the boundary between them.
type AnchorWalk struct {
	// GrantDomain names the cap-read slice this walk's grant branch lands in
	// (Contract #6 §6.14 key space: cap-read.<GrantDomain>.<actorSuffix>).
	// Must match a declared ReadGrantDomain of the same Definition.
	GrantDomain string

	// AnchorType is the anchor vertex's label — the readableAnchors entry's
	// anchorType, and the kind the anchor-coverage gate checks.
	AnchorType string

	// AnchorVar is the chain variable bound to the anchor vertex. The lens's
	// Spec tail must RETURN <AnchorVar>.key AS anchor; the producer branch
	// collects nanoIdFromKey(<AnchorVar>.key).
	AnchorVar string

	// Chain is the walk as ordered pattern clauses. Each entry is a SINGLE
	// linear relationship pattern (at least one relationship; no commas; at
	// least one node variable already bound by an earlier clause or by the
	// actor variable `identity`). Compiled as one OPTIONAL MATCH per clause —
	// verbatim on the data side, variable-renamed on the producer side.
	Chain []string
}

// ReadGrantDomainSpec declares one cap-read producer slice a package owns. One
// producer lens is generated per domain, collecting every walk that names it.
//
// Domains are the §6.14 grouping and blast-radius unit: an actor's effective
// readable set is the union over every slice, so splitting reachability paths
// that not every actor has (a staff-only or provider-hat path) into their own
// domain keeps the base producer's cross-branch fan-out from growing for
// every actor.
type ReadGrantDomainSpec struct {
	// Name is the <domain> in cap-read.<domain>.<actorSuffix>.
	Name string

	// CanonicalName of the generated producer lens; empty defaults to
	// <Name>ReadGrants.
	CanonicalName string
}

// anchorWalkActorVar is the variable the generated head binds to the recipient
// identity, and the one root every chain clause must ultimately hang off.
const anchorWalkActorVar = "identity"

// anchorWalkHead is the head clause both compiled artifacts open with — the
// Personal-lens contract that $actorKey is the enumerated recipient's own key.
const anchorWalkHead = "MATCH (" + anchorWalkActorVar + ":identity {key: $actorKey})"

// ExpandReadGrantWalks compiles every declared AnchorWalk into the two
// artifacts it is the single source of, returning the composed Definition:
//
//   - each walk-bearing lens's Spec becomes the head + one OPTIONAL MATCH per
//     chain clause + the declared Spec (which is the presentation tail only);
//   - one actorAggregate producer LensSpec per ReadGrantDomain is appended,
//     never hand-authored, collecting one branch per member walk.
//
// It is pure (no I/O), idempotent, and the single validation point for the
// walk grammar — every validateAll / VerifyAgainstDefinition site runs it, so
// the testkit and the anchor-coverage gate consume exactly the specs the
// installer installs.
func (def Definition) ExpandReadGrantWalks() (Definition, error) {
	if def.readGrantWalksExpanded {
		return def, nil
	}

	domains, err := def.indexReadGrantDomains()
	if err != nil {
		return Definition{}, err
	}

	lenses := make([]LensSpec, len(def.Lenses))
	copy(lenses, def.Lenses)

	// Member walks accumulate per domain in LENS DECLARATION order, which is
	// what makes the generated producer's clause order deterministic.
	members := make(map[string][]*parsedWalk, len(domains))
	for idx := range lenses {
		l := &lenses[idx]
		if l.Walk == nil {
			if err := validateWalklessPersonalLens(*l); err != nil {
				return Definition{}, err
			}
			continue
		}
		pw, err := parseWalk(*l, domains)
		if err != nil {
			return Definition{}, err
		}
		l.Spec = composeDataLensSpec(pw)
		members[pw.walk.GrantDomain] = append(members[pw.walk.GrantDomain], pw)
	}

	for _, d := range def.ReadGrantDomains {
		ms := members[d.Name]
		if len(ms) == 0 {
			return Definition{}, fmt.Errorf(
				"pkgmgr: ReadGrantDomain %q is declared but no lens Walk names it — "+
					"an empty producer projects an always-empty grant slice", d.Name)
		}
		lenses = append(lenses, generateProducerLens(d, ms))
	}

	def.Lenses = lenses
	def.readGrantWalksExpanded = true
	return def, nil
}

// indexReadGrantDomains checks the declared domains are well-formed and unique.
func (def Definition) indexReadGrantDomains() (map[string]ReadGrantDomainSpec, error) {
	out := make(map[string]ReadGrantDomainSpec, len(def.ReadGrantDomains))
	for i, d := range def.ReadGrantDomains {
		if err := validateGrantDomainName(i, d.Name); err != nil {
			return nil, err
		}
		if _, dup := out[d.Name]; dup {
			return nil, fmt.Errorf("pkgmgr: ReadGrantDomains[%d]: duplicate domain %q", i, d.Name)
		}
		out[d.Name] = d
	}
	return out, nil
}

// validateGrantDomainName rejects a domain name that would not survive the
// §6.14 key space. The name becomes one token of
// `cap-read.<domain>.<actorSuffix>`, and capabilityread lists slices with the
// single-token wildcard `cap-read.*.<actorSuffix>` — so a name carrying a dot
// (or whitespace, or a subject wildcard) yields a key no reader can ever match,
// and every lens in that domain has 100% of its rows dropped, silently.
func validateGrantDomainName(idx int, name string) error {
	if name == "" {
		return fmt.Errorf("pkgmgr: ReadGrantDomains[%d]: Name is required", idx)
	}
	for i := 0; i < len(name); i++ {
		if !isIdentByte(name[i]) {
			return fmt.Errorf(
				"pkgmgr: ReadGrantDomains[%d]: Name %q must be a single key token "+
					"(letters, digits, underscore) — it becomes one segment of "+
					"cap-read.<domain>.<actorSuffix>, which readers enumerate with a "+
					"single-token wildcard, so any other character yields a slice no "+
					"reader can match and every row of its lenses is silently dropped",
				idx, name)
		}
	}
	return nil
}

// producerCanonicalName is the generated producer's name for a domain.
func (d ReadGrantDomainSpec) producerCanonicalName() string {
	if d.CanonicalName != "" {
		return d.CanonicalName
	}
	return d.Name + "ReadGrants"
}

// validateWalklessPersonalLens enforces the invariant the whole primitive
// exists to make structural: a Personal lens anchoring on anything but the
// actor itself MUST declare its walk, so a producer is compiled for it. A
// self-anchored lens (its anchor variable bound `{key: $actorKey}`) is exempt —
// the platform base cap-read self-grant already covers the actor's own key.
func validateWalklessPersonalLens(l LensSpec) error {
	if l.Adapter != "nats-subject" || !l.Personal {
		return nil
	}
	anchorVar := anchorVarOf(l.Spec)
	if anchorVar == "" {
		return fmt.Errorf(
			"pkgmgr: lens %q: Personal lens has no `<var>.key AS anchor` — "+
				"every Personal-lens row must alias a Contract #1 vertex key to `anchor`",
			l.CanonicalName)
	}
	if isSelfAnchoredSpec(l.Spec, anchorVar) {
		return nil
	}
	return fmt.Errorf(
		"pkgmgr: lens %q: non-self-anchored Personal lens (anchors on %q) declares no Walk — "+
			"its read-grant producer cannot be compiled, so Refractor's D1 gate would "+
			"silently drop every row it projects; declare Walk with the actor→anchor chain",
		l.CanonicalName, anchorVar)
}

// parsedWalk is one validated walk: the parsed chain, the variable→label
// bindings it establishes, and the ordered relation names its `via` audit
// array is derived from.
type parsedWalk struct {
	lensName string
	tail     string
	walk     AnchorWalk
	clauses  []*linearPattern
	relNames []string
}

// parseWalk validates one lens's Walk against the §3.3 grammar and returns it
// parsed. Every rule here fails the package build closed — the S1 lint's
// advisory checks promoted to a hard compile error, plus the connectivity rule
// that makes an unbound-scan branch inexpressible rather than merely caught.
func parseWalk(l LensSpec, domains map[string]ReadGrantDomainSpec) (*parsedWalk, error) {
	name := l.CanonicalName
	if l.Adapter != "nats-subject" || !l.Personal {
		return nil, fmt.Errorf(
			"pkgmgr: lens %q: Walk is declared but the lens is not a Personal (nats-subject) lens — "+
				"an AnchorWalk compiles a read-grant producer for the D1 gate, which only Personal lenses meet",
			name)
	}
	w := *l.Walk
	if _, ok := domains[w.GrantDomain]; !ok {
		return nil, fmt.Errorf(
			"pkgmgr: lens %q: Walk.GrantDomain %q is not a declared ReadGrantDomain of this package",
			name, w.GrantDomain)
	}
	if w.AnchorType == "" || w.AnchorVar == "" {
		return nil, fmt.Errorf("pkgmgr: lens %q: Walk.AnchorType and Walk.AnchorVar are both required", name)
	}
	if len(w.Chain) == 0 {
		return nil, fmt.Errorf("pkgmgr: lens %q: Walk.Chain is empty — a non-self anchor needs at least one hop", name)
	}

	pw := &parsedWalk{lensName: name, tail: strings.TrimSpace(l.Spec), walk: w}
	bound := map[string]bool{anchorWalkActorVar: true}
	labels := map[string]string{}
	for ci, clause := range w.Chain {
		lp, err := parseLinearPattern(clause)
		if err != nil {
			return nil, fmt.Errorf("pkgmgr: lens %q: Walk.Chain[%d]: %w", name, ci, err)
		}
		connected := false
		for _, v := range lp.nodeVars {
			if bound[v.name] {
				connected = true
				break
			}
		}
		if !connected {
			return nil, fmt.Errorf(
				"pkgmgr: lens %q: Walk.Chain[%d] %q binds no variable reachable from the actor — "+
					"every clause must reuse `%s` or a variable an earlier clause bound; an unrooted "+
					"clause seeds by scan and would cross-product every matching vertex into every "+
					"actor's grants",
				name, ci, clause, anchorWalkActorVar)
		}
		for _, v := range lp.nodeVars {
			bound[v.name] = true
		}
		for _, v := range lp.relVars {
			bound[v.name] = true
		}
		for k, v := range lp.labels {
			if _, seen := labels[k]; !seen {
				labels[k] = v
			}
		}
		pw.clauses = append(pw.clauses, lp)
		pw.relNames = append(pw.relNames, lp.relTypes...)
	}

	if !bound[w.AnchorVar] {
		return nil, fmt.Errorf(
			"pkgmgr: lens %q: Walk.AnchorVar %q is not bound by Walk.Chain", name, w.AnchorVar)
	}
	if got := labels[w.AnchorVar]; got != w.AnchorType {
		return nil, fmt.Errorf(
			"pkgmgr: lens %q: Walk.AnchorType is %q but Walk.Chain binds %q with label %q — "+
				"the declared anchor kind and the pattern's own label must agree",
			name, w.AnchorType, w.AnchorVar, got)
	}
	if err := validateWalkTail(pw); err != nil {
		return nil, err
	}
	return pw, nil
}

// validateWalkTail enforces the two rules that keep the hand-authored
// presentation tail from silently re-aiming the compiled walk: the tail must
// return the declared anchor, and it must not rebind the anchor variable.
//
// The security direction matters more than the rules: the producer is compiled
// ONLY from the Walk, so a tail that widened the data side would project
// anchors the grant side never granted — and D1 drops them, fail-closed. These
// checks turn that silent drop into a build error.
func validateWalkTail(pw *parsedWalk) error {
	v := pw.walk.AnchorVar
	tail := normalizeWhitespace(pw.tail)
	if !aliasesKeyToAnchor(tail, v) {
		return fmt.Errorf(
			"pkgmgr: lens %q: Spec tail must `RETURN %s.key AS anchor` (the declared Walk.AnchorVar)",
			pw.lensName, v)
	}
	if containsWord(tail, "AS "+v) {
		return fmt.Errorf(
			"pkgmgr: lens %q: Spec tail aliases something to %q, rebinding the Walk's anchor variable — "+
				"the tail may enrich off bound variables but must not re-aim the anchor",
			pw.lensName, v)
	}
	if strings.Contains(tail, "("+v+":") {
		return fmt.Errorf(
			"pkgmgr: lens %q: Spec tail rebinds %q with a fresh labelled node pattern — "+
				"the tail may reuse the bound variable (an unlabelled reuse joins it) but must not rebind it",
			pw.lensName, v)
	}
	return nil
}

// composeDataLensSpec is the data-side compilation: the actor head, one
// OPTIONAL MATCH per chain clause (verbatim, original variable names — the
// hand-authored tail references them), then the tail.
//
// Every clause compiles as OPTIONAL MATCH, including for a walk whose lens
// previously fused the chain into a required MATCH: a degenerate unmatched row
// is declined by the Personal-lens envelope, and any filter that rode the
// required MATCH belongs in the tail, where presentation filters live.
func composeDataLensSpec(pw *parsedWalk) string {
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(anchorWalkHead)
	b.WriteString("\n")
	for _, lp := range pw.clauses {
		b.WriteString("OPTIONAL MATCH ")
		b.WriteString(lp.src)
		b.WriteString("\n")
	}
	b.WriteString(pw.tail)
	b.WriteString("\n")
	return b.String()
}

// generateProducerLens compiles one domain's member walks into the single
// actorAggregate producer that grants their anchors.
//
// The Output descriptor is field-for-field what a hand-authored Path-B producer
// declares, plus the realness filter on `anchorId` — without it the driver's
// empty-delete branch never runs, and an all-OPTIONAL producer mints a
// placeholder-only document for every binding-less identity.
func generateProducerLens(d ReadGrantDomainSpec, walks []*parsedWalk) LensSpec {
	return LensSpec{
		CanonicalName:  d.producerCanonicalName(),
		Class:          "meta.lens",
		Adapter:        "nats-kv",
		Bucket:         "capability-kv",
		Engine:         "full",
		ProjectionKind: "actorAggregate",
		Output: &OutputDescriptorSpec{
			AnchorType:       "identity",
			OutputKeyPattern: "cap-read." + d.Name + ".{actorSuffix}",
			BodyColumns:      []string{"readableAnchors"},
			EmptyBehavior:    "delete",
			RealnessFilter:   "anchorId",
			Freshness:        "auto",
			Lanes:            []string{"default"},
		},
		Spec: generateProducerSpec(walks),
	}
}

// generateProducerSpec emits the producer cypher: the actor head, the member
// walks' chain clauses with deterministic shared-prefix factoring, and one
// collect branch per walk concatenated into readableAnchors.
//
// Factoring: walks whose leading clauses are textually identical share those
// clauses — and their variable bindings — once. That reproduces the shape a
// hand author writes (a residence prefix bound once for four branches) and is
// what keeps the cross-branch fan-out at its current level. Correctness never
// depends on it: a walk that shares nothing simply emits its whole chain.
//
// Renaming: a variable a walk first binds in its UNSHARED suffix, whose name
// another walk already bound, is renamed positionally on the parsed pattern
// (never by textual substitution — variable names, labels and relation types
// overlap freely in one clause, and a regex rename on the security artifact is
// not acceptable). Without it two walks' unrelated variables would silently
// join.
func generateProducerSpec(walks []*parsedWalk) string {
	type prior struct {
		chain   []string
		clauses []*linearPattern
		rename  map[string]string
	}

	var emitted []string
	var branches []string
	var priors []prior
	globalBound := map[string]bool{anchorWalkActorVar: true}

	for wi, pw := range walks {
		bestLen, bestIdx := 0, -1
		for pi := range priors {
			n := commonClausePrefix(priors[pi].chain, pw.walk.Chain)
			if n > bestLen {
				bestLen, bestIdx = n, pi
			}
		}

		// walkBound is what THIS walk has already bound — the shared prefix's
		// variables plus the actor plus everything its own earlier clauses
		// bound. A reference to one of those is the walk's own binding and is
		// never renamed; only a variable this walk binds FRESH, whose name
		// another walk already took, is scoped away.
		rename := map[string]string{}
		walkBound := map[string]bool{anchorWalkActorVar: true}
		if bestIdx >= 0 {
			for _, v := range patternVarNames(priors[bestIdx].clauses[:bestLen]) {
				walkBound[v] = true
				if nn, ok := priors[bestIdx].rename[v]; ok {
					rename[v] = nn
				}
			}
		}

		for ci := bestLen; ci < len(pw.clauses); ci++ {
			lp := pw.clauses[ci]
			for _, v := range patternVarNames([]*linearPattern{lp}) {
				if walkBound[v] {
					continue
				}
				walkBound[v] = true
				if globalBound[v] {
					nn := scopedVarName(v, wi, globalBound)
					rename[v] = nn
					globalBound[nn] = true
					continue
				}
				globalBound[v] = true
			}
			emitted = append(emitted, lp.render(rename))
		}

		anchorVar := pw.walk.AnchorVar
		if nn, ok := rename[anchorVar]; ok {
			anchorVar = nn
		}
		branches = append(branches, collectBranch(pw.walk.AnchorType, anchorVar, pw.relNames))
		priors = append(priors, prior{chain: pw.walk.Chain, clauses: pw.clauses, rename: rename})
	}

	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(anchorWalkHead)
	b.WriteString("\n")
	for _, c := range emitted {
		b.WriteString("OPTIONAL MATCH ")
		b.WriteString(c)
		b.WriteString("\n")
	}
	b.WriteString("RETURN\n  ")
	b.WriteString(anchorWalkActorVar)
	b.WriteString(".key AS actorKey,\n")
	for i, br := range branches {
		b.WriteString("  ")
		b.WriteString(br)
		if i < len(branches)-1 {
			b.WriteString(" +")
		}
		b.WriteString("\n")
	}
	b.WriteString("  AS readableAnchors\n")
	return b.String()
}

// collectBranch renders one walk's grant branch. `via` is the walk's full
// relation list in order — derived, so the third hand-typed copy of the chain
// disappears. It is audit-only: capabilityread.IsReadable matches NanoID to
// NanoID and never reads it.
func collectBranch(anchorType, anchorVar string, relNames []string) string {
	quoted := make([]string, len(relNames))
	for i, r := range relNames {
		quoted[i] = "'" + r + "'"
	}
	return fmt.Sprintf(
		"collect(DISTINCT {anchorType: '%s', anchorId: nanoIdFromKey(%s.key), via: [%s]})",
		anchorType, anchorVar, strings.Join(quoted, ", "))
}

// commonClausePrefix counts the leading clauses two chains declare identically.
func commonClausePrefix(a, b []string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

// patternVarNames lists every variable the given clauses bind, in order.
func patternVarNames(clauses []*linearPattern) []string {
	var out []string
	for _, lp := range clauses {
		for _, v := range lp.nodeVars {
			out = append(out, v.name)
		}
		for _, v := range lp.relVars {
			out = append(out, v.name)
		}
	}
	return out
}

// scopedVarName derives a collision-free walk-scoped name for a variable two
// walks bind independently.
func scopedVarName(v string, walkIdx int, taken map[string]bool) string {
	base := v + "_w" + strconv.Itoa(walkIdx)
	if !taken[base] {
		return base
	}
	for i := 2; ; i++ {
		cand := base + "_" + strconv.Itoa(i)
		if !taken[cand] {
			return cand
		}
	}
}

// --- pattern parsing -------------------------------------------------------

// patternVar is one variable occurrence: its name and the byte span it occupies
// in the clause source, so a rename splices by position.
type patternVar struct {
	name       string
	start, end int
}

// linearPattern is one parsed chain clause — a single linear relationship
// pattern, which is the only shape a Walk.Chain entry may take.
type linearPattern struct {
	src      string
	nodeVars []patternVar
	relVars  []patternVar
	labels   map[string]string
	relTypes []string
}

// render returns the clause source with the given variables renamed, splicing
// by recorded byte span.
func (lp *linearPattern) render(rename map[string]string) string {
	type span struct {
		start, end int
		text       string
	}
	var spans []span
	add := func(vs []patternVar) {
		for _, v := range vs {
			if nn, ok := rename[v.name]; ok && nn != v.name {
				spans = append(spans, span{v.start, v.end, nn})
			}
		}
	}
	add(lp.nodeVars)
	add(lp.relVars)
	if len(spans) == 0 {
		return lp.src
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	var b strings.Builder
	prev := 0
	for _, s := range spans {
		b.WriteString(lp.src[prev:s.start])
		b.WriteString(s.text)
		prev = s.end
	}
	b.WriteString(lp.src[prev:])
	return b.String()
}

type patScanner struct {
	src string
	i   int
}

func isIdentByte(c byte) bool {
	return c == '_' ||
		(c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9')
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func (p *patScanner) eof() bool { return p.i >= len(p.src) }

func (p *patScanner) peek() byte {
	if p.eof() {
		return 0
	}
	return p.src[p.i]
}

func (p *patScanner) skipSpace() {
	for !p.eof() {
		switch p.src[p.i] {
		case ' ', '\t', '\n', '\r':
			p.i++
		default:
			return
		}
	}
}

func (p *patScanner) ident() (string, int, int) {
	start := p.i
	for !p.eof() && isIdentByte(p.src[p.i]) {
		p.i++
	}
	return p.src[start:p.i], start, p.i
}

// node parses `( [var] [:label] [{props}] )`.
func (p *patScanner) node(lp *linearPattern) error {
	if p.peek() != '(' {
		return fmt.Errorf("expected a node pattern `(...)` at offset %d", p.i)
	}
	p.i++
	p.skipSpace()
	var v patternVar
	if isIdentStart(p.peek()) {
		name, s, e := p.ident()
		v = patternVar{name: name, start: s, end: e}
	}
	p.skipSpace()
	label := ""
	if p.peek() == ':' {
		p.i++
		p.skipSpace()
		var ok bool
		label, _, _ = p.ident()
		ok = label != ""
		if !ok {
			return fmt.Errorf("expected a node label after ':' at offset %d", p.i)
		}
	}
	p.skipSpace()
	if p.peek() == '{' {
		depth := 0
		for !p.eof() {
			switch p.src[p.i] {
			case '{':
				depth++
			case '}':
				depth--
			}
			p.i++
			if depth == 0 {
				break
			}
		}
		if depth != 0 {
			return fmt.Errorf("unterminated property map in node pattern")
		}
	}
	p.skipSpace()
	if p.peek() != ')' {
		return fmt.Errorf("expected ')' to close the node pattern at offset %d", p.i)
	}
	p.i++
	if v.name != "" {
		lp.nodeVars = append(lp.nodeVars, v)
		if label != "" {
			if _, seen := lp.labels[v.name]; !seen {
				lp.labels[v.name] = label
			}
		}
	}
	return nil
}

// rel parses `-[ [var] :type [*range] ]->` in either direction.
//
// The relation type is REQUIRED and may name exactly one relation: an untyped
// hop (`-[*0..3]->`) would reach every neighbour over any relation, and an
// alternation (`[:a|:b]`) would reach two — in both cases the walk grants more
// than it says, and the derived `via` audit array would misreport what it
// traversed. A hop this grammar cannot name, it does not admit.
func (p *patScanner) rel(lp *linearPattern) error {
	leftArrow := false
	switch {
	case strings.HasPrefix(p.src[p.i:], "<-"):
		leftArrow = true
		p.i += 2
	case p.peek() == '-':
		p.i++
	case p.peek() == ',':
		return fmt.Errorf(
			"a chain clause must be a SINGLE linear relationship pattern — commas (multiple " +
				"patterns) are rejected, because a clause not connected to the actor seeds by scan " +
				"and would cross-product every matching vertex into every actor's grants")
	default:
		return fmt.Errorf("expected a relationship pattern at offset %d", p.i)
	}
	if p.peek() != '[' {
		return fmt.Errorf("expected '[' to open the relationship pattern at offset %d", p.i)
	}
	p.i++
	p.skipSpace()
	var v patternVar
	if isIdentStart(p.peek()) {
		name, s, e := p.ident()
		v = patternVar{name: name, start: s, end: e}
	}
	p.skipSpace()
	if p.peek() != ':' {
		return fmt.Errorf(
			"a chain hop must name its relation type (`-[:relation]->`) — an untyped hop " +
				"would traverse every relation and grant more than the walk declares")
	}
	p.i++
	p.skipSpace()
	relType, _, _ := p.ident()
	if relType == "" {
		return fmt.Errorf("expected a relationship type after ':' at offset %d", p.i)
	}
	for !p.eof() && p.src[p.i] != ']' {
		switch p.src[p.i] {
		case '(', '{', '[':
			return fmt.Errorf("unexpected %q inside a relationship pattern at offset %d", p.src[p.i], p.i)
		case '|':
			return fmt.Errorf(
				"a chain hop must name exactly ONE relation type — an alternation reaches " +
					"more than the derived `via` audit array would report; declare one walk per relation")
		}
		p.i++
	}
	if p.eof() {
		return fmt.Errorf("unterminated relationship pattern")
	}
	p.i++
	rightArrow := strings.HasPrefix(p.src[p.i:], "->")
	switch {
	case rightArrow:
		p.i += 2
	case p.peek() == '-':
		p.i++
	default:
		return fmt.Errorf("expected the relationship pattern to close with '-' or '->' at offset %d", p.i)
	}
	if leftArrow && rightArrow {
		return fmt.Errorf("a chain hop must have one direction, not both (`<-[...]->`)")
	}
	if v.name != "" {
		lp.relVars = append(lp.relVars, v)
	}
	lp.relTypes = append(lp.relTypes, relType)
	return nil
}

// parseLinearPattern parses one chain clause, enforcing the single-linear-
// pattern grammar a Walk.Chain entry must satisfy.
func parseLinearPattern(src string) (*linearPattern, error) {
	trimmed := strings.TrimSpace(src)
	if trimmed == "" {
		return nil, fmt.Errorf("empty chain clause")
	}
	lp := &linearPattern{src: trimmed, labels: map[string]string{}}
	p := &patScanner{src: trimmed}
	p.skipSpace()
	if err := p.node(lp); err != nil {
		return nil, err
	}
	for {
		p.skipSpace()
		if p.eof() {
			break
		}
		if err := p.rel(lp); err != nil {
			return nil, err
		}
		p.skipSpace()
		if err := p.node(lp); err != nil {
			return nil, err
		}
	}
	if len(lp.relTypes) == 0 {
		return nil, fmt.Errorf(
			"a chain clause must contain at least one relationship — a bare node binds nothing new")
	}
	return lp, nil
}

// --- spec text helpers -----------------------------------------------------

// anchorVarOf returns the variable a spec aliases to `anchor`, or "".
//
// Both ends are identifier-bounded on purpose: without the RIGHT boundary a
// decoy column like `identity.key AS anchorId` reads as the anchor alias, and
// without the LEFT one `art.key AS anchor` reads as variable `t`'s. Either
// misread hands the caller the wrong anchor variable, which is how a
// non-self-anchored lens would slip past the Walk requirement.
func anchorVarOf(spec string) string {
	norm := normalizeWhitespace(spec)
	for _, at := range aliasToAnchorOffsets(norm) {
		end := at
		start := end
		for start > 0 && isIdentByte(norm[start-1]) {
			start--
		}
		if start != end {
			return norm[start:end]
		}
	}
	return ""
}

// aliasToAnchorOffsets returns the offset of each `.key AS anchor` occurrence in
// a normalized spec whose `anchor` is a whole word (not `anchorId` &c).
func aliasToAnchorOffsets(norm string) []int {
	const suffix = ".key AS anchor"
	var out []int
	for idx := 0; ; {
		at := strings.Index(norm[idx:], suffix)
		if at < 0 {
			return out
		}
		at += idx
		end := at + len(suffix)
		if end >= len(norm) || !isIdentByte(norm[end]) {
			out = append(out, at)
		}
		idx = end
	}
}

// aliasesKeyToAnchor reports whether the normalized tail aliases exactly
// `<v>.key` to `anchor`.
func aliasesKeyToAnchor(norm, v string) bool {
	for _, at := range aliasToAnchorOffsets(norm) {
		start := at
		for start > 0 && isIdentByte(norm[start-1]) {
			start--
		}
		if norm[start:at] == v {
			return true
		}
	}
	return false
}

// isSelfAnchoredSpec reports whether the spec binds v with `{key: $actorKey}` —
// the actor itself, covered by the platform base cap-read self-grant.
//
// `key: $actorKey` must appear as its own PROPERTY, not merely as a substring:
// a lens carrying `(wo:workorder {note: "key: $actorKey"})` is not self-anchored,
// and reading it as such would exempt it from declaring a Walk.
func isSelfAnchoredSpec(spec, v string) bool {
	norm := normalizeWhitespace(spec)
	needle := "(" + v + ":"
	for idx := 0; ; {
		at := strings.Index(norm[idx:], needle)
		if at < 0 {
			return false
		}
		at += idx
		open := strings.Index(norm[at:], "{")
		closeParen := strings.Index(norm[at:], ")")
		if open >= 0 && (closeParen < 0 || open < closeParen) {
			closeBrace := strings.Index(norm[at+open:], "}")
			if closeBrace >= 0 && hasActorKeyProperty(norm[at+open+1:at+open+closeBrace]) {
				return true
			}
		}
		idx = at + len(needle)
	}
}

// hasActorKeyProperty reports whether a property-map body declares
// `key: $actorKey` as one of its own comma-separated entries.
func hasActorKeyProperty(body string) bool {
	for _, part := range strings.Split(body, ",") {
		if strings.TrimSpace(part) == "key: $actorKey" {
			return true
		}
	}
	return false
}

// normalizeWhitespace collapses every run of whitespace to a single space so a
// structural check is insensitive to how a spec is laid out.
func normalizeWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// containsWord reports whether sub occurs in s not followed by another
// identifier byte — so `AS inst` does not match `AS instanceKey`.
func containsWord(s, sub string) bool {
	for idx := 0; ; {
		at := strings.Index(s[idx:], sub)
		if at < 0 {
			return false
		}
		at += idx
		end := at + len(sub)
		if end >= len(s) || !isIdentByte(s[end]) {
			return true
		}
		idx = end
	}
}
