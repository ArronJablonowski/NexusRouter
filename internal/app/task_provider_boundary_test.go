package app

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

type boundaryProvider struct {
	streams *atomic.Int32
}

func (*boundaryProvider) Models(context.Context) ([]string, error) { return []string{"a"}, nil }
func (p *boundaryProvider) Stream(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
	if p.streams != nil {
		p.streams.Add(1)
	}
	return emit(providers.Chunk{Text: "boundary answer", Done: true, FinishReason: "stop"})
}

func boundaryStreamRequest() providers.Request {
	return providers.Request{Model: "a", Messages: []providers.Message{{Role: "user", Content: "hello"}}}
}

func TestDeferredTaskProviderIsInertUntilStreamAndOpensOnceAcrossTurns(t *testing.T) {
	var opens, streams, cleanups atomic.Int32
	deferred := newDeferredTaskProvider(func(context.Context) (providers.Provider, func(), error) {
		opens.Add(1)
		return &boundaryProvider{streams: &streams}, func() { cleanups.Add(1) }, nil
	})
	if _, err := deferred.Models(context.Background()); err == nil || opens.Load() != 0 {
		t.Fatal("model discovery opened the deferred execution provider", err, opens.Load())
	}
	for turn := 0; turn < 2; turn++ {
		if err := deferred.Stream(context.Background(), boundaryStreamRequest(), func(providers.Chunk) error { return nil }); err != nil {
			t.Fatal("stream failed", turn, err)
		}
	}
	if opens.Load() != 1 || streams.Load() != 2 || cleanups.Load() != 0 {
		t.Fatal("deferred provider lifecycle mismatch", opens.Load(), streams.Load(), cleanups.Load())
	}
	deferred.Close()
	deferred.Close()
	if cleanups.Load() != 1 {
		t.Fatal("cleanup was not exactly once", cleanups.Load())
	}
}

func TestDeferredTaskProviderContainsOpenFailuresAndCleansUpOnce(t *testing.T) {
	for _, mode := range []string{"error", "panic", "typed_nil"} {
		t.Run(mode, func(t *testing.T) {
			var opens, cleanups atomic.Int32
			deferred := newDeferredTaskProvider(func(context.Context) (providers.Provider, func(), error) {
				opens.Add(1)
				cleanup := func() { cleanups.Add(1) }
				switch mode {
				case "error":
					return nil, cleanup, errors.New("private provider construction failure")
				case "panic":
					panic("private provider construction panic")
				default:
					var provider *boundaryProvider
					return provider, cleanup, nil
				}
			})
			for attempt := 0; attempt < 2; attempt++ {
				err := deferred.Stream(context.Background(), boundaryStreamRequest(), func(providers.Chunk) error { return nil })
				var failure *providers.Failure
				if !errors.As(err, &failure) || failure == nil || failure.Code != "adapter_failure" || errors.Is(err, context.Canceled) {
					t.Fatalf("open failure was not sanitized: %T %v", err, err)
				}
			}
			deferred.Close()
			deferred.Close()
			if opens.Load() != 1 {
				t.Fatal("failed construction was retried", opens.Load())
			}
			wantCleanup := int32(1)
			if mode == "panic" {
				// A callback that panics before returning cannot transfer cleanup ownership.
				wantCleanup = 0
			}
			if cleanups.Load() != wantCleanup {
				t.Fatal("failed construction cleanup mismatch", cleanups.Load(), wantCleanup)
			}
		})
	}
}

func TestDeferredTaskProviderCancellationIsStickyAndCleansUpOnce(t *testing.T) {
	entered := make(chan struct{})
	var opens, cleanups atomic.Int32
	deferred := newDeferredTaskProvider(func(ctx context.Context) (providers.Provider, func(), error) {
		opens.Add(1)
		close(entered)
		<-ctx.Done()
		return nil, func() { cleanups.Add(1) }, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- deferred.Stream(ctx, boundaryStreamRequest(), func(providers.Chunk) error { return nil })
	}()
	<-entered
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation was not preserved", err)
	}
	if err := deferred.Stream(context.Background(), boundaryStreamRequest(), func(providers.Chunk) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal("failed construction was retried or changed", err)
	}
	deferred.Close()
	deferred.Close()
	if opens.Load() != 1 || cleanups.Load() != 1 {
		t.Fatal("canceled construction lifecycle mismatch", opens.Load(), cleanups.Load())
	}
}

func TestExplicitExecutionFactoryReadsCommittedTaskStart(t *testing.T) {
	fixture, cfg := autoFixture(t)
	var starts, builds, streams atomic.Int32
	var startedTask atomic.Value
	factory := applicationProviderFactory(func(_ context.Context, connection providers.Connection) (providers.Provider, error) {
		builds.Add(1)
		if connection.Purpose != providers.PurposeExecution {
			t.Errorf("execution factory received purpose %q", connection.Purpose)
		}
		task, _ := startedTask.Load().(string)
		if task == "" || starts.Load() != 1 {
			t.Error("execution factory ran before the start sink")
			return nil, errors.New("start not observed")
		}
		db, err := telemetry.OpenReadOnly(context.Background(), cfg.Telemetry.Database)
		if err != nil {
			t.Error("open committed task", err)
			return nil, err
		}
		defer db.Close()
		events, err := db.Read(context.Background(), task, 0, 1)
		if err != nil || len(events) != 1 || events[0].Kind != runtime.TaskStarted || events[0].TaskID != task {
			t.Error("execution factory could not read committed task start", events, err)
			return nil, errors.New("committed start unavailable")
		}
		return &boundaryProvider{streams: &streams}, nil
	})
	svc, err := NewServiceWithProviderFactory(cfg, nil, nil, nil, nil, factory)
	if err != nil {
		t.Fatal(err)
	}
	svc.profile = fixture.profile
	if err := installEventSink(svc, runtime.EventSinkFunc(func(_ context.Context, event runtime.Event) error {
		if event.Kind == runtime.TaskStarted {
			startedTask.Store(event.TaskID)
			starts.Add(1)
		}
		return nil
	})); err != nil {
		t.Fatal(err)
	}
	result, err := svc.Run(context.Background(), Request{ModelID: "a", Prompt: "prove the durable boundary"})
	if err != nil || result.TaskID == "" || result.Text != "boundary answer" || builds.Load() != 1 || streams.Load() != 1 || starts.Load() != 1 {
		t.Fatal("explicit durable provider boundary failed", result, err, starts.Load(), builds.Load(), streams.Load())
	}
}
