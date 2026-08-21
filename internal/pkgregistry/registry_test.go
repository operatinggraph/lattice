package pkgregistry

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
)

// TestEveryShippedPackageIsRegistered asserts the registry covers the corpus in
// both directions: every row's key is its definition's own name (the manifest
// lookup key), and every packages/<dir> that ships a parsable manifest has a
// row. An unregistered package is invisible to every consumer at once — it
// cannot be installed by the CLI or by Loupe, and the `lint-package-standard`
// gate never sees it.
func TestEveryShippedPackageIsRegistered(t *testing.T) {
	for _, name := range Names() {
		def, _ := Lookup(name)
		if def.Name != name {
			t.Errorf("registry key %q maps to definition named %q", name, def.Name)
		}
	}
	dirs, err := os.ReadDir("../../packages")
	if err != nil {
		t.Fatalf("read packages dir: %v", err)
	}
	shipped := 0
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		manifest, err := pkgmgr.ParseManifest("../../packages/" + d.Name() + "/manifest.yaml")
		if err != nil {
			continue // not a package dir (no parsable manifest)
		}
		shipped++
		if _, ok := Lookup(manifest.Name); !ok {
			t.Errorf("packages/%s (manifest name %q) is missing from the registry", d.Name(), manifest.Name)
		}
		// A registry name is also a directory path for every consumer that
		// reads a package's own files by name — the lint-package-standard gate
		// stats packages/<name>/lens_cypher_test.go, and both the gate and this
		// test parse packages/<name>/manifest.yaml.
		if manifest.Name != d.Name() {
			t.Errorf("packages/%s declares manifest name %q — the directory and the manifest name must agree, since consumers resolve a package's files by its registered name", d.Name(), manifest.Name)
		}
	}
	if shipped == 0 {
		t.Fatal("no shipped package manifests found — the ../../packages scan is broken")
	}
}

// TestEveryPackageCompilesItsReadGrantWalks runs the install-time compilation
// every shipped package goes through, over the whole registry: read-grant walks
// must compile (which is where the walk grammar and the "every non-self-anchored
// Personal lens declares a Walk" invariant are enforced), and the on-disk
// manifest must agree with the COMPOSED definition — generated cap-read
// producers included, index-wise, so a manifest that lists them out of
// ReadGrantDomains order fails here rather than at install.
//
// A registered package whose manifest does not parse fails outright: it would
// fail the same way at install, and skipping would let the drift check be
// silently disarmed by a malformed file.
func TestEveryPackageCompilesItsReadGrantWalks(t *testing.T) {
	for _, name := range Names() {
		def, _ := Lookup(name)
		t.Run(name, func(t *testing.T) {
			if _, err := def.ExpandReadGrantWalks(); err != nil {
				t.Fatalf("read-grant walks do not compile: %v", err)
			}
			manifest, err := pkgmgr.ParseManifest("../../packages/" + name + "/manifest.yaml")
			if err != nil {
				t.Fatalf("manifest does not parse: %v", err)
			}
			if err := manifest.VerifyAgainstDefinition(def); err != nil {
				t.Errorf("manifest drifts from the composed definition: %v", err)
			}
		})
	}
}

// TestEveryPackagePassesOpDispatchTemplateGate is
// descriptor-floor-template-coverage-design.md §6's "gate breaks nothing"
// proof: it runs the REAL install-time rule —
// pkgmgr.Definition.ValidateOpDispatchTemplates — over every registered
// package's Dispatch.Reads/OptionalReads templates. pkgregistry is the only
// place that can walk every Definition without an import cycle, and running
// the rule itself (rather than a re-implementation) is the whole point:
// pkgmgr's own validate* helpers are unexported and reached only through
// validateAll, so ValidateOpDispatchTemplates is exported specifically so
// this test exercises the same code the install path runs, not a copy that
// could silently diverge from it.
func TestEveryPackagePassesOpDispatchTemplateGate(t *testing.T) {
	for _, name := range Names() {
		def, _ := Lookup(name)
		if err := def.ValidateOpDispatchTemplates(); err != nil {
			t.Errorf("package %q: ValidateOpDispatchTemplates rejected a shipped descriptor: %v", name, err)
		}
	}
}

// censusPlaceholderRe matches one `{...}` template placeholder — the same
// general shape pkgmgr's readTemplatePlaceholderRe and
// lint-package-standard.go's placeholderRe use — walked independently here
// so the census counts below are not derived from the same code the gate
// test above exercises.
var censusPlaceholderRe = regexp.MustCompile(`\{([^{}]+)\}`)

// templateShape classifies one `{...}` placeholder occurrence within a read
// template entry for the §6 structural census.
type templateShape struct {
	clientOnly   bool // {me.<type>}
	entity       bool // {entity.<column>}
	scopedTo     bool // {scopedTo}
	optionalMark bool // trailing `?`
	midSegmentCO bool // client-only AND not occupying a whole dot segment
}

