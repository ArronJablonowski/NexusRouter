// Package app composes admission, providers and the durable runtime.
package app

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/ArronJablonowski/DarwinRouter/contextengine"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/memory"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
	"github.com/ArronJablonowski/DarwinRouter/skills"
	"github.com/ArronJablonowski/DarwinRouter/tools"
)

var ErrAdmission = errors.New("task admission failed")

type Request struct {
	toolExtension                   *tools.Extension
	toolReviewer                    tools.ApprovalReviewer
	toolPresenter                   tools.ApprovalPresenter
	providerFactory                 providers.Factory
	codexLauncher                   codexLaunch
	contextEstimator                providers.ContextEstimator
	contextEngine                   contextengine.Engine
	preparedContext                 *contextengine.Prepared
	delegatedParent                 string
	delegatedTools                  *delegateTools
	delegate                        delegateRunner
	memoryStore                     memory.Store
	skillStore                      skills.Store
	admissionContext                context.Context
	submissionID, submissionToken   string
	eventSink                       func(runtime.Event)
	textSink                        func(string)
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
	if r.delegatedParent != "" {
		r.toolExtension = nil
		r.toolReviewer = nil
		r.toolPresenter = nil
		// Children receive only explicit context and, optionally, a borrowed
		// read-only capability. Never grant memory, skills or recursion.
		s.Tools.Enabled, s.Memory.Enabled, s.Skills.Enabled = r.delegatedTools != nil, false, false
		s.Workers.DelegateModel = ""
		s.Workers.DelegateReadTools = false
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
	if r.contextEstimator != nil && model.ContextTokens < 1 {
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
	if (s.Tools.Enabled || len(r.toolExtension.Names()) > 0) && (model.Locality != "local" || model.ContextTokens == 0) {
		return result, ErrAdmission
	}
	if s.Workers.DelegateModel != "" && model.ContextTokens == 0 {
		return result, ErrAdmission
	}
	var registry *tools.Registry
	toolPolicy := applicationToolPolicy()
	if r.delegatedTools != nil {
		registry, toolPolicy = r.delegatedTools.Registry, r.delegatedTools.Policy
	} else if s.Tools.Enabled {
		var closeTools func()
		var err error
		registry, closeTools, err = readTools(s.Tools.ReadRoot)
		if err != nil {
			return result, err
		}
		defer closeTools()
	}
	if len(r.toolExtension.Names()) > 0 {
		if registry == nil {
			registry = &tools.Registry{}
		}
		if r.toolExtension.RegisterInto(registry) != nil {
			return result, ErrAdmission
		}
		toolPolicy.Rules = append(toolPolicy.Rules, r.toolExtension.Rules()...)
	}
	var provider config.Provider
	for _, p := range s.Providers {
		if p.ID == model.Provider {
			provider = p
			break
		}
	}
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
		sessionID = history.SessionID
	}
	if !r.memoryPrepared && (model.Locality == "local" || !s.Memory.LocalOnly) {
		r.memoryContext, err = loadMemoryContext(ctx, selectMemoryStore(r.memoryStore, db), s.Memory, model.Locality == "local", secrets, memoryTaskQuery(r))
		if err != nil {
			return result, ErrAdmission
		}
	}
	if r.memoryContext != nil {
		if model.ContextTokens < 1 || (r.memoryContext.LocalOnly && model.Locality != "local") {
			return result, ErrAdmission
		}
	}
	if !r.skillPrepared && (model.Locality == "local" || !s.Skills.LocalOnly) {
		r.skillContext, err = loadSkillContextFrom(ctx, r.skillStore, s.Skills, r.Domain, contextTools(s, r.toolExtension), secrets)
		if err != nil {
			return result, ErrAdmission
		}
	}
	if r.skillContext != nil {
		if model.ContextTokens < 1 || (r.skillContext.LocalOnly && model.Locality != "local") {
			return result, ErrAdmission
		}
	}
	messages, err = prepareTaskContext(ctx, &r, secrets)
	if err != nil {
		return result, ErrAdmission
	}
	if provider.Kind == "codex_app_server" && r.ContinueTaskID != "" && r.contextEngine == nil {
		// Credentials may have rotated since this history was recorded. Own and
		// scrub decoded fields before importing them into a new CLI session;
		// never rewrite the original journal or send raw saved context first.
		// Custom-engine assembly already scrubbed its frozen messages. Repeating
		// replacement here can change them if a secret overlaps the marker.
		messages, err = redactCodexHistoryMessages(messages, secrets)
		if err != nil {
			return result, ErrAdmission
		}
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
	if r.textSink != nil {
		j.textDelivery = &textDelivery{secrets: secrets, emit: r.textSink}
	}
	if s.Workers.DelegateModel != "" && r.delegate != nil {
		if registry == nil {
			registry = &tools.Registry{}
		}
		if err := registerDelegate(registry, db, redactingJournal{db: db, secrets: secrets, submissionID: r.submissionID, submissionToken: r.submissionToken}, s, result.TaskID, sessionID, r.submissionID, privacy == "local_only", r.delegate, toolPolicy); err != nil {
			return result, ErrAdmission
		}
	}
	p, closeProvider, err := openTaskProvider(ctx, s, provider, model, r, messages, privacy, key)
	if err != nil {
		return result, ErrAdmission
	}
	defer closeProvider()
	loop := runtime.Loop{ContextEstimator: r.contextEstimator, Provider: withMemoryUse(p, selectMemoryStore(r.memoryStore, db), r.memoryContext), Journal: j, Steering: db, ValidationText: func(text string) string { return redact(text, secrets) }}
	inference := providers.Request{Model: model.Model, Messages: messages}
	maxTurns := s.Runtime.MaxTurns
	if registry != nil {
		inference.Tools = registry.Catalog()
		executor := tools.Executor{Registry: registry, Policy: toolPolicy}
		if (r.toolReviewer != nil || r.toolPresenter != nil) && r.delegatedParent == "" {
			executor.Authority = newToolAuthority(db, r.toolReviewer, r.toolPresenter, secrets)
		}
		loop.Tools = executor
		if s.Tools.Enabled || len(r.toolExtension.Names()) > 0 {
			maxTurns = min(maxTurns, s.Tools.MaxTurns)
		}
	}
	parentID, maxOutput := r.ContinueTaskID, 1<<20
	if r.delegatedParent != "" {
		parentID, maxTurns, maxOutput = r.delegatedParent, 1, 64<<10
		if r.delegatedTools != nil {
			inference.Tools = []providers.Tool{readFileSpec()}
			maxTurns = min(s.Workers.DelegateMaxTurns, s.Tools.MaxTurns, s.Runtime.MaxTurns)
		}
	}
	var compaction *runtime.ContextCompaction
	if r.continuation != nil {
		compaction = r.continuation.Compaction
	}
	out, err := loop.Run(ctx, runtime.RunRequest{SubmissionID: r.submissionID, Compaction: compaction, Validation: r.Validation, RetryOfTaskID: r.retryOfTaskID, RequireText: true, Domain: r.Domain, Profile: r.Profile, Route: r.route, TaskID: result.TaskID, SessionID: sessionID, ProviderID: provider.ID, ParentTaskID: parentID, Privacy: privacy, Inference: inference, MaxTurns: maxTurns, MaxContextTokens: model.ContextTokens, MaxOutputBytes: maxOutput})
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
	textDelivery                  *textDelivery
}

func (j redactingJournal) Append(ctx context.Context, expected int64, e runtime.Event) error {
	return j.appendLeased(ctx, expected, e, "", "")
}

func (j redactingJournal) AppendLeased(ctx context.Context, expected int64, e runtime.Event, token, owner string) error {
	return j.appendLeased(ctx, expected, e, token, owner)
}

func (j redactingJournal) appendLeased(ctx context.Context, expected int64, e runtime.Event, token, owner string) error {
	rawText := e.Data.Text
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
	if token != "" {
		appendErr = j.db.AppendWorker(ctx, expected, e, token, owner, j.submissionID, j.submissionToken)
	} else if j.submissionID != "" {
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
	if j.textDelivery != nil {
		j.textDelivery.accept(e.Kind, rawText)
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
