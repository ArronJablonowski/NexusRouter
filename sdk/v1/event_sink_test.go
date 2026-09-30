package v1_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/resources"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
	"go.yaml.in/yaml/v3"
)

const sdkEventSinkSecret = "sdk-event-sink-private-value"

type sdkEventSinkProvider func(context.Context, providers.Request, func(providers.Chunk) error) error

func (p sdkEventSinkProvider) Stream(ctx context.Context, request providers.Request, emit func(providers.Chunk) error) error {
	return p(ctx, request, emit)
}

func (sdkEventSinkProvider) Models(context.Context) ([]string, error) {
	return []string{"fixture"}, nil
}

func newSDKEventSinkClient(t *testing.T, database string, concurrency int, sink sdk.EventSink, stream sdkEventSinkProvider, providerCalls *atomic.Int32) (*sdk.Client, string) {
	t.Helper()
	if concurrency < 1 {
		concurrency = 1
	}
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Hardware.AutoProfile = false
	cfg.Hardware.Concurrent = strconv.Itoa(concurrency)
	cfg.Workers.Max = concurrency
	cfg.Telemetry.Database = database
	cfg.Providers = []config.Provider{{ID: "fixture", Kind: "ollama", Endpoint: "http://127.0.0.1:1", APIKeyEnv: "SDK_EVENT_SINK_SECRET"}}
	zero := 0.0
	cfg.Models = []config.Model{{ID: "chat", Provider: "fixture", Model: "fixture", Locality: "local", RAMBytes: 1, ContextTokens: 8192, EstimatedCost: &zero, Capabilities: []string{"chat"}}}
	body, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, body, 0600); err != nil {
		t.Fatal(err)
	}
	factory := sdkProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		if providerCalls != nil {
			providerCalls.Add(1)
		}
		return stream, nil
	})
	client, err := sdk.New(sdk.ConfigOptions{
		ProjectFile: configPath,
		EventSink:   sink,
		ResourceProfiler: sdkFixtureProfiler(func(context.Context) (resources.Measurement, error) {
			return resources.Measurement{Version: 1, Snapshot: resources.Snapshot{Time: time.Now().UTC(), CPUs: concurrency + 1, TotalRAM: 16 << 30, AvailableRAM: 16 << 30}}, nil
		}),
		ProviderFactory: factory,
		LookupSecret: func(name string) string {
			if name == "SDK_EVENT_SINK_SECRET" {
				return sdkEventSinkSecret
			}
			return ""
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return client, configPath
}

func sdkEventSinkSuccessProvider() sdkEventSinkProvider {
	return func(ctx context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
		if err := emit(providers.Chunk{Text: strings.Repeat("safe ", 48)}); err != nil {
			return err
		}
		return emit(providers.Chunk{Text: sdkEventSinkSecret + " answer", Done: true, FinishReason: "stop"})
	}
}

func TestSDKEventSinkCoversRunSurfacesAfterDurableCommit(t *testing.T) {
	for _, surface := range []string{"run", "text", "events"} {
		t.Run(surface, func(t *testing.T) {
			database := filepath.Join(t.TempDir(), "events.db")
			var mu sync.Mutex
			var delivered []runtime.Event
			seenDelta := false
			sink := sdk.EventSinkFunc(func(ctx context.Context, event runtime.Event) error {
				if ctx == nil || ctx.Err() != nil {
					return errors.New("invalid callback context")
				}
				db, err := telemetry.OpenReadOnly(ctx, database)
				if err != nil {
					return err
				}
				stored, readErr := db.Read(ctx, event.TaskID, event.Sequence-1, 1)
				_ = db.Close()
				if readErr != nil || len(stored) != 1 || stored[0].ID != event.ID {
					return errors.New("callback preceded durable commit")
				}
				want, wantErr := stored[0].Encode()
				got, gotErr := event.Encode()
				if wantErr != nil || gotErr != nil || string(want) != string(got) {
					return errors.New("callback differed from durable event")
				}
				encoded := string(got)
				if strings.Contains(encoded, sdkEventSinkSecret) || event.Kind == runtime.ModelDelta && event.Data.Text != "" {
					return errors.New("unredacted callback")
				}
				mu.Lock()
				if event.Sequence != int64(len(delivered)+1) {
					mu.Unlock()
					return errors.New("out-of-order callback")
				}
				delivered = append(delivered, event)
				seenDelta = seenDelta || event.Kind == runtime.ModelDelta
				mu.Unlock()
				return nil
			})
			client, _ := newSDKEventSinkClient(t, database, 1, sink, sdkEventSinkSuccessProvider(), nil)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			request := sdk.Request{Version: 1, ModelID: "chat", Prompt: "do not expose " + sdkEventSinkSecret}
			var result sdk.Result
			var err error
			switch surface {
			case "run":
				result, err = client.Run(ctx, request)
			case "text":
				var text strings.Builder
				result, err = client.RunTextStream(ctx, request, func(chunk string) error {
					mu.Lock()
					deltaCommitted := seenDelta
					mu.Unlock()
					if !deltaCommitted {
						return errors.New("text preceded lifecycle callback")
					}
					text.WriteString(chunk)
					return nil
				})
				if err == nil && text.String() != result.Text {
					t.Fatalf("text stream differs from result: %q != %q", text.String(), result.Text)
				}
			case "events":
				var perCall []runtime.Event
				result, err = client.RunStream(ctx, request, func(event runtime.Event) error {
					perCall = append(perCall, event)
					return nil
				})
				mu.Lock()
				configured := append([]runtime.Event(nil), delivered...)
				mu.Unlock()
				if len(perCall) != len(configured) {
					t.Fatalf("fanout length mismatch: configured=%d per-call=%d", len(configured), len(perCall))
				}
				for i := range perCall {
					if perCall[i].ID != configured[i].ID {
						t.Fatalf("fanout event mismatch at %d", i)
					}
				}
			}
			mu.Lock()
			events := append([]runtime.Event(nil), delivered...)
			mu.Unlock()
			if err != nil || result.TaskID == "" || !strings.Contains(result.Text, "[REDACTED]") || strings.Contains(result.Text, sdkEventSinkSecret) || len(events) < 4 || events[0].Kind != runtime.TaskStarted || events[len(events)-1].Kind != runtime.TaskCompleted {
				t.Fatal("unexpected event-sink run", result, len(events), err)
			}
		})
	}
}

func TestSDKEventSinkAndRunStreamReceiveDetachedFanout(t *testing.T) {
	database := filepath.Join(t.TempDir(), "events.db")
	configuredStart := ""
	sink := sdk.EventSinkFunc(func(_ context.Context, event runtime.Event) error {
		if event.Kind == runtime.TaskStarted {
			configuredStart = event.Data.Messages[0].Content
			event.Data.Messages[0].Content = "configured-mutated"
		}
		return nil
	})
	client, _ := newSDKEventSinkClient(t, database, 1, sink, sdkEventSinkSuccessProvider(), nil)
	perCallStart := ""
	result, err := client.RunStream(context.Background(), sdk.Request{Version: 1, ModelID: "chat", Prompt: "prompt " + sdkEventSinkSecret}, func(event runtime.Event) error {
		if event.Kind == runtime.TaskStarted {
			perCallStart = event.Data.Messages[0].Content
			event.Data.Messages[0].Content = "per-call-mutated"
		}
		return nil
	})
	if err != nil || configuredStart != "prompt [REDACTED]" || perCallStart != configuredStart {
		t.Fatal("fanout was not independently cloned", configuredStart, perCallStart, result, err)
	}
	page, err := client.ReadEvents(context.Background(), result.TaskID, 0, 100)
	if err != nil || len(page.Events) == 0 || page.Events[0].Data.Messages[0].Content != configuredStart {
		t.Fatal("callback mutated durable replay", page, err)
	}
}

func TestSDKEventSinkFailureStillDeliversSameCommittedEventToRunStream(t *testing.T) {
	database := filepath.Join(t.TempDir(), "events.db")
	var configured []runtime.Event
	sink := sdk.EventSinkFunc(func(_ context.Context, event runtime.Event) error {
		configured = append(configured, event)
		return errors.New("private configured failure")
	})
	client, _ := newSDKEventSinkClient(t, database, 1, sink, sdkEventSinkSuccessProvider(), nil)
	var perCall []runtime.Event
	result, err := client.RunStream(context.Background(), sdk.Request{Version: 1, ModelID: "chat", Prompt: "same committed event"}, func(event runtime.Event) error {
		perCall = append(perCall, event)
		return nil
	})
	if !errors.Is(err, sdk.ErrEventDelivery) || strings.Contains(err.Error(), "private configured failure") || len(configured) != 1 || len(perCall) != 1 {
		t.Fatal("configured failure broke same-event fanout", result, err, len(configured), len(perCall))
	}
	if configured[0].Kind != runtime.TaskStarted || perCall[0].ID != configured[0].ID || perCall[0].Sequence != configured[0].Sequence {
		t.Fatal("per-call callback did not receive the rejected committed event", configured, perCall)
	}
	page, readErr := client.ReadEvents(context.Background(), result.TaskID, 0, 100)
	if readErr != nil || len(page.Events) != 2 || page.Events[0].ID != configured[0].ID || page.Events[1].Kind != runtime.TaskCanceled {
		t.Fatal("same-event fanout changed durable cleanup", page, readErr)
	}
}

func TestSDKEventSinkExternalCancellationDeliversDurableTerminal(t *testing.T) {
	database := filepath.Join(t.TempDir(), "events.db")
	deltaCommitted := make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	var configured []runtime.Event
	var terminalContextErr error
	sink := sdk.EventSinkFunc(func(ctx context.Context, event runtime.Event) error {
		mu.Lock()
		configured = append(configured, event)
		if event.Kind == runtime.TaskCanceled {
			terminalContextErr = ctx.Err()
		}
		mu.Unlock()
		if event.Kind == runtime.ModelDelta {
			once.Do(func() { close(deltaCommitted) })
		}
		return nil
	})
	provider := sdkEventSinkProvider(func(ctx context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
		if err := emit(providers.Chunk{Text: strings.Repeat("partial ", 40)}); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	})
	client, _ := newSDKEventSinkClient(t, database, 1, sink, provider, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	type outcome struct {
		result sdk.Result
		err    error
	}
	done := make(chan outcome, 1)
	var perCallMu sync.Mutex
	var perCall []runtime.Event
	go func() {
		result, err := client.RunStream(ctx, sdk.Request{Version: 1, ModelID: "chat", Prompt: "cancel after delta"}, func(event runtime.Event) error {
			perCallMu.Lock()
			perCall = append(perCall, event)
			perCallMu.Unlock()
			return nil
		})
		done <- outcome{result: result, err: err}
	}()
	select {
	case <-deltaCommitted:
		cancel()
	case <-ctx.Done():
		t.Fatal("model delta was not committed")
	}
	var completed outcome
	select {
	case completed = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("canceled run did not join")
	}
	if !errors.Is(completed.err, context.Canceled) || errors.Is(completed.err, sdk.ErrEventDelivery) || completed.result.TaskID == "" {
		t.Fatal("caller cancellation was misclassified", completed.result, completed.err)
	}
	mu.Lock()
	observed := append([]runtime.Event(nil), configured...)
	gotTerminalContextErr := terminalContextErr
	mu.Unlock()
	perCallMu.Lock()
	perCallObserved := append([]runtime.Event(nil), perCall...)
	perCallMu.Unlock()
	for name, events := range map[string][]runtime.Event{"configured": observed, "per-call": perCallObserved} {
		canceled, delta := 0, 0
		for _, event := range events {
			delta += map[bool]int{true: 1}[event.Kind == runtime.ModelDelta]
			canceled += map[bool]int{true: 1}[event.Kind == runtime.TaskCanceled]
		}
		if delta != 1 || canceled != 1 || events[len(events)-1].Kind != runtime.TaskCanceled {
			t.Fatalf("%s stream missed ordered cancellation lifecycle: %v", name, events)
		}
	}
	if gotTerminalContextErr != nil {
		t.Fatal("terminal callback inherited canceled execution context", gotTerminalContextErr)
	}
	page, err := client.ReadEvents(context.Background(), completed.result.TaskID, 0, 100)
	if err != nil || page.Events[len(page.Events)-1].Kind != runtime.TaskCanceled {
		t.Fatal("terminal callback lacked matching durable event", page, err)
	}
}

func TestSDKEventSinkFailureSuppressesAssociatedAndPendingText(t *testing.T) {
	for _, failureKind := range []runtime.Kind{runtime.ModelDelta, runtime.TaskCompleted} {
		for _, surface := range []string{"text", "live"} {
			t.Run(string(failureKind)+"/"+surface, func(t *testing.T) {
				database := filepath.Join(t.TempDir(), "events.db")
				var mu sync.Mutex
				var configured, perCall []runtime.Event
				sink := sdk.EventSinkFunc(func(_ context.Context, event runtime.Event) error {
					mu.Lock()
					configured = append(configured, event)
					mu.Unlock()
					if event.Kind == failureKind {
						return errors.New("private configured text-gate failure")
					}
					return nil
				})
				// A possible credential prefix remains buffered until TaskCompleted;
				// the terminal sink failure must suppress that pending flush.
				text := sdkEventSinkSecret[:10]
				if failureKind == runtime.ModelDelta {
					text = strings.Repeat("associated text ", 40)
				}
				provider := sdkEventSinkProvider(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
					return emit(providers.Chunk{Text: text, Done: true, FinishReason: "stop"})
				})
				client, _ := newSDKEventSinkClient(t, database, 1, sink, provider, nil)
				var streamed strings.Builder
				var result sdk.Result
				var err error
				request := sdk.Request{Version: 1, ModelID: "chat", Prompt: "withhold failed delivery"}
				if surface == "text" {
					result, err = client.RunTextStream(context.Background(), request, func(chunk string) error {
						streamed.WriteString(chunk)
						return nil
					})
				} else {
					result, err = client.RunLiveStream(context.Background(), request, func(event runtime.Event) error {
						mu.Lock()
						perCall = append(perCall, event)
						mu.Unlock()
						return nil
					}, func(chunk string) error {
						mu.Lock()
						last := runtime.Kind("")
						if len(perCall) > 0 {
							last = perCall[len(perCall)-1].Kind
						}
						mu.Unlock()
						if last != runtime.ModelDelta && last != runtime.TaskCompleted {
							return errors.New("text preceded lifecycle")
						}
						streamed.WriteString(chunk)
						return nil
					})
				}
				if !errors.Is(err, sdk.ErrEventDelivery) || strings.Contains(err.Error(), "private configured") || result.TaskID == "" || streamed.Len() != 0 {
					t.Fatal("failed configured event delivery leaked text", failureKind, surface, result, streamed.String(), err)
				}
				mu.Lock()
				configuredCopy := append([]runtime.Event(nil), configured...)
				perCallCopy := append([]runtime.Event(nil), perCall...)
				mu.Unlock()
				found := false
				for _, event := range configuredCopy {
					found = found || event.Kind == failureKind
				}
				if !found {
					t.Fatal("configured sink never reached requested failure marker", configuredCopy)
				}
				if surface == "live" {
					matched := false
					for _, event := range perCallCopy {
						matched = matched || event.Kind == failureKind
					}
					if !matched {
						t.Fatal("live event callback missed same committed failure marker", perCallCopy)
					}
				}
			})
		}
	}
}

type typedNilSDKEventSink struct{}

func (*typedNilSDKEventSink) Emit(context.Context, runtime.Event) error {
	panic("typed nil sink invoked")
}

func TestSDKEventSinkTypedNilRejectedBeforeEffects(t *testing.T) {
	database := filepath.Join(t.TempDir(), "missing.db")
	var sink *typedNilSDKEventSink
	var builds atomic.Int32
	client, err := sdk.New(sdk.ConfigOptions{
		Overrides: map[string]string{"telemetry.database": database},
		EventSink: sink,
		ProviderFactory: sdkProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
			builds.Add(1)
			return sdkEventSinkSuccessProvider(), nil
		}),
	})
	if !errors.Is(err, sdk.ErrAdmission) || client != nil || builds.Load() != 0 {
		t.Fatal("typed nil sink admitted", client, err, builds.Load())
	}
	if _, statErr := os.Stat(database); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("typed nil admission touched storage", statErr)
	}
}

