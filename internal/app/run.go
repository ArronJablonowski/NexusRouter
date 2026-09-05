// Package app composes admission, providers and the durable runtime.
package app

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/policy"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
	"github.com/ArronJablonowski/DarwinRouter/tools"
)

var ErrAdmission = errors.New("task admission failed")

type Request struct {
	admissionContext                context.Context
	submissionID, submissionToken   string
	eventSink                       func(runtime.Event)
	SummaryAttemptID                string
	Compaction                      *sessions.CompactionRequest
	continuation                    *continuationContext
	skillPrepared                   bool
	skillContext                    *skillContext
	memoryPrepared                  bool
	memoryContext                   *memoryContext
	Validation                      string
	onlyModelID, retryOfTaskID      string
	ModelID, Prompt, ContinueTaskID string
	Messages                        []providers.Message
	Domain, Profile                 string
	Capabilities                    []string
	ContextTokens                   int
	MaxCost                         float64
	LocalRequired                   bool
	route                           *runtime.Data
}
type Result struct {
	PreviousTaskIDs      []string
	retryable            bool
	retryLocalOnly       bool
	fallbackModelID      string
	reservedCost         float64
	AuditID, AuditStatus string
	TaskID, Text         string
	Turns                int
	FinishReason         string
	Usage                *providers.Usage
}

// runExplicitAdmitted MUST only be called after resource admission. Local
// dispatch retains its loopback-only transport even when cloud use is enabled.
func runExplicitAdmitted(ctx context.Context, s config.Settings, r Request, secret func(string) string) (Result, error) {
	result := Result{}
	if s.Validate() != nil || r.ModelID == "" || validateInput(r) != nil || s.Telemetry.OTEL {
		return result, ErrAdmission
	}
	var model config.Model
	found := false
	for _, m := range s.Models {
		if m.ID == r.ModelID {
			model = m
			found = true
			break
		}
	}
	if !found || (r.LocalRequired && model.Locality != "local") || (s.Mode == "local_only" && model.Locality != "local") || (s.Mode == "cloud_only" && model.Locality != "cloud") {
		return result, ErrAdmission
	}
	// Explicit selection bypasses ranking, not requested admission constraints.
	// Zero cost retains the legacy explicit-model operator override; automatic
	// routing instead interprets zero as a strict zero-cost ceiling.
	if r.ContextTokens > 0 && model.ContextTokens < r.ContextTokens {
		return result, ErrAdmission
	}
	if (r.Compaction != nil || r.SummaryAttemptID != "") && model.ContextTokens < 1 {
		return result, ErrAdmission
	}
	if r.MaxCost > 0 && (model.EstimatedCost == nil || *model.EstimatedCost > r.MaxCost) {
		return result, ErrAdmission
	}
	for _, required := range r.Capabilities {
		matched := false
		for _, capability := range model.Capabilities {
			matched = matched || capability == required
		}
		if !matched {
			return result, ErrAdmission
		}
	}
	// File tools are local-only until an explicit data-egress approval exists.
	if s.Tools.Enabled && (model.Locality != "local" || model.ContextTokens == 0) {
		return result, ErrAdmission
	}
	var registry *tools.Registry
	if s.Tools.Enabled {
		var closeTools func()
		var err error
		registry, closeTools, err = readTools(s.Tools.ReadRoot)
		if err != nil {
			return result, err
		}
		defer closeTools()
	}
	var provider config.Provider
	for _, p := range s.Providers {
		if p.ID == model.Provider {
			provider = p
			break
		}
	}
	tr, err := policy.NewTransport(s.Mode == "local_only" || model.Locality == "local", []string{provider.Endpoint})
	if err != nil {
		return result, ErrAdmission
	}
	defer tr.CloseIdleConnections()
	key := ""
	secrets := []string{}
	if secret != nil {
		if token := secret("DARWIN_API_TOKEN"); token != "" {
			secrets = append(secrets, token)
		}
	}
	for _, p := range s.Providers {
		if p.APIKeyEnv != "" && secret != nil {
			value := secret(p.APIKeyEnv)
			if value != "" {
				secrets = append(secrets, value)
			}
			if p.ID == provider.ID {
				key = value
			}
		}
	}
	if provider.APIKeyEnv != "" && key == "" {
		return result, ErrAdmission
	}
	p, err := providers.NewHTTP(provider.Endpoint, provider.Kind, key, tr)
	if err != nil {
		return result, ErrAdmission
	}
	db, err := telemetry.Open(ctx, s.Telemetry.Database)
	if err != nil {
		return result, errors.New("cannot open task storage")
	}
	defer db.Close()
	messages := []providers.Message{}
	sessionID := ""
	privacy := "cloud_allowed"
	if model.Locality == "local" {
		privacy = "local_only"
	}
	if r.ContinueTaskID != "" {
		if r.continuation == nil {
			r.continuation, err = loadContinuation(ctx, db, r, secrets)
			if err != nil {
				return result, err
			}
		}
		history := r.continuation
		// Unknown legacy privacy is never interpreted as cloud consent.
		if history.Privacy != "cloud_allowed" && model.Locality != "local" {
			return result, ErrAdmission
		}
		if history.Privacy != "cloud_allowed" {
			privacy = "local_only"
		}
		messages = history.Messages
		sessionID = history.SessionID
	}
	if !r.memoryPrepared && (model.Locality == "local" || !s.Memory.LocalOnly) {
		r.memoryContext, err = loadMemoryContext(ctx, db, s.Memory, model.Locality == "local", secrets)
		if err != nil {
			return result, ErrAdmission
		}
	}
	if r.memoryContext != nil {
		if model.ContextTokens < 1 || (r.memoryContext.LocalOnly && model.Locality != "local") {
			return result, ErrAdmission
		}
		messages = append(messages, r.memoryContext.Messages...)
	}
	if !r.skillPrepared && (model.Locality == "local" || !s.Skills.LocalOnly) {
		r.skillContext, err = loadSkillContext(ctx, s.Skills, r.Domain, contextTools(s), secrets)
		if err != nil {
			return result, ErrAdmission
		}
	}
	if r.skillContext != nil {
		if model.ContextTokens < 1 || (r.skillContext.LocalOnly && model.Locality != "local") {
			return result, ErrAdmission
		}
		messages = append(messages, r.skillContext.Messages...)
	}
	if len(r.Messages) > 0 {
		messages = append(messages, r.Messages...)
	} else {
		messages = append(messages, providers.Message{Role: "user", Content: r.Prompt})
	}
	encoded, err := json.Marshal(messages)
	if err != nil || len(encoded) > 4<<20 {
		return result, ErrAdmission
	}
	result.TaskID = rand.Text()
	ctx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	stopWatcher := watchCancellation(ctx, func(query context.Context) (bool, error) {
		return db.CancellationRequested(query, result.TaskID)
	}, cancelRun)
	watcherStopped := false
	defer func() {
		if !watcherStopped {
			_ = stopWatcher()
		}
	}()
	if sessionID == "" {
		sessionID = result.TaskID
	}
	j := redactingJournal{db: db, secrets: secrets, eventSink: r.eventSink, submissionID: r.submissionID, submissionToken: r.submissionToken}
	loop := runtime.Loop{Provider: p, Journal: j, Steering: db, ValidationText: func(text string) string { return redact(text, secrets) }}
	inference := providers.Request{Model: model.Model, Messages: messages}
	maxTurns := s.Runtime.MaxTurns
	if registry != nil {
		inference.Tools = registry.Catalog()
		loop.Tools = tools.Executor{Registry: registry, Policy: &tools.Policy{Default: tools.Deny, Rules: []tools.Rule{{Tool: "read_file", Scope: "workspace", Decision: tools.Allow}}}}
		maxTurns = min(maxTurns, s.Tools.MaxTurns)
	}
	var compaction *runtime.ContextCompaction
	if r.continuation != nil {
		compaction = r.continuation.Compaction
	}
	out, err := loop.Run(ctx, runtime.RunRequest{SubmissionID: r.submissionID, Compaction: compaction, Validation: r.Validation, RetryOfTaskID: r.retryOfTaskID, RequireText: true, Domain: r.Domain, Profile: r.Profile, Route: r.route, TaskID: result.TaskID, SessionID: sessionID, ProviderID: provider.ID, ParentTaskID: r.ContinueTaskID, Privacy: privacy, Inference: inference, MaxTurns: maxTurns, MaxContextTokens: model.ContextTokens, MaxOutputBytes: 1 << 20})
	watchErr := stopWatcher()
	watcherStopped = true
	if watchErr != nil {
		err = errors.Join(err, watchErr)
		out.Retryable = false
	}
	result.retryable = out.Retryable
	result.Text = redact(out.Text, secrets)
	result.Turns = out.Turns
	result.FinishReason = out.FinishReason
	result.Usage = out.Usage
	return result, err
}

