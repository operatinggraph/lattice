package wire

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestValidRef(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		ref  string
		want bool
	}{
		// The Lattice NanoID alphabet (Contract #1: alphanumeric minus I/l/O/0)
		// is a strict subset of what a ref may contain, so any primary key
		// works as a ref unchanged.
		{"lattice nanoid", "V1StGXR8Z5jdHi6BmyTx", true},
		{"attempt chain", "V1StGXR8Z5jdHi6BmyTx.2", true},
		{"dashed", "proposal-42", true},
		{"slash path", "capabilityauthor/abc", true},
		{"base64 padding", "YWJjZA==", true},
		{"empty", "", false},
		// The runner's spend counter lives under "__" in the same bucket; a
		// ref that could address it would be able to zero the daily cap.
		{"usage counter key", "__usage.2026-08-21", false},
		{"single underscore", "a_b", false},
		{"subject wildcard", "ref.*", false},
		{"subject fullwild", "ref.>", false},
		{"leading dot", ".ref", false},
		{"trailing dot", "ref.", false},
		{"space", "a b", false},
		{"too long", strings.Repeat("a", maxRefLen+1), false},
		{"at the length limit", strings.Repeat("a", maxRefLen), true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ValidRef(tc.ref); got != tc.want {
				t.Errorf("ValidRef(%q) = %v, want %v", tc.ref, got, tc.want)
			}
		})
	}
}

func TestAckErr(t *testing.T) {
	t.Parallel()
	if err := (Ack{Status: AckAccepted}).Err(); err != nil {
		t.Errorf("accepted: want nil, got %v", err)
	}
	if err := (Ack{Status: AckBusy}).Err(); !errors.Is(err, ErrBusy) {
		t.Errorf("busy: want ErrBusy, got %v", err)
	}
	if err := (Ack{Status: AckInvalid}).Err(); !errors.Is(err, ErrInvalid) {
		t.Errorf("invalid: want ErrInvalid, got %v", err)
	}
	if err := (Ack{Status: "wat"}).Err(); err == nil {
		t.Error("unknown status: want an error, got nil")
	}
}

func TestResultTerminal(t *testing.T) {
	t.Parallel()
	for state, want := range map[ResultState]bool{
		StateInflight:  false,
		StateCompleted: true,
		StateRefused:   true,
		StateFailed:    true,
		"":             false,
	} {
		if got := (Result{State: state}).Terminal(); got != want {
			t.Errorf("state %q: Terminal() = %v, want %v", state, got, want)
		}
	}
}

// The wire shape is a cross-process contract: both the runner and every caller
// decode these field names, so a rename must break a test rather than a
// deployment.
func TestRequestJSONFieldNames(t *testing.T) {
	t.Parallel()
	body, err := json.Marshal(Request{
		Ref:       "r1",
		Model:     "claude-opus-5",
		MaxTokens: 100,
		System:    "sys",
		Prompt:    "p",
		Tool: Tool{
			Name:        "emit",
			Description: "d",
			InputSchema: ToolSchema{
				Properties: map[string]any{"content": map[string]any{"type": "string"}},
				Required:   []string{"content"},
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, field := range []string{
		`"ref"`, `"model"`, `"maxTokens"`, `"system"`, `"prompt"`,
		`"tool"`, `"name"`, `"description"`, `"inputSchema"`, `"properties"`, `"required"`,
	} {
		if !strings.Contains(string(body), field) {
			t.Errorf("request JSON missing %s: %s", field, body)
		}
	}
}

func TestResultJSONFieldNames(t *testing.T) {
	t.Parallel()
	body, err := json.Marshal(Result{
		State:           StateCompleted,
		Ref:             "r1",
		Output:          json.RawMessage(`{"a":1}`),
		Model:           "claude-opus-5",
		Usage:           Usage{InputTokens: 3, OutputTokens: 4},
		RefusalCategory: "cyber",
		CompletedAt:     "2026-08-21T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, field := range []string{
		`"state"`, `"ref"`, `"output"`, `"model"`, `"usage"`,
		`"inputTokens"`, `"outputTokens"`, `"refusalCategory"`, `"completedAt"`,
	} {
		if !strings.Contains(string(body), field) {
			t.Errorf("result JSON missing %s: %s", field, body)
		}
	}
	// Output must survive as raw JSON, never as a re-encoded string.
	if !strings.Contains(string(body), `"output":{"a":1}`) {
		t.Errorf("output was not passed through verbatim: %s", body)
	}
}

// The subject the permission matrix grants must actually cover the subject the
// runner serves; the two constants live together so this can be asserted.
func TestSubjectPrefixCoversGenerateSubject(t *testing.T) {
	t.Parallel()
	if !strings.HasPrefix(GenerateSubject, SubjectPrefix) {
		t.Fatalf("GenerateSubject %q is not under SubjectPrefix %q — the natsperm grant would not reach it",
			GenerateSubject, SubjectPrefix)
	}
}
