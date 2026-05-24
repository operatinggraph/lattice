// Package identity implements the lattice identity command group.
package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/asolgan/lattice/cmd/lattice/output"
	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
)

// NewCommand returns the cobra.Command for the identity command group.
func NewCommand(natsURL, outputFmt, defaultActor *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "identity",
		Short: "Submit identity domain operations",
	}
	cmd.AddCommand(newCreateUnclaimedCommand(natsURL, outputFmt, defaultActor))
	cmd.AddCommand(newClaimCommand(natsURL, outputFmt, defaultActor))
	return cmd
}

func newCreateUnclaimedCommand(natsURL, outputFmt, defaultActor *string) *cobra.Command {
	var actor string
	var payload string

	cmd := &cobra.Command{
		Use:   "create-unclaimed",
		Short: "Submit a CreateUnclaimedIdentity operation",
		Long: `create-unclaimed submits a CreateUnclaimedIdentity operation on the
default lane. On acceptance, prints the requestId, opTrackerKey, and
the one-time claimKey from the reply (if present).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if actor == "" {
				actor = *defaultActor
			}
			if actor == "" {
				return fmt.Errorf("--actor is required (or set via credential file)")
			}

			payloadBytes, err := readPayload(payload)
			if err != nil {
				return fmt.Errorf("payload: %w", err)
			}

			requestID, err := substrate.NewNanoID()
			if err != nil {
				return fmt.Errorf("generate requestId: %w", err)
			}

			env := &processor.OperationEnvelope{
				RequestID:     requestID,
				Lane:          processor.LaneDefault,
				OperationType: "CreateUnclaimedIdentity",
				Actor:         actor,
				SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
				Payload:       json.RawMessage(payloadBytes),
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			conn, err := output.Connect(ctx, *natsURL)
			if err != nil {
				if *outputFmt == "json" {
					_ = output.PrintJSONError("ConnectionError", err.Error())
					return nil
				}
				return err
			}
			defer conn.Close()

			reply, err := submitOp(ctx, conn, env)
			if err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("SubmitError", err.Error())
				}
				return err
			}

			if reply.Status == processor.ReplyStatusRejected {
				if *outputFmt == "json" {
					return output.PrintJSONError(string(reply.Error.Code), reply.Error.Message)
				}
				fmt.Fprintf(os.Stderr, "rejected: %s — %s\n", reply.Error.Code, reply.Error.Message)
				os.Exit(1)
			}

			if *outputFmt == "json" {
				return output.PrintJSON(reply)
			}
			fmt.Printf("requestId:    %s\nopTrackerKey: %s\nstatus:       %s\n",
				reply.RequestID, reply.OpTrackerKey, reply.Status)
			if reply.Detail != nil {
				if claimKey, ok := reply.Detail["claimKey"].(string); ok && claimKey != "" {
					fmt.Printf("claimKey:     %s\n", claimKey)
				}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&actor, "actor", "", "actor key (defaults to credential file actorKey)")
	cmd.Flags().StringVar(&payload, "payload", "", "payload: @file.json for file, - for stdin, or inline JSON")
	return cmd
}

func newClaimCommand(natsURL, outputFmt, defaultActor *string) *cobra.Command {
	var actor string
	var payload string

	cmd := &cobra.Command{
		Use:   "claim",
		Short: "Submit a ClaimIdentity operation",
		Long: `claim submits a ClaimIdentity operation on the default lane.
Read payload from --payload @file.json or stdin (-).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if actor == "" {
				actor = *defaultActor
			}
			if actor == "" {
				return fmt.Errorf("--actor is required (or set via credential file)")
			}

			payloadBytes, err := readPayload(payload)
			if err != nil {
				return fmt.Errorf("payload: %w", err)
			}

			requestID, err := substrate.NewNanoID()
			if err != nil {
				return fmt.Errorf("generate requestId: %w", err)
			}

			env := &processor.OperationEnvelope{
				RequestID:     requestID,
				Lane:          processor.LaneDefault,
				OperationType: "ClaimIdentity",
				Actor:         actor,
				SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
				Payload:       json.RawMessage(payloadBytes),
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			conn, err := output.Connect(ctx, *natsURL)
			if err != nil {
				if *outputFmt == "json" {
					_ = output.PrintJSONError("ConnectionError", err.Error())
					return nil
				}
				return err
			}
			defer conn.Close()

			reply, err := submitOp(ctx, conn, env)
			if err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("SubmitError", err.Error())
				}
				return err
			}

			if reply.Status == processor.ReplyStatusRejected {
				if *outputFmt == "json" {
					return output.PrintJSONError(string(reply.Error.Code), reply.Error.Message)
				}
				fmt.Fprintf(os.Stderr, "rejected: %s — %s\n", reply.Error.Code, reply.Error.Message)
				os.Exit(1)
			}

			if *outputFmt == "json" {
				return output.PrintJSON(reply)
			}
			fmt.Printf("requestId:    %s\nopTrackerKey: %s\nstatus:       %s\n",
				reply.RequestID, reply.OpTrackerKey, reply.Status)
			return nil
		},
	}

	cmd.Flags().StringVar(&actor, "actor", "", "actor key (defaults to credential file actorKey)")
	cmd.Flags().StringVar(&payload, "payload", "", "payload: @file.json for file, - for stdin, or inline JSON")
	return cmd
}

// submitOp publishes an OperationEnvelope and waits for the Processor reply.
func submitOp(ctx context.Context, conn *substrate.Conn, env *processor.OperationEnvelope) (*processor.OperationReply, error) {
	return output.SubmitOp(ctx, conn, env)
}

// readPayload reads the payload bytes from the given source.
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
