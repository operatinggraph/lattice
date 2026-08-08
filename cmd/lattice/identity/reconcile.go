package identity

import (
	"context"
	"encoding/json"
	"errors"
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

// credentialIndexPrefix is the enumerable half of a credential binding. The
// vertex carries {actorKey, identityKey, boundAt} in plaintext, so the whole
// edge set reconstructs without ever decrypting the sensitive credentialBinding
// aspect that holds the same facts.
const credentialIndexPrefix = "vtx.credentialindex."

// credentialIndexDoc is the subset of the index vertex this driver reads.
type credentialIndexDoc struct {
	IsDeleted bool `json:"isDeleted"`
	Data      struct {
		ActorKey    string `json:"actorKey"`
		IdentityKey string `json:"identityKey"`
	} `json:"data"`
}

// linkDoc is the subset of a boundTo link this driver reads to decide whether
// a credential still needs reconciling.
type linkDoc struct {
	IsDeleted bool `json:"isDeleted"`
}

// reconcileReport is what the command prints, in both output modes.
type reconcileReport struct {
	Scanned    int      `json:"scanned"`
	Tombstoned int      `json:"tombstoned"`
	Retracted  int      `json:"retracted"`
	SelfLoop   int      `json:"selfLoop"`
	AlreadyOK  int      `json:"alreadyLinked"`
	Submitted  int      `json:"submitted"`
	Rejected   int      `json:"rejected"`
	DryRun     bool     `json:"dryRun"`
	Failures   []string `json:"failures,omitempty"`
}

const identityPrefix = "vtx.identity."

// boundToKey mirrors the package's own key builder. It validates rather than
// slices blind: the two halves come out of a stored document, and a short or
// wrongly-typed value there would otherwise panic the whole run instead of
// reporting the malformed vertex the way every other parse failure here does.
func boundToKey(credentialActorKey, ownerIdentityKey string) (string, error) {
	for _, k := range []string{credentialActorKey, ownerIdentityKey} {
		if !strings.HasPrefix(k, identityPrefix) || len(k) == len(identityPrefix) {
			return "", fmt.Errorf("%q is not a %s<NanoID> key", k, identityPrefix)
		}
	}
	return "lnk.identity." + credentialActorKey[len(identityPrefix):] +
		".boundTo.identity." + ownerIdentityKey[len(identityPrefix):], nil
}

func newReconcileBindingsCommand(natsURL, outputFmt, defaultActor *string) *cobra.Command {
	var actor string
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "reconcile-bindings",
		Short: "Converge every credential's boundTo link onto its credentialindex vertex",
		Long: `reconcile-bindings walks the credentialindex keyspace and submits one
ReconcileCredentialBinding operation per credential that has a live index
vertex and no boundTo edge at all.

Its reason to exist is that credentials bound before the boundTo link type
existed have an index vertex and no edge, so a lens over the edge projects
nothing for them. It reaches exactly the credentials the index records: an
identity whose only sign-in is a provisioned raw actor has no index vertex,
and no run makes an edge for it.

It never overturns a retraction. A tombstoned edge is left alone in both this
command and the operation — an unlink tombstones the index alongside it, and
an erasure tombstones the edge while leaving the index standing, so
re-publishing either would undo a removal somebody meant. Safe to re-run: a
converged credential is skipped.

Requires an actor holding ReconcileCredentialBinding (scope=any) — the
operator role.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if actor == "" {
				actor = *defaultActor
			}
			if actor == "" {
				return fmt.Errorf("--actor is required (or set via credential file)")
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
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

			report, err := reconcileBindings(ctx, conn, actor, dryRun)
			if err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ReconcileError", err.Error())
				}
				return err
			}

			if *outputFmt == "json" {
				if err := output.PrintJSON(report); err != nil {
					return err
				}
			} else {
				fmt.Printf("scanned:       %d\ntombstoned:    %d\nretracted:     %d\nselfLoop:      %d\nalreadyLinked: %d\nsubmitted:     %d\nrejected:      %d\ndryRun:        %t\n",
					report.Scanned, report.Tombstoned, report.Retracted, report.SelfLoop,
					report.AlreadyOK, report.Submitted, report.Rejected, report.DryRun)
				for _, f := range report.Failures {
					fmt.Fprintln(os.Stderr, "rejected: "+f)
				}
			}
			// Both output modes exit non-zero on a rejection: a wrapper reading
			// JSON must not see success on a run that reconciled nothing.
			if report.Rejected > 0 {
				os.Exit(1)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&actor, "actor", "", "actor key (defaults to credential file actorKey)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what would be submitted without submitting anything")
	return cmd
}

// reconcileBindings is the driver proper, split out so its counting is testable
// without a cobra command around it.
func reconcileBindings(ctx context.Context, conn *substrate.Conn, actor string, dryRun bool) (*reconcileReport, error) {
	keys, err := conn.KVListKeysPrefix(ctx, bootstrap.CoreKVBucket, credentialIndexPrefix)
	if err != nil {
		return nil, fmt.Errorf("list %s keys: %w", credentialIndexPrefix, err)
	}

	report := &reconcileReport{DryRun: dryRun}
	for _, key := range keys {
		entry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, key)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", key, err)
		}
		var index credentialIndexDoc
		if err := json.Unmarshal(entry.Value, &index); err != nil {
			return nil, fmt.Errorf("parse %s: %w", key, err)
		}
		report.Scanned++
		if index.IsDeleted {
			// The unlinked case. The operation would reject it, and rightly;
			// counting it here keeps the driver from generating rejections it
			// already knows the answer to.
			report.Tombstoned++
			continue
		}
		cred, owner := index.Data.ActorKey, index.Data.IdentityKey
		if cred == "" || owner == "" {
			return nil, fmt.Errorf("%s: index vertex names no actorKey/identityKey", key)
		}
		if cred == owner {
			// A merge writes an index vertex for the primary's own implicit
			// self-credential before it reaches its self-loop guard, so this
			// shape exists on any merged corpus. There is no edge to converge
			// — a vertex is not its own credential — and submitting it would
			// earn a self-loop rejection on every single run, leaving a
			// migration that can never report clean.
			report.SelfLoop++
			continue
		}

		linkKey, err := boundToKey(cred, owner)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		linkEntry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, linkKey)
		switch {
		case err == nil:
			var link linkDoc
			if err := json.Unmarshal(linkEntry.Value, &link); err != nil {
				return nil, fmt.Errorf("parse boundTo for %s: %w", cred, err)
			}
			if link.IsDeleted {
				// An unlink tombstones the index too and is already counted
				// above, so what reaches here is the index and link having
				// diverged: nothing currently writes a boundTo tombstone without
				// also retiring its index (UnlinkCredential and the erasure
				// path's UnbindIdentityCredentials both tombstone the pair
				// together), but this reconciler exists precisely because Core
				// KV can still show this shape. Re-publishing the link here
				// would restore, decrypt-free, whatever credential-to-person
				// association the missing link was severing.
				report.Retracted++
				continue
			}
			report.AlreadyOK++
			continue
		case !errors.Is(err, substrate.ErrKeyNotFound):
			// Every other read failure in this loop aborts the run; treating a
			// transport fault as "no edge" would silently turn an infra
			// problem into work.
			return nil, fmt.Errorf("read boundTo for %s: %w", cred, err)
		}

		if dryRun {
			report.Submitted++
			continue
		}
		if err := submitReconcile(ctx, conn, actor, cred, owner, report); err != nil {
			return nil, err
		}
	}
	return report, nil
}

func submitReconcile(ctx context.Context, conn *substrate.Conn, actor, cred, owner string, report *reconcileReport) error {
	requestID, err := substrate.NewNanoID()
	if err != nil {
		return fmt.Errorf("generate requestId: %w", err)
	}
	payload, err := json.Marshal(map[string]string{"credentialActorKey": cred, "identityKey": owner})
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	env := &processor.OperationEnvelope{
		RequestID:     requestID,
		Lane:          processor.LaneDefault,
		OperationType: "ReconcileCredentialBinding",
		Actor:         actor,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "identity",
		Payload:       payload,
		// Class is what routes to the identity DDL; a scope=any grant never
		// inspects the target. It names the owner anyway so the envelope says
		// truthfully whose edge is being repaired, and so a later tightening of
		// the grant has the right value already in place.
		AuthContext: &processor.AuthContext{Target: owner},
		// Both keys the script reads are class-(g) derived by the package's own
		// derive_reads, so this submitter declares neither.
	}
	reply, err := submitOp(ctx, conn, env)
	if err != nil {
		return fmt.Errorf("submit for %s: %w", cred, err)
	}
	if reply.Status == processor.ReplyStatusRejected {
		report.Rejected++
		msg := "unknown"
		if reply.Error != nil {
			msg = string(reply.Error.Code) + ": " + reply.Error.Message
		}
		report.Failures = append(report.Failures, cred+" — "+msg)
		return nil
	}
	report.Submitted++
	return nil
}
