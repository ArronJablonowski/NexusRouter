package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"slices"
	"time"

	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

var workflowToolID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// SkillWorkflowProcedures derives observations only from actual durable tool
// lifecycle events, never initial conversation claims. All reads share one
// transaction. Mixed routing domain/profile trajectories are conservatively
// ineligible; this is attribution metadata, not executable workflow authority.
func (s *Store) SkillWorkflowProcedures(ctx context.Context, tasks []string) (procedures []skills.WorkflowProcedure, err error) {
	if ctx == nil || s == nil || s.db == nil || len(tasks) < 1 || len(tasks) > 20 {
		return nil, errWorkflowSources
	}
	ids := append([]string(nil), tasks...)
	slices.Sort(ids)
	for i, id := range ids {
		if !workflowSourceID.MatchString(id) || (i > 0 && ids[i-1] == id) {
			return nil, errWorkflowSources
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	defer func() {
		if ctx.Err() != nil {
			procedures, err = nil, ctx.Err()
		} else if err != nil {
			procedures, err = nil, errWorkflowSources
		}
	}()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	procedures = make([]skills.WorkflowProcedure, 0, len(ids))
	budget := 256 << 10
	for _, task := range ids {
		remaining := budget
		source, e := workflowSource(ctx, tx, task, &remaining)
		if errors.Is(e, errWorkflowIneligible) {
			continue
		}
		if e != nil {
			return nil, e
		}
		p, e := workflowProcedure(ctx, tx, source)
		if errors.Is(e, errWorkflowIneligible) {
			continue
		}
		if e != nil {
			return nil, e
		}
		budget = remaining
		procedures = append(procedures, p)
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return procedures, nil
}

func workflowProcedure(ctx context.Context, tx *sql.Tx, source skills.WorkflowSource) (skills.WorkflowProcedure, error) {
	var events []runtime.Event
	if _, err := taskSnapshotWithEvents(ctx, tx, source.Example.TaskID, &events); err != nil {
		return skills.WorkflowProcedure{}, err
	}
	profile, tools, err := workflowObservedTools(events)
	if err != nil {
		return skills.WorkflowProcedure{}, err
	}
	p := skills.WorkflowProcedure{Version: 1, Profile: profile, Tools: tools, Candidate: skills.WorkflowCandidate{TaskID: source.Example.TaskID, SessionID: source.Example.SessionID, Domain: source.Example.Domain, Privacy: source.Privacy, EvaluationID: source.EvaluationID, EvaluationDigest: source.EvaluationDigest, SourceDigest: source.SourceDigest, SourceSequence: source.SourceSequence}}
	if p.Validate() != nil {
		return skills.WorkflowProcedure{}, errWorkflowSources
	}
	return p, nil
}

func workflowObservedTools(events []runtime.Event) (string, []string, error) {
	final, _, err := workflowFinalAttempt(events)
	if err != nil {
		return "", nil, err
	}
	domain, profile := "general", "default"
	tools := make([]string, 0)
	pending := map[string]runtime.Event{}
	for _, event := range events {
		switch event.Kind {
		case runtime.TaskStarted:
			domain, profile = event.Data.Domain, event.Data.Profile
			if domain == "" {
				domain = "general"
			}
			if profile == "" {
				profile = "default"
			}
		case runtime.RouteSelected:
			domain, profile = event.Data.Domain, event.Data.Profile
		case runtime.TurnStarted:
			if domain != final.Domain || profile != final.Profile {
				return "", nil, errWorkflowIneligible
			}
		case runtime.ToolStarted:
			if len(tools) >= 64 || !workflowToolID.MatchString(event.Data.ToolName) || event.Data.Code != "" {
				return "", nil, errWorkflowIneligible
			}
			if _, ok := pending[event.Data.ToolCallID]; ok {
				return "", nil, errWorkflowSources
			}
			pending[event.Data.ToolCallID] = event
			tools = append(tools, event.Data.ToolName)
		case runtime.ToolCompleted:
			start, ok := pending[event.Data.ToolCallID]
			if !ok || start.Data.ToolName != event.Data.ToolName || start.TurnID != event.TurnID || start.AttemptID != event.AttemptID {
				return "", nil, errWorkflowSources
			}
			if event.Data.Code != "" || event.Data.Effect == runtime.UncertainEffect {
				return "", nil, errWorkflowIneligible
			}
			delete(pending, event.Data.ToolCallID)
		}
	}
	if len(pending) != 0 {
		return "", nil, errWorkflowIneligible
	}
	return final.Profile, tools, nil
}
