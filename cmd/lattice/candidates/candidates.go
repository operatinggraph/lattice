// Package candidates implements the lattice candidates command group.
package candidates

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/asolgan/lattice/cmd/lattice/output"
	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
)

// duplicateCandidatesBucket is the NATS KV bucket written by the
// identity-hygiene duplicateCandidates Lens.
const duplicateCandidatesBucket = "duplicate-candidates"

// candidateEntry is the shape of a duplicate-candidates Lens output entry.
type candidateEntry struct {
	PrimaryKey             string   `json:"primaryKey"`
	SecondaryKey           string   `json:"secondaryKey"`
	SecondaryInboundEdges  []string `json:"secondaryInboundEdges,omitempty"`
	SecondaryOutboundEdges []string `json:"secondaryOutboundEdges,omitempty"`
	Score                  float64  `json:"score,omitempty"`
	Criterion              string   `json:"criterion,omitempty"`
}

// NewCommand returns the cobra.Command for the candidates command group.
func NewCommand(natsURL, outputFmt, defaultActor *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "candidates",
		Short: "Inspect identity-hygiene duplicate candidates and submit merges",
	}
	cmd.AddCommand(newListCommand(natsURL, outputFmt))
	cmd.AddCommand(newMergeCommand(natsURL, outputFmt, defaultActor))
	return cmd
}

func newListCommand(natsURL, outputFmt *string) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List duplicate identity candidates from the identity-hygiene Lens",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), output.DefaultTimeout)
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

			keys, err := conn.KVListKeys(ctx, duplicateCandidatesBucket)
			if err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ListError",
						fmt.Sprintf("list %s: %v (is identity-hygiene package installed?)", duplicateCandidatesBucket, err))
				}
				return fmt.Errorf("list %s: %w (is identity-hygiene package installed?)", duplicateCandidatesBucket, err)
			}

			var entries []candidateEntry
			for _, k := range keys {
				entry, err := conn.KVGet(ctx, duplicateCandidatesBucket, k)
				if err != nil {
					continue
				}
				var ce candidateEntry
				if err := json.Unmarshal(entry.Value, &ce); err != nil {
					continue
				}
				entries = append(entries, ce)
			}

			if *outputFmt == "json" {
				return output.PrintJSON(entries)
			}
			if len(entries) == 0 {
				fmt.Println("(no duplicate candidates)")
				return nil
			}
			fmt.Printf("%-45s %-45s %s\n", "PRIMARY_KEY", "SECONDARY_KEY", "SCORE/CRITERION")
			for _, e := range entries {
				scoreStr := fmt.Sprintf("%.2f/%s", e.Score, e.Criterion)
				fmt.Printf("%-45s %-45s %s\n", e.PrimaryKey, e.SecondaryKey, scoreStr)
			}
			return nil
		},
	}
}

func newMergeCommand(natsURL, outputFmt, defaultActor *string) *cobra.Command {
	var actor string
	var force bool

	cmd := &cobra.Command{
		Use:   "merge <primary> <secondary>",
		Short: "Submit a MergeIdentity operation for two identity candidates",
		Long: `merge reads the duplicate-candidates entry for the secondary key
to enumerate inbound/outbound edges, then submits a MergeIdentity
operation with the discovered edge list.

If no duplicate-candidates entry exists for the secondary, the CLI
warns and requires --force to proceed without edge migration.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if actor == "" {
				actor = *defaultActor
			}
			if actor == "" {
				return fmt.Errorf("--actor is required (or set via credential file)")
			}

			primaryKey := args[0]
			secondaryKey := args[1]

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

			// Derive the candidate key from primary + secondary IDs.
			candidateKey := deriveCandidateKey(primaryKey, secondaryKey)

			var edges []string
			kvEntry, err := conn.KVGet(ctx, duplicateCandidatesBucket, candidateKey)
			if err != nil {
				if !force {
					if *outputFmt == "json" {
						return output.PrintJSONError("CandidateNotFound",
							fmt.Sprintf("no duplicate-candidates entry for %s → %s; use --force to proceed without edge migration",
								primaryKey, secondaryKey))
					}
					fmt.Fprintf(os.Stderr, "warning: no duplicate-candidates entry for secondary %s\n", secondaryKey)
					fmt.Fprintf(os.Stderr, "use --force to proceed without edge migration (the Processor will reject if undeclared edges exist)\n")
					os.Exit(1)
				}
			} else {
				var ce candidateEntry
				if err := json.Unmarshal(kvEntry.Value, &ce); err == nil {
					edges = append(edges, ce.SecondaryInboundEdges...)
					edges = append(edges, ce.SecondaryOutboundEdges...)
				}
			}

			payload, _ := json.Marshal(map[string]interface{}{
				"primary":   primaryKey,
				"secondary": secondaryKey,
				"edges":     edges,
			})

			requestID, err := substrate.NewNanoID()
			if err != nil {
				return fmt.Errorf("generate requestId: %w", err)
			}

			env := &processor.OperationEnvelope{
				RequestID:     requestID,
				Lane:          processor.LaneDefault,
				OperationType: "MergeIdentity",
				Actor:         actor,
				SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
				Payload:       json.RawMessage(payload),
			}

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
	cmd.Flags().BoolVar(&force, "force", false, "proceed without edge migration if no candidate entry exists")
	return cmd
}

// deriveCandidateKey builds the candidate bucket key from primary + secondary keys.
// Key shape: flagged.identity.<primaryID>.identity.<secondaryID>
func deriveCandidateKey(primaryKey, secondaryKey string) string {
	primaryID := stripPrefix(primaryKey, "vtx.identity.")
	secondaryID := stripPrefix(secondaryKey, "vtx.identity.")
	return "flagged.identity." + primaryID + ".identity." + secondaryID
}

func stripPrefix(s, prefix string) string {
	if strings.HasPrefix(s, prefix) {
		return s[len(prefix):]
	}
	return s
}

// submitOp publishes to ops.<lane> via NATS request-reply.
func submitOp(ctx context.Context, conn *substrate.Conn, env *processor.OperationEnvelope) (*processor.OperationReply, error) {
	data, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("marshal envelope: %w", err)
	}
	subject := "ops." + string(env.Lane)
	msg, err := conn.NATS().RequestWithContext(ctx, subject, data)
	if err != nil {
		return nil, fmt.Errorf("NATS request to %s: %w", subject, err)
	}
	var reply processor.OperationReply
	if err := json.Unmarshal(msg.Data, &reply); err != nil {
		return nil, fmt.Errorf("parse reply: %w", err)
	}
	return &reply, nil
}
