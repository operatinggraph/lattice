// Package weaver implements the lattice weaver command group: operator
// list/disable/enable/revoke/reset-confidence/reset-budget controls for Weaver
// convergence targets (FR30),
// via the lattice.ctrl.weaver.* NATS Services control plane.
package weaver

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/operatinggraph/lattice/cmd/lattice/output"
	"github.com/operatinggraph/lattice/internal/controlauth"
	"github.com/operatinggraph/lattice/internal/substrate/keys"
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

// validateEntityID and validateGapColumn accept exactly the two extra subject
// tokens `reset-budget` carries — a Contract #1 20-character NanoID entityId
// and a §10.2 missing_<gap> column — and reject everything else. Both are
// whitelists, not rejected-character lists: the tokens ride in the control
// subject and the engine builds a weaver-state key out of them, so a shape
// nobody anticipated must fail rather than travel. The responder re-checks with
// the same rule (weaver.ValidateGapScope, which these two agree with case for
// case — a test pins that); checking here is what turns a typo into a clear
// local error instead of a request to a subject no endpoint matches and an
// opaque "no responders" at the client timeout.
//
// gapColumnPattern is the one restated shape: it spells out the missing_ prefix
// and the single-token charset the engine validates with its own unexported
// constants. The entityId rule is not restated — it reads the canonical
// validator off substrate/keys, the leaf package that owns the alphabet.
var gapColumnPattern = regexp.MustCompile(`^missing_[A-Za-z0-9_-]+$`)

func validateEntityID(entityID string) error {
	if !keys.IsValidNanoID(entityID) {
		return fmt.Errorf("entityId %q must be a %d-character NanoID (Contract #1 alphabet: A-Za-z0-9 minus I, l, O, 0)",
			entityID, keys.NanoIDLength)
	}
	return nil
}

func validateGapColumn(gapColumn string) error {
	if !gapColumnPattern.MatchString(gapColumn) {
		return fmt.Errorf("gapColumn %q must be a missing_<gap> token of letters, digits, '_' or '-'", gapColumn)
	}
	return nil
}

// NewCommand returns the cobra.Command for the weaver command group.
// defaultActor is the credential-file actor key (op.NewCommand's third arg);
// each subcommand also accepts its own --actor override.
func NewCommand(natsURL, outputFmt, defaultActor *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "weaver",
		Short: "Operate Weaver convergence targets (list/disable/enable/revoke/reset-confidence/reset-budget)",
	}
	cmd.AddCommand(newListCommand(natsURL, outputFmt, defaultActor))
	cmd.AddCommand(newDisableCommand(natsURL, outputFmt, defaultActor))
	cmd.AddCommand(newEnableCommand(natsURL, outputFmt, defaultActor))
	cmd.AddCommand(newRevokeCommand(natsURL, outputFmt, defaultActor))
	cmd.AddCommand(newResetConfidenceCommand(natsURL, outputFmt, defaultActor))
	cmd.AddCommand(newResetBudgetCommand(natsURL, outputFmt, defaultActor))
	return cmd
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
			resp, err := request(*natsURL, control.ListSubject(), output.ResolveActorHeader(actor, actorToken))
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
	output.AddActorFlags(cmd, &actor, &actorToken)
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
			resp, err := request(*natsURL, control.TargetSubject(targetID, "disable"), output.ResolveActorHeader(actor, actorToken))
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
	output.AddActorFlags(cmd, &actor, &actorToken)
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
			resp, err := request(*natsURL, control.TargetSubject(targetID, "enable"), output.ResolveActorHeader(actor, actorToken))
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
	output.AddActorFlags(cmd, &actor, &actorToken)
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
			resp, err := request(*natsURL, control.TargetSubject(targetID, "revoke"), output.ResolveActorHeader(actor, actorToken))
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
	output.AddActorFlags(cmd, &actor, &actorToken)
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
			resp, err := request(*natsURL, control.TargetSubject(targetID, "resetConfidence"), output.ResolveActorHeader(actor, actorToken))
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
	output.AddActorFlags(cmd, &actor, &actorToken)
	return cmd
}

// newResetBudgetCommand builds `lattice weaver reset-budget <targetId>
// <entityId> <gapColumn>` — the un-park verb, and the one rung of the
// operator-severity ladder that deletes nothing: it zeroes ONE gap's
// retry-budget dispatch-count so the reconciler's next pass finds that gap
// un-suppressed and re-arms it (clearing the standing GapBudgetExhausted issue
// and dispatching). Scope is a single (target, entity, gap) because the budget
// and the issue are both keyed that way — a target-wide reset would re-arm
// parks nobody looked at.
//
// The two extra scope tokens ride in the control subject, so request() needs no
// change; they are validated locally first, because a malformed token builds a
// subject the responder's wildcards cannot match.
func newResetBudgetCommand(natsURL, outputFmt, defaultActor *string) *cobra.Command {
	var actor string
	var actorToken string
	cmd := &cobra.Command{
		Use:   "reset-budget <targetId> <entityId> <gapColumn>",
		Short: "Reset one gap's retry budget so the reconciler re-arms it (un-park an exhausted gap)",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			if actor == "" {
				actor = *defaultActor
			}
			targetID, entityID, gapColumn := args[0], args[1], args[2]
			for _, err := range []error{
				validateTargetID(targetID),
				validateEntityID(entityID),
				validateGapColumn(gapColumn),
			} {
				if err != nil {
					if *outputFmt == "json" {
						return output.PrintJSONError("ControlError", err.Error())
					}
					return err
				}
			}
			resp, err := request(*natsURL, control.ResetBudgetSubject(targetID, entityID, gapColumn),
				output.ResolveActorHeader(actor, actorToken))
			if err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ControlError", err.Error())
				}
				return err
			}

			if *outputFmt == "json" {
				return output.PrintJSON(resp.ResetBudget)
			}
			if resp.ResetBudget == nil || !resp.ResetBudget.Found {
				fmt.Printf("target %q gap %s/%s had no retry budget recorded (nothing to reset)\n",
					targetID, entityID, gapColumn)
				return nil
			}
			fmt.Printf("target %q gap %s/%s retry budget reset (was %d); the next reconciler sweep re-arms it\n",
				targetID, entityID, gapColumn, resp.ResetBudget.ClearedCount)
			return nil
		},
	}
	output.AddActorFlags(cmd, &actor, &actorToken)
	return cmd
}
