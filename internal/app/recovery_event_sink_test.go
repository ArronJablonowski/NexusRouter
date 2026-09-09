package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/submissions"
)

func interruptedDelegationSubmission(t *testing.T, s *Service, db *telemetry.Store, key string) submissions.Status {
	t.Helper()
	ctx := context.Background()
	status, err := s.Submit(ctx, key, Request{ModelID: "chat", Prompt: "recover delegation"})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := db.ClaimSubmission(ctx, s.submissionConfigDigest(), time.Now().UTC(), 30*time.Second)
	if err != nil || claim.Status.ID != status.ID {
		t.Fatal(claim.Status.ID, status.ID, err)
	}
	stamp := time.Now().Add(-time.Second)
	seq := map[string]int64{}
	appendEvent := func(task, session string, kind runtime.Kind, data runtime.Data) {
		seq[task]++
		stamp = stamp.Add(time.Millisecond)
		event := runtime.Event{Version: 1, ID: fmt.Sprintf("%s-event-%d", task, seq[task]), TaskID: task, SessionID: session, CorrelationID: task, Sequence: seq[task], Time: stamp, Kind: kind, Data: data}
		if task == "secret-work" {
			event.WorkerID = "worker"
		} else if kind != runtime.TaskStarted {
			event.TurnID, event.AttemptID = "turn", "attempt"
		}
		if err := db.AppendSubmission(ctx, seq[task]-1, event, status.ID, claim.Token); err != nil {
			t.Fatal(err)
		}
	}
	start := runtime.Data{SubmissionID: status.ID, Privacy: "local_only", ProviderID: "fixture", ModelID: "model", Messages: []providers.Message{{Role: "user", Content: "question"}}}
	appendEvent("secret-parent", "secret-parent", runtime.TaskStarted, start)
	model := runtime.Data{ProviderID: "fixture", ModelID: "model"}
	appendEvent("secret-parent", "secret-parent", runtime.TurnStarted, model)
	turn := model
	turn.FinishReason = "tool_calls"
	turn.ToolCalls = []providers.ToolCall{{ID: "call", Name: "delegate", Arguments: json.RawMessage(`{"prompt":"question","validation":"text"}`)}}
	appendEvent("secret-parent", "secret-parent", runtime.TurnCompleted, turn)
	appendEvent("secret-parent", "secret-parent", runtime.ToolStarted, runtime.Data{ToolCallID: "call", ToolName: "delegate", Effect: runtime.NoEffect})
	appendEvent("secret-work", "secret-parent", runtime.TaskStarted, runtime.Data{SubmissionID: status.ID, ParentTaskID: "secret-parent", DelegationOrigin: &runtime.DelegationOrigin{Version: 1, TurnID: "turn", AttemptID: "attempt", ToolCallID: "call", ToolName: "delegate"}})
	appendEvent("secret-work", "secret-parent", runtime.WorkerStarted, runtime.Data{})
	childStart := start
	childStart.ParentTaskID = "secret-work"
	appendEvent("secret-child", "secret-child", runtime.TaskStarted, childStart)
	appendEvent("secret-child", "secret-child", runtime.TurnStarted, model)
	answer := model
	answer.Text, answer.FinishReason = "rotated credential answer", "stop"
	appendEvent("secret-child", "secret-child", runtime.TurnCompleted, answer)
	yes := true
	validation := model
	validation.Accepted, validation.Code = &yes, "deterministic.nonempty_text.v1"
	appendEvent("secret-child", "secret-child", runtime.EvaluationRecorded, validation)
	appendEvent("secret-child", "secret-child", runtime.TaskCompleted, model)
	appendEvent("secret-work", "secret-parent", runtime.EvaluationRecorded, runtime.Data{Accepted: &yes, Code: "worker_validator"})
	appendEvent("secret-work", "secret-parent", runtime.WorkerCompleted, runtime.Data{Text: "rotated credential answer"})
	appendEvent("secret-work", "secret-parent", runtime.TaskCompleted, runtime.Data{})
	expireRecoveryClaim(t, s, status.ID)
	return status
}

func recoverableModelSubmission(t *testing.T, s *Service, db *telemetry.Store, key, task string) submissions.Status {
	t.Helper()
	ctx := context.Background()
	status, err := s.Submit(ctx, key, Request{ModelID: "chat", Prompt: "recover without execution"})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := db.ClaimSubmission(ctx, s.submissionConfigDigest(), time.Now().UTC(), 30*time.Second)
	if err != nil || claim.Status.ID != status.ID {
		t.Fatal(claim.Status.ID, status.ID, err)
	}
	start := runtime.Event{Version: 1, ID: task + "-start", TaskID: task, SessionID: task, CorrelationID: task, Sequence: 1, Time: time.Now().Add(-time.Minute).UTC(), Kind: runtime.TaskStarted, Data: runtime.Data{SubmissionID: status.ID, ProviderID: "local", ModelID: "fixture", Privacy: "local_only", Messages: []providers.Message{{Role: "user", Content: "recover without execution"}}}}
	if err = db.AppendSubmission(ctx, 0, start, status.ID, claim.Token); err != nil {
		t.Fatal(err)
	}
	expireRecoveryClaim(t, s, status.ID)
	return status
}

