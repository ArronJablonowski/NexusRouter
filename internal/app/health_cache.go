package app

import (
	"context"
	"errors"
	"sync"
	"time"
)

type healthEntry struct {
	names   []string
	expires time.Time
}

// modelHealthCache is a short-lived discovery hint, not execution admission.
// It coalesces concurrent discovery calls; cancellation/errors are never cached.
// Credential/endpoint/privacy identities are hashed by the caller.
type modelHealthCache struct {
	mu         sync.Mutex
	entries    map[string]healthEntry
	inflight   map[string]chan struct{}
	generation uint64
	now        func() time.Time
}

func newHealthCache() *modelHealthCache {
	return &modelHealthCache{entries: map[string]healthEntry{}, inflight: map[string]chan struct{}{}, now: time.Now}
}

func (c *modelHealthCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = map[string]healthEntry{}
	c.generation++
}

func (c *modelHealthCache) models(ctx context.Context, key string, fetch func(context.Context) ([]string, error)) ([]string, error) {
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		c.mu.Lock()
		now := c.now()
		if entry, ok := c.entries[key]; ok && now.Before(entry.expires) {
			out := append([]string(nil), entry.names...)
			c.mu.Unlock()
			return out, nil
		}
		if wait, ok := c.inflight[key]; ok {
			c.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		wait := make(chan struct{})
		c.inflight[key] = wait
		generation := c.generation
		c.mu.Unlock()
		names, err := fetchModels(ctx, fetch)
		c.mu.Lock()
		if err == nil && ctx.Err() == nil && generation == c.generation {
			for id, entry := range c.entries {
				if !c.now().Before(entry.expires) {
					delete(c.entries, id)
				}
			}
			if len(c.entries) >= 128 {
				c.entries = map[string]healthEntry{}
			}
			c.entries[key] = healthEntry{append([]string(nil), names...), c.now().Add(5 * time.Second)}
		}
		delete(c.inflight, key)
		close(wait)
		c.mu.Unlock()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return names, err
	}
}

func fetchModels(ctx context.Context, fetch func(context.Context) ([]string, error)) (names []string, err error) {
	defer func() {
		if recover() != nil {
			names = nil
			err = errors.New("model discovery failed")
		}
	}()
	return fetch(ctx)
}
