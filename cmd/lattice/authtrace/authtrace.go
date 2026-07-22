// Package authtrace implements the lattice auth-trace command.
//
// auth-trace is the operator-level surface for reading three-plane auth
// trace records. It is distinct from `lattice op trace`, which is
// operation-focused (quick check). auth-trace provides the full JSON
// detail of the AuthTraceRecord including all three planes.
package authtrace

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/operatinggraph/lattice/cmd/lattice/output"
	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/processor"
)

// NewCommand returns the cobra.Command for the auth-trace command.
func NewCommand(natsURL, outputFmt *string) *cobra.Command {
	var instance string

	cmd := &cobra.Command{
		Use:   "auth-trace <requestId>",
		Short: "Read the three-plane auth trace record for a request",
		Long: `auth-trace reads the AuthTraceRecord for the given requestId from
Health KV at health.processor.<instance>.auth-trace.<requestId>.

Records are written on auth denials and optionally on allows
(when TraceAllowDecisions is ON on the Processor). Records expire
after 1 hour (Story 3.5 AC TTL).

Use --instance to select a specific Processor instance (default: "default").`,
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
					return nil
				}
				return err
			}
			defer conn.Close()

			entry, err := conn.KVGet(ctx, bootstrap.HealthKVBucket, traceKey)
			if err != nil {
				if *outputFmt == "json" {
					return output.PrintJSON(map[string]interface{}{
						"requestId": requestID,
						"found":     false,
						"message":   "no auth-trace record found (allowed ops not traced by default, or record expired after 1h TTL)",
					})
				}
				fmt.Printf("no auth-trace record for requestId=%s\n", requestID)
				fmt.Println("(allowed ops are not traced by default; denied ops are traced for 1h)")
				return nil
			}

			var record processor.AuthTraceRecord
			if err := json.Unmarshal(entry.Value, &record); err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ParseError", err.Error())
				}
				return fmt.Errorf("parse auth trace record: %w", err)
			}

			if *outputFmt == "json" {
				return output.PrintJSON(record)
			}

			// Human-readable three-plane output.
			fmt.Printf("Auth Trace Record\n")
			fmt.Printf("  requestId:   %s\n", record.RequestID)
			fmt.Printf("  actor:       %s\n", record.Actor)
			fmt.Printf("  operation:   %s\n", record.Operation)
			fmt.Printf("  authOutcome: %s\n", record.AuthOutcome)
			if record.AuthCode != "" {
				fmt.Printf("  authCode:    %s\n", record.AuthCode)
			}
			if record.AuthReason != "" {
				fmt.Printf("  authReason:  %s\n", record.AuthReason)
			}
			fmt.Printf("  observedAt:  %s\n", record.ObservedAt)
			fmt.Printf("\nPlane 1 — Capability KV\n")
			fmt.Printf("  capKey:      %s\n", record.Plane1.CapabilityKVKey)
			fmt.Printf("  projectedAt: %s\n", record.Plane1.ProjectedAt)
			fmt.Printf("  result:      %s\n", record.Plane1.Result)
			fmt.Printf("\nPlane 2 — Lens Definition\n")
			fmt.Printf("  lensKey:     %s\n", record.Plane2.LensDefinitionKey)
			fmt.Printf("  lensRev:     %d\n", record.Plane2.LensRevisionAtProjection)
			fmt.Printf("  ruleHash:    %s\n", record.Plane2.CypherRuleBodyHash)
			fmt.Printf("\nPlane 3 — Source Vertex Revisions\n")
			for k, rev := range record.Plane3.SourceVertexRevisions {
				fmt.Printf("  %s → rev %d\n", k, rev)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&instance, "instance", "default", "Processor instance name")
	return cmd
}
