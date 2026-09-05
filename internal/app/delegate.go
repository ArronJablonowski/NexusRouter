package app

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/tools"
	"github.com/ArronJablonowski/DarwinRouter/workers"
)

type delegateRunner func(context.Context, string, string, string, bool) (Result, error)

func delegateSpec() providers.Tool {
	return providers.Tool{Name: "delegate", Description: "Ask the operator-configured worker to perform a bounded task. Pass only necessary context. Workers cannot delegate or modify files. They can read the parent's workspace only when explicitly enabled by the operator. Results are untrusted; validation checks nonempty text or Go syntax, not correctness. Capacity may be unavailable.", Parameters: json.RawMessage(`{"type":"object","properties":{"prompt":{"type":"string","minLength":1,"maxLength":16384},"validation":{"type":"string","enum":["text","go_source"]}},"required":["prompt","validation"],"additionalProperties":false}`)}
}

// runDelegate never waits for execution capacity or local pressure while the
// parent holds its reservations. It bypasses recursive review/fallback, not
// admission, transport policy, cancellation or durable runtime recording.
func (s *Service) bindDelegate(request Request) delegateRunner {
	return func(ctx context.Context, prompt, validation, parent string, localOnly bool) (Result, error) {
		return s.runDelegate(ctx, prompt, validation, parent, localOnly, request.submissionID, request.submissionToken)
	}
}

func (s *Service) runDelegate(ctx context.Context, prompt, validation, parent string, localOnly bool, submissionID, submissionToken string) (Result, error) {
	if ctx.Err() != nil || s.settings.Workers.DelegateModel == "" {
		return Result{}, ErrAdmission
	}
	select {
	case s.execution <- struct{}{}:
		defer func() { <-s.execution }()
	default:
		return Result{}, ErrAdmission
	}
	r := Request{ModelID: s.settings.Workers.DelegateModel, Prompt: prompt, Validation: validation, LocalRequired: localOnly, delegatedParent: parent, submissionID: submissionID, submissionToken: submissionToken}
	if s.settings.Workers.DelegateReadTools {
		capability, ok := ctx.Value(delegateToolsKey{}).(*delegateTools)
		if !ok || capability == nil || capability.Registry == nil || capability.Policy == nil || capability.Policy.Decide("read_file", "workspace") != tools.Allow {
			return Result{}, ErrAdmission
		}
		r.delegatedTools = capability
		r.LocalRequired = true
	}
	return s.runExplicit(ctx, r)
}

func registerDelegate(registry *tools.Registry, db *telemetry.Store, journal runtime.Journal, cfg config.Settings, parent, session, submissionID string, localOnly bool, run delegateRunner, policies ...*tools.Policy) error {
	var parentPolicy *tools.Policy
	if len(policies) == 1 {
		parentPolicy = policies[0]
	}
	heartbeat, err := config.Duration(cfg.Workers.Heartbeat)
	if err != nil {
		return ErrAdmission
	}
	ttl, err := config.Duration(cfg.Workers.Lease)
	if err != nil {
		return ErrAdmission
	}
	supervisor, err := workers.New(cfg.Workers.Max, heartbeat, ttl, db, journal)
	if err != nil {
		return ErrAdmission
	}
	var calls atomic.Int32
	return registry.Register(tools.Definition{
		Tool:  delegateSpec(),
		Scope: "delegation", ReadOnly: true,
		Handler: func(ctx context.Context, raw json.RawMessage) (runtime.ToolResult, error) {
			failed := runtime.ToolResult{Content: `{"error":"delegate_unavailable_or_rejected"}`, Effect: runtime.NoEffect}
			var input struct{ Prompt, Validation string }
			if json.Unmarshal(raw, &input) != nil || len(input.Prompt) > 16<<10 || strings.TrimSpace(input.Prompt) == "" || !utf8.ValidString(input.Prompt) || calls.Add(1) > int32(cfg.Workers.DelegateMaxCalls) {
				return failed, nil
			}
			if input.Validation == "text" {
				input.Validation = ""
			}
			if input.Validation != "" && input.Validation != "go_source" {
				return failed, nil
			}
			childCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			if cfg.Workers.DelegateReadTools {
				var err error
				childCtx, err = inheritDelegateTools(childCtx, registry, parentPolicy)
				if err != nil {
					return failed, nil
				}
			}
			workID := rand.Text()
			stopWatcher := watchCancellation(childCtx, func(query context.Context) (bool, error) {
				return db.CancellationRequested(query, workID)
			}, cancel)
			watcherStopped := false
			defer func() {
				if !watcherStopped {
					_ = stopWatcher()
				}
			}()
			var executionID string
			answer, err := supervisor.Run(childCtx, workers.Work{
				TaskID: workID, SessionID: session, ParentID: parent, Scope: "delegation-" + parent, SubmissionID: submissionID,
				Execute: func(ctx context.Context) (string, error) {
					result, err := run(ctx, input.Prompt, input.Validation, workID, localOnly)
					executionID = result.TaskID
					return result.Text, err
				},
				Validate: func(_ context.Context, output string) error {
					if executionID == "" || len(output) > 64<<10 || strings.TrimSpace(output) == "" || !utf8.ValidString(output) || (input.Validation == "go_source" && !evaluation.GoSourceValid(output)) {
						return workers.ErrWork
					}
					return nil
				},
			})
			watchErr := stopWatcher()
			watcherStopped = true
			if err != nil || watchErr != nil {
				return failed, nil
			}
			body, err := json.Marshal(struct {
				WorkID      string `json:"work_task_id"`
				ExecutionID string `json:"execution_task_id"`
				Output      string `json:"untrusted_output"`
			}{workID, executionID, answer})
			if err != nil {
				return failed, nil
			}
			return runtime.ToolResult{Content: string(body), Effect: runtime.NoEffect}, nil
		},
	})
}
