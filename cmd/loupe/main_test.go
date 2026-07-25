package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// TestWarnIfNonLoopback pins the startup exposure warning: a non-loopback or
// unparseable LOUPE_ADDR must emit a loud WARN so an auth-less network-wide
// admin bind is never silent; a loopback bind must stay quiet.
func TestWarnIfNonLoopback(t *testing.T) {
	tests := []struct {
		name     string
		addr     string
		wantWarn bool
	}{
		{"loopback ipv4", "127.0.0.1:7777", false},
		{"loopback ipv6", "[::1]:7777", false},
		{"localhost", "localhost:7777", false},
		{"bare port (all interfaces)", ":7777", true},
		{"all interfaces explicit", "0.0.0.0:7777", true},
		{"public ip", "8.8.8.8:7777", true},
		{"lan ip", "192.168.1.10:7777", true},
		{"unparseable addr", "not-an-addr", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
			warnIfNonLoopback(logger, tc.addr)
			gotWarn := strings.Contains(buf.String(), "level=WARN")
			if gotWarn != tc.wantWarn {
				t.Errorf("warnIfNonLoopback(%q): warn=%v, want %v (log: %q)", tc.addr, gotWarn, tc.wantWarn, buf.String())
			}
		})
	}
}
