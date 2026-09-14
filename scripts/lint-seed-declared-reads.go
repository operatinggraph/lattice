//go:build ignore

// lint-seed-declared-reads — a seed or verify tool that dispatches an operation
// must declare the same optional reads the op's own descriptor declares.
//
// WHY. A script's refusal is a predicate over its declared reads, and the
// descriptor (OpMetaSpec.Dispatch.OptionalReads) is the op's own statement of
// which keys that predicate consumes. The `scripts/` seeds and verify tools
// dispatch the same ops by hand, and a hand-built envelope that stopped
// tracking the descriptor is the tell that the caller never considered the
// state the op now refuses on: it reuses a fixture the op will reject, and
// its must-accept helper turns a designed refusal into an aborted seed
// (wellness `TombstoneSession`/`SessionStarted` 2026-09-14; café
// `OpenTab`/`TenancyEnded` the same day — the second sighting that promoted
// the dossier's dispatcher-census check to this gate). Parity of the declared
// reads is the mechanizable half of that check: a seed that must add
// `.tenancy` to its envelope is a seed whose author met the term.
//
// WHAT IT CHECKS. Every `scripts/*.go` (lint-/gen- tools excluded) is parsed;
// each dispatch site is either a call carrying a registered op-name literal
// and a `&processor.ContextHint{…}` argument, or an `OperationEnvelope{…}`
// literal with an `OperationType:` literal. From the site's `OptionalReads`
// composite the gate collects every string-literal fragment (`x + ".decision"`
// contributes ".decision"; a bare literal contributes itself; a key-building
// helper contributes its literal arguments, so `linkKey(a, "manages", b)`
// contributes the relation). For the op's
// descriptor it derives one marker per OptionalReads template — the trailing
// `.<aspect>` of an aspect template, the relation of a link template — and
// requires each marker to occur in some declared fragment.
//
// BOUNDARY (stated, not hidden). Templates naming `{actor` / `{me.` are the
// op's self-scope probes; a seed dispatching as the operator (scope=any) never
// declares them, so they are not required. A template keyed on a payload field
// the descriptor's InputSchema does not list as `required` binds only on the
// path that supplies the field, so it is not required either. A site whose OptionalReads is not a
// `[]string{…}` composite, or an element built by a call with no literal
// argument at all, cannot be read statically and is listed as UNMODELLED under VERBOSE=1 — it is not a
// verdict either way, and a new site should prefer the literal shape so the
// gate can see it. An op with no descriptor, or a descriptor with no
// OptionalReads, has nothing to require.
//
// Run: STRICT=1 go run ./scripts/lint-seed-declared-reads.go
package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/operatinggraph/lattice/internal/pkgregistry"
)

// descriptorReads maps an op name to the markers its descriptor's
// OptionalReads templates require of an operator-scope dispatcher.
func descriptorReads() map[string][]string {
	out := map[string][]string{}
	for _, def := range pkgregistry.All() {
		for _, m := range def.OpMetas {
			if m.Dispatch == nil {
				continue
			}
			required := requiredPayloadFields(m.InputSchema)
			var markers []string
			for _, tmpl := range m.Dispatch.OptionalReads {
				if strings.Contains(tmpl, "{actor") || strings.Contains(tmpl, "{me.") || !allPayloadFieldsRequired(tmpl, required) {
					continue
				}
				if mk := markerOf(tmpl); mk != "" {
					markers = append(markers, mk)
				}
			}
			if len(markers) > 0 {
				out[m.OperationType] = markers
			}
		}
	}
	return out
}

// markerOf reduces a descriptor read template to the fragment a hand-built
// envelope must contain: a link template's relation segment, an aspect
// template's trailing `.<localName>`; a bare vertex template has none.
func markerOf(tmpl string) string {
	// Placeholders carry dots of their own ({payload.applicant:id}); fold each
	// to one segment before splitting the key.
	flat := placeholder.ReplaceAllString(tmpl, "X")
	if strings.HasPrefix(flat, "lnk.") {
		parts := strings.Split(flat, ".")
		if len(parts) >= 4 {
			return "." + parts[3] + "."
		}
		return ""
	}
	if i := strings.LastIndex(flat, "X"); i >= 0 && i+1 < len(flat) && strings.HasPrefix(flat[i+1:], ".") {
		return flat[i+1:]
	}
	return ""
}

