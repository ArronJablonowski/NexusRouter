package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/resources"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

type workboardE2EProviderRequest struct {
	Model    string          `json:"model"`
	Messages json.RawMessage `json:"messages"`
	Tools    json.RawMessage `json:"tools"`
	Format   json.RawMessage `json:"format"`
	Options  struct {
		MaxOutputTokens int64 `json:"num_predict"`
	} `json:"options"`
}

type workboardE2ECycleResult struct {
	result WorkboardScheduleResult
	err    error
}

func TestConfiguredWorkboardScheduleSupervisorPersistsAcceptedCandidateEndToEnd(t *testing.T) {
	workerRequests := make(chan workboardE2EProviderRequest, 1)
	workerServer := httptest.NewServer(workboardE2EOllamaHandler(t, workerRequests, "factory candidate", 100, 20))
	defer workerServer.Close()
	reviewRequests := make(chan workboardE2EProviderRequest, 1)
	reviewBody := workboardE2EAuditResponse(t)
	reviewServer := httptest.NewServer(workboardE2EOllamaHandler(t, reviewRequests, reviewBody, 40, 10))
	defer reviewServer.Close()

	database := filepath.Join(t.TempDir(), "configured-workboard-e2e.db")
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

	// Exercise the production boundary with a non-UTC host clock. Workboard's
	// evaluation service must canonicalize this instant before durable review
	// admission, regardless of the machine or CI timezone.
	hostNow := func() time.Time { return time.Now().In(time.FixedZone("host-local", -6*60*60)) }
	ctx := context.Background()
	plan, err := prepareConfiguredWorkboardSchedule(ctx, service, store, hostNow)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	configuredScheduler, ok := plan.scheduler.(*WorkboardScheduler)
	if !ok {
		store.Close()
		t.Fatal("configured Workboard scheduler did not retain its production runner")
	}
	configuredRunner, runnerOK := configuredScheduler.runner.(*WorkboardWorkerRunner)
	if !runnerOK {
		store.Close()
		t.Fatal("configured Workboard scheduler did not retain its production runner")
	}
	if _, composed := configuredRunner.evaluator.(*configuredWorkboardCandidateEvaluator); !composed {
		store.Close()
		t.Fatalf("configured runner evaluator=%T", configuredRunner.evaluator)
	}
	cardBefore, cardErr := store.GetCard(ctx, boardID, cardID)
	if cardErr != nil || cardBefore.State != workboard.Ready || len(workerRequests) != 0 || len(reviewRequests) != 0 {
		store.Close()
		t.Fatalf("inert preparation changed state: card=%+v err=%v worker=%d reviewer=%d", cardBefore, cardErr, len(workerRequests), len(reviewRequests))
	}
	cycleDone := make(chan workboardE2ECycleResult, 1)
	scheduler := plan.scheduler
	cycle := WorkboardCycleRunnerFunc(func(run context.Context, requestedBoard string) (WorkboardScheduleResult, error) {
		result, cycleErr := scheduler.RunCycle(run, requestedBoard)
		cycleDone <- workboardE2ECycleResult{result: result, err: cycleErr}
		return result, cycleErr
	})
	plan.scheduler = cycle
	intervalSupervisor, err := plan.Start(ctx)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if duplicate, duplicateErr := plan.Start(ctx); duplicateErr == nil || duplicate != nil {
		intervalSupervisor.Close()
		store.Close()
		t.Fatal("configured scheduling plan started twice")
	}
	defer intervalSupervisor.Close()
	select {
	case completed := <-cycleDone:
		want := WorkboardScheduleResult{Scanned: 1, Ready: 1, Launched: 1, Succeeded: 1}
		if completed.err != nil || completed.result != want {
			tasks, taskErr := store.ListTasks(ctx, sessions.TaskListOptions{Limit: 10})
			lifecycle, lifecycleErr := store.ReadCardLifecycleSnapshots(ctx, boardID, []string{cardID})
			var attempt any
			var events []runtime.Event
			if lifecycle[cardID].Attempt != nil {
				attempt = *lifecycle[cardID].Attempt
				if len(lifecycle[cardID].Attempt.TaskIDs) == 1 {
					events, _ = store.Read(ctx, lifecycle[cardID].Attempt.TaskIDs[0], 0, 10)
				}
			}
			t.Fatalf("cycle=%+v err=%v worker_requests=%d review_requests=%d tasks=%+v task_err=%v attempt=%+v events=%+v lifecycle_err=%v",
				completed.result, completed.err, len(workerRequests), len(reviewRequests), tasks, taskErr, attempt, events, lifecycleErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("configured scheduling cycle did not finish")
	}
	awaitSupervisorHealth(t, intervalSupervisor, "healthy", "supervisor_ok")
	if err = intervalSupervisor.Close(); err != nil {
		store.Close()
		t.Fatal(err)
	}
	awaitSupervisorHealth(t, intervalSupervisor, "unavailable", "supervisor_stopped")
	wantPolicy, policyErr := configuredWorkboardSchedulePolicyDigest(settings)
	lifecycle, lifecycleErr := store.ReadCardLifecycleSnapshots(ctx, boardID, []string{cardID})
	if policyErr != nil || lifecycleErr != nil || lifecycle[cardID].Attempt == nil || lifecycle[cardID].Attempt.Candidate == nil ||
		lifecycle[cardID].Attempt.Candidate.PolicyDigest != wantPolicy {
		store.Close()
		t.Fatalf("configured policy was not durably bound: want=%q lifecycle=%+v errors=%v/%v", wantPolicy, lifecycle, policyErr, lifecycleErr)
	}

	workerRequest := <-workerRequests
	reviewRequest := <-reviewRequests
	if workerRequest.Model != "worker-native" || workerRequest.Options.MaxOutputTokens != 4096 ||
		len(workerRequest.Tools) != 0 || len(workerRequest.Format) != 0 {
		t.Fatalf("worker request=%+v", workerRequest)
	}
	if reviewRequest.Model != "reviewer-native" || reviewRequest.Options.MaxOutputTokens != 200 ||
		len(reviewRequest.Tools) != 0 || len(reviewRequest.Format) == 0 || !strings.Contains(string(reviewRequest.Messages), "factory candidate") {
		t.Fatalf("review request=%+v", reviewRequest)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := telemetry.Open(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	wantConfigID, configErr := settingsConfigID(settings)
	if configErr != nil {
		reopened.Close()
		t.Fatal(configErr)
	}
	assertConfiguredWorkboardE2EDurability(t, reopened, boardID, cardID, wantConfigID)
}

func workboardE2EOllamaHandler(t *testing.T, requests chan<- workboardE2EProviderRequest, content string,
	inputTokens, outputTokens int64,
) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		var request workboardE2EProviderRequest
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		requests <- request
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": content},
			"prompt_eval_count": inputTokens, "eval_count": outputTokens, "done": true, "done_reason": "stop"})
	})
}

