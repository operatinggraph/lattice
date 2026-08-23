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

// erasureMarkerDoc and shreddedEnvelopeDoc are the subsets of the two keys the
// erasure discriminator reads. Both are class-checked, not merely presence-
// checked, because both aspect-type DDLs gate the class rather than the key: a
// document declaring some other class at either key falls to the Processor's
// permissive default and any package script could write one. This driver checks
// the class for the same reason the operation does — and it must, because a
// candidate it selects on a looser rule than the op's would simply be refused
// NotErased on submit, turning every run into a run that reports failures.
type erasureMarkerDoc struct {
	Class string `json:"class"`
}

type shreddedEnvelopeDoc struct {
	Class string `json:"class"`
	Data  struct {
		Shredded bool `json:"shredded"`
	} `json:"data"`
}

// credentialResidueReport is what the command prints, in both output modes.
//
// It is printed even when the run ABORTS partway: a keyspace scan can commit
// thousands of tombstones before a transport blip on some later row, and
// discarding the record of what already landed would leave the operator with an
// error and no idea which subjects were cleared. Every internal failure below
// returns this report alongside its error.
type credentialResidueReport struct {
	Scanned    int `json:"scanned"`
	Tombstoned int `json:"tombstoned"`
	SelfLoop   int `json:"selfLoop"`
	StillBound int `json:"stillBound"`
	NotErased  int `json:"notErased"`
	// Vanished counts index keys the prefix scan listed and the follow-up GET
	// could not find. A concurrent unlink or erasure hard-removing the key
	// between the LIST and the GET is a legitimate race on a live corpus, not a
	// corrupt one: the row this tool would have retired is already gone.
	Vanished int `json:"vanished"`
	// Malformed counts index vertices whose stored data.actorKey does not hash
	// to the key they live at. The operation derives index_key from the payload,
	// so for such a row it would derive a DIFFERENT key than the one scanned,
	// read it absent, and refuse CredentialIndexAlreadyClear — forever, on every
	// re-run, with a diagnosis that is not what is actually wrong. Counted and
	// skipped rather than submitted.
	Malformed int      `json:"malformed"`
	Submitted int      `json:"submitted"`
	Rejected  int      `json:"rejected"`
	DryRun    bool     `json:"dryRun"`
	Failures  []string `json:"failures,omitempty"`
}

