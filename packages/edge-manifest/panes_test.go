package edgemanifest

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

// paneSectionForTest mirrors the section-descriptor shape the pane executor
// and renderer consume. Kept test-local: the descriptor is DATA — the only
// production Go that parses it is the host's executor, against its own
// grammar.
type paneSectionForTest struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	EmptyCopy string `json:"emptyCopy"`
	Source    struct {
		Table   string `json:"table"`
		Columns []struct {
			Name string `json:"name"`
			Kind string `json:"kind"`
			Role string `json:"role"`
		} `json:"columns"`
		Filter *struct {
			Kind   string `json:"kind"`
			Column string `json:"column"`
		} `json:"filter"`
		OrderBy *struct {
			Column string `json:"column"`
		} `json:"orderBy"`
		Limit int `json:"limit"`
	} `json:"source"`
	Dispatch *struct {
		TargetColumn string `json:"targetColumn"`
		TargetType   string `json:"targetType"`
	} `json:"dispatch"`
}

func decodeSections(t *testing.T, sections string) []paneSectionForTest {
	t.Helper()
	var out []paneSectionForTest
	if err := json.Unmarshal([]byte(sections), &out); err != nil {
		t.Fatalf("sections JSON does not parse: %v", err)
	}
	return out
}

// TestPanes_IdentifierGrammar pins every table/column/filter identifier in
// every declared pane to the host executor's grammar, so a descriptor that
// would fail validation at request time fails HERE, at package-test time.
func TestPanes_IdentifierGrammar(t *testing.T) {
	table := regexp.MustCompile(`^read_[a-z_]+$`)
	column := regexp.MustCompile(`^[a-z_]+$`)
	for _, pane := range Panes() {
		if pane.CanonicalName == "" || pane.Title == "" {
			t.Fatalf("pane must carry canonicalName + title: %+v", pane)
		}
		if len(pane.OfferedToRoles) == 0 {
			t.Fatalf("pane %s: offered to no role — it would be projected to no one", pane.CanonicalName)
		}
		for _, sec := range decodeSections(t, pane.Sections) {
			if sec.ID == "" || sec.Title == "" {
				t.Fatalf("pane %s: section must carry id + title", pane.CanonicalName)
			}
			if !table.MatchString(sec.Source.Table) {
				t.Fatalf("pane %s section %s: table %q outside the read_* grammar", pane.CanonicalName, sec.ID, sec.Source.Table)
			}
			if len(sec.Source.Columns) == 0 {
				t.Fatalf("pane %s section %s: no columns", pane.CanonicalName, sec.ID)
			}
			names := map[string]bool{}
			for _, c := range sec.Source.Columns {
				if !column.MatchString(c.Name) {
					t.Fatalf("pane %s section %s: column %q outside the identifier grammar", pane.CanonicalName, sec.ID, c.Name)
				}
				names[c.Name] = true
			}
			if f := sec.Source.Filter; f != nil {
				if f.Kind != "isNull" && f.Kind != "utcDay" {
					t.Fatalf("pane %s section %s: filter kind %q not in the executor's fixed set", pane.CanonicalName, sec.ID, f.Kind)
				}
				if !column.MatchString(f.Column) {
					t.Fatalf("pane %s section %s: filter column %q outside the identifier grammar", pane.CanonicalName, sec.ID, f.Column)
				}
			}
			if o := sec.Source.OrderBy; o != nil && !column.MatchString(o.Column) {
				t.Fatalf("pane %s section %s: orderBy column %q outside the identifier grammar", pane.CanonicalName, sec.ID, o.Column)
			}
			if sec.Source.Limit <= 0 || sec.Source.Limit > 200 {
				t.Fatalf("pane %s section %s: limit %d outside (0,200]", pane.CanonicalName, sec.ID, sec.Source.Limit)
			}
			if d := sec.Dispatch; d != nil {
				if !names[d.TargetColumn] {
					t.Fatalf("pane %s section %s: dispatch targetColumn %q is not a declared column", pane.CanonicalName, sec.ID, d.TargetColumn)
				}
				if d.TargetType == "" {
					t.Fatalf("pane %s section %s: dispatch without targetType", pane.CanonicalName, sec.ID)
				}
			}
		}
	}
}

// TestPanes_ScheduleStaysClinicalContentFree pins the schedule section's
// column list to visit existence and timing. Widening it onto any
// clinical-content column is a data-protection decision that must be made
// deliberately, in review, by editing BOTH the descriptor and this ban.
func TestPanes_ScheduleStaysClinicalContentFree(t *testing.T) {
	banned := []string{"reason", "documented_at", "follow_up_requested", "follow_up_date", "status_note"}
	for _, pane := range Panes() {
		for _, sec := range decodeSections(t, pane.Sections) {
			if sec.Source.Table != "read_clinic_appointments" {
				continue
			}
			for _, c := range sec.Source.Columns {
				for _, b := range banned {
					if c.Name == b {
						t.Fatalf("pane %s section %s projects clinical-content column %q", pane.CanonicalName, sec.ID, b)
					}
				}
			}
		}
	}
}

// TestPanes_VisitSeriesStateColumn pins the visit-series section to carrying
// the `active` state column ops' visibleWhen conditions read — without it the
// Pause/Resume pair would evaluate against a missing column and (correctly,
// fail-closed) never be offered.
func TestPanes_VisitSeriesStateColumn(t *testing.T) {
	found := false
	for _, pane := range Panes() {
		for _, sec := range decodeSections(t, pane.Sections) {
			if sec.Source.Table != "read_visit_series" {
				continue
			}
			for _, c := range sec.Source.Columns {
				if c.Name == "active" && c.Role == "state" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatal("no visit-series section carries the active state column")
	}
}

// TestPanes_SectionsRawStringStaysValid guards the raw-string authoring: a
// stray backtick or trailing comma in staffWorklistSections would otherwise
// surface only at runtime in the host.
func TestPanes_SectionsRawStringStaysValid(t *testing.T) {
	if strings.Contains(staffWorklistSections, "`") {
		t.Fatal("sections raw string cannot contain a backtick")
	}
	decodeSections(t, staffWorklistSections)
}

// TestLenses_KeyColumnsPairWithLabels — the display-name floor's DATA half:
// a projected column whose value is a vertex KEY is only renderable as a
// bare short-id unless a label rides beside it, so the renderer's floor rule
// keeps getting pushed to the floor by data holes. Every tail stamping an
// entityType must stamp its typeLabel; every tail projecting resolvedVia
// must project resolvedViaLabel. Structural — a new lens cannot reintroduce
// the "via Building · A9jnKK" class of hole without failing here.
func TestLenses_KeyColumnsPairWithLabels(t *testing.T) {
	for _, l := range Lenses() {
		spec := l.Spec
		if strings.Contains(spec, "AS entityType") && !strings.Contains(spec, "AS typeLabel") {
			t.Errorf("lens %s stamps entityType without typeLabel", l.CanonicalName)
		}
		if strings.Contains(spec, "AS resolvedVia,") && !strings.Contains(spec, "AS resolvedViaLabel") {
			t.Errorf("lens %s projects resolvedVia without resolvedViaLabel", l.CanonicalName)
		}
	}
}