type redactingJournal struct {
	submissionID, submissionToken string
	db                            *telemetry.Store
	secrets                       []string
	eventSink                     func(runtime.Event)
}

func (j redactingJournal) Append(ctx context.Context, expected int64, e runtime.Event) error {
	// Partial deltas can split a credential across records. Persist lifecycle
	// markers without delta text; the complete turn contains redacted text.
	if e.Kind == runtime.ModelDelta {
		e.Data.Text = ""
	}
	data, err := json.Marshal(e.Data)
	if err != nil {
		return err
	}
	var values any
	if json.Unmarshal(data, &values) != nil {
		return errors.New("cannot redact event")
	}
	var scrub func(any) any
	scrub = func(v any) any {
		switch x := v.(type) {
		case string:
			return redact(x, j.secrets)
		case []any:
			for i := range x {
				x[i] = scrub(x[i])
			}
		case map[string]any:
			for key, value := range x {
				x[key] = scrub(value)
			}
		}
		return v
	}
	data, err = json.Marshal(scrub(values))
	if err != nil {
		return err
	}
	var redacted runtime.Data
	if json.Unmarshal(data, &redacted) != nil {
		return errors.New("cannot redact event")
	}
	e.Data = redacted
	var appendErr error
	if j.submissionID != "" {
		appendErr = j.db.AppendSubmission(ctx, expected, e, j.submissionID, j.submissionToken)
	} else {
		appendErr = j.db.Append(ctx, expected, e)
	}
	if err := appendErr; err != nil {
		return err
	}
	if j.eventSink != nil {
		j.eventSink(e)
	}
	return nil
}
func redact(value string, secrets []string) string {
	ordered := append([]string(nil), secrets...)
	sort.Slice(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })
	for _, secret := range ordered {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[REDACTED]")
		}
	}
	return value
}
