//go:build ignore

// lint-facet-renderer-drift — the FORK-1 vocabulary-parity guard
// (edge-showcase-app-design.md FORK-1): the descriptor-form vocabulary is a
// build-to spec (docs/components/edge-manifest.md), not yet a frozen
// contract, precisely BECAUSE only one renderer proved it. Three independent
// renderers now detect it — `cmd/facet/web/app.js`'s `renderField`,
// `FacetManifestKit`'s `DescriptorForm.fieldKind`, and the staff-plane's
// shared `internal/descriptorform/form.mjs` — and the freeze trigger is
// vocabulary completeness across every one of them. That parity has no
// compiler to enforce it: three independent implementations in two
// languages, and nothing stops one from growing a field kind another
// silently falls back to `.text`/free-text for — an op that WORKS in one
// renderer and quietly loses type fidelity in another, with no build
// failure to say so.
//
// This gate is that measure: for each vocabulary member below, every
// renderer's source must contain the literal marker(s) that detect it. A
// member present in some renderers and missing from at least one other is
// drift — exactly the gap §7.12's freeze-trigger retarget (2026-08-02)
// named, extended to N renderers by staff-descriptor-rendering-design.md
// §13 when the second staff-plane renderer joined.
//
// STRICT=1 (CI) exits non-zero on any drift; unset, it warns.
package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

const (
	appJS           = "cmd/facet/web/app.js"
	descriptorSwift = "clients/facet-swiftui-spike/Sources/FacetManifestKit/DescriptorForm.swift"
	formJS          = "internal/descriptorform/form.mjs"
)

// renderers lists every source file this gate reads, keyed by the same name
// vocabMember.markers uses.
var renderers = []string{appJS, descriptorSwift, formJS}

// vocabMember is one field kind every renderer must detect from the same
// `inputSchema` shape. markers maps a renderer's source path to the literal
// substring(s) that must ALL appear in it for that renderer to count as
// detecting this kind — not an AST comparison (deliberately: a substring
// match is cheap, has no per-language parser to maintain, and is exactly as
// strict as the covenant needs it to be — a renderer that stops detecting a
// kind stops containing its marker).
type vocabMember struct {
	name    string
	markers map[string][]string
}

var vocabulary = []vocabMember{
	{name: "boolean", markers: map[string][]string{
		appJS:           {`schema.type === "boolean"`},
		descriptorSwift: {`schemaType == "boolean"`},
		formJS:          {`schema.type === "boolean"`},
	}},
	{name: "enum", markers: map[string][]string{
		appJS:           {`schema.enum`},
		descriptorSwift: {`schema["enum"]`},
		formJS:          {`schema.enum`},
	}},
	{name: "money", markers: map[string][]string{
		appJS:           {`"money"`, `Cents$/`},
		descriptorSwift: {`hasSuffix("Cents")`},
		formJS:          {`"money"`, `Cents$/`},
	}},
	{name: "date", markers: map[string][]string{
		appJS:           {`schema.format === "date"`},
		descriptorSwift: {`case "date": return .date`},
		formJS:          {`schema.format === "date"`},
	}},
	{name: "date-time", markers: map[string][]string{
		appJS:           {`"date-time"`},
		descriptorSwift: {`"date-time"`},
		formJS:          {`"date-time"`},
	}},
	{name: "entity-ref", markers: map[string][]string{
		appJS:           {`schema["x-entityRef"]`},
		descriptorSwift: {`x-entityRef`},
		formJS:          {`schema["x-entityRef"]`},
	}},
}

func main() {
	src := map[string]string{}
	for _, path := range renderers {
		b, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "lint-facet-renderer-drift: %v\n", err)
			os.Exit(2)
		}
		src[path] = string(b)
	}

	var issues []string
	for _, m := range vocabulary {
		var has, missing []string
		for _, renderer := range renderers {
			if detects(src[renderer], m.markers[renderer]) {
				has = append(has, renderer)
			} else {
				missing = append(missing, renderer)
			}
		}
		// Drift is a member some renderers detect and others don't. A member
		// no renderer declares markers for at all (has is empty) is simply
		// not yet in the vocabulary anywhere, not drift; a member every
		// renderer detects is parity.
		if len(has) == 0 || len(missing) == 0 {
			continue
		}
		issues = append(issues, fmt.Sprintf(
			"%q: %s detect it, %s do not — the vocabulary has drifted out of step",
			m.name, strings.Join(has, ", "), strings.Join(missing, ", ")))
	}
	sort.Strings(issues)

	if len(issues) == 0 {
		fmt.Println("lint-facet-renderer-drift: clean — descriptor vocabulary is in step across all renderers")
		return
	}
	for _, is := range issues {
		fmt.Println(is)
	}
	fmt.Printf("lint-facet-renderer-drift: %d drift issue(s)\n", len(issues))
	if os.Getenv("STRICT") == "1" {
		os.Exit(1)
	}
}

// detects reports whether src contains every one of markers — a renderer
// with no declared markers for a vocabulary member is treated as not
// detecting it (the zero-marker case never arises today: every member below
// declares markers for every renderer, but an empty slice must still read as
// "does not detect" rather than vacuously true).
func detects(src string, markers []string) bool {
	if len(markers) == 0 {
		return false
	}
	for _, marker := range markers {
		if !strings.Contains(src, marker) {
			return false
		}
	}
	return true
}
