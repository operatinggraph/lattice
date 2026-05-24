// Package output provides the shared JSON output envelope and helpers
// used by all lattice command groups.
package output

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// Envelope is the JSON wire format for --output json mode.
// All subcommands use this type so the format is uniform across groups.
type Envelope struct {
	OK    bool        `json:"ok"`
	Data  interface{} `json:"data,omitempty"`
	Error *EnvError   `json:"error,omitempty"`
}

// EnvError is the structured error field inside Envelope.
type EnvError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// PrintJSON emits the data as a success envelope to stdout.
func PrintJSON(data interface{}) error {
	return json.NewEncoder(os.Stdout).Encode(Envelope{OK: true, Data: data})
}

// ErrJSONError is returned by PrintJSONError so callers that use
// `return output.PrintJSONError(...)` propagate a non-nil error to cobra.
var ErrJSONError = errors.New("command failed (see JSON output)")

// PrintJSONError emits an error envelope to stdout and returns ErrJSONError
// so that every `return output.PrintJSONError(...)` call propagates a non-nil
// error to cobra, causing a non-zero exit code (AC2).
func PrintJSONError(code, message string) error {
	_ = json.NewEncoder(os.Stdout).Encode(Envelope{
		OK:    false,
		Error: &EnvError{Code: code, Message: message},
	})
	return ErrJSONError
}

// PrintTable prints human-readable text to stdout.
func PrintTable(format string, args ...interface{}) {
	fmt.Printf(format, args...)
}

// Stderr prints an error message to stderr.
func Stderr(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}