func TestSDKEventSinkFailureIsSanitizedAndClientReusable(t *testing.T) {
	for _, failure := range []string{"error", "panic"} {
		t.Run(failure, func(t *testing.T) {
			database := filepath.Join(t.TempDir(), "events.db")
			var fail atomic.Bool
			fail.Store(true)
			var calls atomic.Int32
			sink := sdk.EventSinkFunc(func(context.Context, runtime.Event) error {
				calls.Add(1)
				if fail.Swap(false) {
					if failure == "panic" {
						panic("private sink panic detail")
					}
					return errors.New("private sink error detail")
				}
				return nil
			})
			var providersBuilt atomic.Int32
			client, _ := newSDKEventSinkClient(t, database, 1, sink, sdkEventSinkSuccessProvider(), &providersBuilt)
			failed, err := client.Run(context.Background(), sdk.Request{Version: 1, ModelID: "chat", Prompt: "first"})
			if !errors.Is(err, sdk.ErrEventDelivery) || strings.Contains(err.Error(), "private sink") || failed.TaskID == "" || providersBuilt.Load() != 0 || calls.Load() != 1 {
				t.Fatal("sink failure was unsafe", failed, err, providersBuilt.Load(), calls.Load())
			}
			page, readErr := client.ReadEvents(context.Background(), failed.TaskID, 0, 100)
			if readErr != nil || len(page.Events) != 2 || page.Events[0].Kind != runtime.TaskStarted || page.Events[1].Kind != runtime.TaskCanceled {
				t.Fatal("sink failure lacked durable cleanup", page, readErr)
			}
			before := calls.Load()
			succeeded, err := client.Run(context.Background(), sdk.Request{Version: 1, ModelID: "chat", Prompt: "second"})
			if err != nil || succeeded.TaskID == "" || succeeded.TaskID == failed.TaskID || providersBuilt.Load() != 1 || calls.Load() <= before {
				t.Fatal("failed delivery poisoned client", succeeded, err, providersBuilt.Load(), calls.Load())
			}
		})
	}
}

