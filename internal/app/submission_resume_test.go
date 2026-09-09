package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
	"github.com/ArronJablonowski/DarwinRouter/submissions"
)

func resumeApplicationConfig(t *testing.T, endpoint string) config.Settings {
	t.Helper()
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Workers.Max = 1
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "resume.db")
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: endpoint}}
	cfg.Models = []config.Model{{ID: "chat", Provider: "local", Model: "fixture", Locality: "local", RAMBytes: 1, ContextTokens: 8192, Capabilities: []string{"chat"}}}
	return cfg
}

func appendResumeEvents(t *testing.T, db *telemetry.Store, events []runtime.Event) sessions.TaskHeadFence {
	t.Helper()
	for _, event := range events {
		if err := db.Append(context.Background(), event.Sequence-1, event); err != nil {
			t.Fatal(err)
		}
	}
	head := events[len(events)-1]
	return sessions.TaskHeadFence{Version: 1, TaskID: head.TaskID, SessionID: head.SessionID, HeadSequence: head.Sequence, HeadEventID: head.ID}
}

func claimResumeSourceSubmission(t *testing.T, db *telemetry.Store, key string) (submissions.Status, submissions.Claim, string) {
	t.Helper()
	body, err := json.Marshal(submissionEnvelope{Version: 1, Request: Request{ModelID: "chat", Prompt: "source recovery fixture"}})
	if err != nil {
		t.Fatal(err)
	}
	configDigest := submissionDigest([]byte("resume-source-config"))
	status, err := db.CreateSubmission(context.Background(), submissionDigest([]byte(key)), submissionDigest(body), configDigest, body)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := db.ClaimSubmission(context.Background(), configDigest, time.Now().UTC(), time.Minute)
	if err != nil || claim.Status.ID != status.ID {
		t.Fatal(claim.Status, err)
	}
	return status, claim, configDigest
}

func appendResumeSubmissionEvents(t *testing.T, db *telemetry.Store, claim submissions.Claim, events []runtime.Event) {
	t.Helper()
	for _, event := range events {
		if err := db.AppendSubmission(context.Background(), event.Sequence-1, event, claim.Status.ID, claim.Token); err != nil {
			t.Fatal(err)
		}
	}
}

func recoveredModelResumeSource(t *testing.T, db *telemetry.Store) (sessions.TaskHeadFence, []providers.Message) {
	t.Helper()
	now := time.Unix(100, 0).UTC()
	status, claim, configDigest := claimResumeSourceSubmission(t, db, "recovered-model-source")
	messages := []providers.Message{{Role: "user", Content: "historical question"}, {Role: "assistant", Content: "complete historical answer"}, {Role: "user", Content: "interrupted request"}}
	start := runtime.Event{Version: 1, ID: "resume-model-start", TaskID: "resume-model-source", SessionID: "resume-session", CorrelationID: "resume-model-source", Sequence: 1, Time: now, Kind: runtime.TaskStarted, Data: runtime.Data{SubmissionID: status.ID, Messages: messages, ModelID: "chat", ProviderID: "local", Privacy: "local_only"}}
	turn := runtime.Event{Version: 1, ID: "resume-model-turn", TaskID: start.TaskID, SessionID: start.SessionID, CorrelationID: start.TaskID, Sequence: 2, Time: now.Add(time.Second), Kind: runtime.TurnStarted, TurnID: "resume-turn", AttemptID: "resume-attempt", Data: runtime.Data{ModelID: "chat", ProviderID: "local"}}
	delta := runtime.Event{Version: 1, ID: "resume-model-delta", TaskID: start.TaskID, SessionID: start.SessionID, CorrelationID: start.TaskID, Sequence: 3, Time: now.Add(2 * time.Second), Kind: runtime.ModelDelta, TurnID: turn.TurnID, AttemptID: turn.AttemptID, Data: runtime.Data{Text: "partial output must stay private"}}
	appendResumeSubmissionEvents(t, db, claim, []runtime.Event{start, turn, delta})
	recoveredAt := time.Now().UTC().Add(2 * time.Minute)
	if ok, err := db.RecoverInterruptedModel(context.Background(), status.ID, configDigest, recoveredAt); err != nil || !ok {
		t.Fatal(ok, err)
	}
	page, err := db.ReadEventPage(context.Background(), start.TaskID, 0, 10)
	if err != nil || page.State != "failed" || len(page.Events) != 4 {
		t.Fatal(page, err)
	}
	head := page.Events[len(page.Events)-1]
	return sessions.TaskHeadFence{Version: 1, TaskID: head.TaskID, SessionID: head.SessionID, HeadSequence: head.Sequence, HeadEventID: head.ID}, messages
}

