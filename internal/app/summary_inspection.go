package app

import (
	"context"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

func summaryInspectionID(value string, optional bool) bool {
	return (optional || value != "") && len(value) <= 128 && utf8.ValidString(value) && strings.TrimSpace(value) == value && !strings.ContainsFunc(value, unicode.IsControl)
}

func summaryInspectionError(ctx context.Context) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return ErrInspection
}

// InspectSummaryAttempt reads a saved proposal without creating storage,
// dispatching inference, or granting permission to use its generated content.
// Records may contain sensitive source-derived text; protect exported copies.
func InspectSummaryAttempt(ctx context.Context, path, id string) (sessions.SummaryAttempt, error) {
	if ctx == nil || path == "" || !summaryInspectionID(id, false) {
		return sessions.SummaryAttempt{}, ErrAdmission
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	db, err := telemetry.OpenReadOnly(ctx, path)
	if err != nil {
		return sessions.SummaryAttempt{}, summaryInspectionError(ctx)
	}
	defer db.Close()
	result, err := db.SummaryAttempt(ctx, id)
	if err != nil || ctx.Err() != nil {
		return sessions.SummaryAttempt{}, summaryInspectionError(ctx)
	}
	return result, nil
}

// ListSummaryAttempts returns one lexical ID page of complete saved proposals.
// Cursors are live observations, not snapshots or execution authority. An empty
// task filter includes all tasks. A failed page never returns partial records.
func ListSummaryAttempts(ctx context.Context, path, task, after string, limit int) ([]sessions.SummaryAttempt, error) {
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
	result, err := db.ListSummaryAttempts(ctx, task, after, limit)
	if err != nil || ctx.Err() != nil {
		return nil, summaryInspectionError(ctx)
	}
	if result == nil {
		result = []sessions.SummaryAttempt{}
	}
	return result, nil
}
