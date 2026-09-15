package app

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
)

func TestSummaryProviderConstructionFollowsDurableStart(t *testing.T) {
	ctx := context.Background()
	svc, cfg := autoFixture(t)
	source, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "source for construction ordering"})
	if err != nil {
		t.Fatal(err)
	}
	var builds atomic.Int32
	svc.providerFactory = applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		builds.Add(1)
		db, openErr := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
		if openErr != nil {
			t.Error(openErr)
			return nil, errors.New("fixture construction failed")
		}
		defer db.Close()
		attempts, readErr := db.ListSummaryAttempts(ctx, source.TaskID, "", 100)
		if readErr != nil || len(attempts) != 1 || attempts[0].Status != "started" {
			t.Error("provider construction preceded durable summary start", attempts, readErr)
		}
		return nil, errors.New("fixture construction failed")
	})
	attempt, err := svc.SummarizeTask(ctx, source.TaskID, "a", 1, 0)
	if err == nil || builds.Load() != 1 || attempt.Status != "failed" || attempt.Code != "summary_failed" || attempt.Draft != nil {
		t.Fatal("construction failure did not terminalize durable attempt", attempt, err, builds.Load())
	}
	db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	saved, err := db.SummaryAttempt(ctx, attempt.ID)
	if err != nil || saved.Status != "failed" || saved.Code != "summary_failed" || saved.Draft != nil {
		t.Fatal("durable construction failure unavailable", saved, err)
	}
}

func TestSummaryRecoveryApplicationArgumentsFailClosed(t *testing.T) {
	for _, test := range []struct {
		after string
		limit int
	}{
		{after: "-1", limit: 1},
		{after: "01", limit: 1},
		{after: "not-a-cursor", limit: 1},
		{limit: 0},
		{limit: 101},
	} {
		if _, _, err := ReconcileInterruptedSummaries(context.Background(), "unused.db", test.after, test.limit); !errors.Is(err, ErrAdmission) {
			t.Fatal("invalid recovery arguments reached storage", test, err)
		}
	}
}
