package app

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

func TestLearningIndependentServicesShareGenerationClaim(t *testing.T) {
	svc, _, calls := learningFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pinned := pinLearning(t, svc)
	other, err := NewService(svc.settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	other.profile = svc.profile
	other.toolExtension = svc.toolExtension
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, runner := range []*Service{svc, other} {
		wg.Add(1)
		go func(s *Service) {
			defer wg.Done()
			<-start
			// A losing admission or cursor CAS may require attention; it must
			// not authorize a second dispatch under another attempt identity.
			_, _ = s.LearningStep(ctx)
		}(runner)
	}
	close(start)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("concurrent generation count = %d, want one", calls.Load())
	}
	db, err := telemetry.OpenReadOnly(ctx, svc.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	attempts, err := db.ListSkillGenerationAttempts(ctx, svc.settings.Skills.Scope, "", 10)
	if err != nil || len(attempts) != 1 || attempts[0].ID != pinned.PendingSelectionID || attempts[0].Status != "drafted" {
		t.Fatalf("concurrent claim did not leave exactly one drafted attempt: count=%d error=%v", len(attempts), err)
	}
	// Complete any remaining publication phase through a restarted service.
	state, err := other.SkillLearningState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.PendingSelectionID != "" {
		if _, err = other.LearningStep(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("publication retried generation")
	}
	catalog, err := skills.Open(svc.settings.Skills.Root, []string{svc.settings.Skills.Scope})
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	key := skills.Key{Scope: svc.settings.Skills.Scope, Name: pinned.PendingBucketID}
	history, err := catalog.History(ctx, key)
	if err != nil || len(history.Versions) != 1 {
		t.Fatal("expected exactly one published inactive draft")
	}
	activation, err := catalog.ActivationState(ctx, key)
	if err != nil || activation.Active != "" {
		t.Fatal("concurrent scheduling activated an unvalidated draft")
	}
}
