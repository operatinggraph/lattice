//go:build ignore

// lint-package-version.go — a packages/ content edit must bump that package's
// manifest version, or running stacks never see it: plain install no-ops an
// unchanged version, so a permission/lens/DDL change silently fails to reach
// any live stack (docs/components/_packages.md "Refresh / upgrade").
//
// Run via `make lint-package-version` or
//
//	go run ./scripts/lint-package-version.go
//
// Modes:
//   - Local (no DIFF_BASE): compares the working tree + index (and untracked
//     files under packages/) against HEAD — run it before committing a
//     packages/ change.
//   - Range (DIFF_BASE=<sha>, set by CI): compares DIFF_BASE..DIFF_HEAD
//     (DIFF_HEAD defaults to HEAD). CI passes the pushed range (push:
//     github.event.before; PR: the base sha). A base missing from a shallow
//     clone is fetched by SHA; if it still can't be resolved the gate skips
//     with a notice rather than failing the build on git plumbing.
//
// A package's "content" is every file under packages/<name>/ except *_test.go
// and *.md — the files that shape what install writes. Two kinds of Go edit are
// not content, because the Definition they compile to is byte-identical and
// install has nothing new to write: a diff that only rewrites import specifiers
// naming the module itself (importOnly), and a diff that only rewrites comments
// (commentOnlyGoChange). The version check reads manifest.yaml's `version:` value; package.go's
// Definition.Version is pinned to it by every package's
// TestPackage_ManifestMatchesDefinition, so one bumped value implies both.
package main

import (
	"bytes"
	"fmt"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

var versionRe = regexp.MustCompile(`(?m)^version:\s*"?([^"\s#]+)`)

// importSpecRe matches a Go import specifier alone on a unified-diff line —
// optional `import` keyword, optional alias or blank identifier, quoted path.
var importSpecRe = regexp.MustCompile(`^[+-]\s*(?:import\s+)?(?:_\s+|[A-Za-z0-9_.]+\s+)?"([^"]+)"\s*$`)

var modulePathRe = regexp.MustCompile(`(?m)^module\s+(\S+)`)

func main() {
	base := strings.TrimSpace(os.Getenv("DIFF_BASE"))
	head := strings.TrimSpace(os.Getenv("DIFF_HEAD"))
	if head == "" {
		head = "HEAD"
	}

	var changed []string
	rangeMode := base != "" && !isZeroSHA(base)
	if rangeMode {
		if !ensureCommit(base) {
			fmt.Printf("lint-package-version: base %s unavailable (shallow clone, fetch failed) — skipping.\n", base)
			return
		}
		changed = gitLines("diff", "--name-only", base, head)
	} else {
		changed = gitLines("diff", "--name-only", "HEAD")
		changed = append(changed, gitLines("ls-files", "--others", "--exclude-standard", "packages/")...)
	}

	modulePaths := modulePathsIn(rangeMode, base, head)

	contentChanged := map[string]int{}
	for _, path := range changed {
		pkg, ok := packageOf(path)
		if !ok || strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, ".md") {
			continue
		}
		if importOnly(rangeMode, base, head, path, modulePaths) {
			continue
		}
		// A comment edit cannot alter a byte of the Definition this file
		// compiles to, and extorting a version bump for one publishes a package
		// revision asserting a content change that did not happen — every stack
		// then diff-applies the package for nothing. Same exemption, and the
		// same reason, as walkGeneratorConsumers below.
		if strings.HasSuffix(path, ".go") && commentOnlyGoChange(rangeMode, base, head, path) {
			continue
		}
		contentChanged[pkg]++
	}
	generatorDriven := map[string]bool{}
	for _, pkg := range walkGeneratorConsumers(rangeMode, base, head, changed) {
		if contentChanged[pkg] == 0 {
			generatorDriven[pkg] = true
		}
		contentChanged[pkg]++
	}
	if len(contentChanged) == 0 {
		fmt.Println("lint-package-version: clean — no packages/ content changes.")
		return
	}

	baseRef := "HEAD"
	if rangeMode {
		baseRef = base
	}
	pkgs := make([]string, 0, len(contentChanged))
	for pkg := range contentChanged {
		pkgs = append(pkgs, pkg)
	}
	sort.Strings(pkgs)

	violations := 0
	for _, pkg := range pkgs {
		manifest := "packages/" + pkg + "/manifest.yaml"
		baseVer, baseOK := versionAt(baseRef, manifest)
		if !baseOK {
			// New package in this range — any version it declares is fresh.
			continue
		}
		var headVer string
		var headOK bool
		if rangeMode {
			headVer, headOK = versionAt(head, manifest)
		} else {
			headVer, headOK = versionOnDisk(manifest)
		}
		if !headOK {
			if len(gitLines("ls-files", "packages/"+pkg+"/")) == 0 {
				continue // package deleted in this range — nothing to install
			}
			fmt.Printf("lint-package-version: packages/%s content changed but it has no readable manifest.yaml version\n", pkg)
			violations++
			continue
		}
		if headVer == baseVer {
			if generatorDriven[pkg] {
				fmt.Printf("lint-package-version: packages/%s declares ReadGrantDomains and %s changed, so its GENERATED read-grant producer lens may differ, but manifest.yaml version is unchanged at %s\n", pkg, walkGeneratorDir, headVer)
			} else {
				fmt.Printf("lint-package-version: packages/%s content changed (%d file(s)) but manifest.yaml version is unchanged at %s\n", pkg, contentChanged[pkg], headVer)
			}
			fmt.Printf("  bump %s `version:` (+ package.go Definition.Version — parity is test-pinned);\n", manifest)
			fmt.Printf("  an unchanged version no-ops plain install, so this change never reaches a running stack.\n")
			violations++
		}
	}
	if violations > 0 {
		fmt.Printf("lint-package-version: %d package(s) need a version bump.\n", violations)
		os.Exit(1)
	}
	fmt.Printf("lint-package-version: clean — %d changed package(s), all version-bumped (or new).\n", len(pkgs))
}

