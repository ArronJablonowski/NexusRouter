package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

var ErrLearningAttention = errors.New("skill learning requires operator attention")

// LearningStep advances one bounded durable phase. Generation claims and cost
// reservations remain in the existing immutable generation ledger, not this
// scheduling cursor. It never creates validation evidence or activates a skill.
func (s *Service) LearningStep(ctx context.Context) (out skills.LearningState, err error) {
	return s.learningStep(ctx, nil)
}

func (s *Service) learningStep(ctx context.Context, validation *learningValidation) (out skills.LearningState, err error) {
	defer func() {
		if recover() != nil {
			out = skills.LearningState{}
			err = ErrLearningAttention
		}
	}()
	if s == nil || ctx == nil || ctx.Err() != nil {
		return out, ErrLearningAttention
	}
	if !s.settings.Skills.Learning.Enabled {
		return out, nil
	}
	if s.settings.Validate() != nil || !s.settings.Skills.GenerationBudget.Enabled || s.skillStore != nil {
		return out, ErrLearningAttention
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	policy, err := s.learningPolicyForValidation(validation)
	if err != nil {
		return out, ErrLearningAttention
	}
	l := s.settings.Skills.Learning
	scope := s.settings.Skills.Scope
	if !selectionValueClean([]string{scope, l.Name, l.Domain, l.ModelID}, memorySecrets(s.settings, s.secret)) {
		return out, ErrLearningAttention
	}
	db, err := telemetry.OpenWorkflowScanControl(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return out, ErrLearningAttention
	}
	defer db.Close()
	current, err := db.LearningState(ctx, scope, l.Name)
	if errors.Is(err, sql.ErrNoRows) {
		current = skills.LearningState{Version: 1, Scope: scope, Name: l.Name, Domain: l.Domain, PolicyDigest: policy, Revision: 1, Phase: "discover"}
		if err = db.PutLearningState(ctx, current, 0); err != nil {
			return out, ErrLearningAttention
		}
		return current, nil
	}
	if err != nil || current.Validate() != nil || current.Scope != scope || current.Name != l.Name || current.PolicyDigest != policy || current.Domain != l.Domain {
		return out, ErrLearningAttention
	}
	if !selectionValueClean(current, memorySecrets(s.settings, s.secret)) {
		return out, ErrLearningAttention
	}
	next := current
	next.Revision++
	switch current.Phase {
	case "discover":
		page, e := s.AdvanceSkillWorkflowScan(ctx, l.Name, l.Domain, current.ScanRevision, l.ScanLimit)
		if e != nil {
			return current, ErrLearningAttention
		}
		next.ScanRevision, next.Epoch, next.Phase = page.Scan.Revision, page.Scan.Epoch, "consume"
	case "consume":
		receipt, e := s.ConsumeSkillWorkflowScan(ctx, l.Name, current.ConsumeRevision)
		if e != nil {
			return current, ErrLearningAttention
		}
		scan, e := s.SkillWorkflowScan(ctx, l.Name)
		if e != nil || scan.Revision != receipt.Revision || scan.Epoch != receipt.Epoch {
			return current, ErrLearningAttention
		}
		next.ConsumeRevision = receipt.Revision
		next.Phase = "discover"
		if scan.Complete {
			next.Phase = "generate"
		}
	case "generate":
		if current.PendingSelectionID != "" {
			completed, e := s.learningGeneration(ctx, db, current, validation)
			if e != nil {
				return current, e
			}
			if !completed {
				return current, nil
			}
			next.BucketAfter = current.PendingBucketID
			next.PendingBucketID, next.PendingSelectionID = "", ""
		} else {
			buckets, e := s.SkillWorkflowScanBuckets(ctx, l.Name, current.Epoch, current.BucketAfter, 1)
			if e != nil {
				return current, ErrLearningAttention
			}
			if len(buckets) == 0 {
				next.Phase = "discover"
				next.BucketAfter = ""
			} else {
				bucket := buckets[0]
				if len(bucket.Sources) < 2 {
					next.BucketAfter = bucket.ID
				} else {
					ids := make([]string, len(bucket.Sources))
					for i, source := range bucket.Sources {
						ids[i] = source.TaskID
					}
					selection, e := s.PlanGroupedWorkflowSelection(ctx, l.ModelID, skills.Key{Scope: scope, Name: bucket.ID}, ids, l.MaxCost)
					if e != nil {
						return current, ErrLearningAttention
					}
					if selection.Group != bucket.ID {
						return current, ErrLearningAttention
					}
					next.PendingBucketID, next.PendingSelectionID = bucket.ID, selection.ID
				}
			}
		}
	default:
		return current, ErrLearningAttention
	}
	if ctx.Err() != nil || !selectionValueClean(next, memorySecrets(s.settings, s.secret)) || db.PutLearningState(ctx, next, current.Revision) != nil {
		return current, ErrLearningAttention
	}
	return next, nil
}

func (s *Service) learningPolicy() (string, error) {
	if !selectionValueClean(s.settings, memorySecrets(s.settings, s.secret)) {
		return "", ErrLearningAttention
	}
	body, err := json.Marshal(s.settings)
	if err != nil || len(body) > 512<<10 {
		return "", ErrLearningAttention
	}
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:]), nil
}

