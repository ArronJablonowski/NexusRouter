package app

import (
	"context"
	"sync"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/memory"
	"github.com/ArronJablonowski/DarwinRouter/providers"
)

// Memory use means an admitted dispatch attempt, not proof of provider receipt.
// Revision checks fail closed: never send context known to have been replaced,
// deleted, or expired since selection. Legacy read-only stores remain supported.
func withMemoryUse(p providers.Provider, store memory.Store, selected *memoryContext) providers.Provider {
	use, ok := store.(memory.UseStore)
	if !ok || selected == nil || len(selected.Refs) == 0 {
		return p
	}
	return &memoryUseProvider{Provider: p, store: use, refs: append([]memory.Fact(nil), selected.Refs...)}
}

type memoryUseProvider struct {
	providers.Provider
	store memory.UseStore
	refs  []memory.Fact
	once  sync.Once
	err   error
}

func (p *memoryUseProvider) Stream(ctx context.Context, req providers.Request, emit func(providers.Chunk) error) error {
	p.once.Do(func() { p.err = touchSelectedMemory(ctx, p.store, p.refs) })
	if p.err != nil {
		return p.err
	}
	return p.Provider.Stream(ctx, req, emit)
}

func touchSelectedMemory(ctx context.Context, store memory.UseStore, refs []memory.Fact) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrAdmission
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	now := time.Now().UTC()
	for _, fact := range refs {
		if ctx.Err() != nil || store.TouchMemoryFact(ctx, fact, now) != nil || ctx.Err() != nil {
			return ErrAdmission
		}
	}
	return nil
}
