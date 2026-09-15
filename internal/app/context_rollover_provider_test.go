package app

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/providers"
)

type rolloverProviderFixture struct {
	name       string
	events     *[]string
	checkErr   error
	closeErr   error
	requests   []providers.Request
	checkCalls int
}

type rolloverCloseFailureFixture struct {
	rolloverProviderFixture
	panicClose bool
	closeCalls int
}

func (p *rolloverCloseFailureFixture) Close() error {
	p.closeCalls++
	*p.events = append(*p.events, "close:"+p.name)
	if p.panicClose {
		panic("private close panic")
	}
	return errors.New("private close error")
}

func (p *rolloverProviderFixture) Models(context.Context) ([]string, error) {
	return []string{"model"}, nil
}
func (p *rolloverProviderFixture) Stream(_ context.Context, request providers.Request, emit func(providers.Chunk) error) error {
	*p.events = append(*p.events, "stream:"+p.name)
	p.requests = append(p.requests, request)
	return emit(providers.Chunk{Text: p.name, Done: true, FinishReason: "stop"})
}
func (p *rolloverProviderFixture) CheckContextRollover(_ context.Context, current, prospective providers.Request) error {
	*p.events = append(*p.events, "check:"+p.name)
	p.checkCalls++
	if current.Model != prospective.Model {
		return errors.New("model mismatch")
	}
	return p.checkErr
}
func (p *rolloverProviderFixture) Close() error {
	*p.events = append(*p.events, "close:"+p.name)
	return p.closeErr
}

func rolloverRequests() (providers.Request, providers.Request, providers.Request) {
	current := providers.Request{Model: "model", Messages: []providers.Message{{Role: "user", Content: "old"}, {Role: "assistant", Content: "answer"}}}
	base := providers.Request{Model: "model", Messages: []providers.Message{{Role: "user", Content: "summary"}, {Role: "assistant", Content: "answer"}}}
	prospective := base
	prospective.Messages = append(append([]providers.Message(nil), base.Messages...), providers.Message{Role: "user", Content: "next"})
	return current, base, prospective
}

func TestContextRolloverProviderRetiresBeforeLazyReplacement(t *testing.T) {
	events := []string{}
	first := &rolloverProviderFixture{name: "one", events: &events}
	second := &rolloverProviderFixture{name: "two", events: &events}
	providersByGeneration := []taskProvider{first, second}
	open := 0
	deferred := newDeferredTaskProvider(func(context.Context) (providers.Provider, func(), error) {
		if open >= len(providersByGeneration) {
			return nil, nil, errors.New("unexpected generation")
		}
		provider := providersByGeneration[open]
		open++
		events = append(events, "open:"+provider.(*rolloverProviderFixture).name)
		return provider, func() {}, nil
	})
	rollover := newContextRolloverTaskProvider(deferred)
	current, base, prospective := rolloverRequests()
	if err := rollover.Stream(context.Background(), current, func(providers.Chunk) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := rollover.CheckContextRollover(context.Background(), current, prospective); err != nil {
		t.Fatal(err)
	}
	if err := rollover.ActivateContextRollover(context.Background(), base); err != nil {
		t.Fatal(err)
	}
	if open != 1 {
		t.Fatal("replacement opened before its durable turn", open)
	}
	if err := rollover.Stream(context.Background(), prospective, func(providers.Chunk) error { return nil }); err != nil {
		t.Fatal(err)
	}
	want := []string{"open:one", "stream:one", "check:one", "close:one", "open:two", "stream:two"}
	if !reflect.DeepEqual(events, want) || open != 2 || len(second.requests) != 1 || !sameProviderRequest(second.requests[0], prospective) {
		t.Fatalf("rollover ordering mismatch\nevents=%v\nopens=%d\nrequests=%v", events, open, second.requests)
	}
}

func TestContextRolloverProviderFailuresAreSticky(t *testing.T) {
	for _, mode := range []string{"check", "close", "request", "launch"} {
		t.Run(mode, func(t *testing.T) {
			events := []string{}
			first := &rolloverProviderFixture{name: "one", events: &events}
			if mode == "check" {
				first.checkErr = errors.New("rejected")
			}
			if mode == "close" {
				first.closeErr = errors.New("ambiguous close")
			}
			open := 0
			deferred := newDeferredTaskProvider(func(context.Context) (providers.Provider, func(), error) {
				open++
				if open == 1 {
					return first, func() {}, nil
				}
				if mode == "launch" {
					return nil, nil, errors.New("launch failed")
				}
				return &rolloverProviderFixture{name: "two", events: &events}, func() {}, nil
			})
			rollover := newContextRolloverTaskProvider(deferred)
			current, base, prospective := rolloverRequests()
			if err := rollover.Stream(context.Background(), current, func(providers.Chunk) error { return nil }); err != nil {
				t.Fatal(err)
			}
			checkErr := rollover.CheckContextRollover(context.Background(), current, prospective)
			if mode == "check" {
				if checkErr == nil || open != 1 {
					t.Fatal("check failure did not remain before activation", checkErr, open)
				}
				return
			}
			if checkErr != nil {
				t.Fatal(checkErr)
			}
			activateErr := rollover.ActivateContextRollover(context.Background(), base)
			if mode == "close" {
				if activateErr == nil || open != 1 {
					t.Fatal("close failure launched replacement", activateErr, open)
				}
				return
			}
			if activateErr != nil {
				t.Fatal(activateErr)
			}
			request := prospective
			if mode == "request" {
				request.Messages[len(request.Messages)-1].Content = "different"
			}
			firstErr := rollover.Stream(context.Background(), request, func(providers.Chunk) error { return nil })
			secondErr := rollover.Stream(context.Background(), prospective, func(providers.Chunk) error { return nil })
			if firstErr == nil || secondErr == nil {
				t.Fatal("rollover failure was retryable", firstErr, secondErr)
			}
			if mode == "request" && open != 1 {
				t.Fatal("mismatch launched replacement", open)
			}
			if mode == "launch" && open != 2 {
				t.Fatal("launch was retried", open)
			}
		})
	}
}

func TestContextRolloverRetirementNeverRetriesAmbiguousClose(t *testing.T) {
	for _, panicClose := range []bool{false, true} {
		t.Run(map[bool]string{false: "error", true: "panic"}[panicClose], func(t *testing.T) {
			events := []string{}
			raw := &rolloverCloseFailureFixture{rolloverProviderFixture: rolloverProviderFixture{name: "old", events: &events}, panicClose: panicClose}
			owned := &ownedCodexProvider{taskProvider: raw, dir: t.TempDir()}
			deferred := newDeferredTaskProvider(func(context.Context) (providers.Provider, func(), error) {
				return owned, func() { _ = owned.Close() }, nil
			})
			rollover := newContextRolloverTaskProvider(deferred)
			current, base, prospective := rolloverRequests()
			if err := rollover.Stream(context.Background(), current, func(providers.Chunk) error { return nil }); err != nil {
				t.Fatal(err)
			}
			if err := rollover.CheckContextRollover(context.Background(), current, prospective); err != nil {
				t.Fatal(err)
			}
			if err := rollover.ActivateContextRollover(context.Background(), base); err == nil {
				t.Fatal("ambiguous close succeeded")
			}
			rollover.Close()
			if raw.closeCalls != 1 {
				t.Fatal("ambiguous close was retried", raw.closeCalls, events)
			}
		})
	}
}