var placeholder = regexp.MustCompile(`\{[^}]*\}`)

// payloadField captures the field a `{payload.<field>[:id]}` placeholder names.
var payloadField = regexp.MustCompile(`\{payload\.([A-Za-z0-9_]+)(?::id)?\}`)

// requiredPayloadFields reads the descriptor's InputSchema `required` list —
// a probe keyed on an OPTIONAL payload field (an instructor's own ledBy link
// on a staff call-off) binds only on the path that supplies the field, so no
// dispatcher is required to declare it.
func requiredPayloadFields(schema string) map[string]bool {
	var doc struct {
		Required []string `json:"required"`
	}
	out := map[string]bool{}
	if err := json.Unmarshal([]byte(schema), &doc); err != nil {
		return out
	}
	for _, f := range doc.Required {
		out[f] = true
	}
	return out
}

func allPayloadFieldsRequired(tmpl string, required map[string]bool) bool {
	for _, m := range payloadField.FindAllStringSubmatch(tmpl, -1) {
		if !required[m[1]] {
			return false
		}
	}
	return true
}

type site struct {
	file      string
	line      int
	op        string
	fragments []string
	modelled  bool
	hasReads  bool
}

// literalFragments collects the string literals under an expression tree —
// each BasicLit of a `+` chain, every element of a composite.
func literalFragments(e ast.Expr, out *[]string, modelled *bool) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind == token.STRING {
			if s, err := strconv.Unquote(v.Value); err == nil {
				*out = append(*out, s)
			}
		}
	case *ast.BinaryExpr:
		literalFragments(v.X, out, modelled)
		literalFragments(v.Y, out, modelled)
	case *ast.ParenExpr:
		literalFragments(v.X, out, modelled)
	case *ast.CallExpr:
		// A key-building helper is read by its literal arguments — a relation
		// word (`linkKey(a, "appliesToUnit", b)`) is the link's marker — so a
		// helper-built element does not hide the site from the verdict. A call
		// whose arguments carry no literal at all (a helper that derives every
		// key from state) is the genuinely opaque shape.
		before := len(*out)
		for _, a := range v.Args {
			literalFragments(a, out, modelled)
		}
		if len(*out) == before {
			*modelled = false
			return
		}
		for _, frag := range (*out)[before:] {
			if !strings.Contains(frag, ".") {
				*out = append(*out, "."+frag+".")
			}
		}
	case *ast.Ident, *ast.SelectorExpr, *ast.IndexExpr:
		// A variable in a concatenation carries no literal fragment of its own;
		// the literal beside it is what the gate reads.
	default:
		*modelled = false
	}
}

// hintReads reads a `ContextHint{…}` composite's OptionalReads.
func hintReads(cl *ast.CompositeLit, s *site) {
	for _, el := range cl.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if id, ok := kv.Key.(*ast.Ident); !ok || id.Name != "OptionalReads" {
			continue
		}
		s.hasReads = true
		arr, ok := kv.Value.(*ast.CompositeLit)
		if !ok {
			s.modelled = false
			return
		}
		s.modelled = true
		for _, e := range arr.Elts {
			literalFragments(e, &s.fragments, &s.modelled)
		}
	}
}

func isContextHint(e ast.Expr) (*ast.CompositeLit, bool) {
	if u, ok := e.(*ast.UnaryExpr); ok && u.Op == token.AND {
		e = u.X
	}
	cl, ok := e.(*ast.CompositeLit)
	if !ok {
		return nil, false
	}
	switch t := cl.Type.(type) {
	case *ast.SelectorExpr:
		return cl, t.Sel.Name == "ContextHint"
	case *ast.Ident:
		return cl, t.Name == "ContextHint"
	}
	return nil, false
}

func typeName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.SelectorExpr:
		return t.Sel.Name
	case *ast.Ident:
		return t.Name
	}
	return ""
}

