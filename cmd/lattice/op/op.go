// Package op implements the lattice op command group.
//
// All operations are submitted via the standard Processor write path
// (ops.<lane> NATS subject). No CLI-specific code path exists in the
// Processor — the CLI is just another NATS client.
package op

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/operatinggraph/lattice/cmd/lattice/output"
	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/opstatus"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// opReplyTimeout is how long the CLI waits for a Processor reply.
const opReplyTimeout = 10 * time.Second

// NewCommand returns the cobra.Command for the op command group.
func NewCommand(natsURL, outputFmt, defaultActor *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "op",
		Short: "Submit operations and inspect their status",
	}
	cmd.AddCommand(newSubmitCommand(natsURL, outputFmt, defaultActor))
	cmd.AddCommand(newStatusCommand(natsURL, outputFmt))
	cmd.AddCommand(newTraceCommand(natsURL, outputFmt))
	return cmd
}

// newSubmitCommand creates the op submit subcommand.
func newSubmitCommand(natsURL, outputFmt, defaultActor *string) *cobra.Command {
	var (
		lane                 string
		operationType        string
		actor                string
		payload              string
		class                string
		contextReads         string
		contextOptionalReads string
	)

	cmd := &cobra.Command{
		Use:   "submit",
		Short: "Submit an operation to the Processor",
		Long: `submit constructs an OperationEnvelope and publishes it to
ops.<lane> via NATS request-reply. On acceptance, prints the requestId
and opTrackerKey. On rejection, prints the error code and message.`,
		Example: `  lattice op submit --lane default --operation-type CreateUnclaimedIdentity --actor vtx.identity.<NanoID> --payload @payload.json
  lattice op submit --lane default --operation-type CreateUnclaimedIdentity --actor vtx.identity.<NanoID> --payload -`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if actor == "" {
				actor = *defaultActor
			}
			if actor == "" {
				return fmt.Errorf("--actor is required (or set via credential file)")
			}
			if lane == "" {
				lane = "default"
			}
			if operationType == "" {
				return fmt.Errorf("--operation-type is required")
			}

			payloadBytes, err := readPayload(payload)
			if err != nil {
				if *outputFmt == "json" {
					_ = output.PrintJSONError("PayloadError", err.Error())
					os.Exit(1)
				}
				return fmt.Errorf("payload: %w", err)
			}

			requestID, err := substrate.NewNanoID()
			if err != nil {
				return fmt.Errorf("generate requestId: %w", err)
			}

			env := processor.OperationEnvelope{
				RequestID:     requestID,
				Lane:          processor.Lane(lane),
				OperationType: operationType,
				Actor:         actor,
				SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
				Payload:       json.RawMessage(payloadBytes),
			}
			if class != "" {
				env.Class = class
			}
			if contextReads != "" || contextOptionalReads != "" {
				env.ContextHint = &processor.ContextHint{}
				if contextReads != "" {
					env.ContextHint.Reads = strings.Split(contextReads, ",")
				}
				if contextOptionalReads != "" {
					env.ContextHint.OptionalReads = strings.Split(contextOptionalReads, ",")
				}
			}

			ctx, cancel := context.WithTimeout(context.Background(), opReplyTimeout)
			defer cancel()

			conn, err := output.Connect(ctx, *natsURL)
			if err != nil {
				if *outputFmt == "json" {
					_ = output.PrintJSONError("ConnectionError", err.Error())
					os.Exit(1)
				}
				return err
			}
			defer conn.Close()

			reply, err := submitOp(ctx, conn, &env)
			if err != nil {
				if *outputFmt == "json" {
					_ = output.PrintJSONError("SubmitError", err.Error())
					os.Exit(1)
				}
				return fmt.Errorf("submit: %w", err)
			}

			if reply.Status == processor.ReplyStatusRejected {
				if *outputFmt == "json" {
					_ = output.PrintJSONError(string(reply.Error.Code), reply.Error.Message)
				} else {
					fmt.Fprintf(os.Stderr, "rejected: %s — %s\n", reply.Error.Code, reply.Error.Message)
				}
				os.Exit(1)
			}

			if *outputFmt == "json" {
				return output.PrintJSON(reply)
			}
			fmt.Printf("requestId:    %s\nopTrackerKey: %s\nstatus:       %s\n",
				reply.RequestID, reply.OpTrackerKey, reply.Status)
			if reply.PrimaryKey != "" {
				fmt.Printf("primaryKey:   %s\n", reply.PrimaryKey)
			}
			for k := range reply.Revisions {
				fmt.Printf("committedKey: %s\n", k)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&lane, "lane", "default", "operation lane (default|meta|urgent|system)")
	cmd.Flags().StringVar(&operationType, "operation-type", "", "operation type (e.g. CreateUnclaimedIdentity)")
	cmd.Flags().StringVar(&actor, "actor", "", "actor key (defaults to credential file actorKey)")
	cmd.Flags().StringVar(&payload, "payload", "", "payload: @file.json for file, - for stdin, or inline JSON")
	cmd.Flags().StringVar(&class, "class", "", "DDL class hint (optional)")
	cmd.Flags().StringVar(&contextReads, "context-hint-reads", "", "comma-separated context hint read keys (optional)")
	cmd.Flags().StringVar(&contextOptionalReads, "context-hint-optional-reads", "", "comma-separated context hint OPTIONAL read keys — absence-tolerant, for read-before-create/revive patterns (optional)")
	_ = cmd.MarkFlagRequired("operation-type")
	return cmd
}

// submitOp publishes the envelope to ops.<lane> and waits for the
// Processor reply. Delegates to output.SubmitOp.
func submitOp(ctx context.Context, conn *substrate.Conn, env *processor.OperationEnvelope) (*processor.OperationReply, error) {
	return output.SubmitOp(ctx, conn, env)
}

// readPayload reads the payload bytes from the given source:
//   - if source starts with '@', reads the file at source[1:]
//   - if source is '-', reads stdin
//   - otherwise returns the source bytes as-is
func readPayload(source string) ([]byte, error) {
	if source == "" {
		return []byte("{}"), nil
	}
	if source == "-" {
		return io.ReadAll(os.Stdin)
	}
	if strings.HasPrefix(source, "@") {
		return os.ReadFile(source[1:])
	}
	return []byte(source), nil
}

// newStatusCommand creates the op status subcommand.
func newStatusCommand(natsURL, outputFmt *string) *cobra.Command {
	return &cobra.Command{
		Use:   "status <requestId>",
		Short: "Ask whether an operation landed (via the lattice.op.status RPC)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			requestID := args[0]

			ctx, cancel := context.WithTimeout(context.Background(), output.DefaultTimeout)
			defer cancel()

			conn, err := output.Connect(ctx, *natsURL)
			if err != nil {
				if *outputFmt == "json" {
					_ = output.PrintJSONError("ConnectionError", err.Error())
					os.Exit(1)
				}
				return err
			}
			defer conn.Close()

			resp, err := requestOpStatus(ctx, conn, requestID)
			if err != nil {
				if *outputFmt == "json" {
					_ = output.PrintJSONError("RequestError", err.Error())
					os.Exit(1)
				}
				fmt.Fprintf(os.Stderr, "request failed: %v\n", err)
				os.Exit(1)
			}
			if resp.Error != "" {
				if *outputFmt == "json" {
					_ = output.PrintJSONError("ResponderError", resp.Error)
					os.Exit(1)
				}
				fmt.Fprintf(os.Stderr, "responder error: %s\n", resp.Error)
				os.Exit(1)
			}
			if !resp.Found {
				if *outputFmt == "json" {
					_ = output.PrintJSONError("NotFound", fmt.Sprintf("requestId %s: no tracker (absent or TTL-expired)", requestID))
					os.Exit(1)
				}
				fmt.Fprintf(os.Stderr, "not found: %s (no tracker — absent or TTL-expired)\n", requestID)
				os.Exit(1)
			}

			if *outputFmt == "json" {
				return output.PrintJSON(resp)
			}
			fmt.Printf("requestId:   %s\ncommitted:   %t\nisDeleted:   %t\ncommittedAt: %s\nclass:       %s\n",
				requestID, resp.Committed, resp.IsDeleted, resp.CommittedAt, resp.Class)
			return nil
		},
	}
}