func workboardE2EAuditResponse(t *testing.T) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{"version": 1, "evaluator_id": "reviewer", "rubric_version": "darwin-review-v2",
		"domain": "general", "verdict": "accept", "confidence": .9, "findings": []any{
			map[string]any{"summary": "The reported result addresses the required check.", "evidence_refs": []string{"criterion_00", "source_binding"}},
		}})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func configuredWorkboardE2ESettings(database, workerEndpoint, reviewerEndpoint string) config.Settings {
	settings := config.Defaults()
	workerCost, reviewerCost := .2, .1
	settings.Mode, settings.Telemetry.Database = "local_only", database
	settings.Memory.Enabled, settings.Skills.Enabled = false, false
	settings.Runtime.MaxTurns = 1
	settings.Workers.Heartbeat, settings.Workers.Lease = "20ms", "5s"
	settings.Providers = []config.Provider{
		{ID: "worker-provider", Kind: "ollama", Endpoint: workerEndpoint},
		{ID: "reviewer-provider", Kind: "ollama", Endpoint: reviewerEndpoint},
	}
	settings.Models = []config.Model{
		{ID: "worker", Provider: "worker-provider", Model: "worker-native", Locality: "local", ContextTokens: 4096,
			EstimatedCost: &workerCost, RAMBytes: 1, Capabilities: []string{"chat"}},
		{ID: "reviewer", Provider: "reviewer-provider", Model: "reviewer-native", Locality: "local", ContextTokens: 32_768,
			EstimatedCost: &reviewerCost, RAMBytes: 1, Capabilities: []string{"audit"}},
	}
	settings.Workboard.Scheduler.Enabled = true
	settings.Workboard.Scheduler.Interval = "1h"
	settings.Workboard.Scheduler.MaxActiveClaims = 1
	settings.Workboard.Scheduler.WorkerModel = "worker"
	settings.Workboard.Scheduler.AcceptanceJudge = config.WorkboardAcceptanceJudge{Enabled: true, ReviewerModel: "reviewer",
		MaxCost: .1, MaxInputTokens: 20_000, MaxOutputTokens: 200, Timeout: "1s"}
	return settings
}