func recoveredDelegationResumeSource(t *testing.T, db *telemetry.Store) sessions.TaskHeadFence {
	t.Helper()
	now := time.Unix(200, 0).UTC()
	status, claim, configDigest := claimResumeSourceSubmission(t, db, "recovered-delegation-source")
	task, session, turn, attempt := "resume-delegation-source", "resume-delegation-session", "delegate-turn", "delegate-attempt"
	call := providers.ToolCall{ID: "delegate-call", Name: "delegate", Arguments: json.RawMessage(`{"prompt":"bounded child task","validation":"text"}`)}
	sequences := map[string]int64{}
	stamp := now
	appendEvent := func(eventTask, eventSession string, kind runtime.Kind, worker string, data runtime.Data) {
		sequences[eventTask]++
		stamp = stamp.Add(time.Millisecond)
		event := runtime.Event{Version: 1, ID: fmt.Sprintf("%s-resume-event-%d", eventTask, sequences[eventTask]), TaskID: eventTask, SessionID: eventSession, CorrelationID: eventTask, WorkerID: worker, Sequence: sequences[eventTask], Time: stamp, Kind: kind, Data: data}
		if kind != runtime.TaskStarted && worker == "" {
			event.TurnID, event.AttemptID = turn, attempt
		}
		appendResumeSubmissionEvents(t, db, claim, []runtime.Event{event})
	}
	start := runtime.Data{SubmissionID: status.ID, Messages: []providers.Message{{Role: "user", Content: "delegate the saved work"}}, ModelID: "chat", ProviderID: "local", Privacy: "local_only"}
	appendEvent(task, session, runtime.TaskStarted, "", start)
	model := runtime.Data{ModelID: "chat", ProviderID: "local"}
	appendEvent(task, session, runtime.TurnStarted, "", model)
	proposal := model
	proposal.ToolCalls, proposal.FinishReason = []providers.ToolCall{call}, "tool_calls"
	appendEvent(task, session, runtime.TurnCompleted, "", proposal)
	appendEvent(task, session, runtime.ToolStarted, "", runtime.Data{ToolCallID: call.ID, ToolName: call.Name, Effect: runtime.NoEffect})
	origin := &runtime.DelegationOrigin{Version: 1, TurnID: turn, AttemptID: attempt, ToolCallID: call.ID, ToolName: call.Name}
	appendEvent("resume-delegation-work", session, runtime.TaskStarted, "resume-worker", runtime.Data{SubmissionID: status.ID, ParentTaskID: task, DelegationOrigin: origin})
	appendEvent("resume-delegation-work", session, runtime.WorkerStarted, "resume-worker", runtime.Data{})
	child := start
	child.ParentTaskID = "resume-delegation-work"
	appendEvent("resume-delegation-child", session, runtime.TaskStarted, "", child)
	appendEvent("resume-delegation-child", session, runtime.TurnStarted, "", model)
	answer := model
	answer.Text, answer.FinishReason = "durable child result", "stop"
	appendEvent("resume-delegation-child", session, runtime.TurnCompleted, "", answer)
	accepted := true
	validation := model
	validation.Accepted, validation.Code = &accepted, "deterministic.nonempty_text.v1"
	appendEvent("resume-delegation-child", session, runtime.EvaluationRecorded, "", validation)
	appendEvent("resume-delegation-child", session, runtime.TaskCompleted, "", model)
	appendEvent("resume-delegation-work", session, runtime.EvaluationRecorded, "resume-worker", runtime.Data{Accepted: &accepted, Code: "worker_validator"})
	appendEvent("resume-delegation-work", session, runtime.WorkerCompleted, "resume-worker", runtime.Data{Text: "durable child result"})
	appendEvent("resume-delegation-work", session, runtime.TaskCompleted, "resume-worker", runtime.Data{})
	if ok, err := db.RecoverInterruptedDelegation(context.Background(), status.ID, configDigest, time.Now().UTC().Add(2*time.Minute)); err != nil || !ok {
		t.Fatal(ok, err)
	}
	page, err := db.ReadEventPage(context.Background(), task, 0, 10)
	if err != nil || page.State != "failed" || len(page.Events) != 6 {
		t.Fatal(page, err)
	}
	head := page.Events[len(page.Events)-1]
	return sessions.TaskHeadFence{Version: 1, TaskID: head.TaskID, SessionID: head.SessionID, HeadSequence: head.Sequence, HeadEventID: head.ID}
}

