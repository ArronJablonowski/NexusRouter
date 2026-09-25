package providers

import (
	"context"
	"encoding/hex"
)

// TokenCounter counts the complete provider-rendered input, including tool and
// output schemas. supported must be false if rendering, tokenizer identity, or
// any request feature cannot be accounted for exactly. Implementations are
// trusted local host code and must honor cancellation without retaining input.
type TokenCounter func(context.Context, Request) (tokens int, supported bool, err error)

// BoundTokenCounter is an explicit host opt-in to replacing the serialized-byte
// floor for one model. Digests identify the host-verified tokenizer and renderer;
// the host must verify their contents and provider parity before construction.
// This is not a tokenizer implementation or a provider identity attestation.
// Unknown models/features retain the conservative floor. Errors fail closed.
type BoundTokenCounter struct {
	model           string
	tokenizerSHA256 string
	rendererSHA256  string
	count           TokenCounter
}

func NewBoundTokenCounter(model, tokenizerSHA256, rendererSHA256 string, count TokenCounter) (*BoundTokenCounter, error) {
	validDigest := func(s string) bool { b, err := hex.DecodeString(s); return err == nil && len(b) == 32 }
	if model == "" || count == nil || !validDigest(tokenizerSHA256) || !validDigest(rendererSHA256) {
		return nil, ErrContextEstimate
	}
	return &BoundTokenCounter{model, tokenizerSHA256, rendererSHA256, count}, nil
}

// Estimate is invoked on the bounded isolated snapshot by EstimateWith. Keep
// the established 1024-token framing/output reserve in addition to exact input.
func (c *BoundTokenCounter) Estimate(ctx context.Context, r Request) (int, error) {
	if c == nil || c.count == nil || ctx == nil || ctx.Err() != nil {
		return 0, ErrContextEstimate
	}
	baseline, err := EstimateContext(r)
	if err != nil {
		return 0, ErrContextEstimate
	}
	if r.Model != c.model {
		return baseline, nil
	}
	n, supported, err := c.count(ctx, r)
	if err != nil || ctx.Err() != nil {
		return 0, ErrContextEstimate
	}
	if !supported {
		return baseline, nil
	}
	if n < 1 || n > maxEstimateRequestBytes {
		return 0, ErrContextEstimate
	}
	return n + 1024, nil
}
