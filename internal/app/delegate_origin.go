package app

import (
	"context"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/tools"
)

// Batch position is assigned by the host's ordered fan-out, not tool arguments.
type delegationBatchIndexKey struct{}

func delegationOrigin(ctx context.Context, parent, session string) (*runtime.DelegationOrigin, error) {
	identity, ok := tools.ExecutionIdentityFromContext(ctx)
	if !ok || identity.TaskID != parent || identity.SessionID != session {
		return nil, ErrAdmission
	}
	origin := runtime.DelegationOrigin{Version: 1, TurnID: identity.TurnID, AttemptID: identity.AttemptID, ToolCallID: identity.ToolCallID, ToolName: identity.ToolName}
	if index, ok := ctx.Value(delegationBatchIndexKey{}).(int); ok {
		origin.BatchIndex = &index
	}
	if origin.Validate() != nil {
		return nil, ErrAdmission
	}
	return origin.Clone(), nil
}

func auditDelegationOriginMatches(d auditDelegation, work auditChildHistory) bool {
	if len(work.events) == 0 {
		return false
	}
	origin := work.events[0].Data.DelegationOrigin
	if origin == nil || origin.Validate() != nil || origin.TurnID != d.parent.TurnID || origin.AttemptID != d.parent.AttemptID || origin.ToolCallID != d.parent.Data.ToolCallID || origin.ToolName != d.parent.Data.ToolName {
		return false
	}
	if origin.BatchIndex == nil || d.batchIndex == nil {
		return origin.BatchIndex == nil && d.batchIndex == nil
	}
	return *origin.BatchIndex == *d.batchIndex
}
