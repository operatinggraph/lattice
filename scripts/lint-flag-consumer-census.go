//go:build ignore

// lint-flag-consumer-census — a process-wide FLAG may not gain a reader without
// that reader being declared here.
//
// # The class this closes
//
// "A widened operation silently drops the bound its narrow predecessor carried."
// A flag on a long-lived object starts life answering one question for one
// consumer, and its comment prices the consequences of being briefly wrong FOR
// THAT CONSUMER. Every later reader inherits the flag and that bound, and nobody
// re-reads the bound. Refractor's rebuild in-flight flag is the worked example:
// priced "bounded, because the sweep is a healer and the attestation reads
// `drained`", which holds for its first three readers and does not hold for a
// personal lens, which publishes NOTHING while the flag is set — there the same
// brief wrongness is a resumed flood of messages every device drops.
//
// A compiler cannot see this and a test cannot either: the new reader is correct
// on its own terms. What is missing is the re-reading. So the gate makes the
// reader set an explicit ledger, and adding a reader means editing the ledger —
// which is where the flag's own comment gets read again.
//
// # What it REFUSES, exactly
//
//  1. a read of a registered flag from a file+function the registry does not
//     declare. The fix is to declare it AND re-read the flag's documented bound
//     against what the new reader does with the answer — if the bound no longer
//     holds, the fix is the mechanism, not the ledger line;
//  2. a declared reader that no longer reads it (the shrink-only ledger rule
//     lint-refractor-single-instance uses): a ledger entry that stopped matching
//     is a gate inspecting nothing while reporting green.
//
// A read is a call or method value of the flag's ACCESSOR, and a `.Load()` on
// the flag's own field. Writers (`Add`, `Store`, `CompareAndSwap`) are not
// readers: they set the fact, they do not inherit its bound.
//
// # What it CANNOT see
//
//   - `_test.go` files, deliberately: a test that drives the flag is pinning the
//     mechanism, not inheriting its bound, and the churn would make the ledger
//     unmaintainable. A test is never the reader this class is about.
//   - A read reached indirectly — the accessor stored in a struct field or passed
//     as a func value and called elsewhere. The func-value FORM is caught at the
//     site that hands it over (which is the site that must re-read the bound);
//     where it is finally invoked is not.
//   - Any flag not in the registry. This is a ledger, not a discovery pass:
//     widening it to every atomic.Bool in the tree would report hundreds of
//     readers with no bound written down for any of them.
//
// STRICT=1 (CI) exits non-zero on any issue; unset, it warns.
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// flagSpec is one process-wide flag, its accessor, and the closed set of places
// that read it. `bound` names where the flag's own comment prices being wrong,
// so a refusal can point the new reader at the paragraph it must re-read.
type flagSpec struct {
	name     string
	accessor string
	field    string
	bound    string
	readers  []string // "<path>#<func>"
}

var registry = []flagSpec{{
	name:     "Pipeline.rebuildWindows (RebuildInFlight)",
	accessor: "RebuildInFlight",
	field:    "rebuildWindows",
	bound:    "internal/refractor/pipeline/pipeline.go, the rebuildWindows field comment",
	readers: []string{
		"internal/refractor/pipeline/pipeline.go#RebuildInFlight",
		"internal/refractor/pipeline/pipeline.go#Run",
		"internal/refractor/pipeline/rebuild.go#beginRebuildIfIdle",
		"internal/refractor/pipeline/sweep.go#suppressed",
		"internal/refractor/pipeline/audit.go#suppressed",
		"internal/refractor/pipeline/publishscope.go#eventPublishScope",
		"internal/refractor/pipeline/reproject_personal.go#ReprojectPersonalActor",
		"internal/refractor/pipeline/reproject.go#rebuildAbandons",
	},
}, {
	// The partition-scoped retraction's arming, gated on its READ SURFACE
	// rather than on the raw bool. partitionArmed is where the flag's three
	// halves are conjoined (activation, rule, audit), and a new reader of the
	// ARMING is a new place authorising a per-partition Delete — exactly what
	// this ledger exists to make someone re-read the bound for.
	//
	// The reader set is SHORT ON PURPOSE, and shrinking it is the point rather
	// than a side effect: the audit half makes this predicate LIVE, so a CDC
	// frame that asked it more than once could act on two different answers.
	// evaluateForEntryRaw reads it once and threads that value through the seed
	// decision, the multi-position producer and the tail, which is why none of
	// those three appears here. A new entry that is inside one event's frame
	// belongs upstream of that read, not beside it.
	name:     "Pipeline.partitionRetraction (partitionArmed)",
	accessor: "partitionArmed",
	field:    "partitionRetraction",
	bound:    "internal/refractor/pipeline/pipeline.go, the partitionArmed doc comment (its three halves, and why a frame reads it once)",
	readers: []string{
		"internal/refractor/pipeline/anchor_derivation_plain.go#plainDerivationIndexRefusal",
		"internal/refractor/pipeline/anchor_derivation_plain.go#noteStaticPlainDerivationRefusal",
		"internal/refractor/pipeline/evaluate.go#evaluateForEntryRaw",
		"internal/refractor/pipeline/retraction_transport.go#PlainRetractionTransport",
		"internal/refractor/pipeline/audit.go#listAnchorPartition",
	},
}, {
	// The ACTIVATION half alone, published for the gate that binds it. Its one
	// reader logs the armed verdict; a second reader deciding anything on this
	// half without the other two would be reading "the adapter and plane allow
	// it" as "it is on".
	name:     "Pipeline.partitionRetraction (PartitionRetraction)",
	accessor: "PartitionRetraction",
	field:    "partitionRetraction",
	bound:    "internal/refractor/pipeline/pipeline.go, the partitionRetraction field comment (\"It is never read alone\")",
	readers: []string{
		"cmd/refractor/retractiontransport.go#admitRetractionTransport",
	},
}}

