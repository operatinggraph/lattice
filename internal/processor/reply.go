package processor

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/asolgan/lattice/internal/substrate"
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

// BuildAcceptedReplyWithDetail extends BuildAcceptedReply with a script-
// supplied detail map. The detail map is surfaced as-is to the caller and
// MUST NOT be logged (NFR-S6/S7 — may carry sensitive tokens). A nil or
// empty detail map is a no-op.
func BuildAcceptedReplyWithDetail(requestID string, committedAt time.Time, detail map[string]any) OperationReply {
	r := BuildAcceptedReply(requestID, committedAt)
	if len(detail) > 0 {
		r.Detail = detail
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
