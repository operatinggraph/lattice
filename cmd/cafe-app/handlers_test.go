package main

import (
	"encoding/json"
	"io"
	"log/slog"
)

// discardLogger is the shared silent logger every test server construction
// below uses (mirrors cmd/clinic-app/readauth_test.go's own discardLogger).
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeKV builds a keys slice + kvGetter over an in-memory map, the seam every
// computeXxx pure function is unit-tested over (mirrors
// cmd/clinic-app/handlers_test.go).
func fakeKV(entries map[string]any) (keys []string, get kvGetter) {
	raw := map[string][]byte{}
	for k, v := range entries {
		b, _ := json.Marshal(v)
		raw[k] = b
		keys = append(keys, k)
	}
	get = func(key string) ([]byte, bool) {
		b, ok := raw[key]
		return b, ok
	}
	return keys, get
}
