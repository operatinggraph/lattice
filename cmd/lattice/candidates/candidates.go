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

	"github.com/operatinggraph/lattice/cmd/lattice/output"
	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// duplicateCandidatesBucket is the NATS KV bucket written by the
// identity-hygiene duplicateCandidates Lens.
const duplicateCandidatesBucket = "duplicate-candidates"

// candidateEntry mirrors the re-authored duplicateCandidates lens row shape
// (dedup-over-encrypted-pii-design.md §3.3): bare NanoIDs + full keys only —
// no PII, no edge columns (this CLI enumerates the secondary's edges itself,
// §3.3/§3.4, a strictly more truthful source than the lens ever carried).
type candidateEntry struct {
	PrimaryID    string   `json:"primaryId"`
	SecondaryID  string   `json:"secondaryId"`
	PrimaryKey   string   `json:"primaryKey"`
	SecondaryKey string   `json:"secondaryKey"`
	Criteria     []string `json:"criteria,omitempty"`
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
				ce.Criteria = duplicateOfCriteria(ctx, conn, ce.PrimaryID, ce.SecondaryID)
				entries = append(entries, ce)
			}

			if *outputFmt == "json" {
				return output.PrintJSON(entries)
			}
			if len(entries) == 0 {
				fmt.Println("(no duplicate candidates)")
				return nil
			}
			fmt.Printf("%-45s %-45s %s\n", "PRIMARY_KEY", "SECONDARY_KEY", "CRITERIA")
			for _, e := range entries {
				fmt.Printf("%-45s %-45s %s\n", e.PrimaryKey, e.SecondaryKey, strings.Join(e.Criteria, ","))
			}
			return nil
		},
	}
}

// duplicateOfCriteria KVGets the pair's duplicateOf link doc for display.
// identity-domain's CreateUnclaimedIdentity always writes it
// secondary→primary (the later-arriving identity is the source, Contract #1
// §1.1), but both directional keys are cheap to derive from the row's bare
// IDs, so try both — one hits.
func duplicateOfCriteria(ctx context.Context, conn *substrate.Conn, primaryID, secondaryID string) []string {
	for _, key := range []string{
		"lnk.identity." + secondaryID + ".duplicateOf.identity." + primaryID,
		"lnk.identity." + primaryID + ".duplicateOf.identity." + secondaryID,
	} {
		entry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, key)
		if err != nil {
			continue
		}
		var doc struct {
			Data struct {
				Criteria []string `json:"criteria"`
			} `json:"data"`
		}
		if json.Unmarshal(entry.Value, &doc) == nil {
			return doc.Data.Criteria
		}
	}
	return nil
}

func newMergeCommand(natsURL, outputFmt, defaultActor *string) *cobra.Command {
	var actor string

	cmd := &cobra.Command{
		Use:   "merge <primary> <secondary>",
		Short: "Submit a MergeIdentity operation for two identity candidates",
		Long: `merge enumerates the secondary identity's live links directly (bounded,
subject-filtered — excluding duplicateOf/indexes pair-evidence classes),
then submits a MergeIdentity operation with the discovered edge list. The
duplicateOf pair link and the secondary's owned identityindex entries are
maintained by the script itself (both declared, not part of the edge list).`,
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
			primaryID := stripPrefix(primaryKey, "vtx.identity.")
			secondaryID := stripPrefix(secondaryKey, "vtx.identity.")

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

			edges, err := enumerateSecondaryEdges(ctx, conn, secondaryID)
			if err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("EnumerateError", err.Error())
				}
				return err
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

			reads := []string{
				primaryKey, secondaryKey,
				primaryKey + ".state", primaryKey + ".mergedInto",
				secondaryKey + ".state", secondaryKey + ".mergedInto",
			}
			reads = append(reads, edges...)

			// dedup-over-encrypted-pii-design.md §3.4: both directional
			// duplicateOf pair-link keys are dispatch-derivable from
			// primary+secondary but only ever one (or neither) is live —
			// optionalReads, absence-tolerant.
			optionalReads := []string{
				"lnk.identity." + secondaryID + ".duplicateOf.identity." + primaryID,
				"lnk.identity." + primaryID + ".duplicateOf.identity." + secondaryID,
			}

			// multi-credential-identity-linking-design.md §3.3: a
			// never-claimed or Scenario-B identity has no credentialBinding
			// aspect — optionalReads, absence-tolerant.
			optionalReads = append(optionalReads,
				secondaryKey+".credentialBinding",
				primaryKey+".credentialBinding",
			)

			env := &processor.OperationEnvelope{
				RequestID:     requestID,
				Lane:          processor.LaneDefault,
				OperationType: "MergeIdentity",
				Actor:         actor,
				SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
				Payload:       json.RawMessage(payload),
				// read-posture (script-read-posture-design §13): Reads is
				// class (a) — the vertices/aspects/edges this CLI already
				// resolved above. OptionalReads is class (d) — the
				// dispatch-derivable duplicateOf probe keys plus the
				// credentialBinding probe keys (multi-credential-identity-
				// linking-design.md §3.3). Enumerations declares the op's
				// two class-(e) kv.Links calls (the secondary-has-open-tasks
				// guard + the indexes-driven repoint, both
				// dedup-over-encrypted-pii-design.md §3.4) as metadata —
				// bounded + paged, never hydrated; the declaration feeds the
				// Edge mirror-coverage gate.
				ContextHint: &processor.ContextHint{
					Reads:         reads,
					OptionalReads: optionalReads,
					Enumerations: []processor.EnumerationHint{
						{Hub: secondaryKey, Relation: "assignedTo", Direction: "in"},
						{Hub: secondaryKey, Relation: "indexes", Direction: "in"},
					},
				},
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
	return cmd
}

// enumerateSecondaryEdges bounded-lists the secondary identity's live links
// in both directions via subject-filtered KVListKeysFilter — outbound
// (secondary as source: `lnk.identity.<sid>.>`) and inbound (secondary as
// target: `lnk.*.*.*.identity.<sid>`, mid-token wildcards) — excluding the
// duplicateOf/indexes pair-evidence classes (dedup-over-encrypted-pii-
// design.md §3.3), which are not business edges and were never part of a
// merge's edge migration. Both filters are server-side-bounded by the
// secondary's degree in that direction, never the keyspace.
func enumerateSecondaryEdges(ctx context.Context, conn *substrate.Conn, secondaryID string) ([]string, error) {
	excludedClass := map[string]bool{"duplicateOf": true, "indexes": true}
	var edges []string
	for _, filter := range []string{
		"lnk.identity." + secondaryID + ".>",
		"lnk.*.*.*.identity." + secondaryID,
	} {
		cursor := ""
		for {
			keys, next, err := conn.KVListKeysFilter(ctx, bootstrap.CoreKVBucket, filter, cursor, 500)
			if err != nil {
				return nil, fmt.Errorf("list %s: %w", filter, err)
			}
			for _, k := range keys {
				parts := strings.Split(k, ".")
				if len(parts) != 6 || excludedClass[parts[3]] {
					continue
				}
				edges = append(edges, k)
			}
			if next == "" {
				break
			}
			cursor = next
		}
	}
	return edges, nil
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
