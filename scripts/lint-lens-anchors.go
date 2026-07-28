//go:build ignore

// lint-lens-anchors — structural guard for the read-grant / lens
// dual-enumeration seam (the "footgun"). A non-self-anchored Personal
// (nats-subject) Lens projects a row keyed on some vertex OTHER than the
// recipient identity, and Refractor's D1 readableAnchors gate
// (internal/refractor/projection/personal.go → capabilityread.IsReadable)
// SILENTLY drops that row unless the actor's cap-read.* slices grant the
// anchor's bare NanoID. Nothing at runtime reports the drop.
//
// The walk is DECLARED once, as `pkgmgr.AnchorWalk`, and pkgmgr compiles both
// artifacts from it (internal/pkgmgr/anchorwalk.go): the lens's own reachability
// prefix and the whole read-grant producer. So the invariant this gate enforces
// is the declaration itself:
//
//   - Every Personal lens either anchors on the ACTOR (its anchor variable
//     bound `{key: $actorKey}` — covered by the platform base cap-read
//     self-grant) or declares `Walks`.
//   - A package that ships Personal lenses does not ALSO hand-author a Path-B
//     cap-read producer (`ProjectionKind: "actorAggregate"` writing a
//     `cap-read.*` key). Those are generated per declared ReadGrantDomain; a
//     hand-authored one beside its own lenses restates every walk it grants,
//     which is the footgun itself. A cap-read slice with no lens counterpart
//     (the kernel's own self-anchor base is this shape) stays legal.
//
// Path A producers (`GrantTable: true`, Postgres `actor_read_grants`) are a
// different mechanism — a row→anchor comprehension meeting actor→anchor grants
// at the anchor vocabulary, not one walk duplicated — and are untouched here.
//
// pkgmgr's expansion pass rejects the missing-Walks violation at package-build
// time too (and a registry-wide test compiles every shipped package), but only
// this gate catches BOTH across every package in CI without an install — which
// is what binds the NEXT author.
//
// Runs per package under packages/. STRICT=1 exits non-zero on any issue.
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type lens struct {
	name           string // CanonicalName
	adapter        string
	personal       bool
	hasWalks       bool
	projectionKind string
	hasOutput      bool
	outputKey      string // Output.OutputKeyPattern, "" when not statically resolvable
	grantTable     bool
	spec           string // resolved cypher (the presentation tail only, with Walks)
	pos            token.Position
}

var reAnchor = regexp.MustCompile(`(\w+)\.key\s+AS\s+anchor\b`)

// isSelfAnchored reports whether v is bound with `{key: $actorKey}` — the actor
// itself, covered by the base self-grant.
func isSelfAnchored(cypher, v string) bool {
	re := regexp.MustCompile(`\(\s*` + regexp.QuoteMeta(v) + `\s*:\s*[A-Za-z0-9_]+\s*\{[^}]*key\s*:\s*\$actorKey`)
	return re.MatchString(cypher)
}

func main() {
	strict := os.Getenv("STRICT") == "1"
	for _, a := range os.Args[1:] {
		if a == "--strict" {
			strict = true
		}
	}

	dirs, _ := filepath.Glob("packages/*")
	issues, warns := 0, 0
	for _, dir := range dirs {
		fi, err := os.Stat(dir)
		if err != nil || !fi.IsDir() {
			continue
		}
		i, w := checkPackage(dir)
		issues += i
		warns += w
	}

	if issues == 0 && warns == 0 {
		fmt.Println("lint-lens-anchors: 0 issues — every non-self-anchored Personal lens declares its read-grant Walks")
		return
	}
	fmt.Printf("lint-lens-anchors: %d issue(s), %d advisory warning(s)\n", issues, warns)
	if strict && issues > 0 {
		os.Exit(1)
	}
}

// checkPackage parses one package directory and enforces both invariants over
// its lens declarations.
func checkPackage(dir string) (issues, warns int) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		// A package that does not parse is another gate's problem, not ours.
		return 0, 0
	}

	consts := map[string]string{}
	var lenses []lens
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			collectConsts(f, consts)
		}
	}
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			lenses = append(lenses, collectLenses(fset, f, consts)...)
		}
	}

	// A hand-authored Path-B producer is only a DUPLICATED walk if this package
	// also ships Personal lenses whose walks it would be restating. A package
	// with none has no walk to duplicate — the carve-out for a cap-read slice
	// with no lens counterpart (the kernel's own self-anchor base is this shape)
	// stays legal.
	shipsPersonalLens := false
	for _, l := range lenses {
		if l.adapter == "nats-subject" && l.personal {
			shipsPersonalLens = true
			break
		}
	}

	for _, l := range lenses {
		if shipsPersonalLens && l.projectionKind == "actorAggregate" && !l.grantTable && l.hasOutput {
			switch {
			case strings.HasPrefix(l.outputKey, "cap-read."):
				fmt.Printf("%s: lens %s hand-authors a Path-B cap-read producer (%s) beside this package's own Personal lenses — declare a pkgmgr.ReadGrantDomain and let their Walks compile it; a hand-authored producer restates every walk it grants, which is the dual-enumeration footgun\n",
					posOf(l), l.name, l.outputKey)
				issues++
				continue
			case l.outputKey == "":
				// Fail closed: a pattern this gate cannot read statically may
				// well be a cap-read key.
				fmt.Printf("%s: lens %s is an actorAggregate producer whose OutputKeyPattern is neither a string literal nor a string const, so this gate cannot tell whether it writes a cap-read.* slice — spell it as one so the Path-B rule stays checkable\n",
					posOf(l), l.name)
				issues++
				continue
			}
		}

		if l.adapter != "nats-subject" || !l.personal {
			continue
		}
		if l.hasWalks {
			continue // non-self by construction, and its producer is compiled
		}
		m := reAnchor.FindStringSubmatch(l.spec)
		if m == nil {
			fmt.Printf("%s: warn: Personal lens %s declares no Walks and has no `<var>.key AS anchor` — cannot verify its read-grant coverage\n", posOf(l), l.name)
			warns++
			continue
		}
		if isSelfAnchored(l.spec, m[1]) {
			continue // self-anchored — base cap-read self-grant covers it
		}
		fmt.Printf("%s: Personal lens %s anchors on `%s` (not the actor) but declares no Walks — pkgmgr cannot compile its read-grant producer, so Refractor's D1 gate would silently drop every row it projects (the 'forgot the slice' dual-enumeration bug). Declare Walks with the actor→anchor chain and a GrantDomain.\n",
			posOf(l), l.name, m[1])
		issues++
	}
	return issues, warns
}

