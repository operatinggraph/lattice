package main

import (
	"io"
	"log/slog"
	"testing"
)

// TestEnvInt pins the load-bearing distinction for the daily cap: an explicit
// "0" is returned as 0 (the "stop spending" switch), never collapsed to the
// default; only an unset, unparseable, or negative value falls back.
func TestEnvInt(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	const key = "MODEL_RUNNER_TEST_INT"

	cases := []struct {
		name string
		set  bool
		val  string
		def  int
		want int
	}{
		{"unset falls back to default", false, "", 20, 20},
		{"explicit zero is preserved, not defaulted", true, "0", 20, 0},
		{"positive is preserved", true, "5", 20, 5},
		{"negative falls back (cannot disable via env)", true, "-1", 20, 20},
		{"garbage falls back", true, "abc", 20, 20},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv(key, tc.val)
			} else {
				t.Setenv(key, "")
			}
			if got := envInt(key, tc.def, logger); got != tc.want {
				t.Errorf("envInt(%q=%q, def=%d) = %d, want %d", key, tc.val, tc.def, got, tc.want)
			}
		})
	}
}
