package weaver

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestPromotionHookCensus_EveryEffectCloseCreditAsksForPromotion pins the
// pairing between the __effect close credit and the promotion recommendation:
// every non-test call to recordEffectClose in this package must be followed,
// within its own success handling, by a proposePromotionIfClean call. The
// window can only become full-and-clean at a credit, so a credit site that
// does not ask is a site where a recommendation earned there waits on some
// other observer of the same triple — for a row that has gone quiet, forever.
// The credit has three seams (lane-1 gap close, the leg release, the sweep's
// gapClosed delete); a fourth added without the ask fails here rather than in
// review.
func TestPromotionHookCensus_EveryEffectCloseCreditAsksForPromotion(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	credit := regexp.MustCompile(`\.recordEffectClose\(`)
	const window = 24 // lines after the credit within which the ask must appear
	sites := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "state.go" {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(src), "\n")
		for i, line := range lines {
			if !credit.MatchString(line) {
				continue
			}
			sites++
			end := i + window
			if end > len(lines) {
				end = len(lines)
			}
			if !strings.Contains(strings.Join(lines[i:end], "\n"), "proposePromotionIfClean(") {
				t.Errorf("%s:%d credits the __effect window but never asks proposePromotionIfClean within %d lines", f, i+1, window)
			}
		}
	}
	if sites != 3 {
		t.Errorf("recordEffectClose call sites = %d, want 3 (lane-1 gap close, leg release, sweep gapClosed); update this pin with the new seam's ask", sites)
	}
}
