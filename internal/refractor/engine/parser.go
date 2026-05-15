// Package engine implements the v1 openCypher parser and query compiler.
//
// Parse is a pure function: string → (*Query, error).
// It has no NATS dependency, performs no I/O, and has no side effects (ADR-6).
// All tests in this package run without any infrastructure.
package engine

import (
	"fmt"
	"strings"
	"unicode"
)

// ─── Lexer ──────────────────────────────────────────────────────────────────

type tokKind int

const (
	tEOF tokKind = iota
	tIdent
	tLParen   // (
	tRParen   // )
	tLBracket // [
	tRBracket // ]
	tColon    // :
	tDot      // .
	tComma    // ,
	tDash     // -
	tArrowR   // ->
	tArrowL   // <-
	tKeyword  // MATCH, OPTIONAL, RETURN, AS
)

// keywords (stored as upper-case canonical forms)
const (
	kwMATCH    = "MATCH"
	kwOPTIONAL = "OPTIONAL"
	kwRETURN   = "RETURN"
	kwAS       = "AS"
)

type token struct {
	kind  tokKind
	value string
	line  int
	col   int
}

type lexer struct {
	input []rune
	pos   int
	line  int
	col   int
}

func newLexer(input string) *lexer {
	return &lexer{input: []rune(input), line: 1, col: 1}
}

func (l *lexer) peek() (rune, bool) {
	if l.pos >= len(l.input) {
		return 0, false
	}
	return l.input[l.pos], true
}

func (l *lexer) advance() rune {
	ch := l.input[l.pos]
	l.pos++
	if ch == '\n' {
		l.line++
		l.col = 1
	} else {
		l.col++
	}
	return ch
}

func (l *lexer) skipWhitespace() {
	for {
		ch, ok := l.peek()
		if !ok || !unicode.IsSpace(ch) {
			break
		}
		l.advance()
	}
}

func (l *lexer) nextToken() token {
	l.skipWhitespace()

	ch, ok := l.peek()
	if !ok {
		return token{kind: tEOF, line: l.line, col: l.col}
	}

	line, col := l.line, l.col

	switch {
	case ch == '(':
		l.advance()
		return token{kind: tLParen, value: "(", line: line, col: col}
	case ch == ')':
		l.advance()
		return token{kind: tRParen, value: ")", line: line, col: col}
	case ch == '[':
		l.advance()
		return token{kind: tLBracket, value: "[", line: line, col: col}
	case ch == ']':
		l.advance()
		return token{kind: tRBracket, value: "]", line: line, col: col}
	case ch == ':':
		l.advance()
		return token{kind: tColon, value: ":", line: line, col: col}
	case ch == '.':
		l.advance()
		return token{kind: tDot, value: ".", line: line, col: col}
	case ch == ',':
		l.advance()
		return token{kind: tComma, value: ",", line: line, col: col}
	case ch == '-':
		l.advance()
		if next, ok2 := l.peek(); ok2 && next == '>' {
			l.advance()
			return token{kind: tArrowR, value: "->", line: line, col: col}
		}
		return token{kind: tDash, value: "-", line: line, col: col}
	case ch == '<':
		l.advance()
		if next, ok2 := l.peek(); ok2 && next == '-' {
			l.advance()
			return token{kind: tArrowL, value: "<-", line: line, col: col}
		}
		// '<' without '-' is not valid Cypher; surface as unknown for error reporting
		return token{kind: tIdent, value: "<", line: line, col: col}
	case ch == '_' || unicode.IsLetter(ch):
		var sb strings.Builder
		for {
			ch2, ok2 := l.peek()
			if !ok2 || (!unicode.IsLetter(ch2) && !unicode.IsDigit(ch2) && ch2 != '_') {
				break
			}
			sb.WriteRune(l.advance())
		}
		word := sb.String()
		upper := strings.ToUpper(word)
		if upper == kwMATCH || upper == kwOPTIONAL || upper == kwRETURN || upper == kwAS {
			return token{kind: tKeyword, value: upper, line: line, col: col}
		}
		return token{kind: tIdent, value: word, line: line, col: col}
	default:
		l.advance()
		return token{kind: tIdent, value: string(ch), line: line, col: col}
	}
}

func tokenize(input string) []token {
	l := newLexer(input)
	var tokens []token
	for {
		tok := l.nextToken()
		tokens = append(tokens, tok)
		if tok.kind == tEOF {
			break
		}
	}
	return tokens
}

// ─── Parser ─────────────────────────────────────────────────────────────────

type parser struct {
	tokens []token
	pos    int
}

func newParser(tokens []token) *parser {
	return &parser{tokens: tokens}
}

func (p *parser) peek() token {
	if p.pos >= len(p.tokens) {
		return token{kind: tEOF}
	}
	return p.tokens[p.pos]
}

