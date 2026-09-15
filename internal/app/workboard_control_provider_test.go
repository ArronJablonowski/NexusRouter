package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/resources"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/tools"
	"github.com/ArronJablonowski/DarwinRouter/webui"
	"github.com/ArronJablonowski/DarwinRouter/workboard"
	"github.com/ArronJablonowski/DarwinRouter/workers"
)

type workboardPauseResumeProvider struct {
	store         *telemetry.Store
	boardID       string
	cardID        string
	pauseRevision int64
	reachBoundary chan struct{}
	workerDone    <-chan error
	stage         atomic.Int32
	boundaryOnce  sync.Once
	errMu         sync.Mutex
	lastErr       error
}

func (*workboardPauseResumeProvider) Models(context.Context) ([]string, error) {
	return []string{"coordinator"}, nil
}

func (p *workboardPauseResumeProvider) Stream(ctx context.Context, request providers.Request, emit func(providers.Chunk) error) (err error) {
	defer func() {
		if err != nil {
			p.errMu.Lock()
			p.lastErr = err
			p.errMu.Unlock()
		}
	}()
	wantTools := []string{"workboard_add_dependency", "workboard_archive_board", "workboard_create_board", "workboard_create_card", "workboard_list", "workboard_read", "workboard_remove_dependency", "workboard_reorder_card", "workboard_request_cancel", "workboard_request_pause", "workboard_request_resume", "workboard_revise_board", "workboard_transition_card", "workboard_update_card"}
	names := make([]string, len(request.Tools))
	for index := range request.Tools {
		names[index] = request.Tools[index].Name
	}
	if !slices.Equal(names, wantTools) {
		return fmt.Errorf("provider tool catalog = %v, want %v", names, wantTools)
	}
	switch stage := p.stage.Add(1); stage {
	case 1:
		return emitWorkboardControlCall(emit, "pause-call", "workboard_request_pause", "provider-pause-key-01", p.boardID, p.cardID, p.pauseRevision)
	case 2:
		if err := requireLastWorkboardToolResult(request.Messages, "pause-call"); err != nil {
			return err
		}
		p.boundaryOnce.Do(func() { close(p.reachBoundary) })
		if _, err := waitProviderPausePhase(ctx, p.store, p.boardID, p.cardID, workboard.PauseAcknowledged); err != nil {
			return err
		}
		card, err := p.store.GetCard(ctx, p.boardID, p.cardID)
		if err != nil {
			return err
		}
		return emitWorkboardControlCall(emit, "resume-call", "workboard_request_resume", "provider-resume-key-1", p.boardID, p.cardID, card.Revision)
	case 3:
		if err := requireLastWorkboardToolResult(request.Messages, "resume-call"); err != nil {
			return err
		}
		select {
		case err := <-p.workerDone:
			if err != nil {
				return fmt.Errorf("worker after resume: %w", err)
			}
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
			return errors.New("worker did not complete after resume")
		}
		card, err := p.store.GetCard(ctx, p.boardID, p.cardID)
		if err != nil || card.PausePhase != workboard.PauseNone || card.PauseRequested {
			return fmt.Errorf("resume acknowledgement unavailable: card=%+v err=%v", card, err)
		}
		return emit(providers.Chunk{Text: "worker resumed and completed", Done: true, FinishReason: "stop"})
	default:
		return errors.New("unexpected provider turn")
	}
}

func (p *workboardPauseResumeProvider) error() error {
	p.errMu.Lock()
	defer p.errMu.Unlock()
	return p.lastErr
}

func emitWorkboardControlCall(emit func(providers.Chunk) error, callID, name, key, boardID, cardID string, revision int64) error {
	arguments, err := json.Marshal(map[string]any{"idempotency_key": key, "board_id": boardID, "card_id": cardID, "expected_card_revision": revision})
	if err != nil {
		return err
	}
	if err = emit(providers.Chunk{ToolCall: &providers.ToolCall{ID: callID, Name: name, Arguments: arguments}}); err != nil {
		return err
	}
	return emit(providers.Chunk{Done: true, FinishReason: "tool_calls"})
}

func requireLastWorkboardToolResult(messages []providers.Message, callID string) error {
	if len(messages) == 0 {
		return errors.New("provider received no messages")
	}
	message := messages[len(messages)-1]
	if message.Role != "tool" || message.ToolCallID != callID || message.ToolFailed || message.Content == "" {
		return fmt.Errorf("provider received invalid %s result: %+v", callID, message)
	}
	var receipt struct {
		Outcome string `json:"outcome"`
	}
	if json.Unmarshal([]byte(message.Content), &receipt) != nil || receipt.Outcome != "committed" {
		return fmt.Errorf("provider received invalid %s receipt", callID)
	}
	return nil
}

