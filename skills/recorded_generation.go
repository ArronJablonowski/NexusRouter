package skills

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

var ErrGenerationPersistence = errors.New("skills: generation persistence unavailable; inspect attempt before retrying")

// GenerationRecorder must durably claim each ID once. Duplicate Begin calls
// must fail even when identical; successful Finish retries may be idempotent.
// Implementations are trusted, cancellation-aware, concurrency-safe host code.
type GenerationRecorder interface {
	BeginSkillGeneration(context.Context, GenerationAttempt) error
	FinishSkillGeneration(context.Context, GenerationAttempt) error
}

// GenerateRecorded persists admission before model dispatch and a terminal
// proposal before returning it. It never publishes or activates a skill. On
// terminal persistence failure the returned started record is an observation,
// not permission to retry inference. The host must inspect its durable store.
// Provider naming, policy admission, redaction and resource reservations remain
// the host's responsibility. Reuse the same ID to prevent duplicate dispatch.
func (g ModelGenerator) GenerateRecorded(ctx context.Context, recorder GenerationRecorder, id, provider string, key Key, examples []WorkflowExample) (GenerationAttempt, error) {
	if ctx == nil || recorder == nil {
		return GenerationAttempt{}, ErrInvalid
	}
	if ctx.Err() != nil {
		return GenerationAttempt{}, ctx.Err()
	}
	owned, sessions, evidence, err := prepareWorkflows(key, examples)
	if err != nil {
		return GenerationAttempt{}, err
	}
	// Bind admitted content and configured generation parameters, not transient
	// timestamps. Transport policy and estimator identity must be pinned by host.
	body, err := json.Marshal(struct {
		Key                    Key
		Provider, Model        string
		ContextTokens          int
		Timeout                time.Duration
		EstimatedCost, MaxCost float64
		Examples               []WorkflowExample
	}{key, provider, g.Model, g.ContextTokens, g.Timeout, g.EstimatedCost, g.MaxCost, owned})
	if err != nil {
		return GenerationAttempt{}, ErrInvalid
	}
	digest := sha256.Sum256(body)
	started := GenerationAttempt{Version: 1, ID: id, Key: key, Model: g.Model, Provider: provider, InputDigest: hex.EncodeToString(digest[:]), SourceSessions: sessions, SourceEvidence: evidence, Status: "started", EstimatedCost: g.EstimatedCost, StartedAt: time.Now().UTC()}
	if started.Validate() != nil {
		return GenerationAttempt{}, ErrInvalid
	}
	if err := recordGeneration(ctx, recorder, started, true); err != nil {
		return GenerationAttempt{}, ErrGenerationPersistence
	}
	result, generateErr := g.GenerateDetailed(ctx, key, owned)
	terminal := started
	terminal.FinishedAt = time.Now().UTC()
	if terminal.FinishedAt.Before(started.StartedAt) {
		terminal.FinishedAt = started.StartedAt
	}
	if generateErr != nil {
		terminal.Status, terminal.Code = "failed", "generation_failed"
		if errors.Is(generateErr, context.Canceled) || errors.Is(generateErr, context.DeadlineExceeded) {
			terminal.Code = "canceled"
		}
	} else {
		terminal.Status, terminal.Result = "drafted", &result
	}
	// A canceled inference still needs a bounded terminal write. This never
	// resumes inference and does not turn an uncertain dispatch into a retry.
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := recordGeneration(cleanup, recorder, terminal, false); err != nil {
		return started, ErrGenerationPersistence
	}
	return terminal, generateErr
}

func recordGeneration(ctx context.Context, recorder GenerationRecorder, a GenerationAttempt, begin bool) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrGenerationPersistence
		}
	}()
	if a.Validate() != nil {
		return ErrInvalid
	}
	body, marshalErr := json.Marshal(a)
	if marshalErr != nil || len(body) > 512<<10 {
		return ErrInvalid
	}
	var owned GenerationAttempt
	if json.Unmarshal(body, &owned) != nil {
		return ErrInvalid
	}
	if begin {
		return recorder.BeginSkillGeneration(ctx, owned)
	}
	return recorder.FinishSkillGeneration(ctx, owned)
}