func assertConfiguredWorkboardE2EDurability(t *testing.T, store *telemetry.Store, boardID, cardID, wantConfigID string) {
	t.Helper()
	ctx := context.Background()
	card, err := store.GetCard(ctx, boardID, cardID)
	snapshots, snapshotErr := store.ReadCardLifecycleSnapshots(ctx, boardID, []string{cardID})
	attempt := snapshots[cardID].Attempt
	if err != nil || snapshotErr != nil || card.State != workboard.Done || card.CurrentClaimID != "" || card.AcceptanceID == "" ||
		attempt == nil || attempt.Validate() != nil || attempt.State != "accepted" || attempt.Claim.State != string(workboard.LeaseReleased) ||
		attempt.Candidate == nil || attempt.Candidate.Summary != "factory candidate" || attempt.Candidate.EvidenceCount != 2 ||
		len(attempt.Evidence) != 2 || attempt.Acceptance == nil || attempt.Acceptance.ID != card.AcceptanceID ||
		attempt.Acceptance.Decision != "accepted" || attempt.Acceptance.DecidedBy != configuredAcceptanceAuthority ||
		attempt.Acceptance.DecidedByType != "validator" || len(attempt.TaskIDs) != 1 || len(attempt.SessionIDs) != 1 {
		t.Fatalf("card=%+v lifecycle=%+v errors=%v/%v", card, snapshots[cardID], err, snapshotErr)
	}
	deterministic, auditEvidence := attempt.Evidence[0], attempt.Evidence[1]
	if deterministic.Source != "deterministic" || deterministic.Outcome != "passed" || deterministic.CriterionID != "tests" ||
		deterministic.ActorID != "deterministic.nonempty_text.v1" || deterministic.ActorType != "validator" || deterministic.Reference == "" ||
		auditEvidence.Source != "model_audit" || auditEvidence.Outcome != "passed" || auditEvidence.CriterionID != "tests" ||
		auditEvidence.ActorID != "reviewer" || auditEvidence.ActorType != "model" || auditEvidence.Reference == "" ||
		!strings.Contains(attempt.Acceptance.Rationale, deterministic.Reference) || strings.Contains(attempt.Acceptance.Rationale, auditEvidence.Reference) {
		t.Fatalf("evidence=%+v acceptance=%+v", attempt.Evidence, attempt.Acceptance)
	}
	taskID, sessionID := attempt.TaskIDs[0], attempt.SessionIDs[0]
	events, err := store.Read(ctx, taskID, 0, 10)
	wantKinds := []runtime.Kind{runtime.TaskStarted, runtime.TurnStarted, runtime.ModelDelta, runtime.TurnCompleted,
		runtime.EvaluationRecorded, runtime.TaskCompleted}
	if err != nil || len(events) != len(wantKinds) {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	for index, kind := range wantKinds {
		if events[index].Kind != kind || events[index].Sequence != int64(index+1) || events[index].TaskID != taskID || events[index].SessionID != sessionID {
			t.Fatalf("event[%d]=%+v", index, events[index])
		}
	}
	start, completed, terminal := events[0], events[3], events[5]
	if start.Data.ModelID != "worker-native" || start.Data.ProviderID != "worker-provider" || start.Data.ConfigID != wantConfigID || start.Data.RouteEstimatedCost == nil ||
		*start.Data.RouteEstimatedCost != .2 || completed.Data.Text != "factory candidate" || completed.Data.Usage == nil ||
		completed.Data.Usage.InputTokens != 100 || completed.Data.Usage.OutputTokens != 20 {
		t.Fatalf("runtime binding start=%+v completed=%+v", start, completed)
	}
	tasks, err := store.ListTasks(ctx, sessions.TaskListOptions{Limit: 10})
	if err != nil || len(tasks.Items) != 1 || tasks.Items[0].TaskID != taskID || tasks.Items[0].SessionID != sessionID || tasks.Items[0].State != "completed" {
		t.Fatalf("tasks=%+v err=%v", tasks, err)
	}
	operation := "candidate-review-" + attempt.Candidate.ID
	admission, found, err := store.ReplayAuxiliaryReviewAdmission(ctx, operation)
	if err != nil || !found || admission.Validate() != nil || admission.ReviewerID != "reviewer" || admission.ModelID != "reviewer-native" ||
		admission.ProviderID != "reviewer-provider" || admission.TimeLimitMS != 1_000 || admission.TokenLimit != 20_200 || admission.CostMicros != 100_000 {
		t.Fatalf("review admission=%+v found=%v err=%v", admission, found, err)
	}
	settlement, found, err := store.ReplayAuxiliaryReviewSettlement(ctx, operation, workboard.AuxiliaryReviewCompleted)
	if err != nil || !found || settlement.Validate() != nil || settlement.ChargedTokens != 50 || settlement.ChargedCostMicros != 100_000 ||
		settlement.ChargedTimeMS < 1 || settlement.ChargedTimeMS > admission.TimeLimitMS {
		t.Fatalf("review settlement=%+v found=%v err=%v", settlement, found, err)
	}
	outcome, found, legacy, err := store.ReplayAuxiliaryReviewOutcome(ctx, operation)
	if err != nil || !found || legacy || outcome.Validate() != nil || outcome.CandidateID != attempt.Candidate.ID ||
		outcome.SourceTaskID != taskID || outcome.SourceSessionID != sessionID || outcome.SourceCompletionEventID != completed.ID ||
		outcome.SourceTerminalEventID != terminal.ID || outcome.SourceConfigID != wantConfigID || outcome.ReviewerProviderID != admission.ProviderID ||
		outcome.AdmissionDigest != admission.AdmissionDigest || outcome.SettlementDigest != settlement.SettlementDigest ||
		outcome.AuditID != auditEvidence.Reference || outcome.EvidenceCount != 2 || outcome.EvidenceDigest != attempt.Candidate.EvidenceDigest {
		t.Fatalf("review outcome=%+v found=%v legacy=%v err=%v", outcome, found, legacy, err)
	}
	audit, err := store.Audit(ctx, outcome.AuditID)
	if err != nil || audit.Validate() != nil || audit.Audit.Verdict != "accept" || audit.Audit.Domain != "general" || len(audit.Audit.Findings) != 1 {
		t.Fatalf("audit=%+v err=%v", audit, err)
	}
	projection, err := store.ReadWorkboardBudgetProjection(ctx, boardID, cardID)
	workerTime := durationMillisCeil(terminal.Time.Sub(start.Time))
	if err != nil || projection.RemainingTokens != 49_830 || projection.RemainingCostMicros != 700_000 ||
		projection.RemainingTimeMS != 60_000-workerTime-settlement.ChargedTimeMS {
		t.Fatalf("projection=%+v worker_time=%d review_time=%d err=%v", projection, workerTime, settlement.ChargedTimeMS, err)
	}
	if attempt.Candidate.ArtifactRefs == nil || len(attempt.Candidate.ArtifactRefs) != 0 {
		t.Fatalf("candidate artifacts=%v", attempt.Candidate.ArtifactRefs)
	}
}
