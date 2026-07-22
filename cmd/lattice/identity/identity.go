// Package identity implements the lattice identity command group.
package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/operatinggraph/lattice/cmd/lattice/output"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// mintClaimSecret generates a fresh client-side claim secret and returns the
// plaintext (shown to the operator once) plus its lowercase-hex sha256 hash
// (submitted to Lattice). Option C: the plaintext never enters Lattice.
func mintClaimSecret() (plaintext, hash string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	plaintext = hex.EncodeToString(buf)
	sum := sha256.Sum256([]byte(plaintext))
	return plaintext, hex.EncodeToString(sum[:]), nil
}

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
default lane. The CLI mints a one-time claim secret locally, submits only its
sha256 hash (claimKeyHash) in the payload, and prints the plaintext once. The
plaintext never enters Lattice. On acceptance, prints the requestId,
opTrackerKey, the created identity key (primaryKey), and the claim secret.`,
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

			// Option C: the client mints the claim secret and submits only its
			// hash. Merge claimKeyHash into the payload object.
			claimPlaintext, claimHash, err := mintClaimSecret()
			if err != nil {
				return fmt.Errorf("mint claim secret: %w", err)
			}
			var payloadObj map[string]any
			if err := json.Unmarshal(payloadBytes, &payloadObj); err != nil {
				return fmt.Errorf("payload must be a JSON object: %w", err)
			}
			if payloadObj == nil {
				payloadObj = map[string]any{}
			}
			payloadObj["claimKeyHash"] = claimHash
			payloadBytes, err = json.Marshal(payloadObj)
			if err != nil {
				return fmt.Errorf("payload: re-encode: %w", err)
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
				ContextHint:   &processor.ContextHint{OptionalReads: identityIndexProbeKeys(payloadObj)},
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

			// The claim plaintext is meaningful only when THIS op created the
			// identity: its hash matches the stored aspect only on `accepted`.
			// On `duplicate` the stored hash belongs to the original op, so this
			// invocation's plaintext would be misleading — suppress it.
			accepted := reply.Status == processor.ReplyStatusAccepted
			if *outputFmt == "json" {
				if accepted {
					// Option C: the plaintext is delivered here only — it never
					// enters Lattice, so it is not in the reply.
					return output.PrintJSON(struct {
						processor.OperationReply
						ClaimKey string `json:"claimKey"`
					}{OperationReply: *reply, ClaimKey: claimPlaintext})
				}
				return output.PrintJSON(reply)
			}
			fmt.Printf("requestId:    %s\nopTrackerKey: %s\nstatus:       %s\n",
				reply.RequestID, reply.OpTrackerKey, reply.Status)
			if reply.PrimaryKey != "" {
				fmt.Printf("identityKey:  %s\n", reply.PrimaryKey)
			}
			if accepted {
				fmt.Printf("claimKey:     %s\n", claimPlaintext)
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
				// ClaimIdentity's scope=self is enforced at step 3 via a
				// self-match gate (authContext.target == actor) — omitting
				// this rejects with AuthDenied/NoCapabilityEntry regardless
				// of role grants (packages/identity-domain/permissions.go,
				// claim_test.go's TestClaimIdentity_Success).
				AuthContext: &processor.AuthContext{Target: actor},
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

// identityIndexProbeKeys computes the dedup identityindex probe keys
// (email/phone/name) for a CreateUnclaimedIdentity payload, mirroring the
// normalization identity-domain's script applies (packages/identity-domain/ddls.go)
// byte-for-byte so the derived crypto.SHA256NanoID keys match. Declaring
// them as optionalReads activates the dormant duplicate-flag probe and
// avoids the RevisionConflict a duplicate contact would otherwise hit.
func identityIndexProbeKeys(payload map[string]any) []string {
	keys := []string{}
	if email, ok := payload["email"].(string); ok {
		if e := strings.ToLower(strings.TrimSpace(email)); e != "" {
			keys = append(keys, "vtx.identityindex."+substrate.SHA256NanoID("email:"+e))
		}
	}
	if phone, ok := payload["phone"].(string); ok {
		var b strings.Builder
		for _, ch := range phone {
			if (ch >= '0' && ch <= '9') || ch == '+' {
				b.WriteRune(ch)
			}
		}
		if p := b.String(); p != "" {
			keys = append(keys, "vtx.identityindex."+substrate.SHA256NanoID("phone:"+p))
		}
	}
	if name, ok := payload["name"].(string); ok {
		if n := strings.Join(strings.Fields(strings.ToLower(name)), " "); n != "" {
			keys = append(keys, "vtx.identityindex."+substrate.SHA256NanoID("name:"+n))
		}
	}
	return keys
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