// opStatusTimeout bounds the CLI's own wait on the lattice.op.status RPC,
// mirroring internal/gateway's opStatusTimeout / internal/loom's
// trackerExistsTimeout.
const opStatusTimeout = 5 * time.Second

// requestOpStatus asks the Processor-hosted lattice.op.status RPC whether
// requestID landed (op-status-read-surface-design.md Fire 4), replacing the
// CLI's former raw KVGet of the Contract #4 tracker — mirrors
// internal/gateway's requestOpStatus / internal/loom's trackerExists.
func requestOpStatus(ctx context.Context, conn *substrate.Conn, requestID string) (*opstatus.Response, error) {
	reqBody, err := json.Marshal(opstatus.Request{RequestID: requestID})
	if err != nil {
		return nil, fmt.Errorf("marshal op-status request: %w", err)
	}
	rctx, cancel := context.WithTimeout(ctx, opStatusTimeout)
	defer cancel()
	data, err := conn.Request(rctx, opstatus.Subject, reqBody)
	if err != nil {
		return nil, fmt.Errorf("request op-status %q: %w", requestID, err)
	}
	var resp opstatus.Response
	if uerr := json.Unmarshal(data, &resp); uerr != nil {
		return nil, fmt.Errorf("parse op-status reply for %q: %w", requestID, uerr)
	}
	return &resp, nil
}

