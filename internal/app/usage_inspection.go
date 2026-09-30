package app

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/ArronJablonowski/NexusRouter/accounting"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

var ErrUsageInspection = errors.New("usage accounting unavailable")

// InspectTaskUsage returns separately reconciled routed and auxiliary totals.
// It opens existing storage read-only and never creates, migrates, corrects, or
// resumes work. Unknown and partial measurements remain explicit in Totals.
func (s *Service) InspectTaskUsage(ctx context.Context, task string) (accounting.Totals, error) {
	zero := accounting.Totals{Version: 1}
	if s == nil || ctx == nil || !sessions.ValidEventPageID(task) {
		return zero, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	db, err := telemetry.OpenReadOnly(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return zero, usageInspectionError(ctx)
	}
	defer db.Close()
	totals, err := db.UsageTotals(ctx, accounting.Scope{TaskID: task})
	if errors.Is(err, sql.ErrNoRows) {
		return zero, sql.ErrNoRows
	}
	if err != nil || totals.Validate() != nil || totals.Scope.TaskID != task || totals.Scope.SessionID == "" || ctx.Err() != nil || !selectionValueClean(totals, memorySecrets(s.settings, s.secret)) {
		return zero, usageInspectionError(ctx)
	}
	return cloneUsageTotals(totals), nil
}

func usageInspectionError(ctx context.Context) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return ErrUsageInspection
}

func cloneUsageTotals(in accounting.Totals) accounting.Totals {
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
