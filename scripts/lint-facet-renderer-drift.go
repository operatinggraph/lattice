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

// vocabMember is one capability of the descriptor vocabulary every renderer
// must implement — mostly a field kind detected from the same `inputSchema`
// shape, and otherwise a `dispatch` column a renderer has to honour for one
// descriptor to mean the same thing wherever it is rendered. markers maps a
// renderer's source path to the literal substring(s) that must ALL appear in
// it for that renderer to count as implementing the member — not an AST
// comparison (deliberately: a substring match is cheap, has no per-language
// parser to maintain, and is exactly as strict as the covenant needs it to be
// — a renderer that stops implementing a member stops containing its marker).
type vocabMember struct {
	name    string
	markers map[string][]string
	// exempt lists renderers that deliberately do not implement this member —
	// a documented product decision, not an unnoticed gap (e.g. Swift's own
	// DescriptorForm.swift comment: `textarea` and `x-sensitive` are PWA
	// presentation choices over the SAME `.text` value, not a distinct
	// submission shape, so Swift carries no case for either). An exempt
	// renderer is excluded from the has/missing comparison entirely, so its
	// absence never reads as drift.
	exempt []string
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
	// textarea: a long string (maxLength > 120) renders as a multiline
	// control. Swift is EXEMPT (see vocabMember.exempt) — `textarea` and
	// `x-sensitive` are presentation choices over the same `.text` submission
	// shape there, not a distinct case, per DescriptorForm.swift's own comment.
	{name: "textarea", markers: map[string][]string{
		appJS:  {`(schema.maxLength || 0) > 120`},
		formJS: {`(schema.maxLength || 0) > 120`},
	}, exempt: []string{descriptorSwift}},
	// contextParams is a dispatch column rather than a field kind, and it is
	// the member whose loss in a single renderer is silent AND wrong in both
	// directions at once: that renderer asks a person to type a raw vertex key
	// the descriptor promised they would never see, and — for a field the
	// owning package dropped from `required` precisely because it declared a
	// contextParam — sends no value at all. Café's OpenTab spent an increment
	// unmigratable for exactly that reason.
	//
	// It takes TWO markers per renderer because the column has two halves and
	// shipping one without the other is the failure above: the field must be
	// EXCLUDED from what renders, and it must be FILLED at submit. Both are
	// spelled from the local variable rather than from the column name, which
	// keeps a prose mention of `dispatch.contextParams` in a doc comment from
	// satisfying the marker on its own.
	{name: "contextParams", markers: map[string][]string{
		appJS:           {`!(f in contextParams)`, `Object.entries(contextParams)`},
		descriptorSwift: {`!contextParamKeys.contains($0)`, `for (field, template) in contextParams`},
		formJS:          {`!(name in contextParams)`, `Object.entries(contextParams)`},
	}},
	// ceremony: the mint-and-reveal client contract (OpCeremonySpec,
	// internal/pkgmgr/definition.go) — a runtime that can't perform it must
	// REFUSE to offer the op rather than fall back to rendering the raw hash
	// field. Swift is EXEMPT: the facet-swiftui-spike is a shelved macOS-proxy
	// build with no ceremony-bearing op reachable from its manifest, and
	// implementing mint/hash/reveal there is unbuilt scope, not a gap this
	// gate should flag as drift.
	{name: "ceremony", markers: map[string][]string{
		appJS:  {`ceremonySupported`},
		formJS: {`ceremonySupported`},
	}, exempt: []string{descriptorSwift}},
	// selfAnchor: the `{me.<type>}` read/contextParams template — the
	// submitting identity's own vertex of that type, resolved off the
	// caller's declared selfAnchors. All three renderers carry the case.
	{name: "selfAnchor", markers: map[string][]string{
		appJS:           {`expr.startsWith("me.")`},
		descriptorSwift: {`expr.hasPrefix("me.")`},
		formJS:          {`expr.startsWith("me.")`},
	}},
	// entityColumn: the `{entity.<column>}` template (form.mjs also accepts
	// the `{context.<field>}` spelling as an alias, same resolution) — a
	// projected column of the row the form was opened from. Swift is
	// EXEMPT: `DescriptorContext` (DescriptorForm.swift) carries no row/
	// entity field at all, and its one production call site
	// (DescriptorFormSheet.swift) constructs it with only
	// `actorIdentityKey` — no caller anywhere threads a viewed row in, so
	// the template could never resolve there regardless of whether a case
	// existed for it. Unbuilt scope in the shelved macOS-proxy spike, not
	// new drift.
	{name: "entityColumn", markers: map[string][]string{
		appJS:  {`expr.startsWith("entity.")`},
		formJS: {`expr.startsWith("context.")`, `expr.startsWith("entity.")`},
	}, exempt: []string{descriptorSwift}},
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
		exempt := map[string]bool{}
		for _, r := range m.exempt {
			exempt[r] = true
		}
		var has, missing []string
		for _, renderer := range renderers {
			if exempt[renderer] {
				continue
			}
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