func scanFile(path string, known map[string][]string) []site {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lint-seed-declared-reads: parse %s: %v\n", path, err)
		os.Exit(2)
	}
	var sites []site
	ast.Inspect(f, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.CallExpr:
			// Shape A: submit(ctx, conn, actor, "OpenTab", "tab", payload, &processor.ContextHint{…}).
			var op string
			var hint *ast.CompositeLit
			for _, a := range v.Args {
				if bl, ok := a.(*ast.BasicLit); ok && bl.Kind == token.STRING {
					if s, err := strconv.Unquote(bl.Value); err == nil {
						if _, isOp := known[s]; isOp && op == "" {
							op = s
						}
					}
				}
				if cl, ok := isContextHint(a); ok {
					hint = cl
				}
			}
			if op != "" && hint != nil {
				s := site{file: path, line: fset.Position(v.Pos()).Line, op: op}
				hintReads(hint, &s)
				sites = append(sites, s)
			}
		case *ast.CompositeLit:
			// Shape B: processor.OperationEnvelope{OperationType: "OpenTab", …, ContextHint: &processor.ContextHint{…}}.
			if typeName(v.Type) != "OperationEnvelope" {
				return true
			}
			var op string
			var hint *ast.CompositeLit
			for _, el := range v.Elts {
				kv, ok := el.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, _ := kv.Key.(*ast.Ident)
				if key == nil {
					continue
				}
				switch key.Name {
				case "OperationType":
					if bl, ok := kv.Value.(*ast.BasicLit); ok && bl.Kind == token.STRING {
						op, _ = strconv.Unquote(bl.Value)
					}
				case "ContextHint":
					if cl, ok := isContextHint(kv.Value); ok {
						hint = cl
					}
				}
			}
			if _, isOp := known[op]; isOp && hint != nil {
				s := site{file: path, line: fset.Position(v.Pos()).Line, op: op}
				hintReads(hint, &s)
				sites = append(sites, s)
			}
		}
		return true
	})
	return sites
}

func main() {
	strict := os.Getenv("STRICT") == "1"
	verbose := os.Getenv("VERBOSE") == "1"
	known := descriptorReads()

	files, _ := filepath.Glob("scripts/*.go")
	sort.Strings(files)
	var issues, unmodelled []string
	sitesSeen := 0
	for _, f := range files {
		base := filepath.Base(f)
		if strings.HasPrefix(base, "lint-") || strings.HasPrefix(base, "gen-") {
			continue
		}
		for _, s := range scanFile(f, known) {
			sitesSeen++
			if !s.hasReads {
				// The envelope declares no optional reads at all: every
				// descriptor marker is missing.
				s.modelled = true
			}
			if !s.modelled {
				unmodelled = append(unmodelled, fmt.Sprintf("%s:%d: %s — OptionalReads is not a []string literal; not checked", s.file, s.line, s.op))
				continue
			}
			var missing []string
			for _, mk := range known[s.op] {
				found := false
				for _, frag := range s.fragments {
					if strings.Contains(frag, mk) || strings.HasSuffix(frag, strings.TrimSuffix(mk, ".")) {
						found = true
						break
					}
				}
				if !found {
					missing = append(missing, mk)
				}
			}
			if len(missing) > 0 {
				issues = append(issues, fmt.Sprintf(
					"%s:%d: %s dispatch declares no %s — its descriptor's Dispatch.OptionalReads names it, so the op's refusals read it; declare it here (and ask what state this fixture may now be in that the op refuses).",
					s.file, s.line, s.op, strings.Join(missing, ", ")))
			}
		}
	}
	if verbose {
		for _, u := range unmodelled {
			fmt.Println("UNMODELLED " + u)
		}
	}
	if len(issues) == 0 {
		fmt.Printf("lint-seed-declared-reads: clean — %d dispatch site(s) checked against %d described op(s), %d unmodelled (VERBOSE=1 lists them)\n", sitesSeen, len(known), len(unmodelled))
		return
	}
	for _, i := range issues {
		fmt.Println(i)
	}
	fmt.Printf("lint-seed-declared-reads: %d issue(s)\n", len(issues))
	if strict {
		os.Exit(1)
	}
}
