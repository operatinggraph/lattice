// Package weaver implements the lattice weaver command group: operator
// list/disable/enable/revoke/reset-confidence controls for Weaver convergence targets (FR30),
// via the lattice.ctrl.weaver.* NATS Services control plane.
package weaver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/operatinggraph/lattice/cmd/lattice/output"
	"github.com/operatinggraph/lattice/internal/controlauth"
	"github.com/operatinggraph/lattice/internal/weaver/control"
)

// validateTargetID rejects a targetId that is empty or contains a "." before
// the request is published. The control subject is
// lattice.ctrl.weaver.<targetId>.<op> and the endpoints subscribe a
// single-token wildcard for <targetId>, so a dotted (or empty) targetId builds
// a subject no endpoint matches — the request would otherwise hang to the
// client timeout with an opaque "no responders" rather than a clear error.
// Registered target ids are dot-free single tokens (install-validated), so this
// mirrors the server-side targetId shape.
func validateTargetID(targetID string) error {
	if targetID == "" {
		return fmt.Errorf("targetId must not be empty")
	}
	if strings.Contains(targetID, ".") {
		return fmt.Errorf("targetId %q must not contain '.' (a registered targetId is a single dot-free token)", targetID)
	}
	return nil
}

// NewCommand returns the cobra.Command for the weaver command group.
// defaultActor is the credential-file actor key (op.NewCommand's third arg);
// each subcommand also accepts its own --actor override.
func NewCommand(natsURL, outputFmt, defaultActor *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "weaver",
		Short: "Operate Weaver convergence targets (list/disable/enable/revoke/reset-confidence)",
	}
	cmd.AddCommand(newListCommand(natsURL, outputFmt, defaultActor))
	cmd.AddCommand(newDisableCommand(natsURL, outputFmt, defaultActor))
	cmd.AddCommand(newEnableCommand(natsURL, outputFmt, defaultActor))
	cmd.AddCommand(newRevokeCommand(natsURL, outputFmt, defaultActor))
	cmd.AddCommand(newResetConfidenceCommand(natsURL, outputFmt, defaultActor))
	return cmd
}

// addActorFlag registers the --actor and --actor-token flags shared by every
// subcommand, defaulting --actor at RunE time to *defaultActor (the
// credential-file actorKey) when unset. A resolved-empty actor is NOT an
// error here (unlike the write-path `op submit`): in Fire 1 posture (no JWT
// trust root configured server-side) the capability gate is not enforced, so
// an anonymous request must keep working. --actor-token carries a signed
// actor JWT (Fire 2, control-plane-capability-authz-design.md — mint one
// with `gateway dev-token -sub <identityNanoID>` against a dev-mode server);
// when set it is stamped in place of --actor and wins if both are given,
// since presenting a token is the deliberate opt-in to verified-actor mode.
func addActorFlag(cmd *cobra.Command, actor, actorToken *string) {
	cmd.Flags().StringVar(actor, "actor", "", "actor key stamped on the control request (defaults to credential file actorKey)")
	cmd.Flags().StringVar(actorToken, "actor-token", "", "signed actor JWT stamped on the control request (Fire 2 verified-actor mode; overrides --actor)")
}

// resolveActorHeader picks the control-request HeaderActor value: actorToken
// wins when non-empty (verified-actor mode), otherwise the raw actor key
// (Fire 1 self-asserted mode).
func resolveActorHeader(actor, actorToken string) string {
	if actorToken != "" {
		return actorToken
	}
	return actor
}

// request sends a control-plane request to subject, stamping actorHeader as
// the Lattice-Actor header when non-empty, and decodes the
// control.ControlResponse. Connection is via output.Connect's raw *nats.Conn
// (conn.NATS()) since the weaver-control endpoints are plain NATS Services
// responders, not JetStream.
func request(natsURL, subject, actorHeader string) (control.ControlResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), output.DefaultTimeout)
	defer cancel()

	conn, err := output.Connect(ctx, natsURL)
	if err != nil {
		return control.ControlResponse{}, err
	}
	defer conn.Close()

	reply, err := conn.NATS().RequestMsgWithContext(ctx, controlauth.NewActorRequestMsg(subject, actorHeader))
	if err != nil {
		return control.ControlResponse{}, fmt.Errorf("request %s: %w", subject, err)
	}

	var resp control.ControlResponse
	if err := json.Unmarshal(reply.Data, &resp); err != nil {
		return control.ControlResponse{}, fmt.Errorf("decode response from %s: %w", subject, err)
	}
	if resp.Error != "" {
		return resp, fmt.Errorf("%s", resp.Error)
	}
	return resp, nil
}

