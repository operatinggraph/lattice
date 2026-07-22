// Package lens implements the lattice lens command group.
package lens

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/operatinggraph/lattice/cmd/lattice/output"
	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/processor"
	refractorlens "github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// NewCommand returns the cobra.Command for the lens command group.
func NewCommand(natsURL, outputFmt, defaultActor *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "lens",
		Short: "Manage Lens lifecycle and inspect projection lag",
	}
	cmd.AddCommand(newListCommand(natsURL, outputFmt))
	cmd.AddCommand(newActivateCommand(natsURL, outputFmt, defaultActor))
	cmd.AddCommand(newDeactivateCommand(natsURL, outputFmt, defaultActor))
	cmd.AddCommand(newLagCommand(natsURL, outputFmt))
	cmd.AddCommand(newEmitDDLCommand(natsURL, outputFmt))
	return cmd
}

// newEmitDDLCommand prints the out-of-band provisioning DDL for every installed
// protected/grant Postgres read-path lens (Contract #6 §6.14, verify-and-pause).
// Refractor no longer issues this DDL at activation — it verifies the posture
// and pauses fail-closed — so the operator (or `make provision-readpath`)
// applies this script against the read-model database out-of-band. It is
// read-only against Core KV and connects to no Postgres. The grant table is
// emitted first (every protected policy references it).
func newEmitDDLCommand(natsURL, outputFmt *string) *cobra.Command {
	return &cobra.Command{
		Use:   "emit-ddl",
		Short: "Print the out-of-band DDL for installed protected/grant read-path lenses",
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

			stmts, err := refractorlens.EmitReadPathDDL(ctx, conn, bootstrap.CoreKVBucket)
			if err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("EmitError", err.Error())
				}
				return fmt.Errorf("emit read-path DDL: %w", err)
			}

			if *outputFmt == "json" {
				return output.PrintJSON(stmts)
			}
			if len(stmts) == 0 {
				fmt.Fprintln(os.Stderr, "-- no protected/grant read-path lenses installed; nothing to provision")
				return nil
			}
			fmt.Print(refractorlens.ReadPathDDLScript(stmts))
			return nil
		},
	}
}

// lensEntry is the display shape for a Lens meta-vertex.
type lensEntry struct {
	Key           string `json:"key"`
	CanonicalName string `json:"canonicalName"`
	IsDeleted     bool   `json:"isDeleted"`
}

func newListCommand(natsURL, outputFmt *string) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List installed Lenses",
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

			allKeys, err := conn.KVListKeys(ctx, bootstrap.CoreKVBucket)
			if err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ListError", err.Error())
				}
				return fmt.Errorf("list core KV keys: %w", err)
			}

			var lenses []lensEntry
			for _, k := range allKeys {
				if !strings.HasPrefix(k, "vtx.meta.") {
					continue
				}
				// Only top-level meta-vertex keys (3 segments).
				parts := strings.Split(k, ".")
				if len(parts) != 3 {
					continue
				}
				entry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, k)
				if err != nil {
					continue
				}
				var doc map[string]interface{}
				if err := json.Unmarshal(entry.Value, &doc); err != nil {
					continue
				}
				class, _ := doc["class"].(string)
				if class != "meta.lens" {
					continue
				}
				isDeleted, _ := doc["isDeleted"].(bool)
				lenses = append(lenses, lensEntry{
					Key:           k,
					CanonicalName: canonicalNameFromDoc(doc),
					IsDeleted:     isDeleted,
				})
			}

			if *outputFmt == "json" {
				return output.PrintJSON(lenses)
			}
			fmt.Printf("%-45s %-30s %s\n", "KEY", "CANONICAL_NAME", "IS_DELETED")
			for _, l := range lenses {
				fmt.Printf("%-45s %-30s %v\n", l.Key, l.CanonicalName, l.IsDeleted)
			}
			return nil
		},
	}
}

// canonicalNameFromDoc extracts the canonicalName from a vertex data map.
func canonicalNameFromDoc(doc map[string]interface{}) string {
	if data, ok := doc["data"].(map[string]interface{}); ok {
		if cn, ok := data["canonicalName"].(string); ok {
			return cn
		}
	}
	return ""
}

