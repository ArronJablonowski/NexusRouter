package tools

import "context"

// ConsumedApproval identifies operator authority that the durable tool gate has
// already spent for the current handler call. It is provenance, not authority:
// a downstream store must re-read and validate the durable approval record.
type ConsumedApproval struct {
	ID string
}

type consumedApprovalKey struct{}

// WithConsumedApproval is for trusted host authorities after durable approval
// consumption. This context value is forgeable provenance by design; stores
// must re-read the durable record before granting any authority.
func WithConsumedApproval(ctx context.Context, approval ConsumedApproval) context.Context {
	if ctx == nil || !executionIdentifier(approval.ID, 128) {
		return ctx
	}
	return context.WithValue(ctx, consumedApprovalKey{}, approval)
}

// ConsumedApprovalFromContext returns the current handler's spent approval.
// Presence alone never permits a side effect.
func ConsumedApprovalFromContext(ctx context.Context) (ConsumedApproval, bool) {
	if ctx == nil {
		return ConsumedApproval{}, false
	}
	approval, ok := ctx.Value(consumedApprovalKey{}).(ConsumedApproval)
	return approval, ok && executionIdentifier(approval.ID, 128)
}

func executionIdentifier(value string, limit int) bool {
	if len(value) < 1 || len(value) > limit {
		return false
	}
	for _, c := range []byte(value) {
		if c != '-' && c != '_' && !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') && !(c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}