func waitProviderPausePhase(ctx context.Context, store *telemetry.Store, boardID, cardID string, phase workboard.PausePhase) (workboard.Card, error) {
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		card, err := store.GetCard(ctx, boardID, cardID)
		if err == nil && card.PausePhase == phase {
			return card, nil
		}
		select {
		case <-ctx.Done():
			return workboard.Card{}, ctx.Err()
		case <-timer.C:
			return workboard.Card{}, errors.New("worker pause acknowledgement timed out")
		case <-ticker.C:
		}
	}
}

func TestProviderNeutralWorkboardPauseAcknowledgementAndResume(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	database := filepath.Join(t.TempDir(), "provider-controls.db")
	store, boardID, cardID, cardRevision := readyWorkboardCardAt(t, database)
	defer store.Close()
	supervisor, err := workers.New(1, 20*time.Millisecond, 500*time.Millisecond, store, store)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewWorkboardWorkerRunner(supervisor, store, &capturingWorkboardEvaluator{}, strings.Repeat("a", 64), 20*time.Millisecond, 500*time.Millisecond, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	entered, reachBoundary := make(chan struct{}), make(chan struct{})
	workerDone := make(chan error, 1)
	go func() {
		_, runErr := runner.Run(ctx, WorkboardWorkerTask{BoardID: boardID, CardID: cardID, TaskID: "provider-control-worker-task",
			SessionID: "provider-control-worker-session", ParentTaskID: "provider-control-parent", Scope: "board-card-" + cardID,
			ExpectedCardRevision: cardRevision, FailureEffect: runtime.NoEffect,
			Execute: func(run context.Context, worker *WorkboardWorkerHandle) (WorkboardCandidate, error) {
				if bindErr := bindTestWorkboardRuntime(run, worker); bindErr != nil {
					return WorkboardCandidate{}, bindErr
				}
				close(entered)
				select {
				case <-reachBoundary:
				case <-run.Done():
					return WorkboardCandidate{}, run.Err()
				}
				if boundaryErr := worker.SafeBoundary(run); boundaryErr != nil {
					return WorkboardCandidate{}, boundaryErr
				}
				return WorkboardCandidate{Summary: "completed after resume"}, nil
			}, Validate: func(context.Context, WorkboardCandidate) error { return nil }})
		workerDone <- runErr
	}()
	waitSignal(t, entered, "provider fixture worker")
	card, err := store.GetCard(ctx, boardID, cardID)
	if err != nil {
		t.Fatal(err)
	}
	provider := &workboardPauseResumeProvider{store: store, boardID: boardID, cardID: cardID, pauseRevision: card.Revision,
		reachBoundary: reachBoundary, workerDone: workerDone}
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Telemetry.Database = database
	cfg.Tools.WorkboardReadEnabled = true
	cfg.Tools.WorkboardWriteEnabled = true
	cfg.Security.ToolPolicy = "ask"
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: "http://127.0.0.1:1"}}
	zero := 0.0
	cfg.Models = []config.Model{{ID: "coordinator", Model: "coordinator", Provider: "local", Locality: "local",
		Capabilities: []string{"chat"}, ContextTokens: 200_000, EstimatedCost: &zero, RAMBytes: 100}}
	svc, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc.profile = func(context.Context) (resources.Snapshot, error) {
		return resources.Snapshot{Time: time.Now(), TotalRAM: 1000, AvailableRAM: 1000}, nil
	}
	svc.toolReviewer = func(context.Context, tools.ApprovalPrompt) (string, bool, error) { return "operator", true, nil }
	svc.providerFactory = applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) { return provider, nil })
	result, err := svc.Run(ctx, Request{ModelID: "coordinator", Prompt: "Pause the worker, wait for acknowledgement, then resume it."})
	if err != nil || result.Text != "worker resumed and completed" || provider.stage.Load() != 3 {
		t.Fatalf("result=%+v provider_turns=%d err=%v provider_err=%v", result, provider.stage.Load(), err, provider.error())
	}
	bridge, err := NewWorkboardBridge(store, store, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	events, err := bridge.NativeEvents(ctx, boardID, webui.BoardEventOptions{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var controls []webui.BoardEvent
	for _, event := range events.Items {
		switch event.Kind {
		case webui.CardPauseRequest, webui.CardPauseAck, webui.CardResumeRequest, webui.CardResumeAck:
			controls = append(controls, event)
		}
	}
	if len(controls) != 4 || controls[0].Kind != webui.CardPauseRequest || controls[0].ActorType != "model" ||
		controls[1].Kind != webui.CardPauseAck || controls[1].ActorType != "worker" ||
		controls[2].Kind != webui.CardResumeRequest || controls[2].ActorType != "model" ||
		controls[3].Kind != webui.CardResumeAck || controls[3].ActorType != "worker" {
		t.Fatalf("provider-driven control lifecycle = %+v", controls)
	}
}