func classifyPlaceholder(entry string, start, end int, body string) templateShape {
	var s templateShape
	base := strings.TrimSuffix(body, "?")
	s.optionalMark = base != body
	base = strings.TrimSuffix(base, ":id")

	switch {
	case base == "scopedTo":
		s.scopedTo = true
	case strings.HasPrefix(base, "entity."):
		s.entity = true
	case strings.HasPrefix(base, "me.") && base != "me.":
		s.clientOnly = true
		before := byte(0)
		if start > 0 {
			before = entry[start-1]
		}
		after := byte(0)
		if end < len(entry) {
			after = entry[end]
		}
		wholeSegment := (start == 0 || before == '.') && (end == len(entry) || after == '.')
		s.midSegmentCO = !wholeSegment
	}
	return s
}

// TestOpDispatchTemplateCensus re-derives the §6 structural census — the
// counts descriptor-floor-template-coverage-design.md §6 pinned at Phase 0
// (654d5924) by walking every registered package's
// Definition.OpMetas[].Dispatch.{Reads,OptionalReads} structurally. A hand-
// tuned grep does not reproduce this census (the design's own §6 finding:
// `{me.*}`/`{entity.*}` are overwhelmingly ContextParams-vocabulary hits in
// the same files), so this walks the parsed Definition the way the gate
// itself does. A failing count means either a package's read-template shape
// changed (fix the package) or the pinned number needs to move on purpose
// (update this test deliberately) — the message says which numbers moved so
// the next author can tell.
func TestOpDispatchTemplateCensus(t *testing.T) {
	var clientOnlyOptional, clientOnlyRequired, entityHits, scopedToHits, optionalMarkHits, midSegmentClientOnly int

	walk := func(entries []string, isRequired bool) {
		for _, entry := range entries {
			for _, loc := range censusPlaceholderRe.FindAllStringSubmatchIndex(entry, -1) {
				start, end := loc[0], loc[1]
				body := entry[loc[2]:loc[3]]
				shape := classifyPlaceholder(entry, start, end, body)
				if shape.clientOnly {
					if isRequired {
						clientOnlyRequired++
					} else {
						clientOnlyOptional++
					}
				}
				if shape.entity {
					entityHits++
				}
				if shape.scopedTo {
					scopedToHits++
				}
				if shape.optionalMark {
					optionalMarkHits++
				}
				if shape.midSegmentCO {
					midSegmentClientOnly++
				}
			}
		}
	}

	for _, name := range Names() {
		def, _ := Lookup(name)
		for _, m := range def.OpMetas {
			if m.Dispatch == nil {
				continue
			}
			walk(m.Dispatch.Reads, true)
			walk(m.Dispatch.OptionalReads, false)
		}
	}

	const (
		wantClientOnlyOptional   = 3
		wantClientOnlyRequired   = 0
		wantEntityHits           = 0
		wantScopedToHits         = 0
		wantOptionalMarkHits     = 0
		wantMidSegmentClientOnly = 0
	)
	if clientOnlyOptional != wantClientOnlyOptional {
		t.Errorf("client-only ({me.*}) placeholders in OptionalReads: got %d, want %d (descriptor-floor-template-coverage-design.md §6, pinned at 654d5924, live in cafe-domain Charge/Settle) — a package's read-template shape changed; fix the package, or move this pinned number deliberately if the new shape is intended",
			clientOnlyOptional, wantClientOnlyOptional)
	}
	if clientOnlyRequired != wantClientOnlyRequired {
		t.Errorf("client-only ({me.*}) placeholders in Reads: got %d, want %d — a required-side {me.*} should have been refused by ValidateOpDispatchTemplates at install; if one shipped anyway, the gate has a hole",
			clientOnlyRequired, wantClientOnlyRequired)
	}
	if entityHits != wantEntityHits {
		t.Errorf("{entity.*} placeholders in a Reads/OptionalReads template: got %d, want %d — {entity.*} is ContextParams-only vocabulary and should have been refused at install",
			entityHits, wantEntityHits)
	}
	if scopedToHits != wantScopedToHits {
		t.Errorf("{scopedTo} placeholders in a Reads/OptionalReads template: got %d, want %d (descriptor-floor-template-coverage-design.md §6) — {scopedTo} is legal read-template vocabulary with no live user today; a package started using it, so move this pinned number deliberately",
			scopedToHits, wantScopedToHits)
	}
	if optionalMarkHits != wantOptionalMarkHits {
		t.Errorf("`?`-marked entries in a Reads/OptionalReads template: got %d, want %d — `?` is ContextParams-only vocabulary and should have been refused at install",
			optionalMarkHits, wantOptionalMarkHits)
	}
	if midSegmentClientOnly != wantMidSegmentClientOnly {
		t.Errorf("mid-segment client-only ({me.*}) fragments in a Reads/OptionalReads template: got %d, want %d — a mid-segment client-only fragment should have been refused at install (whole-segment rule)",
			midSegmentClientOnly, wantMidSegmentClientOnly)
	}
}
