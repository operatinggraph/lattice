package testutil

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/operatinggraph/lattice/internal/processor"
)

// ReadCensusEnvVar names the file a read census is appended to. Unset (the
// normal case) leaves the census inert and every pipeline observer nil.
const ReadCensusEnvVar = "LATTICE_READ_CENSUS"

// ReadCensus is a processor.ScriptReadObserver that appends one JSON line per
// script execution: what the script ACTUALLY read (processor.ScriptReadRecord)
// beside what its envelope DECLARED (contextHint). It answers "how far do our
// own scripts drift from their declarations, and in which shapes", over whatever
// corpus the test run exercises.
//
// MEASUREMENT ONLY — it asserts nothing and can fail nothing. A census that
// could fail a test would make every suite hostage to a declaration sweep still
// in progress; the drift gate is a separate mechanism built on these numbers.
// A write error is reported once to stderr and then swallowed, for the same
// reason: losing census lines must never turn into a red suite.
//
// Safe for concurrent use — tests run in parallel, and several pipelines in one
// process can share a census file. Serialized by a mutex around an O_APPEND
// handle, so lines from separate observers interleave without tearing.
type ReadCensus struct {
	mu       sync.Mutex
	f        *os.File
	reported bool
}

// ReadCensusLine is one execution's row. Actual-side fields come from the
// record, declared-side fields from the envelope's contextHint; a drift question
// is answered by comparing the two halves of one line.
type ReadCensusLine struct {
	OperationType string `json:"operationType"`
	Class         string `json:"class,omitempty"`
	RequestID     string `json:"requestId,omitempty"`

	LiveReads          []string                      `json:"liveReads,omitempty"`
	DeclaredServed     []string                      `json:"declaredServed,omitempty"`
	Enumerations       []processor.ScriptEnumeration `json:"enumerations,omitempty"`
	EnumeratedVertices []string                      `json:"enumeratedVertices,omitempty"`

	HintReads         []string                    `json:"hintReads,omitempty"`
	HintOptionalReads []string                    `json:"hintOptionalReads,omitempty"`
	HintEgressReads   []string                    `json:"hintEgressReads,omitempty"`
	HintEnumerations  []processor.EnumerationHint `json:"hintEnumerations,omitempty"`
}

var (
	censusOnce   sync.Once
	sharedCensus *ReadCensus
)

// SharedReadCensus returns this process's census, opening it on first use, or
// nil when LATTICE_READ_CENSUS is unset. One census per PROCESS rather than one
// per pipeline: a test binary stands up many pipelines, and a single file handle
// behind a single mutex keeps their lines from interleaving. Concurrent test
// binaries still share the file, where O_APPEND is what keeps whole lines
// intact.
func SharedReadCensus() *ReadCensus {
	censusOnce.Do(func() { sharedCensus = openReadCensus() })
	return sharedCensus
}

// openReadCensus opens the census file named by LATTICE_READ_CENSUS in append
// mode. It returns nil when the variable is unset — the caller then leaves
// Deps.ScriptReadObserver at its inert default — and also when the file cannot
// be opened, since a census is never worth failing a run over.
func openReadCensus() *ReadCensus {
	path := os.Getenv(ReadCensusEnvVar)
	if path == "" {
		return nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "testutil: read census disabled, cannot open %s: %v\n", path, err)
		return nil
	}
	return &ReadCensus{f: f}
}

// ObserveScriptReads implements processor.ScriptReadObserver.
func (c *ReadCensus) ObserveScriptReads(_ context.Context, env *processor.OperationEnvelope, record processor.ScriptReadRecord) {
	if c == nil || env == nil {
		return
	}
	line := ReadCensusLine{
		OperationType:      env.OperationType,
		Class:              env.Class,
		RequestID:          env.RequestID,
		LiveReads:          record.LiveReads,
		DeclaredServed:     record.DeclaredReads,
		Enumerations:       record.Enumerations,
		EnumeratedVertices: record.EnumeratedVertices,
	}
	if h := env.ContextHint; h != nil {
		line.HintReads = h.Reads
		line.HintOptionalReads = h.OptionalReads
		line.HintEgressReads = h.EgressReads
		line.HintEnumerations = h.Enumerations
	}
	b, err := json.Marshal(line)
	if err != nil {
		c.report("marshal", err)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.f.Write(append(b, '\n')); err != nil {
		c.reportLocked("write", err)
	}
}

// report notes the first failure of this census to stderr and stays quiet
// afterwards, so a broken census costs one line rather than one line per
// execution across a whole suite.
func (c *ReadCensus) report(stage string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reportLocked(stage, err)
}

// reportLocked is report's body for callers already holding c.mu.
func (c *ReadCensus) reportLocked(stage string, err error) {
	if c.reported {
		return
	}
	c.reported = true
	fmt.Fprintf(os.Stderr, "testutil: read census %s failed, further errors suppressed: %v\n", stage, err)
}