func newListCommand(natsURL, outputFmt, defaultActor *string) *cobra.Command {
	var actor string
	var actorToken string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List registered Weaver convergence targets",
		RunE: func(cmd *cobra.Command, args []string) error {
			if actor == "" {
				actor = *defaultActor
			}
			resp, err := request(*natsURL, control.ListSubject(), resolveActorHeader(actor, actorToken))
			if err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ControlError", err.Error())
				}
				return err
			}

			if *outputFmt == "json" {
				return output.PrintJSON(resp.Targets)
			}
			if len(resp.Targets) == 0 {
				fmt.Println("(no registered targets)")
				return nil
			}
			fmt.Printf("%-20s %-30s %-10s %s\n", "TARGET_ID", "LENS_REF", "STATE", "GAPS")
			for _, t := range resp.Targets {
				fmt.Printf("%-20s %-30s %-10s %v\n", t.TargetID, t.LensRef, t.State, t.Gaps)
			}
			return nil
		},
	}
	addActorFlag(cmd, &actor, &actorToken)
	return cmd
}

func newDisableCommand(natsURL, outputFmt, defaultActor *string) *cobra.Command {
	var actor string
	var actorToken string
	cmd := &cobra.Command{
		Use:   "disable <targetId>",
		Short: "Disable a Weaver convergence target (pause dispatch)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if actor == "" {
				actor = *defaultActor
			}
			targetID := args[0]
			if err := validateTargetID(targetID); err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ControlError", err.Error())
				}
				return err
			}
			resp, err := request(*natsURL, control.TargetSubject(targetID, "disable"), resolveActorHeader(actor, actorToken))
			if err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ControlError", err.Error())
				}
				return err
			}

			if *outputFmt == "json" {
				return output.PrintJSON(resp.Disable)
			}
			fmt.Printf("target %q disabled\n", targetID)
			return nil
		},
	}
	addActorFlag(cmd, &actor, &actorToken)
	return cmd
}

func newEnableCommand(natsURL, outputFmt, defaultActor *string) *cobra.Command {
	var actor string
	var actorToken string
	cmd := &cobra.Command{
		Use:   "enable <targetId>",
		Short: "Enable a Weaver convergence target (resume dispatch)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if actor == "" {
				actor = *defaultActor
			}
			targetID := args[0]
			if err := validateTargetID(targetID); err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ControlError", err.Error())
				}
				return err
			}
			resp, err := request(*natsURL, control.TargetSubject(targetID, "enable"), resolveActorHeader(actor, actorToken))
			if err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ControlError", err.Error())
				}
				return err
			}

			if *outputFmt == "json" {
				return output.PrintJSON(resp.Enable)
			}
			fmt.Printf("target %q enabled\n", targetID)
			return nil
		},
	}
	addActorFlag(cmd, &actor, &actorToken)
	return cmd
}

func newRevokeCommand(natsURL, outputFmt, defaultActor *string) *cobra.Command {
	var actor string
	var actorToken string
	cmd := &cobra.Command{
		Use:   "revoke <targetId>",
		Short: "Revoke a Weaver convergence target (remove durable + in-flight marks; stays disabled)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if actor == "" {
				actor = *defaultActor
			}
			targetID := args[0]
			if err := validateTargetID(targetID); err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ControlError", err.Error())
				}
				return err
			}
			resp, err := request(*natsURL, control.TargetSubject(targetID, "revoke"), resolveActorHeader(actor, actorToken))
			if err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ControlError", err.Error())
				}
				return err
			}

			if *outputFmt == "json" {
				return output.PrintJSON(resp.Revoke)
			}
			fmt.Printf("target %q revoked\n", targetID)
			return nil
		},
	}
	addActorFlag(cmd, &actor, &actorToken)
	return cmd
}

// newResetConfidenceCommand builds `lattice weaver reset-confidence
// <targetId>` — the middle rung of the operator-severity ladder between
// disable (deletes nothing) and revoke (deletes everything): it drains the
// target's `__effect` confidence windows and nothing else, clearing a standing
// LensEffectMismatch raised by windows the pre-5b58f66 bookkeeping polluted.
func newResetConfidenceCommand(natsURL, outputFmt, defaultActor *string) *cobra.Command {
	var actor string
	var actorToken string
	cmd := &cobra.Command{
		Use:   "reset-confidence <targetId>",
		Short: "Drain a target's __effect confidence windows (advisory data only; dispatch state untouched)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if actor == "" {
				actor = *defaultActor
			}
			targetID := args[0]
			if err := validateTargetID(targetID); err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ControlError", err.Error())
				}
				return err
			}
			resp, err := request(*natsURL, control.TargetSubject(targetID, "resetConfidence"), resolveActorHeader(actor, actorToken))
			if err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ControlError", err.Error())
				}
				return err
			}

			if *outputFmt == "json" {
				return output.PrintJSON(resp.ResetConfidence)
			}
			deleted := 0
			if resp.ResetConfidence != nil {
				deleted = resp.ResetConfidence.WindowsDeleted
			}
			fmt.Printf("target %q confidence reset (%d window(s) deleted)\n", targetID, deleted)
			return nil
		},
	}
	addActorFlag(cmd, &actor, &actorToken)
	return cmd
}