// newTraceCommand creates the op trace subcommand.
func newTraceCommand(natsURL, outputFmt *string) *cobra.Command {
	var instance string

	cmd := &cobra.Command{
		Use:   "trace <requestId>",
		Short: "Read the auth-trace record for an operation from Health KV",
		Long: `trace reads the three-plane AuthTraceRecord for the given requestId
from Health KV (health.processor.<instance>.auth-trace.<requestId>).

Records expire after 1 hour. If no record exists (op was allowed with
TraceAllowDecisions OFF, or the TTL has expired), a clear message is
printed and the command exits 0.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			requestID := args[0]
			traceKey := fmt.Sprintf("health.processor.%s.auth-trace.%s", instance, requestID)

			ctx, cancel := context.WithTimeout(context.Background(), output.DefaultTimeout)
			defer cancel()

			conn, err := output.Connect(ctx, *natsURL)
			if err != nil {
				if *outputFmt == "json" {
					_ = output.PrintJSONError("ConnectionError", err.Error())
					os.Exit(1)
				}
				return err
			}
			defer conn.Close()

			entry, err := conn.KVGet(ctx, bootstrap.HealthKVBucket, traceKey)
			if err != nil {
				// No record is not an error condition — the op may have been
				// allowed with TraceAllowDecisions OFF, or the TTL may have expired.
				if *outputFmt == "json" {
					return output.PrintJSON(map[string]interface{}{
						"requestId": requestID,
						"found":     false,
						"message":   "no auth-trace record found (allowed ops not traced by default, or record expired)",
					})
				}
				fmt.Printf("no auth-trace record for %s (allowed ops are not traced by default, or record expired)\n", requestID)
				return nil
			}

			if *outputFmt == "json" {
				var doc processor.AuthTraceRecord
				if err := json.Unmarshal(entry.Value, &doc); err != nil {
					return output.PrintJSONError("ParseError", err.Error())
				}
				return output.PrintJSON(doc)
			}
			fmt.Printf("auth-trace key: %s\n%s\n", traceKey, string(entry.Value))
			return nil
		},
	}

	cmd.Flags().StringVar(&instance, "instance", "default", "processor instance name")
	return cmd
}