// packageOf extracts the package name from a packages/<name>/... path.
func packageOf(path string) (string, bool) {
	rest, ok := strings.CutPrefix(path, "packages/")
	if !ok {
		return "", false
	}
	name, _, found := strings.Cut(rest, "/")
	if !found || name == "" {
		return "", false
	}
	return name, true
}

func isZeroSHA(s string) bool {
	return strings.Trim(s, "0") == ""
}

// modulePathsIn returns the module paths declared in go.mod at both ends of the
// comparison, so an import rewritten across a module rename is recognised at
// either name.
func modulePathsIn(rangeMode bool, base, head string) []string {
	var srcs []string
	if rangeMode {
		if out, err := exec.Command("git", "show", base+":go.mod").Output(); err == nil {
			srcs = append(srcs, string(out))
		}
		if out, err := exec.Command("git", "show", head+":go.mod").Output(); err == nil {
			srcs = append(srcs, string(out))
		}
	} else {
		if out, err := exec.Command("git", "show", "HEAD:go.mod").Output(); err == nil {
			srcs = append(srcs, string(out))
		}
	}
	if out, err := os.ReadFile("go.mod"); err == nil {
		srcs = append(srcs, string(out))
	}
	seen := map[string]bool{}
	var paths []string
	for _, src := range srcs {
		if m := modulePathRe.FindStringSubmatch(src); m != nil && !seen[m[1]] {
			seen[m[1]] = true
			paths = append(paths, m[1])
		}
	}
	return paths
}

// importOnly reports whether every line the diff changes in path is a Go import
// specifier naming one of modulePaths. False when nothing changed — an
// untracked file has no diff and must still count as content.
func importOnly(rangeMode bool, base, head, path string, modulePaths []string) bool {
	if len(modulePaths) == 0 {
		return false
	}
	args := []string{"diff", "-U0"}
	if rangeMode {
		args = append(args, base, head)
	} else {
		args = append(args, "HEAD")
	}
	args = append(args, "--", path)

	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return false
	}
	changedLines := 0
	for _, ln := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(ln, "+") && !strings.HasPrefix(ln, "-") {
			continue
		}
		if strings.HasPrefix(ln, "+++") || strings.HasPrefix(ln, "---") {
			continue
		}
		changedLines++
		m := importSpecRe.FindStringSubmatch(ln)
		if m == nil || !underAnyModule(m[1], modulePaths) {
			return false
		}
	}
	return changedLines > 0
}

// underAnyModule reports whether an import path is the module itself or one of
// its subpackages.
func underAnyModule(importPath string, modulePaths []string) bool {
	for _, mod := range modulePaths {
		if importPath == mod || strings.HasPrefix(importPath, mod+"/") {
			return true
		}
	}
	return false
}

// ensureCommit makes sure the base SHA is resolvable, fetching it by SHA into
// a shallow clone if needed.
func ensureCommit(sha string) bool {
	if exec.Command("git", "cat-file", "-e", sha+"^{commit}").Run() == nil {
		return true
	}
	_ = exec.Command("git", "fetch", "--depth=1", "origin", sha).Run()
	return exec.Command("git", "cat-file", "-e", sha+"^{commit}").Run() == nil
}

// versionAt reads the manifest's version value at a git ref; ok=false when the
// file does not exist there or carries no version line.
func versionAt(ref, path string) (string, bool) {
	out, err := exec.Command("git", "show", ref+":"+path).Output()
	if err != nil {
		return "", false
	}
	return parseVersion(string(out))
}

// versionOnDisk reads the manifest's version value from the working tree.
func versionOnDisk(path string) (string, bool) {
	out, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return parseVersion(string(out))
}

func parseVersion(src string) (string, bool) {
	m := versionRe.FindStringSubmatch(src)
	if m == nil {
		return "", false
	}
	return m[1], true
}

