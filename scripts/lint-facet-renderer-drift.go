//go:build ignore

// lint-facet-renderer-drift — the FORK-1 vocabulary-parity guard
// (edge-showcase-app-design.md FORK-1): the descriptor-form vocabulary is a
// build-to spec (docs/components/edge-manifest.md), not yet a frozen
// contract, precisely BECAUSE only one renderer proved it. A second renderer
// (`clients/facet-swiftui-spike`) now exists, and the freeze trigger is
// vocabulary completeness — both renderers detecting the SAME schema-derived
// field kinds. That parity has no compiler to enforce it: `cmd/facet/web/app.js`'s
// `renderField` and `FacetManifestKit`'s `DescriptorForm.fieldKind` are two
// independent implementations in two languages, and nothing stops one from
// growing a field kind the other silently falls back to `.text`/free-text
// for — an op that WORKS in the PWA and quietly loses type fidelity in the
// second renderer, with no build failure to say so.
//
// This gate is that measure: for each vocabulary member below, both source
// files must contain the literal marker that detects it. A member present in
// one file and missing from the other is drift — exactly the gap §7.12's
// freeze-trigger retarget (2026-08-02) named and this fire closes.
//
// STRICT=1 (CI) exits non-zero on any drift; unset, it warns.
package main

import (
	"fmt"
	"os"
	"strings"
)

const (
	appJS           = "cmd/facet/web/app.js"
	descriptorSwift = "clients/facet-swiftui-spike/Sources/FacetManifestKit/DescriptorForm.swift"
)

// vocabMember is one field kind both renderers must detect from the same
// `inputSchema` shape. jsMarkers/swiftMarkers are literal substrings that
// must appear in each file's descriptor-rendering source — not an AST
// comparison (deliberately: a substring match is cheap, has no per-language
// parser to maintain, and is exactly as strict as the covenant needs it to
// be — a renderer that stops detecting a kind stops containing its marker).
type vocabMember struct {
	name        string
	jsMarkers   []string
	swiftMarker string
}

var vocabulary = []vocabMember{
	{name: "boolean", jsMarkers: []string{`schema.type === "boolean"`}, swiftMarker: `schemaType == "boolean"`},
	{name: "enum", jsMarkers: []string{`schema.enum`}, swiftMarker: `schema["enum"]`},
	{name: "money", jsMarkers: []string{`"money"`, `Cents$/`}, swiftMarker: `hasSuffix("Cents")`},
	{name: "date", jsMarkers: []string{`schema.format === "date"`}, swiftMarker: `case "date": return .date`},
	{name: "date-time", jsMarkers: []string{`"date-time"`}, swiftMarker: `"date-time"`},
	{name: "entity-ref", jsMarkers: []string{`schema["x-entityRef"]`}, swiftMarker: `x-entityRef`},
}

func main() {
	js, err := os.ReadFile(appJS)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lint-facet-renderer-drift: %v\n", err)
		os.Exit(2)
	}
	sw, err := os.ReadFile(descriptorSwift)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lint-facet-renderer-drift: %v\n", err)
		os.Exit(2)
	}
	jsSrc, swSrc := string(js), string(sw)

	var issues []string
	for _, m := range vocabulary {
		jsHas := true
		for _, marker := range m.jsMarkers {
			if !strings.Contains(jsSrc, marker) {
				jsHas = false
				break
			}
		}
		swiftHas := strings.Contains(swSrc, m.swiftMarker)
		switch {
		case jsHas && !swiftHas:
			issues = append(issues, fmt.Sprintf(
				"%q: %s detects it, %s does not — the second renderer has fallen behind the PWA's vocabulary",
				m.name, appJS, descriptorSwift))
		case swiftHas && !jsHas:
			issues = append(issues, fmt.Sprintf(
				"%q: %s detects it, %s does not — the PWA has fallen behind the second renderer's vocabulary",
				m.name, descriptorSwift, appJS))
		}
	}

	if len(issues) == 0 {
		fmt.Println("lint-facet-renderer-drift: clean — descriptor vocabulary is in step across both renderers")
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
