//go:build ignore

// lint-doc-orphan — catches a doc comment that was orphaned from the thing it
// documents.
//
// THE DEFECT. A Go doc comment sits directly above its declaration with no
// blank line between them. So a cut taken at the `func` line — moving a
// declaration to another file, or reordering two declarations — leaves the
// comment behind. It does not become a dangling comment that anyone would
// notice: it silently WELDS onto the next declaration's own doc block, because
// there is no blank line to separate them. The result is two wrongs at once —
// the moved declaration is left undocumented, and its former neighbour is
// misdocumented by a paragraph describing a different function.
//
// Nothing else catches it. It compiles. `gofmt` is happy. `go vet` is happy.
// `golangci-lint` is happy. Every test passes. It is invisible to every gate
// this repo has, which is exactly why it kept happening.
//
// WHY THIS IS A GATE AND NOT A DOSSIER NOTE. The class was minted during the
// `pipeline.go` consolidation (Inc 2, `75f838b5`): `writeResults` moved to
// results.go and its 18-line doc comment stayed in pipeline.go, fused to
// `supersededRule`'s. It was recorded as a standing check. It then happened a
// SECOND time, independently, in `internal/refractor/ruleengine/full/executor.go`,
// where `propertyOf`'s doc sits welded above `resolveProperty` while
// `propertyOf` itself is declared 75 lines lower. A class seen twice gets
// mechanized (agents/steward/SKILL.md §4) — a human check that has already
// failed twice is not a check.
//
// THE RULE. For every top-level declaration carrying a doc comment: if the
// comment's FIRST line opens by naming an identifier, that identifier must be
// the declaration it sits above.
//
// FIRST LINE ONLY — this is load-bearing. Go doc comments routinely mention
// other identifiers in their bodies, and a naive scan over the whole block
// false-positives on ordinary prose. When this check was first prototyped as a
// grep over entire comment blocks, two of its three hits were bogus:
// `AuditOptions`' doc legitimately discussing its `AuthPlane` field, and
// `registrationFailedDecision`'s doc legitimately naming
// `registerWithFilterFallback` as the other writer it must agree with. Reading
// only the first line — the one position where Go convention puts the
// declaration's own name — removes that entire false-positive class.
//
// The identifier must also actually be DECLARED in the same package. That way a
// comment opening with an ordinary capitalized word, a section banner
// (`// --- expression evaluation ---`), or a `// Deprecated:` marker is never
// flagged: those name nothing the package declares.
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// roots are the trees whose Go source this repo owns and hand-maintains.
var roots = []string{"internal", "cmd"}

func main() {
	strict := os.Getenv("STRICT") == "1"

	dirs, err := packageDirs(roots)
	if err != nil {
		fmt.Fprintln(os.Stderr, "lint-doc-orphan:", err)
		os.Exit(2)
	}

	issues := 0
	for _, dir := range dirs {
		issues += checkPackage(dir)
	}

	if issues == 0 {
		fmt.Println("lint-doc-orphan: 0 issues — every doc comment names the declaration it sits above")
		return
	}
	fmt.Printf("lint-doc-orphan: %d issue(s)\n", issues)
	if strict {
		os.Exit(1)
	}
}