func TestSDKEventSinkDoesNotReceiveFailedPersistence(t *testing.T) {
	database := filepath.Join(t.TempDir(), "database-is-a-directory")
	var sinkCalls, providerCalls atomic.Int32
	client, _ := newSDKEventSinkClient(t, database, 1, sdk.EventSinkFunc(func(context.Context, runtime.Event) error {
		sinkCalls.Add(1)
		return nil
	}), sdkEventSinkSuccessProvider(), &providerCalls)
	if err := os.Mkdir(database, 0700); err != nil {
		t.Fatal(err)
	}
	result, err := client.Run(context.Background(), sdk.Request{Version: 1, ModelID: "chat", Prompt: "never durable"})
	if err == nil || result.TaskID != "" || sinkCalls.Load() != 0 || providerCalls.Load() != 0 {
		t.Fatal("failed persistence reached sink or provider", result, err, sinkCalls.Load(), providerCalls.Load())
	}
}

func TestSDKEventSinkConcurrentRunsPreservePerTaskSequence(t *testing.T) {
	const taskCount = 8
	database := filepath.Join(t.TempDir(), "events.db")
	db, err := telemetry.Open(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	sequences := map[string][]int64{}
	sink := sdk.EventSinkFunc(func(_ context.Context, event runtime.Event) error {
		mu.Lock()
		sequences[event.TaskID] = append(sequences[event.TaskID], event.Sequence)
		mu.Unlock()
		return nil
	})
	client, _ := newSDKEventSinkClient(t, database, taskCount, sink, sdkEventSinkSuccessProvider(), nil)
	start := make(chan struct{})
	var wait sync.WaitGroup
	errorsByRun := make(chan error, taskCount)
	for i := 0; i < taskCount; i++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			result, err := client.Run(context.Background(), sdk.Request{Version: 1, ModelID: "chat", Prompt: "concurrent " + strconv.Itoa(index)})
			if err == nil && result.TaskID == "" {
				err = errors.New("missing task identity")
			}
			errorsByRun <- err
		}(i)
	}
	close(start)
	wait.Wait()
	close(errorsByRun)
	for runErr := range errorsByRun {
		if runErr != nil {
			t.Fatal(runErr)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sequences) != taskCount {
		t.Fatalf("sink observed %d tasks, want %d", len(sequences), taskCount)
	}
	for task, seq := range sequences {
		if len(seq) < 4 {
			t.Fatalf("task %s had too few events: %v", task, seq)
		}
		for i, value := range seq {
			if value != int64(i+1) {
				t.Fatalf("task %s sequence gap: %v", task, seq)
			}
		}
	}
}

