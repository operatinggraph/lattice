//go:build ignore

// lint-facet-discovery — the structural guard for Facet's founding covenant
// (edge-showcase-app-design.md §1, re-established by
// facet-discovery-restoration-design.md): cmd/facet hardcodes ONLY login
// machinery, deployment config, and the service-agnostic descriptor-vocabulary
// interpreter. Everything vertical arrives as DATA (manifest rows, op
// descriptors, pane descriptors).
//
// The covenant did not die in a rogue commit — it eroded through
// individually-ratified widenings that nothing measured in aggregate. This
// gate is that measure: design docs cannot override it silently, because
// widening the permitted surface requires editing the ALLOWLISTS BELOW in the
// same diff, each entry carrying its citation. That edit in a review is the
// visibility the covenant's erosion never had.
//
// Rules, over cmd/facet non-test sources (Go, js/mjs, html, css — comments
// INCLUDED, because stale vertical facts accumulate in comments first):
//
//	R1 — key-shape literals: `vtx.<type>.` / `lnk.<type>.` may name only the
//	     platform-generic types in allowedVertexTypes.
//	R2 — SQL FROM literals: only the tables in allowedSQLTables. (The pane
//	     executor names NO table — tables arrive as descriptor data; that is
//	     the point.)
//	R3 — vertical canary words: the unambiguous vertical vocabulary in
//	     bannedWords never appears, in any casing, comments included.
//	R4 — operationType literals: only the identity-plane ceremony ops +
//	     ClaimTask in allowedOpLiterals. Vertical ops are dispatched from
//	     descriptors, never by name.
//
// Test files (_test.go, *.test.mjs) are exempt: fixtures are data, and tests
// legitimately pin domain-shaped vectors against the generic machinery.
//
// STRICT=1 (CI) exits non-zero on any violation; unset, it warns.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// allowedVertexTypes are the platform-generic key shapes the covenant's
// interpreter may construct or parse.
var allowedVertexTypes = map[string]string{
	"identity":        "covenant surface — session/claim machinery (edge-showcase-app-design.md §7.1/§7.2)",
	"task":            "covenant vocabulary — the task archetype (edge-showcase-app-design.md §3.2)",
	"meta":            "covenant vocabulary — op/pane meta keys (edge-showcase-app-design.md §3.3)",
	"credentialindex": "ratified ceremony surface (edge-showcase-app-design.md §7.2 Inc 3); its undiscoverability is board-filed vocabulary work ('Five identity ceremony ops stay undiscoverable')",
}

// allowedSQLTables are the only read-model tables cmd/facet may name in a SQL
// string literal.
var allowedSQLTables = map[string]string{
	"read_identity_credentials": "ratified credentials pane (edge-showcase-app-design.md §7.2 Inc 3)",
}

// allowedOpLiterals are the only operationType string literals cmd/facet
// sources may carry.
var allowedOpLiterals = map[string]string{
	"ClaimIdentity":           "the claim ceremony (edge-showcase-app-design.md §7.1)",
	"InitiateCredentialLink":  "credential-link ceremony (edge-showcase-app-design.md §7.2 Inc 3)",
	"CompleteCredentialLink":  "credential-link ceremony (edge-showcase-app-design.md §7.2 Inc 3)",
	"UnlinkCredential":        "credential-unlink ceremony (edge-showcase-app-design.md §7.2 Inc 3)",
	"ClaimTask":              "the ratified claim affordance (facet-staff-worlds-design.md F2; facet-entity-browse-design.md §6.1)",
}

// bannedWords is the vertical canary: unambiguous domain vocabulary that has
// no business in a discovery-driven client, in code OR comments. Deliberately
// excludes collision-prone words whose vertex types R1 already covers
// (session, tab, provider, booking-adjacent "book" prose is fine — "booking"
// itself is banned).
var bannedWords = []string{
	// verticals
	"clinic", "cafe", "café", "wellness", "loftspace", "laundry",
	// vertical entity/role vocabulary
	"patient", "visitseries", "menuitem", "leaseapp", "lease",
	"appointment", "studio", "booking", "clinician",
	"resident", "applicant", "tenant", "landlord",
	// seed personas/places (they read as fixtures and go stale as facts)
	"riley", "osei", "okafor", "vinyasa", "riverside",
}