func packageDirs(roots []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, root := range roots {
		if _, err := os.Stat(root); os.IsNotExist(err) {
			continue
		}
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			dir := filepath.Dir(path)
			if !seen[dir] {
				seen[dir] = true
				out = append(out, dir)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func checkPackage(dir string) int {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		return strings.HasSuffix(fi.Name(), ".go")
	}, parser.ParseComments)
	if err != nil {
		// A package that does not parse is someone else's failure to report;
		// this gate stays silent rather than double-reporting a build break.
		return 0
	}

	issues := 0
	for _, pkg := range pkgs {
		declared := declaredNames(pkg)
		for path, file := range pkg.Files {
			if isGenerated(file) || strings.HasSuffix(path, "_test.go") {
				continue
			}
			for _, decl := range file.Decls {
				doc, names := docAndNames(decl)
				if doc == nil || len(doc.List) == 0 || len(names) == 0 {
					continue
				}
				subject, ok := firstLineSubject(doc.List[0].Text)
				if !ok || !declared[subject] {
					continue
				}
				if containsName(names, subject) {
					continue
				}
				// A grouped `const (...)` / `var (...)` block is legitimately
				// documented by the type whose values it enumerates — Loom's
				// `// Step ...` above StepKindSystemOp/StepKindUserTask is the
				// intended shape, not an orphan.
				if len(names) > 1 {
					continue
				}
				// A declaration whose own name CONTAINS the subject is naming
				// it deliberately. This is overwhelmingly the test convention —
				// `// emitHeartbeat ...` above TestEmitHeartbeat_WritesWithDerivedTTL —
				// and also covers wrappers like lensSweptThenHeld over
				// sweptThenHeld. Without this the gate is ~80% test-convention
				// noise and nobody would read its output.
				if strings.Contains(strings.ToLower(names[0]), strings.ToLower(subject)) {
					continue
				}
				pos := fset.Position(doc.List[0].Slash)
				fmt.Printf("%s:%d: doc comment opens by naming %q but sits above %s — %q is declared elsewhere in this package, so this comment was orphaned from it (a cut taken at the declaration line leaves the comment welded onto its former neighbour, invisibly to the compiler, gofmt, vet, golangci-lint and every test). Move the comment back above %q, or reword this line to name %s.\n",
					relPath(path), pos.Line, subject, strings.Join(names, "/"), subject, subject, strings.Join(names, "/"))
				issues++
			}
		}
	}
	return issues
}

// declaredNames collects every top-level identifier the package declares, so a
// doc comment opening with an ordinary word is never mistaken for an orphan.
func declaredNames(pkg *ast.Package) map[string]bool {
	out := map[string]bool{}
	for _, file := range pkg.Files {
		for _, decl := range file.Decls {
			_, names := docAndNames(decl)
			for _, n := range names {
				out[n] = true
			}
		}
	}
	return out
}

// docAndNames returns a declaration's doc comment and every identifier it
// declares. A grouped `const (...)` / `var (...)` block yields every name in the
// group, so a block comment naming any one member is accepted.
func docAndNames(decl ast.Decl) (*ast.CommentGroup, []string) {
	switch d := decl.(type) {
	case *ast.FuncDecl:
		return d.Doc, []string{d.Name.Name}
	case *ast.GenDecl:
		var names []string
		for _, spec := range d.Specs {
			switch s := spec.(type) {
			case *ast.TypeSpec:
				names = append(names, s.Name.Name)
			case *ast.ValueSpec:
				for _, n := range s.Names {
					names = append(names, n.Name)
				}
			}
		}
		return d.Doc, names
	}
	return nil, nil
}

// firstLineSubject extracts the identifier a doc comment's first line opens
// with, following the Go convention "// Name verbs ...". It reports ok=false for
// anything that is not that shape — section banners, `// Deprecated:` markers,
// and prose openings all fall out here.
func firstLineSubject(line string) (string, bool) {
	text := strings.TrimPrefix(line, "//")
	if text == line { // a /* */ block comment; not the convention this checks
		return "", false
	}
	fields := strings.Fields(text)
	if len(fields) < 2 {
		return "", false
	}
	subject := fields[0]
	if !isIdent(subject) {
		return "", false
	}
	// Require a following word that reads as prose rather than more punctuation,
	// so `// --- x ---` and similar never reach the declared-name lookup.
	if !isWordish(fields[1]) {
		return "", false
	}
	return subject, true
}

func isIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

func isWordish(s string) bool {
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			return true
		}
	}
	return false
}

func containsName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

func isGenerated(file *ast.File) bool {
	for _, cg := range file.Comments {
		for _, c := range cg.List {
			if strings.Contains(c.Text, "Code generated") && strings.Contains(c.Text, "DO NOT EDIT") {
				return true
			}
		}
	}
	return false
}

func relPath(p string) string {
	if rel, err := filepath.Rel(".", p); err == nil {
		return rel
	}
	return p
}