func newSweepCredentialResidueCommand(natsURL, outputFmt, defaultActor *string) *cobra.Command {
	var actor string
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "sweep-credential-residue",
		Short: "Retire credentialindex vertices a pre-narrowing erasure left standing",
		Long: `sweep-credential-residue walks the credentialindex keyspace and submits one
TombstoneOrphanedCredentialIndex operation per credential whose boundTo edge is
gone AND at least one of whose two endpoints has already been erased.

Its reason to exist is that an identity erased before the shred was narrowed had
its boundTo links tombstoned in both directions while each credential's index
vertex was deliberately left standing. Those vertices carry
{actorKey, identityKey, boundAt} in the clear — sha256(raw sign-in id) mapped to
the erased person — and no walk can reach them: the ordinary
UnbindIdentityCredentials sweep enumerates boundTo links and finds none live, so
it emits nothing, and the completion seal's own re-walk reads the same zero. The
subject earns a clean erasure attestation over rows that still name them.

Both link directions left residue, and both are swept:

  - the erased subject as the row's OWNER (identityKey) — a sign-in credential
    mapped onto the erased person;
  - the erased subject as the row's CREDENTIAL (actorKey) — a merged-away
    identity folded into its survivor as an implicit self-credential, or a
    Scenario-B identity later linked to another. That row is keyed at the hash
    of the erased subject's own identity and names them in its body, so it
    answers "who is this person now" without decrypting anything.

It is deliberately narrow, and skips more than it submits:

  - a LIVE boundTo edge is left alone — that pair is still inside
    UnbindIdentityCredentials' ordinary sweep, which retires the index and the
    edge together, and is the correct path for it;
  - a row whose BOTH endpoints still have an open write path is left alone,
    whether the edge is missing or tombstoned. A missing edge between two live
    people is reconcile-bindings' repair job; a tombstoned edge between two live
    people is the retraction that command already declines to touch, and this
    one leaves it exactly as untouched;
  - a row whose stored actorKey does not hash to the key it lives at is counted
    "malformed" and skipped: the operation derives the index key from the
    payload, so submitting one would earn a permanent, misleading
    "already clear" refusal on every run.

The operation re-checks every one of those conditions itself, so this driver's
classification is a way to avoid generating rejections it already knows the
answer to, never the thing that makes the sweep safe. Safe to re-run, and safe
to run concurrently with another operator: a cleared index is refused, not
re-written, and that refusal counts as already-handled rather than as a failure.

Use --dry-run first: what it reports is the live count of affected subjects on
this deployment, which is not knowable any other way.

The counts are printed even when a run aborts partway, so a scan interrupted by
a transport fault still reports what it had already committed.

Requires an actor holding TombstoneOrphanedCredentialIndex (scope=any) — the
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

			// The report comes back even when the sweep aborts, and is printed in
			// both modes either way. A run that tombstoned four thousand rows and
			// then hit a transport fault must still tell the operator what it
			// cleared; printing only the error would make those commits invisible
			// and leave the re-run unanchored.
			report, sweepErr := sweepCredentialResidue(ctx, conn, actor, dryRun)

			if *outputFmt == "json" {
				if sweepErr != nil {
					// ONE envelope carrying both halves, rather than a success
					// envelope followed by an error envelope: a decoder reading
					// this stdout gets a single document, with the partial counts
					// in `data` and the abort in `error`.
					_ = json.NewEncoder(os.Stdout).Encode(output.Envelope{
						OK:    false,
						Data:  report,
						Error: &output.EnvError{Code: "CredentialResidueError", Message: sweepErr.Error()},
					})
					return output.ErrJSONError
				}
				if err := output.PrintJSON(report); err != nil {
					return err
				}
			} else {
				fmt.Printf("scanned:     %d\ntombstoned:  %d\nselfLoop:    %d\nstillBound:  %d\nnotErased:   %d\nvanished:    %d\nmalformed:   %d\nsubmitted:   %d\nrejected:    %d\ndryRun:      %t\n",
					report.Scanned, report.Tombstoned, report.SelfLoop,
					report.StillBound, report.NotErased, report.Vanished, report.Malformed,
					report.Submitted, report.Rejected, report.DryRun)
				for _, f := range report.Failures {
					fmt.Fprintln(os.Stderr, "rejected: "+f)
				}
				if sweepErr != nil {
					return sweepErr
				}
			}
			// Both output modes exit non-zero on a rejection: a wrapper reading
			// JSON must not see success on a run that cleared nothing.
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

// sweepCredentialResidue is the driver proper, split out so its classification
// is testable without a cobra command around it.
//
// It returns the report on EVERY path, error included. The report is allocated
// before the first read for that reason: a caller must always have the record of
// what a run committed before it stopped, and a partial run's counts are the
// only thing that makes the abort actionable.
func sweepCredentialResidue(ctx context.Context, conn *substrate.Conn, actor string, dryRun bool) (*credentialResidueReport, error) {
	report := &credentialResidueReport{DryRun: dryRun}

	keys, err := conn.KVListKeysPrefix(ctx, bootstrap.CoreKVBucket, credentialIndexPrefix)
	if err != nil {
		return report, fmt.Errorf("list %s keys: %w", credentialIndexPrefix, err)
	}

	for _, key := range keys {
		entry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, key)
		if errors.Is(err, substrate.ErrKeyNotFound) {
			// Listed by the scan, gone by the time we read it. A concurrent
			// unlink or erasure hard-removing the key is a legitimate race on a
			// live corpus — the row this tool would have retired is already gone
			// — so it is counted and skipped, not treated as an infra fault. Any
			// OTHER read failure still aborts, for the reason the boundTo GET
			// below gives: a transport fault must never read as "nothing here".
			report.Vanished++
			continue
		}
		if err != nil {
			return report, fmt.Errorf("read %s: %w", key, err)
		}
		var index credentialIndexDoc
		if err := json.Unmarshal(entry.Value, &index); err != nil {
			return report, fmt.Errorf("parse %s: %w", key, err)
		}
		report.Scanned++
		if index.IsDeleted {
			// Already cleared — by this sweep on an earlier run, by an unlink,
			// or by the ordinary erasure sweep. The operation refuses it
			// (CredentialIndexAlreadyClear), so counting it here is what keeps a
			// re-run from generating rejections it already knows the answer to.
			report.Tombstoned++
			continue
		}
		cred, owner := index.Data.ActorKey, index.Data.IdentityKey
		if cred == "" || owner == "" {
			return report, fmt.Errorf("%s: index vertex names no actorKey/identityKey", key)
		}
		// The stored body must hash to the key it lives at. The operation derives
		// index_key from the PAYLOAD, so for a row whose data.actorKey does not
		// hash to its own key the op would derive a different key than the one
		// scanned, read it absent, and refuse CredentialIndexAlreadyClear — on
		// this run and on every re-run after it, with a diagnosis that says
		// "already clear" about a row that is nothing of the kind. Submitting
		// such a row can never do anything but manufacture a permanent, misread
		// rejection, so it is counted and skipped here instead.
		//
		// derived-key: re-derives the index key from the row's own stored
		// actorKey purely to COMPARE it against the key the scan already
		// produced. Nothing is read at the derived key and nothing is declared
		// from it — the submit below still declares the SCANNED key. A
		// derive_reads cannot express this: the check is about two keys agreeing
		// on a corpus this driver enumerated, which the operation, reached only
		// through its payload, has no way to see.
		if key != credentialIndexPrefix+substrate.SHA256NanoID(cred) {
			report.Malformed++
			continue
		}
		if cred == owner {
			// A merge's implicit self-credential. There is no edge and no
			// residue class here — the operation's own self-loop guard refuses
			// it, so submitting would earn a rejection on every run forever.
			report.SelfLoop++
			continue
		}

		linkKey, err := boundToKey(cred, owner)
		if err != nil {
			return report, fmt.Errorf("%s: %w", key, err)
		}
		linkEntry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, linkKey)
		switch {
		case err == nil:
			var link linkDoc
			if err := json.Unmarshal(linkEntry.Value, &link); err != nil {
				return report, fmt.Errorf("parse boundTo for %s: %w", cred, err)
			}
			if !link.IsDeleted {
				// Still bound. Whatever this pair's owner's state is, the
				// ordinary UnbindIdentityCredentials sweep still enumerates it
				// and retires the index and the edge in one batch; clearing the
				// index alone here would leave the edge pointing at a vertex
				// that no longer resolves. The operation refuses this too
				// (StillBound).
				report.StillBound++
				continue
			}
		case !errors.Is(err, substrate.ErrKeyNotFound):
			// Every other read failure in this loop aborts the run; treating a
			// transport fault as "no edge" would silently turn an infra problem
			// into a tombstone.
			return report, fmt.Errorf("read boundTo for %s: %w", cred, err)
		}

		// The edge is absent or tombstoned. That alone says nothing: it is also
		// the shape reconcile-bindings repairs (missing edge, live owner) and
		// the shape it deliberately leaves alone (tombstoned edge, live owner).
		// What separates this tool's population from both is an ENDPOINT being
		// erased — and it is symmetric in the two endpoints, exactly as the
		// operation's own gate is, because the pre-narrowing shred tombstoned
		// boundTo in both directions. The erased subject is the row's owner in
		// the inbound shape and the row's credential in the outbound one (a
		// merged-away identity folded into its survivor, a Scenario-B identity
		// later linked to another), and both leave a live plaintext row naming an
		// erased person. Checking the owner alone would leave half the class
		// silently skipped by this driver and refused NotErased by the op.
		ownerErased, err := ownerWritePathClosed(ctx, conn, owner)
		if err != nil {
			return report, err
		}
		erased := ownerErased
		if !erased {
			erased, err = ownerWritePathClosed(ctx, conn, cred)
			if err != nil {
				return report, err
			}
		}
		if !erased {
			report.NotErased++
			continue
		}

		if dryRun {
			report.Submitted++
			continue
		}
		// The owner's own answer is carried separately from the disjunction,
		// because it is what decides whether the owner's credentialBinding
		// array is part of this submit's declared read set — see
		// submitTombstoneOrphanedIndex.
		if err := submitTombstoneOrphanedIndex(ctx, conn, actor, cred, owner, key, linkKey, ownerErased, report); err != nil {
			return report, err
		}
	}
	return report, nil
}

// ownerWritePathClosed answers the identity DDL's write_path_closed for one
// identity, over the SAME two keys and the SAME class checks the Starlark helper
// applies (packages/identity-domain/ddls.go): a live-class erasureRequested
// marker, OR a piiKey envelope carrying shredded=true.
//
// It takes an arbitrary identity, not specifically a row's owner: the caller
// asks it about BOTH endpoints of an index row, because the residue is symmetric
// in them.
//
// Reimplementing it loosely would not be a safety hole — the operation re-runs
// the real gate and refuses NotErased — but it would make every run report
// failures for candidates the operation was always going to decline. Reading
// them as two extra KVGets costs nothing here: unlike the Starlark op, this
// driver has no live-read budget to respect.
//
// Tombstone-tolerant on both, like the script: nothing removes the marker, and a
// destroyed key does not become undestroyed when its envelope aspect is deleted.
func ownerWritePathClosed(ctx context.Context, conn *substrate.Conn, identityKey string) (bool, error) {
	markerEntry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, identityKey+".erasureRequested")
	switch {
	case err == nil:
		var marker erasureMarkerDoc
		if err := json.Unmarshal(markerEntry.Value, &marker); err != nil {
			return false, fmt.Errorf("parse erasureRequested for %s: %w", identityKey, err)
		}
		if marker.Class == "erasureRequested" {
			return true, nil
		}
	case !errors.Is(err, substrate.ErrKeyNotFound):
		return false, fmt.Errorf("read erasureRequested for %s: %w", identityKey, err)
	}

	envelopeEntry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, identityKey+".piiKey")
	switch {
	case err == nil:
		var envelope shreddedEnvelopeDoc
		if err := json.Unmarshal(envelopeEntry.Value, &envelope); err != nil {
			return false, fmt.Errorf("parse piiKey for %s: %w", identityKey, err)
		}
		return envelope.Class == "piiKey" && envelope.Data.Shredded, nil
	case !errors.Is(err, substrate.ErrKeyNotFound):
		return false, fmt.Errorf("read piiKey for %s: %w", identityKey, err)
	}
	return false, nil
}

// optionalReadsFor builds the absence-tolerant half of the declared read set:
// both endpoints' erasure-discriminator keys, the boundTo link, and — only when
// the owner's own write path is still OPEN — the owner's credentialBinding
// array.
//
// That last one is conditional, and it is the one declaration here that is not
// merely an optimisation. credentialBinding is a SENSITIVE aspect, so step 4
// decrypts every declared instance of it under the named owner's DEK before the
// script runs (internal/processor/sensitive_decrypt.go). An owner erased by a
// shredded key — the inbound residue shape, which is the population this whole
// sweep exists for — has no usable DEK, so declaring their array would fault the
// operation at hydrate instead of retiring the index. The script reaches that
// read only on the outbound arm, where the owner is a live third party, so the
// declaration is scoped to exactly the arm that performs it.
//
// The classification is this driver's read of the corpus a moment earlier, and
// an owner sealed or shredded in the window between that read and hydration
// turns this declaration into a hydration fault: loud, terminal for that one
// row, and self-clearing on the next pass, which re-reads the owner and no
// longer declares the key. That is the fail-closed direction — the alternative
// is a submit that quietly decrypts an erased person's aspect.
func optionalReadsFor(owner, cred, linkKey string, ownerErased bool) []string {
	reads := []string{
		owner + ".erasureRequested",
		owner + ".piiKey",
		cred + ".erasureRequested",
		cred + ".piiKey",
		linkKey,
	}
	if !ownerErased {
		reads = append(reads, owner+".credentialBinding")
	}
	return reads
}

func submitTombstoneOrphanedIndex(ctx context.Context, conn *substrate.Conn, actor, cred, owner, indexKey, linkKey string, ownerErased bool, report *credentialResidueReport) error {
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
		OperationType: "TombstoneOrphanedCredentialIndex",
		Actor:         actor,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "tombstoneOrphanedCredentialIndex",
		Payload:       payload,
		// A scope=any grant never inspects the target. It names the erased owner
		// anyway so the envelope says truthfully whose residue is being cleared,
		// and so a later tightening of the grant has the right value in place.
		AuthContext: &processor.AuthContext{Target: owner},
		// This op's DDL declares no derive_reads, so the submitter declares.
		// The index vertex goes in reads, fail-closed: this driver read the key
		// off the scan moments ago, so its absence at hydrate is a corpus
		// changing underfoot, not a branch to tolerate. The gate keys go in
		// optionalReads, where absence is the ordinary case — and it is the
		// declaration alone that is optional, not the read: the script reads
		// them through kv.Read, so an undeclared one falls through to a live
		// Core KV GET rather than reading absent, and both gates refuse either
		// way. Declaring them buys the step-4 snapshot, nothing more.
		// Both endpoints' discriminator keys, because the op's gate is symmetric
		// in them: the erased subject is the owner in the inbound residue shape
		// and the credential in the outbound one.
		ContextHint: &processor.ContextHint{
			Reads:         []string{indexKey},
			OptionalReads: optionalReadsFor(owner, cred, linkKey, ownerErased),
		},
	}
	reply, err := submitOp(ctx, conn, env)
	if err != nil {
		return fmt.Errorf("submit for %s: %w", cred, err)
	}
	if reply.Status == processor.ReplyStatusRejected {
		// CredentialIndexAlreadyClear is not a failure — it is the row already
		// being in the state this run wanted it in. Two operators sweeping
		// concurrently both classify the same row as work; the loser's commit is
		// re-hydrated against the winner's tombstone and correctly re-executes to
		// this refusal. Counting it as a rejection would exit the loser non-zero
		// over a row that IS clear, on a tool whose whole point is to be re-run
		// until it reports clean.
		//
		// Matched on the message, not on Error.Code: a script fail() surfaces as
		// the wire code ScriptFailed, and the refusal word the script chose lives
		// inside the message (internal/processor/commit_path.go's
		// classifyStepError).
		if reply.Error != nil && strings.Contains(reply.Error.Message, "CredentialIndexAlreadyClear") {
			report.Tombstoned++
			return nil
		}
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
