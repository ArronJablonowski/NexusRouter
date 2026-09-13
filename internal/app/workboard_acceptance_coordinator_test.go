package app

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/resources"
	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

func TestConfiguredWorkboardCriterionCoordinatorAcceptsObjectiveEvidence(t *testing.T) {
	workerRequests := make(chan workboardE2EProviderRequest, 1)
	workerServer := httptest.NewServer(workboardE2EOllamaHandler(t, workerRequests, "Implemented the requested change with focused tests.", 100, 20))
	defer workerServer.Close()
	reviewRequests := make(chan workboardE2EProviderRequest, 1)
	reviewServer := httptest.NewServer(workboardE2EOllamaHandler(t, reviewRequests, workboardE2EAuditResponse(t), 40, 10))
	defer reviewServer.Close()

	database := filepath.Join(t.TempDir(), "criterion-coordinator.db")
	settings := configuredWorkboardE2ESettings(database, workerServer.URL, reviewServer.URL)
	service, err := NewService(settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	service.profile = func(context.Context) (resources.Snapshot, error) {
		return resources.Snapshot{Time: time.Now().UTC(), TotalRAM: 100, AvailableRAM: 100}, nil
	}
	store, boardID, cardID, _ := readyFactoryCard(t, database, workboard.WorkBudget{
		AttemptLimit: 3, TimeLimitMS: 60_000, TokenLimit: 50_000, CostMicros: 1_000_000})
	defer store.Close()
	card, err := store.GetCard(context.Background(), boardID, cardID)
	if err != nil {
		t.Fatal(err)
	}
	progress, err := workboard.NewProgressService(store, fixedWorkboardAuthority{authority: workboard.Authority{
		CreationScope: "criterion-test", Actor: workboard.Actor{ID: "operator-test", Type: "operator"}}}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	criteria := []workboard.AcceptanceCriterion{{Version: 1, ID: "tests", Kind: "objective", RequiredSource: "deterministic",
		ValidatorID: meaningfulWorkboardOutputValidator, Description: "The worker returned meaningful output.", Required: true}}
	if _, err = progress.ReviseCriteria(context.Background(), workboard.ReviseCriteriaRequest{BoardID: boardID, CardID: cardID,
		IdempotencyKey: "criterion-coordinator-revise", ExpectedCardRevision: card.Revision,
		ExpectedCriteriaRevision: card.CriteriaRevision, Criteria: criteria}); err != nil {
		t.Fatal(err)
	}
	plan, err := prepareConfiguredWorkboardSchedule(context.Background(), service, store, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	result, err := plan.scheduler.RunCycle(context.Background(), boardID)
	if err != nil || result.Succeeded != 1 || result.Failed != 0 {
		cardAfter, _ := store.GetCard(context.Background(), boardID, cardID)
		lifeAfter, _ := store.ReadCardLifecycleSnapshots(context.Background(), boardID, []string{cardID})
		t.Fatalf("cycle=%+v err=%v card=%+v lifecycle=%+v", result, err, cardAfter, lifeAfter[cardID])
	}
	finalCard, err := store.GetCard(context.Background(), boardID, cardID)
	snapshots, snapshotErr := store.ReadCardLifecycleSnapshots(context.Background(), boardID, []string{cardID})
	attempt := snapshots[cardID].Attempt
	if err != nil || snapshotErr != nil || finalCard.State != workboard.Done || attempt == nil ||
		attempt.State != "accepted" || attempt.Acceptance == nil || attempt.Acceptance.Decision != "accepted" ||
		attempt.Acceptance.DecidedBy != configuredAcceptanceAuthority || attempt.Acceptance.DecidedByType != "validator" ||
		!strings.Contains(attempt.Acceptance.Rationale, attempt.Evidence[0].Reference) {
		t.Fatalf("card=%+v attempt=%+v errors=%v/%v", finalCard, attempt, err, snapshotErr)
	}
	if len(attempt.Evidence) != 2 || attempt.Evidence[0].Source != "deterministic" || attempt.Evidence[1].Source != "model_audit" {
		t.Fatalf("evidence=%+v", attempt.Evidence)
	}
}
