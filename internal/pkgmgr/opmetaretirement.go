package pkgmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// enforceOpMetaDisposition is the op-meta retirement guard
// (opmeta-retirement-open-task-guard-design.md §2): a package version that
// tombstones an op-meta must declare, for every dropped operationType, how
// its open `forOperation` referents are handled. The check is unconditional
// — it refuses an undeclared drop even when this environment currently holds
// zero open referents, because the decision is authorship-time policy, not
// an apply-time convenience keyed on what one environment happens to hold
// (a referent-free dev apply going green must not hide the missing
// declaration until prod).
//
// A RetireCancelsOpenTasks-declared drop cancels its open referents (oldest
// first is irrelevant — CancelTask is independent per task) before the
// caller submits the upgrade mutations, so the tombstone never lands while a
// live task still points at it. A MovedOps-declared drop always refuses
// today — work-preserving moves are not yet built (§3).
func (i *Installer) enforceOpMetaDisposition(ctx context.Context, def Definition, dropped []tombstonedOpMeta) error {
	cancels := make(map[string]bool, len(def.RetireCancelsOpenTasks))
	for _, ot := range def.RetireCancelsOpenTasks {
		cancels[ot] = true
	}
	for _, d := range dropped {
		if dest, moved := def.MovedOps[d.OperationType]; moved {
			return fmt.Errorf(
				"pkgmgr: op %q is being dropped in favor of package %q — work-preserving moves are not yet supported; "+
					"declare RetireCancelsOpenTasks for it instead, or hold this upgrade until MovedOps ships",
				d.OperationType, dest)
		}
		if !cancels[d.OperationType] {
			return fmt.Errorf(
				"pkgmgr: op %q's op-meta is tombstoned by this upgrade with no declared disposition — "+
					"declare it in Definition.RetireCancelsOpenTasks (or MovedOps, not yet supported) before upgrading",
				d.OperationType)
		}
	}
	for _, d := range dropped {
		if err := i.cancelOpenTasksForOpMeta(ctx, d.Key); err != nil {
			return fmt.Errorf("pkgmgr: cancelling open tasks referencing %q: %w", d.OperationType, err)
		}
	}
	return nil
}

// cancelOpenTasksForOpMeta enumerates every live `forOperation` link into
// opMetaKey (a bounded, server-side subject-filtered read — the read is
// bounded by this op-meta's own referent degree, never the keyspace) and
// submits CancelTask for each referent whose task is still a live, open,
// unexpired referent.
//
// Each page's link docs and task roots are two independent key shapes, so
// they're read as two separate KVGetMulti calls rather than one merged
// request — mirroring registry_probe.go's vertex-root/spec split.
func (i *Installer) cancelOpenTasksForOpMeta(ctx context.Context, opMetaKey string) error {
	opMetaID := strings.TrimPrefix(opMetaKey, metaVertexPrefix)
	filter := "lnk.task.*.forOperation.meta." + opMetaID
	cursor := ""
	for {
		keys, next, err := i.Conn.KVListKeysFilter(ctx, CoreBucket, filter, cursor, 500)
		if err != nil {
			return fmt.Errorf("list %s: %w", filter, err)
		}
		var linkKeys, taskKeys []string
		taskKeyForLink := make(map[string]string, len(keys))
		for _, k := range keys {
			parts := strings.Split(k, ".")
			if len(parts) != 6 {
				continue
			}
			taskKey := "vtx.task." + parts[2]
			linkKeys = append(linkKeys, k)
			taskKeys = append(taskKeys, taskKey)
			taskKeyForLink[k] = taskKey
		}
		linkEntries, err := i.Conn.KVGetMulti(ctx, CoreBucket, linkKeys)
		if err != nil {
			return fmt.Errorf("get-multi %d link keys: %w", len(linkKeys), err)
		}
		taskEntries, err := i.Conn.KVGetMulti(ctx, CoreBucket, taskKeys)
		if err != nil {
			return fmt.Errorf("get-multi %d task keys: %w", len(taskKeys), err)
		}
		for _, k := range linkKeys {
			taskKey := taskKeyForLink[k]
			open, err := i.taskIsOpenReferent(linkEntries[k], taskEntries[taskKey], taskKey, k)
			if err != nil {
				return err
			}
			if !open {
				continue
			}
			if err := i.submitCancelTask(ctx, taskKey); err != nil {
				return err
			}
		}
		if next == "" {
			return nil
		}
		cursor = next
	}
}

