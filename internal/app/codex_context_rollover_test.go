package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/codexbridge"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

type appCodexRolloverGeneration struct {
	name, answer string
	entered      chan struct{}
	release      chan struct{}
	events       *[]string
	eventsMu     *sync.Mutex
	closeOnce    sync.Once
	check        func(providers.Request, providers.Request) error
	closed       func() error
	inspect      func(providers.Request) error
}

func (p *appCodexRolloverGeneration) record(event string) {
	p.eventsMu.Lock()
	*p.events = append(*p.events, event+":"+p.name)
	p.eventsMu.Unlock()
}
func (p *appCodexRolloverGeneration) Models(context.Context) ([]string, error) {
	return []string{"gpt-5.6-sol"}, nil
}
func (p *appCodexRolloverGeneration) Stream(ctx context.Context, request providers.Request, emit func(providers.Chunk) error) error {
	p.record("stream")
	if p.inspect != nil {
		if err := p.inspect(request); err != nil {
			return err
		}
	}
	if p.entered != nil {
		close(p.entered)
	}
	if p.release != nil {
		select {
		case <-p.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return emit(providers.Chunk{Text: p.answer, Done: true, FinishReason: "stop"})
}
func (p *appCodexRolloverGeneration) CheckContextRollover(_ context.Context, current, prospective providers.Request) error {
	p.record("check")
	if p.check == nil {
		return errors.New("unexpected rollover check")
	}
	return p.check(current, prospective)
}
func (p *appCodexRolloverGeneration) Close() (err error) {
	p.closeOnce.Do(func() {
		p.record("close")
		if p.closed != nil {
			err = p.closed()
		}
	})
	return err
}

func TestCodexContextRolloverActivatesBeforeOwnedReplacement(t *testing.T) {
	// This fixture deliberately sets a tiny context to force rollover.
	// A real user catalog must not replace that synthetic limit.
	t.Setenv("CODEX_HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	svc, attempt, _ := prepareCodexPlanEvidence(t, nil)
	request := Request{ModelID: "brain", ContinueTaskID: attempt.TaskID, Prompt: "complete the initial native turn"}
	read, err := telemetry.OpenReadOnly(ctx, svc.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	prepared, inference, err := svc.prepareExplicitInference(ctx, read, request, svc.settings.Models[0], memorySecrets(svc.settings, svc.secret))
	read.Close()
	if err != nil || prepared.continuation == nil {
		t.Fatal("prepare complete Codex context", err)
	}
	limit, err := providers.EstimateContext(inference)
	if err != nil {
		t.Fatal(err)
	}
	svc.settings.Models[0].ContextTokens = limit

	var eventsMu sync.Mutex
	events := []string{}
	entered, release := make(chan struct{}), make(chan struct{})
	var taskID string
	first := &appCodexRolloverGeneration{name: "old", answer: "initial native answer", entered: entered, release: release, events: &events, eventsMu: &eventsMu}
	first.check = func(current, prospective providers.Request) error {
		if current.Messages[len(current.Messages)-1].Role != "assistant" || current.Messages[len(current.Messages)-1].Content != first.answer ||
			prospective.Messages[len(prospective.Messages)-1].Role != "user" || prospective.Messages[len(prospective.Messages)-1].Content != "use the compacted context" {
			return errors.New("wrong completed or prospective boundary")
		}
		return nil
	}
	first.closed = func() error {
		check, err := telemetry.OpenReadOnly(context.Background(), svc.settings.Telemetry.Database)
		if err != nil {
			return err
		}
		defer check.Close()
		state, err := check.ContextCompactionPlanForAttempt(context.Background(), attempt.ID)
		if err != nil || state.Status != sessions.ContextCompactionActivated || state.Plan == nil || taskID == "" {
			return errors.New("old generation closed before durable activation")
		}
		last := state.Facts[len(state.Facts)-1]
		if last.Activation == nil || last.Activation.TaskID != taskID {
			return errors.New("activation did not bind the running task")
		}
		return nil
	}
	second := &appCodexRolloverGeneration{name: "new", answer: "replacement native answer", events: &events, eventsMu: &eventsMu}
	second.inspect = func(next providers.Request) error {
		body := providerRequestText(next)
		if strings.Contains(body, "codex-plan-source-") || !strings.Contains(body, "Preserve Codex plan evidence") ||
			!strings.Contains(body, "initial native answer") || !strings.Contains(body, "use the compacted context") {
			return errors.New("replacement lost compacted prefix or exact live suffix")
		}
		return nil
	}
	generations := []taskProvider{first, second}
	launches := 0
	svc.codexLauncher = func(context.Context, codexbridge.LaunchSpec) (taskProvider, error) {
		if launches >= len(generations) {
			return nil, errors.New("duplicate replacement launch")
		}
		provider := generations[launches]
		launches++
		eventsMu.Lock()
		events = append(events, "open:"+provider.(*appCodexRolloverGeneration).name)
		eventsMu.Unlock()
		return provider, nil
	}
	control, err := NewService(svc.settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	type completion struct {
		result Result
		err    error
	}
	done := make(chan completion, 1)
	go func() {
		result, runErr := svc.RunStream(ctx, request, func(event runtime.Event) error {
			if event.Kind == runtime.TaskStarted {
				taskID = event.TaskID
			}
			return nil
		})
		done <- completion{result, runErr}
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("old generation did not start")
	}
	steering, err := control.SteerTask(ctx, taskID, "codex-rollover-steering", "use the compacted context")
	if err != nil || steering.State != "pending" {
		t.Fatal("queue rollover steering", steering, err)
	}
	close(release)
	var completed completion
	select {
	case completed = <-done:
	case <-ctx.Done():
		t.Fatal("rollover did not finish")
	}
	if completed.err != nil || completed.result.Text != second.answer || launches != 2 {
		t.Fatalf("result=%+v err=%v launches=%d", completed.result, completed.err, launches)
	}
	eventsMu.Lock()
	gotEvents := append([]string(nil), events...)
	eventsMu.Unlock()
	wantEvents := []string{"open:old", "stream:old", "check:old", "close:old", "open:new", "stream:new", "close:new"}
	if strings.Join(gotEvents, ",") != strings.Join(wantEvents, ",") {
		t.Fatalf("wrong generation order\n got: %v\nwant: %v", gotEvents, wantEvents)
	}
	inspect, err := telemetry.OpenReadOnly(ctx, svc.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer inspect.Close()
	state, err := inspect.ContextCompactionPlanForAttempt(ctx, attempt.ID)
	journal, journalErr := inspect.Read(ctx, taskID, 0, 100)
	if err != nil || journalErr != nil || state.Status != sessions.ContextCompactionActivated {
		t.Fatal("missing durable rollover evidence", state, err, journalErr)
	}
	compactions, starts := 0, 0
	for _, event := range journal {
		if event.Kind == runtime.ContextCompacted {
			compactions++
		}
		if event.Kind == runtime.TurnStarted {
			starts++
		}
	}
	if compactions != 1 || starts != 2 {
		t.Fatal("duplicate or missing provider work", compactions, starts)
	}
}

func providerRequestText(request providers.Request) string {
	var b strings.Builder
	for _, message := range request.Messages {
		b.WriteString(message.Content)
	}
	return b.String()
}