func TestSDKEventSinkRestartDoesNotAutomaticallyReplay(t *testing.T) {
	database := filepath.Join(t.TempDir(), "events.db")
	var firstMu sync.Mutex
	var firstEvents []runtime.Event
	first, configPath := newSDKEventSinkClient(t, database, 1, sdk.EventSinkFunc(func(_ context.Context, event runtime.Event) error {
		firstMu.Lock()
		firstEvents = append(firstEvents, event)
		firstMu.Unlock()
		return nil
	}), sdkEventSinkSuccessProvider(), nil)
	initial, err := first.Run(context.Background(), sdk.Request{Version: 1, ModelID: "chat", Prompt: "initial"})
	if err != nil {
		t.Fatal(err)
	}
	var reopenedMu sync.Mutex
	var reopenedEvents []runtime.Event
	reopened, err := sdk.New(sdk.ConfigOptions{
		ProjectFile: configPath,
		EventSink: sdk.EventSinkFunc(func(_ context.Context, event runtime.Event) error {
			reopenedMu.Lock()
			reopenedEvents = append(reopenedEvents, event)
			reopenedMu.Unlock()
			return nil
		}),
		ResourceProfiler: sdkFixtureProfiler(func(context.Context) (resources.Measurement, error) {
			return sdkGoodMeasurement(), nil
		}),
		ProviderFactory: sdkProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
			return sdkEventSinkSuccessProvider(), nil
		}),
		LookupSecret: func(name string) string {
			if name == "SDK_EVENT_SINK_SECRET" {
				return sdkEventSinkSecret
			}
			return ""
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reopened.InspectTask(context.Background(), initial.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err = reopened.ReadEvents(context.Background(), initial.TaskID, 0, 100); err != nil {
		t.Fatal(err)
	}
	reopenedMu.Lock()
	count := len(reopenedEvents)
	reopenedMu.Unlock()
	if count != 0 {
		t.Fatal("construction or inspection replayed historical events", count)
	}
	next, err := reopened.Run(context.Background(), sdk.Request{Version: 1, ModelID: "chat", Prompt: "next"})
	if err != nil || next.TaskID == "" || next.TaskID == initial.TaskID {
		t.Fatal(next, err)
	}
	reopenedMu.Lock()
	defer reopenedMu.Unlock()
	if len(reopenedEvents) == 0 {
		t.Fatal("reopened sink missed new task")
	}
	for _, event := range reopenedEvents {
		if event.TaskID != next.TaskID {
			t.Fatal("reopened sink replayed old task", event.TaskID, initial.TaskID)
		}
	}
}
