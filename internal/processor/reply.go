package processor

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// BuildAcceptedReply constructs an `accepted` reply for a successful
// step-8 commit with `decision: committed`.
func BuildAcceptedReply(requestID string, committedAt time.Time) OperationReply {
	return OperationReply{
		RequestID:    requestID,
		OpTrackerKey: TrackerKey(requestID),
		Status:       ReplyStatusAccepted,
		CommittedAt:  substrate.FormatTimestamp(committedAt),
		Decision:     "committed",
	}
}

// BuildAcceptedReplyWithRevisions extends BuildAcceptedReply with the
// validated principal primaryKey (a commit-trace identifier — see
// OperationReply.PrimaryKey) and the per-key committed revisions returned
// by the substrate. Both fields are set only when non-empty so callers can
// use Revisions for read-your-own-writes polling and PrimaryKey to address
// the operation's principal entity. The caller (commit path) is responsible
// for validating primaryKey membership in the committed mutation set before
// invoking this builder.
func BuildAcceptedReplyWithRevisions(requestID string, committedAt time.Time,
	primaryKey string, revisions map[string]uint64) OperationReply {
	r := BuildAcceptedReply(requestID, committedAt)
	if primaryKey != "" {
		r.PrimaryKey = primaryKey
	}
	if len(revisions) > 0 {
		r.Revisions = revisions
	}
	return r
}

// BuildDuplicateReply constructs a `duplicate` reply from an existing
// tracker.
func BuildDuplicateReply(requestID string, original *Tracker) OperationReply {
	r := OperationReply{
		RequestID:    requestID,
		OpTrackerKey: TrackerKey(requestID),
		Status:       ReplyStatusDuplicate,
	}
	if original != nil {
		r.OriginalCommittedAt = original.CommittedAt()
	}
	return r
}

// BuildRejectedReply constructs a `rejected` reply with the given error.
func BuildRejectedReply(requestID string, code ErrorCode, message string, details map[string]any) OperationReply {
	return OperationReply{
		RequestID:    requestID,
		OpTrackerKey: "",
		Status:       ReplyStatusRejected,
		Error: &ReplyError{
			Code:    code,
			Message: message,
			Details: details,
		},
	}
}

// MarshalReply serializes a reply to wire format. Centralized so the
// commit path and tests share encoding.
func MarshalReply(r OperationReply) ([]byte, error) {
	b, err := json.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("reply: marshal: %w", err)
	}
	return b, nil
}
