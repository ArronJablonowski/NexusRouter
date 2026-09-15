package app

import (
	"context"
	"strconv"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func summaryRecoveryCursor(value string) bool {
	if value == "" {
		return true
	}
	cursor, err := strconv.ParseInt(value, 10, 64)
	return err == nil && cursor > 0 && strconv.FormatInt(cursor, 10) == value
}

// ReconcileInterruptedSummaries closes one bounded page whose exact local
// process owners are proven stopped. It never retries provider work.
func ReconcileInterruptedSummaries(ctx context.Context, path, after string, limit int) (string, int, error) {
	if ctx == nil || path == "" || !summaryRecoveryCursor(after) || limit < 1 || limit > 100 {
		return "", 0, ErrAdmission
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	db, err := telemetry.Open(ctx, path)
	if err != nil {
		if ctx.Err() != nil {
			return "", 0, ctx.Err()
		}
		return "", 0, ErrSubmission
	}
	defer db.Close()
	next, recovered, err := db.ReconcileSummaryAttemptsPage(ctx, after, limit, time.Now().UTC())
	if err != nil || ctx.Err() != nil {
		if ctx.Err() != nil {
			return "", 0, ctx.Err()
		}
		return "", 0, ErrSubmission
	}
	return next, recovered, nil
}

// InspectSummaryRecovery reads a redaction-safe interruption receipt. Private
// process-lock metadata and generated output never cross this boundary.
func InspectSummaryRecovery(ctx context.Context, path, attempt string) (sessions.SummaryRecovery, error) {
	if ctx == nil || path == "" || !summaryInspectionID(attempt, false) {
		return sessions.SummaryRecovery{}, ErrAdmission
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	db, err := telemetry.OpenReadOnly(ctx, path)
	if err != nil {
		return sessions.SummaryRecovery{}, summaryInspectionError(ctx)
	}
	defer db.Close()
	recovery, err := db.SummaryAttemptRecovery(ctx, attempt)
	if err != nil || ctx.Err() != nil {
		return sessions.SummaryRecovery{}, summaryInspectionError(ctx)
	}
	return recovery, nil
}

// ListSummaryRecoveries reads one live lexical receipt page. An empty task
// includes every source task; after is the prior recovery receipt ID.
func ListSummaryRecoveries(ctx context.Context, path, task, after string, limit int) ([]sessions.SummaryRecovery, error) {
	if ctx == nil || path == "" || !summaryInspectionID(task, true) || !summaryInspectionID(after, true) || limit < 1 || limit > 100 {
		return nil, ErrAdmission
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	db, err := telemetry.OpenReadOnly(ctx, path)
	if err != nil {
		return nil, summaryInspectionError(ctx)
	}
	defer db.Close()
	recoveries, err := db.ListSummaryAttemptRecoveries(ctx, task, after, limit)
	if err != nil || ctx.Err() != nil {
		return nil, summaryInspectionError(ctx)
	}
	if recoveries == nil {
		recoveries = []sessions.SummaryRecovery{}
	}
	return recoveries, nil
}