func TestRecoveryEventSinkReceivesOnlyNewInterruptedEvents(t *testing.T) {
	s, db, calls := recoveryFixture(t)
	status := recoverableModelSubmission(t, s, db, "recovery-sink-model", "recovery-sink-task")
	var got []runtime.Event
	sink := runtime.EventSinkFunc(func(_ context.Context, event runtime.Event) error {
		got = append(got, event)
		event.Data.Code = "caller-mutation"
		return nil
	})
	if err := installEventSink(s, sink); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	active, err := StartDispatcher(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	awaitSubmission(t, ctx, s, status.ID, "failed")
	if err = active.Close(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Kind != runtime.TaskFailed || got[0].TaskID != "recovery-sink-task" || calls.Load() != 0 {
		t.Fatal(got, calls.Load())
	}
	page, err := db.ReadEventPage(context.Background(), "recovery-sink-task", 1, 10)
	if err != nil || len(page.Events) != 1 || page.Events[0].Data.Code == "caller-mutation" || page.Events[0].ID != got[0].ID {
		t.Fatal("sink did not receive a detached durable event", page, err)
	}
	d := &Dispatcher{db: db, eventSink: sink}
	if _, err = d.recoverPage(context.Background(), s.submissionConfigDigest(), ""); err != nil || len(got) != 1 {
		t.Fatal("recovery replay redelivered", got, err)
	}
	final, err := s.SubmissionStatus(context.Background(), status.ID)
	if err != nil || final.State != "failed" {
		t.Fatal(final, err)
	}

	queued, err := s.Submit(context.Background(), "recovery-sink-retirement", Request{ModelID: "chat", Prompt: "retire"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.reconcileConfigurationPage(context.Background(), submissionDigest([]byte(s.submissionConfigDigest()+"-new")), ""); err != nil {
		t.Fatal(err)
	}
	retired, err := s.SubmissionStatus(context.Background(), queued.ID)
	if err != nil || retired.ErrorCode != "configuration_changed" || len(got) != 1 {
		t.Fatal("configuration retirement emitted a runtime event", retired, got, err)
	}

	terminal, err := s.Submit(context.Background(), "recovery-sink-terminal", Request{ModelID: "chat", Prompt: "complete before recovery"})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := db.ClaimSubmission(context.Background(), s.submissionConfigDigest(), time.Now().UTC(), 30*time.Second)
	if err != nil || claim.Status.ID != terminal.ID {
		t.Fatal(claim.Status.ID, terminal.ID, err)
	}
	request, err := decodeSubmission(claim.Request)
	if err != nil {
		t.Fatal(err)
	}
	request.submissionID, request.submissionToken = terminal.ID, claim.Token
	if _, err = s.Run(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	expireRecoveryClaim(t, s, terminal.ID)
	beforeTerminalProjection := len(got)
	if _, err = d.recoverPage(context.Background(), s.submissionConfigDigest(), ""); err != nil || len(got) != beforeTerminalProjection {
		t.Fatal("terminal projection redelivered existing events", len(got), beforeTerminalProjection, err)
	}
}

func TestRecoveryEventSinkFailureDoesNotBlockLaterCandidateOrRedeliver(t *testing.T) {
	s, db, calls := recoveryFixture(t)
	first := recoverableModelSubmission(t, s, db, "recovery-sink-failure-one", "recovery-sink-failure-task-one")
	second := recoverableModelSubmission(t, s, db, "recovery-sink-failure-two", "recovery-sink-failure-task-two")
	var mu sync.Mutex
	invocations := 0
	var accepted []runtime.Event
	sink := runtime.EventSinkFunc(func(_ context.Context, event runtime.Event) error {
		mu.Lock()
		defer mu.Unlock()
		invocations++
		if invocations == 1 {
			panic("private recovery sink panic")
		}
		accepted = append(accepted, event)
		return nil
	})
	d := &Dispatcher{db: db, eventSink: sink}
	if _, err := d.recoverPage(context.Background(), s.submissionConfigDigest(), ""); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	count, delivered := invocations, append([]runtime.Event(nil), accepted...)
	mu.Unlock()
	if count != 2 || len(delivered) != 1 || delivered[0].TaskID != "recovery-sink-failure-task-two" || !errors.Is(d.err, ErrSubmission) || calls.Load() != 0 {
		t.Fatal(count, delivered, d.err, calls.Load())
	}
	for _, id := range []string{first.ID, second.ID} {
		status, err := s.SubmissionStatus(context.Background(), id)
		if err != nil || status.State != "failed" {
			t.Fatal(status, err)
		}
	}
	if _, err := d.recoverPage(context.Background(), s.submissionConfigDigest(), ""); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if invocations != 2 {
		t.Fatal("committed recovery was redelivered", invocations)
	}
	page, err := db.ReadEventPage(context.Background(), "recovery-sink-failure-task-one", 1, 10)
	if err != nil || len(page.Events) != 1 || page.Events[0].Kind != runtime.TaskFailed {
		t.Fatal("explicit catch-up did not expose callback checkpoint gap", page, err)
	}
	history, err := db.RecoveryHistory(context.Background(), first.ID)
	if err != nil || len(history) != 1 {
		t.Fatal("terminal recovery was appended more than once", history, err)
	}
}

func TestDispatcherRecoveryScreensRotatedSecretBeforeWritesAndContinues(t *testing.T) {
	s, db, calls := recoveryFixture(t)
	secret := interruptedDelegationSubmission(t, s, db, "rotated-secret-delegation")
	later := recoverableModelSubmission(t, s, db, "rotated-secret-later", "rotated-secret-later-task")
	var delivered []runtime.Event
	d := &Dispatcher{
		db:                 db,
		eventSink:          runtime.EventSinkFunc(func(_ context.Context, event runtime.Event) error { delivered = append(delivered, event); return nil }),
		eventSinkSequencer: &configuredSinkSequencer{},
		lifecycle:          context.Background(),
		recoverySecrets:    func() []string { return []string{"rotated credential"} },
	}
	if _, err := d.recoverPage(context.Background(), s.submissionConfigDigest(), ""); err != nil {
		t.Fatal(err)
	}
	secretStatus, secretErr := s.SubmissionStatus(context.Background(), secret.ID)
	laterStatus, laterErr := s.SubmissionStatus(context.Background(), later.ID)
	if secretErr != nil || laterErr != nil || secretStatus.State != "running" || laterStatus.State != "failed" || len(delivered) != 1 || delivered[0].TaskID != "rotated-secret-later-task" || calls.Load() != 0 {
		t.Fatal(secretStatus, secretErr, laterStatus, laterErr, delivered, calls.Load())
	}
	page, err := db.ReadEventPage(context.Background(), "secret-parent", 0, 100)
	history, historyErr := db.RecoveryHistory(context.Background(), secret.ID)
	if err != nil || page.HeadSequence != 4 || page.State != "running" || historyErr != nil || len(history) != 0 {
		t.Fatal("secret screen wrote recovery state", page, err, history, historyErr)
	}
}

func TestRecoveryEventSinkUsesLiveDispatcherContextAfterCommit(t *testing.T) {
	s, db, _ := recoveryFixture(t)
	status := recoverableModelSubmission(t, s, db, "recovery-context", "recovery-context-task")
	scanCtx, cancelScan := context.WithCancel(context.Background())
	lifecycle, cancelLifecycle := context.WithCancel(context.Background())
	defer cancelLifecycle()
	callback := make(chan error, 1)
	d := &Dispatcher{
		db:                 db,
		eventSinkSequencer: &configuredSinkSequencer{},
		lifecycle:          lifecycle,
		eventSink: runtime.EventSinkFunc(func(ctx context.Context, _ runtime.Event) error {
			cancelScan()
			if scanCtx.Err() != context.Canceled {
				callback <- errors.New("scan context remained live")
			} else {
				callback <- ctx.Err()
			}
			return nil
		}),
	}
	if _, err := d.recoverPage(scanCtx, s.submissionConfigDigest(), ""); err != nil {
		t.Fatal(err)
	}
	if err := <-callback; err != nil {
		t.Fatal("callback inherited expired scan context", err)
	}
	final, err := s.SubmissionStatus(context.Background(), status.ID)
	if err != nil || final.State != "failed" {
		t.Fatal(final, err)
	}
}

func TestRecoveryEventSinkPreservesBatchOrderAndStopsFailedBatch(t *testing.T) {
	first := deliveryFixtureEvent()
	second := runtime.Event{Version: 1, ID: "terminal", TaskID: first.TaskID, SessionID: first.SessionID, CorrelationID: first.CorrelationID, Sequence: 2, Time: first.Time.Add(time.Millisecond), Kind: runtime.TaskFailed, CausationID: first.ID, Data: runtime.Data{Code: "interrupted_after_delegation"}}
	var sequences []int64
	d := &Dispatcher{eventSink: runtime.EventSinkFunc(func(_ context.Context, event runtime.Event) error {
		sequences = append(sequences, event.Sequence)
		return nil
	})}
	if err := d.deliverRecoveryEvents(context.Background(), []runtime.Event{first, second}); err != nil || len(sequences) != 2 || sequences[0] != 1 || sequences[1] != 2 {
		t.Fatal(sequences, err)
	}
	calls := 0
	d.eventSink = runtime.EventSinkFunc(func(context.Context, runtime.Event) error {
		calls++
		return errors.New("private recovery sink error")
	})
	if err := d.deliverRecoveryEvents(context.Background(), []runtime.Event{first, second}); !errors.Is(err, ErrEventDelivery) || calls != 1 {
		t.Fatal("failed batch continued delivery", calls, err)
	}
}

func TestRecoveryEventSinkSequencerFencesCommitBehindPriorCallback(t *testing.T) {
	s, db, _ := recoveryFixture(t)
	status := recoverableModelSubmission(t, s, db, "recovery-sink-sequence", "recovery-sink-sequence-task")
	sequencer := &configuredSinkSequencer{tasks: map[string]*configuredSinkSequence{}}
	entered, releaseCallback := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	var order []runtime.Kind
	sink := runtime.EventSinkFunc(func(_ context.Context, event runtime.Event) error {
		mu.Lock()
		order = append(order, event.Kind)
		mu.Unlock()
		if event.Kind == runtime.TaskStarted {
			close(entered)
			<-releaseCallback
		}
		return nil
	})
	delivery := newEventDelivery(func() {}, sink, nil, sequencer)
	liveEvent := deliveryFixtureEvent()
	liveEvent.TaskID, liveEvent.SessionID, liveEvent.CorrelationID = "recovery-sink-sequence-task", "recovery-sink-sequence-task", "recovery-sink-sequence-task"
	liveDone := make(chan error, 1)
	go func() {
		liveDone <- delivery.CommitAndDeliver(context.Background(), liveEvent, false, func() error { return nil })
	}()
	<-entered
	recoveryAttempted := make(chan struct{})
	d := &Dispatcher{db: db, eventSink: sink, eventSinkSequencer: sequencer, lifecycle: context.Background(), recoverySecrets: func() []string {
		close(recoveryAttempted)
		return nil
	}}
	recoveryDone := make(chan error, 1)
	go func() {
		_, err := d.recoverPage(context.Background(), s.submissionConfigDigest(), "")
		recoveryDone <- err
	}()
	<-recoveryAttempted
	select {
	case err := <-recoveryDone:
		t.Fatal("recovery overtook prior callback", err)
	default:
	}
	before, err := s.SubmissionStatus(context.Background(), status.ID)
	if err != nil || before.State != "running" {
		t.Fatal("recovery committed before prior delivery", before, err)
	}
	close(releaseCallback)
	if err = <-liveDone; err != nil {
		t.Fatal(err)
	}
	if err = <-recoveryDone; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != runtime.TaskStarted || order[1] != runtime.TaskFailed {
		t.Fatal("configured sink order", order)
	}
}

func TestRecoverySecretResolverPanicLeavesCandidateFencedAndContinues(t *testing.T) {
	s, db, calls := recoveryFixture(t)
	first := recoverableModelSubmission(t, s, db, "recovery-secret-one", "recovery-secret-task-one")
	second := recoverableModelSubmission(t, s, db, "recovery-secret-two", "recovery-secret-task-two")
	resolutions := 0
	d := &Dispatcher{db: db, recoverySecrets: func() []string {
		resolutions++
		if resolutions == 1 {
			panic("private secret store panic")
		}
		return nil
	}}
	if _, err := d.recoverPage(context.Background(), s.submissionConfigDigest(), ""); err != nil {
		t.Fatal(err)
	}
	firstStatus, firstErr := s.SubmissionStatus(context.Background(), first.ID)
	secondStatus, secondErr := s.SubmissionStatus(context.Background(), second.ID)
	if firstErr != nil || secondErr != nil || firstStatus.State != "running" || secondStatus.State != "failed" || !errors.Is(d.err, ErrSubmission) || calls.Load() != 0 {
		t.Fatal(firstStatus, firstErr, secondStatus, secondErr, d.err, calls.Load())
	}
	if _, err := d.recoverPage(context.Background(), s.submissionConfigDigest(), ""); err != nil {
		t.Fatal(err)
	}
	firstStatus, firstErr = s.SubmissionStatus(context.Background(), first.ID)
	if firstErr != nil || firstStatus.State != "failed" || calls.Load() != 0 {
		t.Fatal("later sweep did not reconsider secret failure", firstStatus, firstErr, calls.Load())
	}
}