// SkillLearningState remains inspectable after scheduling is disabled or its
// configuration changes. It never initializes storage or rewrites the cursor.
func (s *Service) SkillLearningState(ctx context.Context) (skills.LearningState, error) {
	if s == nil || ctx == nil || ctx.Err() != nil || !skillGenerationIdentifier.MatchString(s.settings.Skills.Scope) || !skillGenerationIdentifier.MatchString(s.settings.Skills.Learning.Name) {
		return skills.LearningState{}, ErrLearningAttention
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	db, err := telemetry.OpenReadOnly(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return skills.LearningState{}, ErrLearningAttention
	}
	defer db.Close()
	state, err := db.LearningState(ctx, s.settings.Skills.Scope, s.settings.Skills.Learning.Name)
	if err != nil || state.Validate() != nil || state.Scope != s.settings.Skills.Scope || state.Name != s.settings.Skills.Learning.Name || ctx.Err() != nil || !selectionValueClean(state, memorySecrets(s.settings, s.secret)) {
		return skills.LearningState{}, ErrLearningAttention
	}
	return state, nil
}

func (s *Service) learningGeneration(ctx context.Context, db *telemetry.Store, state skills.LearningState, validation *learningValidation) (bool, error) {
	attempt, err := db.SkillGenerationAttempt(ctx, state.PendingSelectionID)
	if errors.Is(err, sql.ErrNoRows) {
		// A durable intent proves this selection already reached the
		// drafted phase. Missing generation evidence is corruption, not
		// permission to dispatch that selection again, in either mode.
		_, intentErr := db.LearningActivationIntent(ctx, state.Scope, state.Name, state.PendingSelectionID)
		if !errors.Is(intentErr, sql.ErrNoRows) {
			return false, ErrLearningAttention
		}
		// The pinned selection survives a restart. Never recompute a different
		// attempt identity after a possibly dispatched generation.
		_, err = s.GenerateSkillSelection(ctx, state.PendingSelectionID, s.settings.Skills.Learning.MaxCost)
		if errors.Is(err, skills.ErrGenerationBudget) {
			return false, skills.ErrGenerationBudget
		}
		if err != nil {
			return false, ErrLearningAttention
		}
		// Publication is its own later phase; the cursor intentionally stays.
		return false, nil
	}
	if err != nil || attempt.Validate() != nil || attempt.Key.Scope != state.Scope || attempt.Key.Name != state.PendingBucketID || attempt.ID != state.PendingSelectionID || attempt.Status != "drafted" {
		return false, ErrLearningAttention
	}
	version, err := s.PublishSkillGeneration(ctx, state.PendingSelectionID)
	if err != nil {
		return false, ErrLearningAttention
	}
	if validation != nil {
		return s.learningActivate(ctx, db, state, version, validation)
	}
	return true, nil
}