func (p *parser) advance() token {
	tok := p.peek()
	if p.pos < len(p.tokens) {
		p.pos++
	}
	return tok
}

// expectKind consumes the next token if its kind matches, otherwise errors.
func (p *parser) expectKind(kind tokKind) (token, error) {
	tok := p.peek()
	if tok.kind != kind {
		return tok, fmt.Errorf("parse: unexpected token %q at line %d:%d", tok.value, tok.line, tok.col)
	}
	return p.advance(), nil
}

// expectKeyword consumes the next token if it is the given keyword, otherwise errors.
func (p *parser) expectKeyword(kw string) error {
	tok := p.peek()
	if tok.kind != tKeyword || tok.value != kw {
		return fmt.Errorf("parse: expected %q at line %d:%d, got %q", kw, tok.line, tok.col, tok.value)
	}
	p.advance()
	return nil
}

// expectIdent consumes an identifier token (tIdent or tKeyword used as name).
// In label/type/alias positions keywords may appear as names; we accept both.
func (p *parser) expectIdent() (string, error) {
	tok := p.peek()
	if tok.kind == tIdent || tok.kind == tKeyword {
		p.advance()
		return tok.value, nil
	}
	return "", fmt.Errorf("parse: expected identifier at line %d:%d, got %q", tok.line, tok.col, tok.value)
}

// ─── Grammar productions ─────────────────────────────────────────────────────

func (p *parser) parseQuery() (*Query, error) {
	var matches []MatchClause

	for {
		tok := p.peek()
		if tok.kind == tEOF {
			break
		}
		if tok.kind != tKeyword || (tok.value != kwMATCH && tok.value != kwOPTIONAL) {
			break
		}
		mc, err := p.parseMatchClause()
		if err != nil {
			return nil, err
		}
		matches = append(matches, mc)
	}

	if len(matches) == 0 {
		tok := p.peek()
		return nil, fmt.Errorf("parse: expected MATCH clause at line %d:%d, got %q", tok.line, tok.col, tok.value)
	}

	tok := p.peek()
	if tok.kind != tKeyword || tok.value != kwRETURN {
		return nil, fmt.Errorf("parse: RETURN clause is required, got %q at line %d:%d", tok.value, tok.line, tok.col)
	}

	ret, err := p.parseReturnClause()
	if err != nil {
		return nil, err
	}

	if tok := p.peek(); tok.kind != tEOF {
		return nil, fmt.Errorf("parse: unexpected token %q at line %d:%d after RETURN clause", tok.value, tok.line, tok.col)
	}

	q := &Query{Matches: matches, Return: ret}
	if err := detectCycles(q); err != nil {
		return nil, err
	}
	return q, nil
}

func (p *parser) parseMatchClause() (MatchClause, error) {
	optional := false
	if tok := p.peek(); tok.kind == tKeyword && tok.value == kwOPTIONAL {
		p.advance()
		optional = true
	}
	if err := p.expectKeyword(kwMATCH); err != nil {
		return MatchClause{}, err
	}

	pat, err := p.parsePattern()
	if err != nil {
		return MatchClause{}, err
	}
	patterns := []Pattern{pat}

	for p.peek().kind == tComma {
		p.advance()
		pat, err = p.parsePattern()
		if err != nil {
			return MatchClause{}, err
		}
		patterns = append(patterns, pat)
	}
	return MatchClause{Optional: optional, Patterns: patterns}, nil
}

func (p *parser) parsePattern() (Pattern, error) {
	node, err := p.parseNodePattern()
	if err != nil {
		return Pattern{}, err
	}
	nodes := []NodePattern{node}
	var edges []EdgePattern

	for {
		tok := p.peek()
		if tok.kind != tDash && tok.kind != tArrowL {
			break
		}
		edge, err := p.parseEdgePattern()
		if err != nil {
			return Pattern{}, err
		}
		edges = append(edges, edge)

		node, err = p.parseNodePattern()
		if err != nil {
			return Pattern{}, err
		}
		nodes = append(nodes, node)
	}
	return Pattern{Nodes: nodes, Edges: edges}, nil
}

func (p *parser) parseNodePattern() (NodePattern, error) {
	if _, err := p.expectKind(tLParen); err != nil {
		return NodePattern{}, fmt.Errorf("parse: expected '(' for node pattern: %w", err)
	}

	var variable, label string

	if tok := p.peek(); tok.kind == tIdent {
		variable = p.advance().value
	}

	if tok := p.peek(); tok.kind == tColon {
		p.advance()
		lbl, err := p.expectIdent()
		if err != nil {
			return NodePattern{}, fmt.Errorf("parse: expected label after ':': %w", err)
		}
		label = lbl
	}

	if _, err := p.expectKind(tRParen); err != nil {
		return NodePattern{}, fmt.Errorf("parse: expected ')' to close node pattern: %w", err)
	}
	return NodePattern{Variable: variable, Label: label}, nil
}