var (
	keyShapeRe = regexp.MustCompile(`(?:vtx|lnk)\.([a-z][a-zA-Z0-9]*)\.`)
	sqlFromRe  = regexp.MustCompile(`(?i)FROM\s+"?([a-z_]{3,})"?`)
	opLitRe    = regexp.MustCompile(`(?i)operationtype"?\s*[:=]+\s*"([A-Za-z]+)"|"operationType"\s*,\s*"([A-Za-z]+)"`)
	opCmpRe    = regexp.MustCompile(`(?i)operationtype\s*===?\s*"([A-Za-z]+)"`)
)

type issue struct {
	file string
	line int
	msg  string
}

func main() {
	root := "cmd/facet"
	var issues []issue

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		name := info.Name()
		ext := filepath.Ext(name)
		switch ext {
		case ".go", ".js", ".mjs", ".html", ".css", ".webmanifest", ".svg":
		default:
			return nil
		}
		if strings.HasSuffix(name, "_test.go") || strings.HasSuffix(name, ".test.mjs") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lines := strings.Split(string(raw), "\n")
		for i, line := range lines {
			ln := i + 1

			// R1 — key-shape literals.
			for _, m := range keyShapeRe.FindAllStringSubmatch(line, -1) {
				if _, ok := allowedVertexTypes[m[1]]; !ok {
					issues = append(issues, issue{path, ln,
						fmt.Sprintf("R1: key shape names vertex type %q — only platform-generic types are permitted; a vertical key must arrive as descriptor/lens data (or extend allowedVertexTypes with a citation)", m[1])})
				}
			}

			// R2 — SQL FROM literals (Go sources only; the web client runs no SQL).
			if ext == ".go" {
				for _, m := range sqlFromRe.FindAllStringSubmatch(line, -1) {
					table := strings.ToLower(m[1])
					if !strings.HasPrefix(table, "read_") {
						continue // FROM in prose/comments ("from the mirror")
					}
					if _, ok := allowedSQLTables[table]; !ok {
						issues = append(issues, issue{path, ln,
							fmt.Sprintf("R2: SQL names table %q — pane reads arrive as descriptor data; a new literal table needs an allowlist entry with a citation", table)})
					}
				}
			}

			// R3 — vertical canary words (word-boundary, any casing).
			lower := strings.ToLower(line)
			for _, w := range bannedWords {
				idx := 0
				for {
					j := strings.Index(lower[idx:], w)
					if j < 0 {
						break
					}
					j += idx
					before := j == 0 || !isWordChar(lower[j-1])
					after := j+len(w) >= len(lower) || !isWordChar(lower[j+len(w)])
					if before && after {
						issues = append(issues, issue{path, ln,
							fmt.Sprintf("R3: vertical vocabulary %q — a discovery-driven client carries no vertical words, comments included", w)})
						break
					}
					idx = j + len(w)
				}
			}

			// R4 — operationType literals.
			for _, re := range []*regexp.Regexp{opLitRe, opCmpRe} {
				for _, m := range re.FindAllStringSubmatch(line, -1) {
					op := m[1]
					if op == "" && len(m) > 2 {
						op = m[2]
					}
					if op == "" {
						continue
					}
					if _, ok := allowedOpLiterals[op]; !ok {
						issues = append(issues, issue{path, ln,
							fmt.Sprintf("R4: operationType literal %q — ops dispatch from descriptors, never by name (or extend allowedOpLiterals with a citation)", op)})
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "lint-facet-discovery: walk: %v\n", err)
		os.Exit(2)
	}

	if len(issues) == 0 {
		fmt.Println("lint-facet-discovery: clean")
		return
	}
	for _, is := range issues {
		fmt.Printf("%s:%d: %s\n", is.file, is.line, is.msg)
	}
	fmt.Printf("lint-facet-discovery: %d issue(s)\n", len(issues))
	if os.Getenv("STRICT") == "1" {
		os.Exit(1)
	}
}

func isWordChar(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '_' || b >= 'A' && b <= 'Z'
}
