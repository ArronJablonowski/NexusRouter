package v1

import (
	"context"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

type SummaryAttempt = sessions.SummaryAttempt
type SummaryRecovery = sessions.SummaryRecovery
type SummaryReview = sessions.SummaryReview
type SummaryValidationInput = sessions.SummaryValidationInput
type SummaryValidationDecision = sessions.SummaryValidationDecision
type SummaryValidator = sessions.SummaryValidator
type SummaryValidatorFunc = sessions.SummaryValidatorFunc
type SummaryValidatorRegistry = sessions.SummaryValidatorRegistry
type SummaryIntegrityValidator = sessions.SummaryIntegrityValidator
type PrepareSummaryRequest = app.PrepareSummaryRequest
type SummaryPreparation = sessions.ContextCompactionOperationState

const SummaryIntegrityValidatorID = sessions.SummaryIntegrityValidatorID

// NewSummaryIntegrityValidator returns the conservative stock linter. It can
// reject mechanical integrity defects but always abstains otherwise, so it
// never grants continuation authority without a later operator review.
func NewSummaryIntegrityValidator() SummaryValidator {
	return sessions.NewSummaryIntegrityValidator()
}

// NewSummaryValidatorRegistry binds stable identities to trusted deterministic
// host validators. Changing validator semantics requires a new identity.
func NewSummaryValidatorRegistry(input map[string]SummaryValidator) (*SummaryValidatorRegistry, error) {
	registry, err := sessions.NewSummaryValidatorRegistry(input)
	if err != nil {
		return nil, ErrAdmission
	}
	return registry, nil
}

// SummarizeTask explicitly requests one bounded auxiliary draft. It never
// approves the proposal or applies it to a continuation automatically.
func (c *Client) SummarizeTask(ctx context.Context, task, modelID string, keep int, maxCost float64) (SummaryAttempt, error) {
	if !c.valid(ctx) {
		return SummaryAttempt{}, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return SummaryAttempt{}, err
	}
	return c.service.SummarizeTask(ctx, task, modelID, keep, maxCost)
}

// PrepareSummary starts or resumes one caller-keyed durable preparation. Exact
// retries return the committed terminal attempt without redispatching a model.
func (c *Client) PrepareSummary(ctx context.Context, idempotencyKey string, request PrepareSummaryRequest) (SummaryPreparation, error) {
	if !c.valid(ctx) {
		return SummaryPreparation{}, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return SummaryPreparation{}, err
	}
	return c.service.PrepareSummary(ctx, idempotencyKey, request)
}

// InspectSummaryPreparation reads one operation without creating storage or
// dispatching inference.
func (c *Client) InspectSummaryPreparation(ctx context.Context, operationID string) (SummaryPreparation, error) {
	if !c.valid(ctx) {
		return SummaryPreparation{}, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return SummaryPreparation{}, err
	}
	store, err := telemetry.OpenReadOnly(ctx, c.database)
	if err != nil {
		return SummaryPreparation{}, err
	}
	defer store.Close()
	return store.ContextCompactionPlan(ctx, operationID)
}

// ValidateSummary runs one trusted deterministic validator against an existing
// durable draft. operationID provides lost-ack idempotency. Approved evidence
// authorizes later use but does not itself start a continuation.
func (c *Client) ValidateSummary(ctx context.Context, attempt, expectedReview, operationID, validatorID string, registry *SummaryValidatorRegistry) (SummaryReview, error) {
	if !c.valid(ctx) {
		return SummaryReview{}, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return SummaryReview{}, err
	}
	return c.service.ValidateSummary(ctx, attempt, expectedReview, operationID, validatorID, registry)
}

// SummarizeTaskValidated is an opt-in draft-then-validate convenience. Draft
// generation remains single-use and is not automatically retried after an
// uncertain acknowledgement; use ValidateSummary to retry validation safely.
func (c *Client) SummarizeTaskValidated(ctx context.Context, task, modelID string, keep int, maxCost float64, validatorID string, registry *SummaryValidatorRegistry) (SummaryAttempt, SummaryReview, error) {
	if !c.valid(ctx) {
		return SummaryAttempt{}, SummaryReview{}, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return SummaryAttempt{}, SummaryReview{}, err
	}
	return c.service.SummarizeTaskValidated(ctx, task, modelID, keep, maxCost, validatorID, registry)
}

// InspectSummaryAttempt reads an existing proposal without creating storage,
// dispatching inference, or retaining a persistent database handle.
func (c *Client) InspectSummaryAttempt(ctx context.Context, id string) (SummaryAttempt, error) {
	if !c.valid(ctx) {
		return SummaryAttempt{}, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return SummaryAttempt{}, err
	}
	return app.InspectSummaryAttempt(ctx, c.database, id)
}

// ListSummaryAttempts reads one page from existing storage. The cursor is the
// prior attempt ID; inspection does not create storage or start background work.
func (c *Client) ListSummaryAttempts(ctx context.Context, task, after string, limit int) ([]SummaryAttempt, error) {
	if !c.valid(ctx) {
		return nil, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return app.ListSummaryAttempts(ctx, c.database, task, after, limit)
}

// ReconcileInterruptedSummaries performs one explicit bounded recovery page.
// It never redispatches a provider or grants approval to uncertain output.
func (c *Client) ReconcileInterruptedSummaries(ctx context.Context, after string, limit int) (string, int, error) {
	if !c.valid(ctx) {
		return "", 0, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	return app.ReconcileInterruptedSummaries(ctx, c.database, after, limit)
}

// InspectSummaryRecovery reads one safe interruption receipt without exposing
// process-lock metadata or generated output.
func (c *Client) InspectSummaryRecovery(ctx context.Context, attempt string) (SummaryRecovery, error) {
	if !c.valid(ctx) {
		return SummaryRecovery{}, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return SummaryRecovery{}, err
	}
	return app.InspectSummaryRecovery(ctx, c.database, attempt)
}

// ListSummaryRecoveries reads one lexical page of interruption receipts.
func (c *Client) ListSummaryRecoveries(ctx context.Context, task, after string, limit int) ([]SummaryRecovery, error) {
	if !c.valid(ctx) {
		return nil, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return app.ListSummaryRecoveries(ctx, c.database, task, after, limit)
}

// ReviewSummary records an explicit operator decision against the expected
// previous review ID. It is not an LLM judgment or automatic approval, and does
// not start a continuation. Hosts must authenticate the reviewing operator.
func (c *Client) ReviewSummary(ctx context.Context, attempt, expected, decision, note string) (SummaryReview, error) {
	if !c.valid(ctx) {
		return SummaryReview{}, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return SummaryReview{}, err
	}
	return c.service.ReviewSummary(ctx, attempt, expected, decision, note)
}

// SummaryReviewHistory reads recorded operator decisions without creating
// storage or retaining a persistent database handle.
func (c *Client) SummaryReviewHistory(ctx context.Context, attempt string) ([]SummaryReview, error) {
	if !c.valid(ctx) {
		return nil, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return app.SummaryReviewHistory(ctx, c.database, attempt)
}
