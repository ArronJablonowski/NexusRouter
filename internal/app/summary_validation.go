package app

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

// ValidateSummary performs one explicitly requested trusted deterministic
// validation and appends its evidence as a review. The operation ID is the
// immutable review ID, allowing lost-ack retries without callback re-execution.
func (s *Service) ValidateSummary(ctx context.Context, attemptID, expectedReviewID, operationID, validatorID string, registry *sessions.SummaryValidatorRegistry) (sessions.SummaryReview, error) {
	bad := func() (sessions.SummaryReview, error) { return sessions.SummaryReview{}, ErrAdmission }
	if s == nil || ctx == nil || ctx.Err() != nil || !sessions.ValidEventPageID(attemptID) || !sessions.ValidEventPageID(operationID) || (expectedReviewID != "" && !sessions.ValidEventPageID(expectedReviewID)) {
		return bad()
	}
	validator, err := registry.Resolve(validatorID)
	if err != nil {
		return bad()
	}
	read, err := telemetry.OpenReadOnly(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return bad()
	}
	if existing, existingErr := read.SummaryReview(ctx, attemptID, operationID); existingErr == nil {
		existingAttempt, attemptErr := read.SummaryAttempt(ctx, attemptID)
		existingDigest := ""
		if attemptErr == nil && existingAttempt.Draft != nil {
			existingDigest, attemptErr = sessions.SummaryDraftDigest(*existingAttempt.Draft)
		}
		read.Close()
		if existing.Version != 2 || existing.ValidatorID != validatorID || existing.PreviousID != expectedReviewID || attemptErr != nil || existing.SourceSequence != existingAttempt.SourceSequence || existing.SourceDigest != existingAttempt.SourceDigest || existing.DraftDigest != existingDigest {
			return bad()
		}
		return existing, nil
	} else if !errors.Is(existingErr, sql.ErrNoRows) {
		read.Close()
		return bad()
	}
	attempt, err := read.SummaryAttempt(ctx, attemptID)
	if err != nil || attempt.Status != "drafted" || attempt.Draft == nil {
		read.Close()
		return bad()
	}
	source, err := sessions.Replay(ctx, read, attempt.TaskID)
	read.Close()
	draftDigest, digestErr := sessions.SummaryDraftDigest(*attempt.Draft)
	if err != nil || digestErr != nil || !summarySourceMatches(source, attempt) {
		return bad()
	}
	input := sessions.SummaryValidationInput{AttemptID: attempt.ID, TaskID: attempt.TaskID, SourceDigest: attempt.SourceDigest, DraftDigest: draftDigest, Source: source, Draft: *attempt.Draft}
	validationCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	decision, err := callSummaryValidator(validationCtx, validator, input)
	cancel()
	if err != nil || decision.Validate() != nil || ctx.Err() != nil {
		return bad()
	}
	// Re-read every binding after untrusted callback execution. A validator may
	// retain or mutate its input; neither grants authority over current storage.
	read, err = telemetry.OpenReadOnly(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return bad()
	}
	current, attemptErr := read.SummaryAttempt(ctx, attemptID)
	currentSource, sourceErr := sessions.Replay(ctx, read, attempt.TaskID)
	read.Close()
	currentDigest := ""
	if attemptErr == nil && current.Draft != nil {
		currentDigest, err = sessions.SummaryDraftDigest(*current.Draft)
	}
	if attemptErr != nil || sourceErr != nil || err != nil || currentDigest != draftDigest || !summarySourceMatches(currentSource, current) {
		return bad()
	}
	review := sessions.SummaryReview{Version: 2, ID: operationID, AttemptID: attemptID, PreviousID: expectedReviewID, Decision: decision.Decision, Note: redact(decision.Note, memorySecrets(s.settings, s.secret)), ValidatorID: validatorID, SourceSequence: attempt.SourceSequence, SourceDigest: attempt.SourceDigest, DraftDigest: draftDigest, Time: time.Now().UTC()}
	if review.Validate() != nil {
		return bad()
	}
	write, err := telemetry.Open(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return bad()
	}
	defer write.Close()
	if err := write.RecordSummaryReview(ctx, review); err != nil {
		return sessions.SummaryReview{}, err
	}
	return review, nil
}

// SummarizeTaskValidated is the opt-in composed path. A failed or abstaining
// validation leaves the durable draft inspectable but inactive.
func (s *Service) SummarizeTaskValidated(ctx context.Context, task, modelID string, keep int, maxCost float64, validatorID string, registry *sessions.SummaryValidatorRegistry) (sessions.SummaryAttempt, sessions.SummaryReview, error) {
	attempt, err := s.SummarizeTask(ctx, task, modelID, keep, maxCost)
	if err != nil {
		return attempt, sessions.SummaryReview{}, err
	}
	review, err := s.ValidateSummary(ctx, attempt.ID, "", rand.Text(), validatorID, registry)
	return attempt, review, err
}

func summarySourceMatches(source sessions.Snapshot, attempt sessions.SummaryAttempt) bool {
	if attempt.Draft == nil || source.TaskID != attempt.TaskID || source.Sequence != attempt.SourceSequence {
		return false
	}
	_, checkpoint, err := sessions.PrepareContinuation(source, attempt.Draft.Request)
	return err == nil && checkpoint.SourceDigest == attempt.SourceDigest
}

func callSummaryValidator(ctx context.Context, validator sessions.SummaryValidator, input sessions.SummaryValidationInput) (decision sessions.SummaryValidationDecision, err error) {
	defer func() {
		if recover() != nil {
			decision = sessions.SummaryValidationDecision{}
			err = ErrAdmission
		}
	}()
	return validator.ValidateSummary(ctx, input)
}