func gitLines(args ...string) []string {
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "lint-package-version: git %s: %v\n", strings.Join(args, " "), err)
		os.Exit(2)
	}
	var lines []string
	for _, ln := range strings.Split(string(out), "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			lines = append(lines, ln)
		}
	}
	return lines
}

// walkGeneratorDir holds the compiler that turns a package's declared
// AnchorWalks into its cap-read read-grant producer lens. The producer is never
// written in packages/ — pkgmgr emits it — so a change here alters the installed
// lens content of every package that declares a ReadGrantDomain while leaving
// that package's own files untouched. The whole directory is the trigger, not
// one file: splitting the generator across files must not reopen the gap.
const walkGeneratorDir = "internal/pkgmgr/"

// commentOnlyGoChange reports whether a Go file's only change is to its
// comments. It compares the two revisions as ASTs parsed WITHOUT comments and
// re-printed canonically, rather than by inspecting diff lines: this directory
// emits Cypher, where `//` opens a comment too, so a changed line inside a raw
// string literal reads exactly like a Go comment to any line-based test. The
// AST cannot be fooled that way — if a single token of code moved, the printed
// forms differ.
//
// A parse failure on either side answers false, so an unreadable revision is
// treated as a real change and the gate stays fail-closed.
//
// Directive comments are the exception the AST cannot see: dropping comments
// drops `//go:build`, `//go:embed` and `//go:generate` with them, yet each one
// is content. A build tag decides whether the file compiles into the package at
// all — packages/lease-signing carries four constrained files whose tag picks
// which freshness/renewal window the installed Definition gets — so the
// directive lines are compared separately, and any difference answers false.
func commentOnlyGoChange(rangeMode bool, base, head, path string) bool {
	oldRef := "HEAD"
	if rangeMode {
		oldRef = base
	}
	oldSrc, err := exec.Command("git", "show", oldRef+":"+path).Output()
	if err != nil {
		return false
	}

	var newSrc []byte
	if rangeMode {
		newSrc, err = exec.Command("git", "show", head+":"+path).Output()
	} else {
		newSrc, err = os.ReadFile(path)
	}
	if err != nil {
		return false
	}

	oldAST, ok := canonicalGo(path, oldSrc)
	if !ok {
		return false
	}
	newAST, ok := canonicalGo(path, newSrc)
	if !ok {
		return false
	}
	if !bytes.Equal(oldAST, newAST) {
		return false
	}
	return slices.Equal(goDirectives(oldSrc), goDirectives(newSrc))
}

// goDirectives lists a file's `//go:` directive lines in source order. Matched
// after trimming leading space and without asking where the directive sits: a
// line inside a raw string literal that merely looks like one is admitted, and
// that only ever pushes the comparison toward "this is a real change".
func goDirectives(src []byte) []string {
	var out []string
	for _, ln := range strings.Split(string(src), "\n") {
		if t := strings.TrimSpace(ln); strings.HasPrefix(t, "//go:") {
			out = append(out, t)
		}
	}
	return out
}

// canonicalGo renders src as source text with every comment dropped, so two
// revisions differing only in comments render identically.
func canonicalGo(path string, src []byte) ([]byte, bool) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, 0) // no parser.ParseComments
	if err != nil {
		return nil, false
	}
	var buf bytes.Buffer
	if err := (&printer.Config{Mode: printer.RawFormat, Tabwidth: 8}).Fprint(&buf, fset, file); err != nil {
		return nil, false
	}
	return buf.Bytes(), true
}

// readGrantDomainDecl is the declaration that makes a package a consumer of the
// walk generator.
const readGrantDomainDecl = "ReadGrantDomains"

// walkGeneratorConsumers lists the packages whose generated producer lens the
// changed set puts in doubt: empty unless the generator itself changed, and
// then every package declaring a ReadGrantDomain. Those packages need a version
// bump exactly as an in-package edit would, because the content that reaches a
// running stack changed for them too.
func walkGeneratorConsumers(rangeMode bool, base, head string, changed []string) []string {
	touched := false
	for _, path := range changed {
		if strings.HasPrefix(path, walkGeneratorDir) && strings.HasSuffix(path, ".go") &&
			!strings.HasSuffix(path, "_test.go") {
			// A change that survives comment-stripping is a real one; a pure
			// comment edit cannot alter a single byte the generator emits, and
			// extorting a version bump for one would publish a package revision
			// asserting a content change that did not happen — every stack then
			// diff-applies the package for nothing. Mirrors importOnly above.
			if commentOnlyGoChange(rangeMode, base, head, path) {
				continue
			}
			touched = true
			break
		}
	}
	if !touched {
		return nil
	}

	entries, err := os.ReadDir("packages")
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if packageDeclaresReadGrantDomains(filepath.Join("packages", e.Name())) {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// packageDeclaresReadGrantDomains reports whether any non-test Go file in dir
// declares a ReadGrantDomain.
func packageDeclaresReadGrantDomains(dir string) bool {
	files, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, f := range files {
		name := f.Name()
		if f.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		if strings.Contains(string(src), readGrantDomainDecl) {
			return true
		}
	}
	return false
}