func newActivateCommand(natsURL, outputFmt, defaultActor *string) *cobra.Command {
	var actor string

	cmd := &cobra.Command{
		Use:   "activate <file>",
		Short: "Submit a CreateMetaVertex operation for a Lens definition",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if actor == "" {
				actor = *defaultActor
			}
			if actor == "" {
				return fmt.Errorf("--actor is required (or set via credential file)")
			}

			payloadBytes, err := os.ReadFile(args[0])
			if err != nil {
				return fmt.Errorf("read %s: %w", args[0], err)
			}

			requestID, err := substrate.NewNanoID()
			if err != nil {
				return fmt.Errorf("generate requestId: %w", err)
			}

			env := &processor.OperationEnvelope{
				RequestID:     requestID,
				Lane:          processor.LaneMeta,
				OperationType: "CreateMetaVertex",
				Actor:         actor,
				SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
				Class:         "meta.lens",
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
				return fmt.Errorf("rejected: %s — %s", reply.Error.Code, reply.Error.Message)
			}

			if *outputFmt == "json" {
				return output.PrintJSON(reply)
			}
			fmt.Printf("requestId: %s\nmetaKey:   %s\n", reply.RequestID, reply.PrimaryKey)
			return nil
		},
	}

	cmd.Flags().StringVar(&actor, "actor", "", "actor key (defaults to credential file actorKey)")
	return cmd
}

func newDeactivateCommand(natsURL, outputFmt, defaultActor *string) *cobra.Command {
	var actor string

	cmd := &cobra.Command{
		Use:   "deactivate <key>",
		Short: "Submit a TombstoneMetaVertex operation for a Lens",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if actor == "" {
				actor = *defaultActor
			}
			if actor == "" {
				return fmt.Errorf("--actor is required (or set via credential file)")
			}

			metaKey := args[0]
			requestID, err := substrate.NewNanoID()
			if err != nil {
				return fmt.Errorf("generate requestId: %w", err)
			}

			payload, _ := json.Marshal(map[string]string{"metaKey": metaKey})
			env := &processor.OperationEnvelope{
				RequestID:     requestID,
				Lane:          processor.LaneMeta,
				OperationType: "TombstoneMetaVertex",
				Actor:         actor,
				SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
				Payload:       json.RawMessage(payload),
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
				return fmt.Errorf("rejected: %s — %s", reply.Error.Code, reply.Error.Message)
			}

			if *outputFmt == "json" {
				return output.PrintJSON(reply)
			}
			fmt.Printf("requestId: %s\nstatus:    %s\n", reply.RequestID, reply.Status)
			return nil
		},
	}

	cmd.Flags().StringVar(&actor, "actor", "", "actor key (defaults to credential file actorKey)")
	return cmd
}

func newLagCommand(natsURL, outputFmt *string) *cobra.Command {
	return &cobra.Command{
		Use:   "lag",
		Short: "Show per-Lens projection lag from Health KV",
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

			allKeys, err := conn.KVListKeys(ctx, bootstrap.HealthKVBucket)
			if err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ListError", err.Error())
				}
				return fmt.Errorf("list health KV keys: %w", err)
			}

			type lagEntry struct {
				Key string      `json:"key"`
				Doc interface{} `json:"doc"`
			}
			var entries []lagEntry
			for _, k := range allKeys {
				if !strings.HasPrefix(k, "health.refractor.") {
					continue
				}
				entry, err := conn.KVGet(ctx, bootstrap.HealthKVBucket, k)
				if err != nil {
					continue
				}
				var doc interface{}
				_ = json.Unmarshal(entry.Value, &doc)
				entries = append(entries, lagEntry{Key: k, Doc: doc})
			}

			if *outputFmt == "json" {
				return output.PrintJSON(entries)
			}
			if len(entries) == 0 {
				fmt.Println("(no refractor health entries found)")
				return nil
			}
			fmt.Printf("%-50s %s\n", "KEY", "VALUE")
			for _, e := range entries {
				val, _ := json.Marshal(e.Doc)
				fmt.Printf("%-50s %s\n", e.Key, string(val))
			}
			return nil
		},
	}
}

// submitOp publishes to ops.<lane> and polls the Core KV tracker key for
// the commit signal. Delegates to output.SubmitOp.
func submitOp(ctx context.Context, conn *substrate.Conn, env *processor.OperationEnvelope) (*processor.OperationReply, error) {
	return output.SubmitOp(ctx, conn, env)
}