// taskIsOpenReferent reports whether a `forOperation` referent is one this
// guard must cancel: the link itself live, the task root alive, its status
// "open", and unexpired. Complete/cancelled tasks and expired ones don't
// count (nothing to cancel); an unparseable expiresAt counts (cancel it
// rather than silently strand it — the opposite fail-closed direction from
// step 3's ephemeral-grant expiry check, which treats unparseable as "no
// match" because that check's job is to deny an ambiguous grant, not drain
// one). linkEntry/taskEntry are nil when their key is absent from the
// caller's KVGetMulti response — the batched equivalent of ErrKeyNotFound —
// which this treats exactly as the single-key path treated a not-found GET:
// not an open referent.
func (i *Installer) taskIsOpenReferent(linkEntry, taskEntry *substrate.KVEntry, taskKey, linkKey string) (bool, error) {
	if linkEntry == nil {
		return false, nil
	}
	var linkDoc struct {
		IsDeleted bool `json:"isDeleted"`
	}
	if err := json.Unmarshal(linkEntry.Value, &linkDoc); err != nil {
		return false, fmt.Errorf("parse %s: %w", linkKey, err)
	}
	if linkDoc.IsDeleted {
		return false, nil
	}

	if taskEntry == nil {
		return false, nil
	}
	var taskDoc struct {
		IsDeleted bool `json:"isDeleted"`
		Data      struct {
			Status    string `json:"status"`
			ExpiresAt string `json:"expiresAt"`
		} `json:"data"`
	}
	if err := json.Unmarshal(taskEntry.Value, &taskDoc); err != nil {
		return false, fmt.Errorf("parse %s: %w", taskKey, err)
	}
	if taskDoc.IsDeleted || taskDoc.Data.Status != "open" {
		return false, nil
	}
	if taskDoc.Data.ExpiresAt != "" {
		if expiresAt, err := time.Parse(time.RFC3339, taskDoc.Data.ExpiresAt); err == nil && !i.Now().Before(expiresAt) {
			return false, nil // parseable and expired — Contract #6 §6.6: expiresAt > now
		}
		// Unparseable falls through and counts (see the doc comment above).
	}
	return true, nil
}

// submitCancelTask submits a CancelTask op directly over NATS on the default
// lane. CancelTask is a normal business op (operator role, scope=any —
// internal/bootstrap's admin holdsRole->operator grant covers the
// installer's AdminActor), not a package-lifecycle op, so it never routes
// through Submit's meta-lane Gateway relay (scoped to Install/Upgrade/
// UninstallPackage only — cmd/loupe/gatewayrelay.go's pkgmgrSubmit comment):
// the Processor's step-3 lane gate rejects any non-platform auth path on a
// non-default lane, and CancelTask's path is a role grant, not platform.
// The requestId is deterministic per taskKey, so a retried upgrade re-issues
// the identical CancelTask and the Processor's dedup tracker absorbs it
// rather than re-running the script against an already-cancelled task.
func (i *Installer) submitCancelTask(ctx context.Context, taskKey string) error {
	requestID := deterministicNanoID(taskKey, "", "opmeta-retire-cancel")
	payload := map[string]any{"taskKey": taskKey}
	reply, err := i.submitDirectOp(ctx, processor.LaneDefault, "CancelTask", "task", requestID, payload,
		&processor.ContextHint{Reads: []string{taskKey}})
	if err != nil {
		return fmt.Errorf("submit CancelTask for %s: %w", taskKey, err)
	}
	switch reply.Status {
	case processor.ReplyStatusAccepted, processor.ReplyStatusDuplicate:
		return nil
	default:
		return fmt.Errorf("CancelTask %s rejected: %s", taskKey, replyError(reply))
	}
}