func TestSubmittedResumeUsesExactRecoveredModelAfterRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	requests := make(chan []providers.Message, 1)
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []providers.Message `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid provider request")
			return
		}
		calls.Add(1)
		requests <- body.Messages
		fmt.Fprintln(w, `{"message":{"content":"fresh resumed answer"},"done":true,"done_reason":"stop"}`)
	}))
	defer provider.Close()
	cfg := resumeApplicationConfig(t, provider.URL)
	svc, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc.profile = healthProfile
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	fence, historical := recoveredModelResumeSource(t, db)
	before, err := db.Read(ctx, fence.TaskID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	request := Request{ModelID: "chat", Prompt: "explicit new prompt"}
	stale := fence
	stale.HeadSequence--
	if _, err := svc.SubmitResume(ctx, "stale-resume-key-01", stale, request); !errors.Is(err, ErrAdmission) {
		t.Fatal("stale public resume fence admitted", err)
	}
	queued, err := svc.SubmitResume(ctx, "exact-resume-key-01", fence, request)
	if err != nil || queued.State != "queued" {
		t.Fatal(queued, err)
	}
	restarted, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	restarted.profile = healthProfile
	again, err := restarted.SubmitResume(ctx, "exact-resume-key-01", fence, request)
	if err != nil || again.ID != queued.ID {
		t.Fatal(again, err)
	}
	changed := request
	changed.Prompt = "changed prompt"
	if _, err := restarted.SubmitResume(ctx, "exact-resume-key-01", fence, changed); !errors.Is(err, submissions.ErrConflict) {
		t.Fatal("changed resume did not conflict", err)
	}
	dispatcher, err := StartDispatcher(ctx, restarted)
	if err != nil {
		t.Fatal(err)
	}
	defer dispatcher.Close()
	status := awaitSubmission(t, ctx, restarted, queued.ID, "succeeded")
	if status.Result == nil || status.Result.Text != "fresh resumed answer" || calls.Load() != 1 {
		t.Fatal(status, calls.Load())
	}
	got := <-requests
	want := append(append([]providers.Message(nil), historical...), providers.Message{Role: "user", Content: request.Prompt})
	if !reflect.DeepEqual(got, want) {
		t.Fatal("resume imported partial output or lost completed context", got)
	}
	child, err := sessions.Replay(ctx, db, status.Result.TaskID)
	if err != nil || child.ParentTaskID != fence.TaskID || child.SessionID != fence.SessionID || child.State != "completed" || child.Privacy != "local_only" {
		t.Fatal(child, err)
	}
	childEvents, err := db.Read(ctx, status.Result.TaskID, 0, 100)
	if err != nil || len(childEvents) == 0 || childEvents[0].Data.Domain != "general" || childEvents[0].Data.Profile != "default" {
		t.Fatal("resume intent was not classified before durable execution", childEvents, err)
	}
	after, err := db.Read(ctx, fence.TaskID, 0, 100)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("resume mutated recovered source", err)
	}
}

func TestSubmittedResumeConsumesRecoveredDelegationWithoutRedispatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	requests := make(chan []providers.Message, 1)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []providers.Message `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid provider request")
			return
		}
		requests <- body.Messages
		fmt.Fprintln(w, `{"message":{"content":"reviewed recovered delegation"},"done":true,"done_reason":"stop"}`)
	}))
	defer provider.Close()
	cfg := resumeApplicationConfig(t, provider.URL)
	svc, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc.profile = healthProfile
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	fence := recoveredDelegationResumeSource(t, db)
	before, err := db.Read(ctx, fence.TaskID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	queued, err := svc.SubmitResume(ctx, "delegate-resume-key", fence, Request{ModelID: "chat", Prompt: "review without delegating"})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := StartDispatcher(ctx, svc)
	if err != nil {
		t.Fatal(err)
	}
	defer dispatcher.Close()
	status := awaitSubmission(t, ctx, svc, queued.ID, "succeeded")
	if status.Result == nil || status.Result.Text != "reviewed recovered delegation" {
		t.Fatal(status)
	}
	got := <-requests
	if len(got) != 4 || got[0].Content != "delegate the saved work" || len(got[1].ToolCalls) != 1 || got[2].Role != "tool" || !strings.Contains(got[2].Content, "durable child result") || got[3].Content != "review without delegating" {
		t.Fatal("recovered delegation context was not reused exactly", got)
	}
	after, err := db.Read(ctx, fence.TaskID, 0, 100)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("resume mutated recovered delegation source", err)
	}
}

