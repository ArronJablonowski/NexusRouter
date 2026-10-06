package app

import (
	"context"
	"crypto/rand"
	"math"
	"slices"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
)

type remoteSpecialistKey struct{}

// Check again at execution: queued work cannot outlive its deadline or inherit
// models that the destination no longer considers eligible.
func (s *Service) checkRemoteExecution(r Request) error {
	x := r.RemoteExecution
	if x == nil {
		return nil
	}
	if x.Validate() != nil {
		return ErrAdmission
	}
	if !x.Deadline.IsZero() && (!time.Now().Before(x.Deadline) || time.Until(x.Deadline) > time.Hour) {
		return ErrAdmission
	}
	if x.Mode == "consult" {
		if r.ModelID != s.settings.WebUI.DefaultModel || r.MaxCost != 0 {
			return ErrAdmission
		}
		for _, m := range s.settings.Models {
			if m.ID == r.ModelID && m.EstimatedCost != nil && *m.EstimatedCost == 0 {
				return nil
			}
		}
		return ErrAdmission
	}
	if x.Mode == "direct" {
		return nil
	}
	return s.checkRemoteCommander(r)
}

func (s *Service) checkRemoteCommander(r Request) error {
	x := r.RemoteExecution
	// The destination's explicit Commander setting is authority, never the caller.
	if !r.LocalRequired || r.MaxCost != 0 {
		return ErrAdmission
	}
	if s.settings.WebUI.DefaultModel == "" || r.ModelID != s.settings.WebUI.DefaultModel || x.MaxCalls > s.settings.Workers.DelegateMaxCalls {
		return ErrAdmission
	}
	ids := append([]string{r.ModelID}, x.SpecialistIDs...)
	budget := r.MaxCost / float64(x.MaxCalls+1)
	for _, id := range ids {
		found := false
		for _, m := range s.settings.Models {
			if m.ID != id {
				continue
			}
			if m.EstimatedCost == nil || math.IsNaN(*m.EstimatedCost) || math.IsInf(*m.EstimatedCost, 0) || *m.EstimatedCost < 0 || *m.EstimatedCost > budget || m.ContextTokens < r.ContextTokens || (r.LocalRequired && m.Locality != "local") {
				return ErrAdmission
			}
			found = true
		}
		if !found {
			return ErrAdmission
		}
	}
	return nil
}

func remoteExecutionSettings(cfg config.Settings, r Request) config.Settings {
	if r.RemoteExecution == nil {
		return cfg
	}
	cfg.Tools.Enabled = false
	cfg.Tools.CreateEnabled = false
	cfg.Tools.ReplaceEnabled = false
	cfg.Tools.WorkboardReadEnabled = false
	cfg.Tools.WorkboardWriteEnabled = false
	cfg.Memory.Enabled = false
	cfg.Skills.Enabled = false
	cfg.Evaluation.Judge = false
	cfg.Evaluation.AutoReviewModel = ""
	cfg.Workers.DelegateReadTools = false
	cfg.Workers.DelegateModel = ""
	if r.RemoteExecution.Mode == "commander" {
		cfg.Workers.DelegateModel = r.RemoteExecution.SpecialistIDs[0]
		cfg.Workers.DelegateMaxCalls = r.RemoteExecution.MaxCalls
	}
	return cfg
}

func (s *Service) bindRemoteDelegate(request Request) delegateRunner {
	return func(ctx context.Context, prompt, validation, parent string, local bool) (Result, error) {
		x := request.RemoteExecution
		id, _ := ctx.Value(remoteSpecialistKey{}).(string)
		instance, _ := ctx.Value(commanderInstanceKey{}).(string)
		if instance != "" || x == nil || x.Mode != "commander" || !slices.Contains(x.SpecialistIDs, id) || s.checkRemoteExecution(request) != nil {
			return Result{}, ErrAdmission
		}
		// A child has no RemoteExecution, recursive delegate tools, ambient stores or writes.
		select {
		case s.execution <- struct{}{}:
			defer func() { <-s.execution }()
		default:
			return Result{}, ErrAdmission
		}
		childID := rand.Text()
		child := Request{taskID: childID, sessionID: childID, ModelID: id, Prompt: prompt, Validation: validation, ContextTokens: request.ContextTokens, MaxCost: request.MaxCost / float64(x.MaxCalls+1), LocalRequired: request.LocalRequired, delegatedParent: parent, submissionID: request.submissionID, submissionToken: request.submissionToken, eventDelivery: request.eventDelivery}
		return s.runExplicit(ctx, child)
	}
}