// scanRoots are the trees a reader can live in. Both, not just the flag's own
// package: every accessor here is exported.
var scanRoots = []string{"internal", "cmd"}

func main() {
	strict := os.Getenv("STRICT") == "1"
	for _, a := range os.Args[1:] {
		if a == "--strict" {
			strict = true
		}
	}

	files, err := goFiles(scanRoots)
	if err != nil {
		fmt.Fprintln(os.Stderr, "lint-flag-consumer-census:", err)
		os.Exit(2)
	}

	var issues []string
	for _, spec := range registry {
		declared := map[string]bool{}
		for _, r := range spec.readers {
			declared[r] = false
		}
		for _, site := range readersOf(spec, files) {
			if _, ok := declared[site]; !ok {
				issues = append(issues, fmt.Sprintf(
					"%s: undeclared reader %s — declare it in scripts/lint-flag-consumer-census.go and re-read the flag's bound (%s) against what this reader does with the answer",
					spec.name, site, spec.bound))
				continue
			}
			declared[site] = true
		}
		for site, seen := range declared {
			if !seen {
				issues = append(issues, fmt.Sprintf(
					"%s: declared reader %s no longer reads it — drop the ledger line, or the census inspects nothing while reporting green",
					spec.name, site))
			}
		}
	}
	sort.Strings(issues)

	if len(issues) == 0 {
		fmt.Printf("lint-flag-consumer-census: 0 issues — %d flag(s), every reader declared\n", len(registry))
		return
	}
	fmt.Printf("lint-flag-consumer-census: %d issue(s)\n", len(issues))
	for _, s := range issues {
		fmt.Println(s)
	}
	if strict {
		os.Exit(1)
	}
}

// goFiles lists every non-test .go file under the given roots.
func goFiles(roots []string) ([]string, error) {
	var out []string
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			out = append(out, path)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// readersOf returns every "<path>#<func>" that reads spec, deduplicated.
func readersOf(spec flagSpec, files []string) []string {
	seen := map[string]bool{}
	fset := token.NewFileSet()
	for _, path := range files {
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			continue // a file that does not parse is the compiler's finding, not this gate's
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if readsFlag(n, spec) {
					seen[filepath.ToSlash(path)+"#"+fn.Name.Name] = true
				}
				return true
			})
		}
	}
	out := make([]string, 0, len(seen))
	for site := range seen {
		out = append(out, site)
	}
	sort.Strings(out)
	return out
}

// readsFlag reports whether n is a read of spec: a call or method value of the
// accessor, or a `.Load()` on the flag's own field. A write to the field
// (`Add`, `Store`, `CompareAndSwap`) sets the fact rather than inheriting its
// bound, so it is not a read.
func readsFlag(n ast.Node, spec flagSpec) bool {
	sel, ok := n.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if sel.Sel.Name == spec.accessor {
		return true
	}
	if sel.Sel.Name != "Load" {
		return false
	}
	inner, ok := sel.X.(*ast.SelectorExpr)
	return ok && inner.Sel.Name == spec.field
}