func TestSubmittedResumeRejectsAmbiguousIntentBeforeStorage(t *testing.T) {
	valid := sessions.TaskHeadFence{Version: 1, TaskID: "source", SessionID: "session", HeadSequence: 2, HeadEventID: "terminal"}
	compaction := &sessions.CompactionRequest{Keep: 1, Summary: sessions.Summary{Requirements: []string{"retain"}}}
	for _, request := range []Request{
		{ModelID: "chat", Prompt: "new", ContinueTaskID: "source"},
		{ModelID: "chat", Prompt: "new", SummaryAttemptID: "summary"},
		{ModelID: "chat", Prompt: "new", Compaction: compaction},
		{ModelID: "chat", Messages: []providers.Message{{Role: "user", Content: "new"}}},
		{ModelID: "chat", Prompt: " \n"},
	} {
		svc := submissionService(t)
		if _, err := svc.SubmitResume(context.Background(), "exact-resume-key", valid, request); !errors.Is(err, ErrAdmission) {
			t.Fatal("ambiguous resume intent admitted", request, err)
		}
		if _, err := os.Stat(svc.settings.Telemetry.Database); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("invalid resume intent created storage", err)
		}
	}
}

func TestSubmittedResumeRejectsCompletedAndPrivateCloudSourcesWithoutProviderConstruction(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var builds atomic.Int32
	factory := applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		builds.Add(1)
		return nil, errors.New("provider must not be constructed")
	})
	cfg := resumeApplicationConfig(t, "http://127.0.0.1:1")
	cfg.Mode = "hybrid"
	cfg.Providers = []config.Provider{{ID: "cloud", Kind: "openai_compatible", Endpoint: "https://example.invalid", APIKeyEnv: "CLOUD_KEY"}}
	cost := 0.0
	cfg.Models = []config.Model{{ID: "cloud", Provider: "cloud", Model: "fixture", Locality: "cloud", ContextTokens: 8192, EstimatedCost: &cost, Capabilities: []string{"chat"}}}
	svc, err := NewServiceWithProviderFactory(cfg, func(name string) string {
		if name == "CLOUD_KEY" {
			return "credential"
		}
		return ""
	}, nil, nil, nil, factory)
	if err != nil {
		t.Fatal(err)
	}
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	completedStart := runtime.Event{Version: 1, ID: "completed-start", TaskID: "completed-source", SessionID: "completed-session", CorrelationID: "completed-source", Sequence: 1, Time: time.Unix(300, 0).UTC(), Kind: runtime.TaskStarted, Data: runtime.Data{Privacy: "local_only"}}
	completedEnd := runtime.Event{Version: 1, ID: "completed-end", TaskID: completedStart.TaskID, SessionID: completedStart.SessionID, CorrelationID: completedStart.TaskID, Sequence: 2, Time: time.Unix(301, 0).UTC(), Kind: runtime.TaskCompleted}
	completedFence := appendResumeEvents(t, db, []runtime.Event{completedStart, completedEnd})
	if _, err := svc.SubmitResume(ctx, "completed-resume-key", completedFence, Request{ModelID: "cloud", Prompt: "must reject"}); !errors.Is(err, submissions.ErrInvalid) {
		t.Fatal("completed source admitted as recovered resume", err)
	}
	recoveredFence, _ := recoveredModelResumeSource(t, db)
	queued, err := svc.SubmitResume(ctx, "private-resume-key", recoveredFence, Request{ModelID: "cloud", Prompt: "must stay local"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	dispatcher, err := StartDispatcher(ctx, svc)
	if err != nil {
		t.Fatal(err)
	}
	defer dispatcher.Close()
	status := awaitSubmission(t, ctx, svc, queued.ID, "failed")
	if status.ErrorCode != "admission_denied" || len(status.TaskIDs) != 0 || status.Result != nil || builds.Load() != 0 {
		t.Fatal("private resume reached cloud construction", status, builds.Load())
	}
}

func TestSubmittedResumeSourceDriftFailsBeforeProviderConstruction(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var builds atomic.Int32
	factory := applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		builds.Add(1)
		return applicationProvider{discoveries: &atomic.Int32{}}, nil
	})
	cfg := resumeApplicationConfig(t, "http://127.0.0.1:1")
	svc, err := NewServiceWithProviderFactory(cfg, nil, nil, nil, nil, factory)
	if err != nil {
		t.Fatal(err)
	}
	svc.profile = healthProfile
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	fence, _ := recoveredModelResumeSource(t, db)
	queued, err := svc.SubmitResume(ctx, "drifted-resume-key", fence, Request{ModelID: "chat", Prompt: "must not execute"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `UPDATE events SET body=CAST(body AS TEXT)||' ' WHERE task_id=? AND sequence=?`, fence.TaskID, fence.HeadSequence); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	dispatcher, err := StartDispatcher(ctx, svc)
	if err != nil {
		t.Fatal(err)
	}
	defer dispatcher.Close()
	status := awaitSubmission(t, ctx, svc, queued.ID, "failed")
	if status.ErrorCode != "admission_denied" || len(status.TaskIDs) != 0 || status.Result != nil || builds.Load() != 0 {
		t.Fatal("drifted resume reached provider construction", status, builds.Load())
	}
}
