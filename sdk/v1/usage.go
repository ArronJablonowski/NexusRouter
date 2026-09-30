package v1

import (
	"context"

	"github.com/ArronJablonowski/NexusRouter/accounting"
	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

var ErrUsageInspection = app.ErrUsageInspection

type UsageTotal = accounting.Total
type TaskUsage = accounting.Totals

// InspectTaskUsage reads immutable accounting heads and returns separately
// reconciled routed and auxiliary totals. It never creates or migrates storage.
func (c *Client) InspectTaskUsage(ctx context.Context, task string) (TaskUsage, error) {
	if !c.valid(ctx) || !sessions.ValidEventPageID(task) {
		return TaskUsage{Version: 1}, contextErrorOrAdmission(ctx)
	}
	if err := ctx.Err(); err != nil {
		return TaskUsage{Version: 1}, err
	}
	totals, err := c.service.InspectTaskUsage(ctx, task)
	if err != nil {
		return TaskUsage{Version: 1}, err
	}
	if totals.Validate() != nil || totals.Scope.TaskID != task || totals.Scope.SessionID == "" {
		return TaskUsage{Version: 1}, ErrAdmission
	}
	return cloneSDKUsageTotals(totals), nil
}

func cloneSDKUsageTotals(in accounting.Totals) accounting.Totals {
	out := in
	clone := func(total accounting.Total) accounting.Total {
		if total.InputTokens != nil {
			value := *total.InputTokens
			total.InputTokens = &value
		}
		if total.OutputTokens != nil {
			value := *total.OutputTokens
			total.OutputTokens = &value
		}
		if total.NormalizedCost != nil {
			value := *total.NormalizedCost
			total.NormalizedCost = &value
		}
		return total
	}
	out.Primary, out.Fallback = clone(in.Primary), clone(in.Fallback)
	out.Classifier, out.Summarizer = clone(in.Classifier), clone(in.Summarizer)
	out.OrchestratorAudit, out.Judge = clone(in.OrchestratorAudit), clone(in.Judge)
	out.Routed, out.Auxiliary, out.Overall = clone(in.Routed), clone(in.Auxiliary), clone(in.Overall)
	return out
}