func posOf(l lens) string {
	return fmt.Sprintf("%s:%d", l.pos.Filename, l.pos.Line)
}

// collectConsts records every string const's value (backtick or quoted).
func collectConsts(f *ast.File, out map[string]string) {
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, s := range gd.Specs {
			vs, ok := s.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				if bl, ok := vs.Values[i].(*ast.BasicLit); ok && bl.Kind == token.STRING {
					if v, err := strconv.Unquote(bl.Value); err == nil {
						out[name.Name] = v
					}
				}
			}
		}
	}
}

// collectLenses finds every `[]pkgmgr.LensSpec{...}` composite literal and reads
// each element's fields, resolving the Spec const to its cypher.
func collectLenses(fset *token.FileSet, f *ast.File, consts map[string]string) []lens {
	var out []lens
	ast.Inspect(f, func(n ast.Node) bool {
		cl, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		at, ok := cl.Type.(*ast.ArrayType)
		if !ok || !isLensSpecSelector(at.Elt) {
			return true
		}
		for _, e := range cl.Elts {
			el, ok := e.(*ast.CompositeLit)
			if !ok {
				continue
			}
			l := lens{pos: fset.Position(el.Pos())}
			for _, fe := range el.Elts {
				kv, ok := fe.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok {
					continue
				}
				switch key.Name {
				case "CanonicalName":
					l.name = stringLit(kv.Value)
				case "Adapter":
					l.adapter = stringLit(kv.Value)
				case "ProjectionKind":
					l.projectionKind = stringLit(kv.Value)
				case "Walks":
					// An explicit `Walks: nil` or an empty slice literal
					// declares nothing.
					switch v := kv.Value.(type) {
					case *ast.Ident:
						l.hasWalks = v.Name != "nil"
					case *ast.CompositeLit:
						l.hasWalks = len(v.Elts) > 0
					default:
						l.hasWalks = true
					}
				case "Personal":
					if id, ok := kv.Value.(*ast.Ident); ok {
						l.personal = id.Name == "true"
					}
				case "GrantTable":
					if id, ok := kv.Value.(*ast.Ident); ok {
						l.grantTable = id.Name == "true"
					}
				case "Output":
					l.hasOutput = true
					l.outputKey = outputKeyPattern(kv.Value, consts)
				case "Spec":
					switch v := kv.Value.(type) {
					case *ast.Ident:
						l.spec = consts[v.Name]
					case *ast.BasicLit:
						if s, err := strconv.Unquote(v.Value); err == nil {
							l.spec = s
						}
					}
				}
			}
			out = append(out, l)
		}
		return true
	})
	return out
}

// outputKeyPattern reads OutputKeyPattern out of an
// `&pkgmgr.OutputDescriptorSpec{...}` literal, resolving a bare const
// reference. Returns "" when the expression is not statically resolvable — the
// caller treats that as UNVERIFIABLE rather than as "not a cap-read producer",
// so a computed key pattern cannot walk past the gate.
func outputKeyPattern(e ast.Expr, consts map[string]string) string {
	if u, ok := e.(*ast.UnaryExpr); ok {
		e = u.X
	}
	cl, ok := e.(*ast.CompositeLit)
	if !ok {
		return ""
	}
	for _, fe := range cl.Elts {
		kv, ok := fe.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		id, ok := kv.Key.(*ast.Ident)
		if !ok || id.Name != "OutputKeyPattern" {
			continue
		}
		if lit := stringLit(kv.Value); lit != "" {
			return lit
		}
		if ref, ok := kv.Value.(*ast.Ident); ok {
			return consts[ref.Name]
		}
		return ""
	}
	return ""
}

func isLensSpecSelector(e ast.Expr) bool {
	se, ok := e.(*ast.SelectorExpr)
	return ok && se.Sel != nil && se.Sel.Name == "LensSpec"
}

func stringLit(e ast.Expr) string {
	if bl, ok := e.(*ast.BasicLit); ok && bl.Kind == token.STRING {
		if v, err := strconv.Unquote(bl.Value); err == nil {
			return v
		}
	}
	return ""
}