func (p *parser) parseEdgePattern() (EdgePattern, error) {
	tok := p.peek()

	if tok.kind == tArrowL {
		// <-[:TYPE]-
		p.advance()
		edge, err := p.parseEdgeBracket()
		if err != nil {
			return EdgePattern{}, err
		}
		if _, err := p.expectKind(tDash); err != nil {
			return EdgePattern{}, fmt.Errorf("parse: expected '-' after inbound relationship bracket: %w", err)
		}
		edge.Direction = Inbound
		return edge, nil
	}

	// -[:TYPE]->  or  -[:TYPE]-
	p.advance() // consume '-'
	edge, err := p.parseEdgeBracket()
	if err != nil {
		return EdgePattern{}, err
	}
	tok2 := p.peek()
	switch tok2.kind {
	case tArrowR:
		p.advance()
		edge.Direction = Outbound
	case tDash:
		p.advance()
		edge.Direction = Both
	default:
		return EdgePattern{}, fmt.Errorf("parse: expected '->' or '-' after relationship bracket at line %d:%d", tok2.line, tok2.col)
	}
	return edge, nil
}

func (p *parser) parseEdgeBracket() (EdgePattern, error) {
	if _, err := p.expectKind(tLBracket); err != nil {
		return EdgePattern{}, fmt.Errorf("parse: expected '[' for relationship pattern: %w", err)
	}

	var variable string
	if tok := p.peek(); tok.kind == tIdent {
		variable = p.advance().value
	}

	if _, err := p.expectKind(tColon); err != nil {
		return EdgePattern{}, fmt.Errorf("parse: expected ':' in relationship pattern: %w", err)
	}

	relType, err := p.expectIdent()
	if err != nil {
		return EdgePattern{}, fmt.Errorf("parse: expected relationship type: %w", err)
	}

	if _, err := p.expectKind(tRBracket); err != nil {
		return EdgePattern{}, fmt.Errorf("parse: expected ']' to close relationship pattern: %w", err)
	}
	return EdgePattern{Variable: variable, Type: relType}, nil
}

func (p *parser) parseReturnClause() (ReturnClause, error) {
	if err := p.expectKeyword(kwRETURN); err != nil {
		return ReturnClause{}, err
	}

	item, err := p.parseReturnItem()
	if err != nil {
		return ReturnClause{}, err
	}
	items := []ReturnItem{item}

	for p.peek().kind == tComma {
		p.advance()
		item, err = p.parseReturnItem()
		if err != nil {
			return ReturnClause{}, err
		}
		items = append(items, item)
	}
	return ReturnClause{Items: items}, nil
}

func (p *parser) parseReturnItem() (ReturnItem, error) {
	tok := p.peek()
	if tok.kind != tIdent {
		return ReturnItem{}, fmt.Errorf("parse: expected identifier in RETURN expression at line %d:%d, got %q", tok.line, tok.col, tok.value)
	}
	left := p.advance().value

	if _, err := p.expectKind(tDot); err != nil {
		return ReturnItem{}, fmt.Errorf("parse: expected '.' in RETURN expression: %w", err)
	}

	right, err := p.expectIdent()
	if err != nil {
		return ReturnItem{}, fmt.Errorf("parse: expected property name after '.': %w", err)
	}
	expr := left + "." + right

	if err := p.expectKeyword(kwAS); err != nil {
		return ReturnItem{}, fmt.Errorf("parse: expected AS in RETURN clause: %w", err)
	}

	alias, err := p.expectIdent()
	if err != nil {
		return ReturnItem{}, fmt.Errorf("parse: expected alias after AS: %w", err)
	}
	return ReturnItem{Expression: expr, Alias: alias}, nil
}

// ─── Post-parse validation ───────────────────────────────────────────────────

func detectCycles(q *Query) error {
	for _, m := range q.Matches {
		for _, pat := range m.Patterns {
			seen := make(map[string]bool, len(pat.Nodes))
			for _, n := range pat.Nodes {
				if n.Variable == "" {
					continue
				}
				if seen[n.Variable] {
					return fmt.Errorf("parse: circular relationship detected: variable %q appears multiple times in pattern", n.Variable)
				}
				seen[n.Variable] = true
			}
		}
	}
	return nil
}

// ─── Public API ──────────────────────────────────────────────────────────────

// Parse converts an openCypher v1 query string into an AST.
//
// It is a pure function: no I/O, no NATS dependency, no global state (ADR-6).
// Supported clauses: MATCH, OPTIONAL MATCH, RETURN. Keywords are case-insensitive.
// Returns an error for missing RETURN clause, syntax errors, and circular patterns.
func Parse(query string) (*Query, error) {
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("parse: query must not be empty")
	}
	tokens := tokenize(query)
	p := newParser(tokens)
	return p.parseQuery()
}
