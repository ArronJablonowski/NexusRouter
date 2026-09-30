package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/providers"
)

// contextRolloverTaskProvider owns successive task-scoped provider generations.
// Check is read-only and runs before durable activation. Activate irreversibly
// retires the checked generation only after that activation commits, then arms
// the exact first request accepted by the next lazily opened generation.
type contextRolloverTaskProvider struct {
	*deferredTaskProvider
	checkedProvider    providers.Provider
	checkedCurrent     *providers.Request
	checkedProspective *providers.Request
	armedBase          *providers.Request
	armedProspective   *providers.Request
	poisoned           error
}

func newContextRolloverTaskProvider(p *deferredTaskProvider) *contextRolloverTaskProvider {
	return &contextRolloverTaskProvider{deferredTaskProvider: p}
}

func cloneProviderRequest(r providers.Request) (*providers.Request, error) {
	body, err := json.Marshal(r)
	if err != nil || len(body) > 8<<20 {
		return nil, &providers.Failure{Code: "context_rollover"}
	}
	var clone providers.Request
	if json.Unmarshal(body, &clone) != nil {
		return nil, &providers.Failure{Code: "context_rollover"}
	}
	if r.JSONSchema == nil {
		clone.JSONSchema = nil
	}
	return &clone, nil
}

func sameProviderRequest(a, b providers.Request) bool {
	left, leftErr := json.Marshal(a)
	right, rightErr := json.Marshal(b)
	return leftErr == nil && rightErr == nil && bytes.Equal(left, right)
}

func requestPrefix(all, prefix providers.Request) bool {
	if len(all.Messages) < len(prefix.Messages) {
		return false
	}
	want := all
	want.Messages = want.Messages[:len(prefix.Messages)]
	return sameProviderRequest(want, prefix)
}

func validRolloverGuidance(messages []providers.Message) bool {
	if len(messages) == 0 || len(messages) > 32 {
		return false
	}
	for _, message := range messages {
		if message.Role != "user" || message.Content == "" || len(message.Content) > 64<<10 || !utf8.ValidString(message.Content) ||
			len(message.ToolCalls) != 0 || message.ToolCallID != "" || message.ToolFailed {
			return false
		}
	}
	return true
}

func unwrapRolloverProvider(p providers.Provider) providers.Provider {
	if memoryProvider, ok := p.(*memoryUseProvider); ok && memoryProvider != nil {
		return memoryProvider.Provider
	}
	return p
}

func (p *contextRolloverTaskProvider) CheckContextRollover(ctx context.Context, current, prospective providers.Request) error {
	if p == nil || p.deferredTaskProvider == nil || ctx == nil || ctx.Err() != nil {
		return &providers.Failure{Code: "context_rollover"}
	}
	currentClone, err := cloneProviderRequest(current)
	if err != nil {
		return err
	}
	prospectiveClone, err := cloneProviderRequest(prospective)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.poisoned != nil || !p.opened || p.openErr != nil || nilTaskProvider(p.provider) || p.armedBase != nil {
		return &providers.Failure{Code: "context_rollover"}
	}
	inspector, ok := unwrapRolloverProvider(p.provider).(interface {
		CheckContextRollover(context.Context, providers.Request, providers.Request) error
	})
	if !ok || inspector.CheckContextRollover(ctx, *currentClone, *prospectiveClone) != nil || ctx.Err() != nil {
		return &providers.Failure{Code: "context_rollover"}
	}
	p.checkedProvider = p.provider
	p.checkedCurrent = currentClone
	p.checkedProspective = prospectiveClone
	return nil
}

func (p *contextRolloverTaskProvider) ActivateContextRollover(ctx context.Context, base providers.Request) error {
	if p == nil || p.deferredTaskProvider == nil || ctx == nil {
		return &providers.Failure{Code: "context_rollover"}
	}
	baseClone, err := cloneProviderRequest(base)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	fail := func(cause error) error {
		p.poisoned = &providers.Failure{Code: "context_rollover", Partial: true}
		p.openErr = p.poisoned
		return errors.Join(p.poisoned, cause)
	}
	if ctx.Err() != nil || p.closed || p.poisoned != nil || p.checkedProvider == nil || p.provider != p.checkedProvider ||
		p.checkedCurrent == nil || p.checkedProspective == nil || !requestPrefix(*p.checkedProspective, *baseClone) {
		return fail(ctx.Err())
	}
	guidance := p.checkedProspective.Messages[len(baseClone.Messages):]
	if !validRolloverGuidance(guidance) {
		return fail(nil)
	}
	owned, ok := unwrapRolloverProvider(p.provider).(taskProvider)
	if !ok || nilTaskProvider(owned) {
		return fail(nil)
	}
	// Activation is the authority boundary. A close failure is ambiguous, so
	// never open a replacement or retry this generation.
	if closeErr := owned.Close(); closeErr != nil {
		return fail(closeErr)
	}
	cleanup := p.cleanup
	p.provider, p.cleanup, p.openErr = nil, nil, nil
	p.opened = false
	p.armedProspective = p.checkedProspective
	p.checkedProvider, p.checkedCurrent, p.checkedProspective = nil, nil, nil
	p.armedBase = baseClone
	safeTaskProviderCleanup(cleanup)
	return nil
}

func (p *contextRolloverTaskProvider) Stream(ctx context.Context, request providers.Request, emit func(providers.Chunk) error) error {
	if p == nil || p.deferredTaskProvider == nil {
		return &providers.Failure{Code: "context_rollover"}
	}
	p.mu.Lock()
	if p.poisoned != nil {
		err := p.poisoned
		p.mu.Unlock()
		return err
	}
	armed := p.armedBase
	if armed != nil {
		if p.armedProspective == nil || !requestPrefix(request, *p.armedProspective) ||
			!validRolloverGuidance(request.Messages[len(armed.Messages):]) {
			p.poisoned = &providers.Failure{Code: "context_rollover", Partial: true}
			err := p.poisoned
			p.mu.Unlock()
			return err
		}
	}
	p.mu.Unlock()
	provider, err := p.acquire(ctx)
	if err != nil {
		return err
	}
	if err = provider.Stream(ctx, request, emit); err != nil {
		if armed != nil {
			p.mu.Lock()
			p.poisoned = &providers.Failure{Code: "context_rollover", Partial: true}
			p.openErr = p.poisoned
			p.mu.Unlock()
		}
		return err
	}
	if armed != nil {
		p.mu.Lock()
		if p.armedBase != nil && reflect.DeepEqual(*p.armedBase, *armed) {
			p.armedBase, p.armedProspective = nil, nil
		}
		p.mu.Unlock()
	}
	return nil
}
