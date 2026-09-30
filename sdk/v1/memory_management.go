package v1

import (
	"context"
	"github.com/ArronJablonowski/NexusRouter/memory"
)

type MemoryFact = memory.Fact

var ErrMemoryConflict = memory.ErrConflict

// Memory and Memories inspect the configured scope without enabling retrieval.
// Credentials are redacted in factual text; identities are never rewritten.
func (c *Client) Memory(ctx context.Context, id string) (MemoryFact, error) {
	if !c.valid(ctx) {
		return MemoryFact{}, ErrAdmission
	}
	return c.service.Memory(ctx, id)
}
func (c *Client) Memories(ctx context.Context, after, contains string, limit int, includeExpired bool) ([]MemoryFact, error) {
	if !c.valid(ctx) {
		return nil, ErrAdmission
	}
	return c.service.Memories(ctx, after, contains, limit, includeExpired)
}

// PutMemory and DeleteMemory require configured scope and exact revision CAS.
// Built-in storage must already exist; these operations never invoke models.
func (c *Client) PutMemory(ctx context.Context, fact MemoryFact, expected int64) error {
	if !c.valid(ctx) {
		return ErrAdmission
	}
	return c.service.PutMemory(ctx, fact, expected)
}
func (c *Client) DeleteMemory(ctx context.Context, id string, expected int64) error {
	if !c.valid(ctx) {
		return ErrAdmission
	}
	return c.service.DeleteMemory(ctx, id, expected)
}
