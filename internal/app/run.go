// Package app composes admission, providers and the durable runtime.
package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/ArronJablonowski/NexusRouter/contextengine"
	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/memory"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
	"github.com/ArronJablonowski/NexusRouter/skills"
	"github.com/ArronJablonowski/NexusRouter/tools"
)

var ErrAdmission = errors.New("task admission failed")

type Request struct {
	HarnessID                       string `json:"harness_id,omitempty"`
	nativeHarness                   *NativeHarness
	openTaskStore                   func(context.Context) (*telemetry.Store, func(), error)
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
	delegateAudit                   delegateAuditRunner
	memoryStore                     memory.Store
	skillStore                      skills.Store
	admissionContext                context.Context
	submissionID, submissionToken   string
	eventSink                       func(runtime.Event)
	eventDelivery                   *eventDelivery
	deliverPerCall                  bool
	textSink                        func(string)
	presentationTextSink            PresentationTextSink
	SummaryAttemptID                string
	Compaction                      *sessions.CompactionRequest
	approvedCompaction              *runtime.ApprovedCompaction
	compactionPlan                  *runtime.ContextCompactionPlan
	delegationCompactionPolicy      *delegationCompactionPolicy
	continuation                    *continuationContext
	skillPrepared                   bool
	skillContext                    *skillContext
	memoryPrepared                  bool
	memoryContext                   *memoryContext
	autoCompactionTried             bool
	runtimeHostAdmission            *runtimeHostAdmission
	Validation                      string
	onlyModelID, retryOfTaskID      string
	intentPrepared                  bool
	domainExplicit                  bool
	capabilitiesExplicit            bool
	intentAmbiguous                 bool
	intentClassification            *intentClassificationState
	intentClassificationUse         *runtime.IntentClassificationUse
	intentClassificationCharged     bool
	taskID, sessionID               string
	ModelID, Prompt, ContinueTaskID string
	Messages                        []providers.Message
	Domain, Profile                 string
	Capabilities                    []string
	ContextTokens                   int
	MaxCost                         float64
	LocalRequired                   bool
	route                           *runtime.Data
}

// runtimeHostAdmission binds one app execution to identities allocated by a
// trusted enclosing host. The callback owns the first durable append so it can
// atomically commit that exact redacted TaskStarted event with host state.
// Copies share state and therefore cannot reuse the one-shot callback.
type runtimeHostAdmission struct {
	taskID, sessionID, parentTaskID, workerID string
	maxOutputTokens                           int64
	store                                     *telemetry.Store
	commitFirst                               func(context.Context, runtime.Event) error
	state                                     *runtimeHostAdmissionState
}

type runtimeHostAdmissionState struct {
	mu        sync.Mutex
	attempted bool
	committed bool
}

// withRuntimeHostAdmission is intentionally package-private: runtime identity
// is trusted host configuration, never a public Request input.
func withRuntimeHostAdmission(r Request, admission runtimeHostAdmission) (Request, error) {
	if r.runtimeHostAdmission != nil || admission.state != nil || admission.store == nil || admission.commitFirst == nil ||
		!sessions.ValidEventPageID(admission.taskID) || !sessions.ValidEventPageID(admission.sessionID) ||
		(admission.parentTaskID != "" && !sessions.ValidEventPageID(admission.parentTaskID)) ||
		!validRuntimeHostWorkerID(admission.workerID) || admission.maxOutputTokens < 0 || admission.maxOutputTokens > providers.MaxOutputTokens ||
		invalidRuntimeHostRequestState(r) {
		return Request{}, ErrAdmission
	}
	admission.state = &runtimeHostAdmissionState{}
	r.runtimeHostAdmission = &admission
	return r, nil
}

func invalidRuntimeHostRequestState(r Request) bool {
	return r.submissionID != "" || r.delegatedParent != "" || r.ContinueTaskID != "" || r.Compaction != nil ||
		r.SummaryAttemptID != "" || r.approvedCompaction != nil || r.compactionPlan != nil || r.delegationCompactionPolicy != nil || r.continuation != nil || r.retryOfTaskID != "" ||
		r.onlyModelID != "" || r.autoCompactionTried || r.intentClassification != nil || r.intentClassificationUse != nil || r.intentClassificationCharged || r.taskID != "" || r.sessionID != ""
}

func validRuntimeHostWorkerID(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, c := range value {
		if c < 0x21 || c > 0x7e {
			return false
		}
	}
	return true
}

type Result struct {
	HarnessOutcome       *harness.Execution
	PreviousTaskIDs      []string
	RouteEstimatedCost   *float64
	retryable            bool
	fallbackModelIDs     []string
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
	configID, configErr := settingsConfigID(s)
	if configErr != nil {
		return result, ErrAdmission
	}
	if r.runtimeHostAdmission != nil {
		// A host-bound Workboard execution is an effect-free child capability.
		// It receives only the explicit frozen card context: no recursive
		// delegation, ambient memory or skills, or process-wide tool registry.
		// Keep this enforcement at the final provider boundary so an internal
		// caller cannot regain authority by pre-populating Request fields.
		r.delegate, r.delegateAudit = nil, nil
		r.delegatedTools, r.toolExtension = nil, nil
		r.toolReviewer, r.toolPresenter = nil, nil
		r.memoryContext, r.skillContext = nil, nil
		r.memoryPrepared, r.skillPrepared = true, true
		s.Tools.Enabled, s.Tools.CreateEnabled, s.Tools.ReplaceEnabled = false, false, false
		s.Tools.WorkboardReadEnabled, s.Tools.WorkboardWriteEnabled = false, false
		s.Memory.Enabled, s.Skills.Enabled = false, false
		s.Workers.DelegateModel = ""
		s.Workers.DelegateReadTools = false
	}
	var classifyErr error
	r, classifyErr = classifyRequestIntent(r)
	if classifyErr != nil {
		return result, classifyErr
	}
	if s.Validate() != nil || r.ModelID == "" || validateInput(r) != nil || validateRuntimeHostStore(ctx, s, r) != nil {
		return result, ErrAdmission
	}
	if r.delegatedParent != "" {
		r.toolExtension = nil
		r.toolReviewer = nil
		r.toolPresenter = nil
		// Children receive only explicit context and, optionally, a borrowed
		// read-only capability. Never grant memory, skills or recursion.
		s.Tools.Enabled, s.Tools.WorkboardReadEnabled, s.Tools.WorkboardWriteEnabled, s.Memory.Enabled, s.Skills.Enabled = r.delegatedTools != nil, false, false, false, false
		s.Tools.CreateEnabled = false
		s.Tools.ReplaceEnabled = false
		s.Workers.DelegateModel = ""
		s.Workers.DelegateReadTools = false
	}
	if (s.Tools.CreateEnabled || s.Tools.ReplaceEnabled || s.Tools.WorkboardWriteEnabled) && r.toolReviewer == nil && r.toolPresenter == nil {
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
	// Direct file tools are local-only. A cloud coordinator may hold only the
	// delegate tool while an independently scoped local worker borrows read_file.
	// Allowing read tools is optional capability, not a requirement to use them.
	// Models without a known context bound and ordinary cloud models receive no
	// file tools. Explicit writing/delegated-read authority must still fail closed.
	if s.Tools.Enabled && (model.ContextTokens == 0 || model.Locality != "local" && !cloudDelegatedReads(s, r, model)) {
		if s.Tools.CreateEnabled || s.Tools.ReplaceEnabled || s.Workers.DelegateReadTools {
			return result, ErrAdmission
		}
		s.Tools.Enabled = false
	}
	toolingEnabled := s.Tools.Enabled || s.Tools.WorkboardReadEnabled || len(r.toolExtension.Names()) > 0
	if toolingEnabled && (model.ContextTokens == 0 || model.Locality != "local" && !cloudDelegatedReads(s, r, model)) {
		return result, ErrAdmission
	}
	if s.Workers.DelegateModel != "" && model.ContextTokens == 0 {
		return result, ErrAdmission
	}
	var registry *tools.Registry
	var delegatedReadCapability *delegateTools
	toolPolicy := applicationToolPolicyFor(s.Security.ToolPolicy)
	if r.delegatedTools != nil {
		registry, toolPolicy = r.delegatedTools.Registry, r.delegatedTools.Policy
	} else if s.Tools.Enabled {
		var closeTools func()
		var err error
		var readRegistry *tools.Registry
		if cloudDelegatedReads(s, r, model) {
			readRegistry, closeTools, err = delegatedCountTools(s.Tools.ReadRoot)
		} else {
			readRegistry, closeTools, err = readTools(s.Tools.ReadRoot)
		}
		if err != nil {
			return result, err
		}
		defer closeTools()
		if cloudDelegatedReads(s, r, model) {
			delegatedReadCapability = &delegateTools{Registry: readRegistry, Policy: toolPolicy}
		} else {
			registry = readRegistry
			if s.Workers.DelegateReadTools {
				delegatedReadCapability = &delegateTools{Registry: readRegistry, Policy: toolPolicy}
			}
		}
	}
	if s.Tools.CreateEnabled {
		closeCreate, scope, err := registerCreateTool(registry, s.Tools.CreateRoot)
		if err != nil {
			return result, err
		}
		defer closeCreate()
		toolPolicy.Rules = append(toolPolicy.Rules, tools.Rule{Tool: "create_file", Scope: scope, Decision: tools.Ask})
	}
	if s.Tools.ReplaceEnabled {
		closeReplace, scope, err := registerReplaceTool(registry, s.Tools.ReplaceRoot)
		if err != nil {
			return result, err
		}
		defer closeReplace()
		toolPolicy.Rules = append(toolPolicy.Rules, tools.Rule{Tool: "replace_file", Scope: scope, Decision: tools.Ask})
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
		for _, name := range []string{"NEXUS_API_TOKEN", "DARWIN_API_TOKEN"} {
			if token := secret(name); token != "" {
				secrets = append(secrets, token)
			}
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
	for _, name := range s.Security.RedactEnv {
		if secret != nil {
			if value := secret(name); value != "" {
				secrets = append(secrets, value)
			}
		}
	}
	if provider.APIKeyEnv != "" && key == "" {
		return result, ErrAdmission
	}
	if value := metricsExportSecret(s, secret); value != "" {
		secrets = append(secrets, value)
	}
	var (
		db  *telemetry.Store
		err error
	)
	if r.runtimeHostAdmission != nil {
		db = r.runtimeHostAdmission.store
		if _, err := db.WorkspaceIdentity(ctx); err != nil {
			return result, ErrAdmission
		}
	} else {
		var release func()
		if r.openTaskStore != nil {
			db, release, err = r.openTaskStore(ctx)
		} else {
			db, err = telemetry.Open(ctx, s.Telemetry.Database)
			release = func() {
				if db != nil {
					_ = db.Close()
				}
			}
		}
		if err != nil {
			return result, errors.New("cannot open task storage")
		}
		defer release()
	}
	if s.Tools.WorkboardReadEnabled {
		if registry == nil {
			registry = &tools.Registry{}
		}
		if registerWorkboardReadTools(registry, db) != nil {
			return result, ErrAdmission
		}
	}
	if s.Tools.WorkboardWriteEnabled {
		decomposition, policyErr := configuredWorkboardDecompositionPolicy(s, configID)
		bridge, bridgeErr := NewWorkboardBridgeWithDecomposition(db, db, defaultWorkboardNow, decomposition)
		if policyErr != nil || bridgeErr != nil || registerWorkboardMutationTools(registry, bridge) != nil || registerWorkboardAgentProposalTools(registry, db) != nil {
			return result, ErrAdmission
		}
	}
	messages := []providers.Message{}
	sessionID := r.sessionID
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
		if sessionID != "" && sessionID != history.SessionID {
			return result, ErrAdmission
		}
		sessionID = history.SessionID
		if provider.Kind == "codex_app_server" && history.Compaction != nil {
			// Inspect original retained tool arguments before any context engine
			// or ordinary map-based redaction could collapse duplicate keys.
			// The returned projection is discarded; assembly still owns its
			// single redaction pass and the journal keeps canonical provenance.
			if _, err := nativeSummaryMessages(history.Messages, secrets); err != nil {
				return result, ErrAdmission
			}
		}
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
	encoded, err := json.Marshal(messages)
	if err != nil || len(encoded) > 4<<20 {
		return result, ErrAdmission
	}
	result.TaskID = r.taskID
	if result.TaskID == "" {
		result.TaskID = rand.Text()
	}
	if r.runtimeHostAdmission != nil {
		result.TaskID = r.runtimeHostAdmission.taskID
	}
	if model.EstimatedCost != nil {
		cost := *model.EstimatedCost
		result.RouteEstimatedCost = &cost
		result.reservedCost = cost
	}
	ctx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	cancellationBoundary := &sync.Mutex{}
	stopWatcher := watchCancellation(ctx, func(query context.Context) (bool, error) {
		return db.CancellationRequested(query, result.TaskID)
	}, cancelRun, cancellationBoundary)
	watcherStopped := false
	defer func() {
		if !watcherStopped {
			_ = stopWatcher()
		}
	}()
	if sessionID == "" {
		sessionID = result.TaskID
	}
	if r.runtimeHostAdmission != nil {
		sessionID = r.runtimeHostAdmission.sessionID
	}
	j := redactingJournal{db: db, secrets: secrets, eventSink: r.eventSink, eventDelivery: r.eventDelivery, deliverPerCall: r.deliverPerCall, submissionID: r.submissionID, submissionToken: r.submissionToken, runtimeHostAdmission: r.runtimeHostAdmission, cancellationBoundary: cancellationBoundary}
	if r.textSink != nil || r.presentationTextSink != nil {
		finalOnly := responseContract(r).Active()
		j.textDelivery = &textDelivery{secrets: secrets, finalOnly: finalOnly, emit: func(text string, final bool) {
			if !final || finalOnly {
				deliverPresentationText(r.presentationTextSink, result.TaskID, sessionID, text)
			}
			if r.textSink != nil {
				r.textSink(text)
			}
		}}
	}
	if s.Workers.DelegateModel != "" && r.delegate != nil {
		if registry == nil {
			registry = &tools.Registry{}
		}
		compactionAuthority, authorityErr := sealDelegationCompactionAuthority(result.TaskID, r.compactionPlan, r.delegationCompactionPolicy)
		if authorityErr != nil {
			return result, authorityErr
		}
		if err := registerDelegate(registry, delegatedReadCapability, db, redactingJournal{db: db, secrets: secrets, eventDelivery: r.eventDelivery, submissionID: r.submissionID, submissionToken: r.submissionToken, cancellationBoundary: cancellationBoundary}, s, result.TaskID, sessionID, r.submissionID, privacy == "local_only", r.delegate, r.delegateAudit, compactionAuthority); err != nil {
			return result, ErrAdmission
		}
	}
	if r.HarnessID != "" {
		nativeResult, nativeErr := runNativeAdmitted(ctx, s, r, provider, model, key, messages, j, result, sessionID, secrets)
		watchErr := stopWatcher()
		watcherStopped = true
		if watchErr != nil {
			nativeErr = errors.Join(nativeErr, watchErr)
			nativeResult.Text = ""
			nativeResult.HarnessOutcome = nil
		}
		return nativeResult, nativeErr
	}
	providerOpen, providerAdmissionCleanup, err := prepareTaskProvider(s, provider, model, r, messages, privacy, key, providers.PurposeExecution)
	if err != nil {
		return result, ErrAdmission
	}
	defer providerAdmissionCleanup()
	deferredProvider := newDeferredTaskProvider(func(openCtx context.Context) (providers.Provider, func(), error) {
		adapter, cleanup, openErr := providerOpen(openCtx)
		if openErr != nil {
			return nil, cleanup, openErr
		}
		return withMemoryUse(adapter, selectMemoryStore(r.memoryStore, db), r.memoryContext), cleanup, nil
	})
	defer deferredProvider.Close()
	var executionProvider providers.Provider = deferredProvider
	requireContextRollover := provider.Kind == "codex_app_server" && r.compactionPlan != nil
	if requireContextRollover {
		executionProvider = newContextRolloverTaskProvider(deferredProvider)
	}
	loop := runtime.Loop{ContextEstimator: r.contextEstimator, Provider: executionProvider, Journal: j, Steering: db, ValidationText: func(text string) string { return redact(text, secrets) }}
	contextTokens := model.WorkingContextTokens()
	if r.ContextTokens > 0 {
		contextTokens = r.ContextTokens
	}
	inference := providers.Request{Model: model.Model, Messages: messages, ContextTokens: int64(contextTokens)}
	maxTurns := s.Runtime.MaxTurns
	if registry != nil {
		inference.Tools = registry.Catalog()
		executor := tools.Executor{Registry: registry, Policy: toolPolicy, Reader: newToolAuthority(db, nil, nil, secrets)}
		if (r.toolReviewer != nil || r.toolPresenter != nil) && r.delegatedParent == "" {
			executor.Authority = newToolAuthority(db, r.toolReviewer, r.toolPresenter, secrets)
		}
		loop.Tools = executor
		if s.Tools.Enabled || s.Tools.WorkboardReadEnabled || len(r.toolExtension.Names()) > 0 {
			maxTurns = min(maxTurns, s.Tools.MaxTurns)
		}
	}
	parentID, maxOutput := r.ContinueTaskID, 1<<20
	if r.delegatedParent != "" {
		parentID, maxTurns, maxOutput = r.delegatedParent, 1, 64<<10
		if r.delegatedTools != nil {
			inference.Tools = delegatedReadCatalog(r.delegatedTools.Registry)
			if len(inference.Tools) != 1 {
				return result, ErrAdmission
			}
			maxTurns = min(s.Workers.DelegateMaxTurns, s.Tools.MaxTurns, s.Runtime.MaxTurns)
		}
	}
	workerID := ""
	if r.runtimeHostAdmission != nil {
		parentID = r.runtimeHostAdmission.parentTaskID
		workerID = r.runtimeHostAdmission.workerID
		inference.MaxOutputTokens = r.runtimeHostAdmission.maxOutputTokens
	}
	var compaction *runtime.ContextCompaction
	var contextLineage *runtime.ContextLineage
	if r.continuation != nil {
		compaction = r.continuation.Compaction
		contextLineage = r.continuation.ContextLineage
	}
	out, err := loop.Run(ctx, runtime.RunRequest{ResponseInstructions: responseInstructions(r), SkillContext: freshSkillContextUse(r.skillContext), IntentClassification: r.intentClassificationUse, SubmissionID: r.submissionID, WorkerID: workerID, Compaction: compaction, ApprovedCompaction: r.approvedCompaction, CompactionPlan: r.compactionPlan, RequireContextRollover: requireContextRollover, ContextLineage: contextLineage, Validation: r.Validation, RetryOfTaskID: r.retryOfTaskID, ConfigID: configID, RouteEstimatedCost: result.RouteEstimatedCost, RequireText: true, Domain: r.Domain, Profile: r.Profile, Capabilities: r.Capabilities, Route: r.route, TaskID: result.TaskID, SessionID: sessionID, ProviderID: provider.ID, ParentTaskID: parentID, Privacy: privacy, Inference: inference, MaxTurns: maxTurns, MaxContextTokens: model.ContextTokens, MaxOutputBytes: maxOutput})
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

func validateRuntimeHostStore(ctx context.Context, s config.Settings, r Request) error {
	if r.runtimeHostAdmission == nil {
		return nil
	}
	a := r.runtimeHostAdmission
	if ctx == nil || a.store == nil || a.commitFirst == nil || a.state == nil || invalidRuntimeHostRequestState(r) ||
		!sessions.ValidEventPageID(a.taskID) || !sessions.ValidEventPageID(a.sessionID) ||
		(a.parentTaskID != "" && !sessions.ValidEventPageID(a.parentTaskID)) || !validRuntimeHostWorkerID(a.workerID) ||
		r.ModelID == "" || r.ModelID == "auto" {
		return ErrAdmission
	}
	same, err := a.store.SameDatabaseFile(ctx, s.Telemetry.Database)
	if err != nil || !same {
		return ErrAdmission
	}
	return nil
}

type redactingJournal struct {
	submissionID, submissionToken string
	db                            *telemetry.Store
	secrets                       []string
	eventSink                     func(runtime.Event)
	eventDelivery                 *eventDelivery
	deliverPerCall                bool
	textDelivery                  *textDelivery
	runtimeHostAdmission          *runtimeHostAdmission
	cancellationBoundary          *sync.Mutex
}

func (j redactingJournal) Append(ctx context.Context, expected int64, e runtime.Event) error {
	return j.appendLeased(ctx, expected, e, "", "")
}

// AppendContextCompaction preserves ordinary journal redaction, fencing and
// delivery while requiring storage to commit the replacement event and plan
// activation as one transaction.
func (j redactingJournal) AppendContextCompaction(ctx context.Context, expected int64, e runtime.Event, plan runtime.ContextCompactionPlan) error {
	return j.appendJournal(ctx, expected, e, "", "", false, &plan)
}

func (j redactingJournal) AppendLeased(ctx context.Context, expected int64, e runtime.Event, token, owner string) error {
	return j.appendLeased(ctx, expected, e, token, owner)
}

// FinishLeased preserves the same redaction, submission fencing and committed
// event delivery as normal appends while atomically finalizing joined work.
func (j redactingJournal) FinishLeased(ctx context.Context, expected int64, e runtime.Event, token, owner string) error {
	return j.appendJournal(ctx, expected, e, token, owner, true, nil)
}

func (j redactingJournal) appendLeased(ctx context.Context, expected int64, e runtime.Event, token, owner string) error {
	return j.appendJournal(ctx, expected, e, token, owner, false, nil)
}

func (j redactingJournal) appendJournal(ctx context.Context, expected int64, e runtime.Event, token, owner string, finish bool, plan *runtime.ContextCompactionPlan) error {
	rawText := e.Data.Text
	// Partial deltas can split a credential across records. Persist lifecycle
	// markers without delta text; the complete turn contains redacted text.
	if e.Kind == runtime.ModelDelta {
		e.Data.Text = ""
	}
	e.Data.SkillContext = redactSkillContextUse(e.Data.SkillContext, j.secrets)
	data, err := json.Marshal(e.Data)
	if err != nil {
		return err
	}
	var values any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if decoder.Decode(&values) != nil {
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
	if plan != nil && (finish || j.runtimeHostAdmission != nil || plan.Validate() != nil || !selectionValueClean(*plan, j.secrets) ||
		e.Kind != runtime.ContextCompacted || !reflect.DeepEqual(e.Data.Compaction, plan.Compaction) ||
		e.Data.ReplacedMessages != plan.LiveSuffixBoundary || !sameMessages(e.Data.Messages, plan.ReplacementPrefix)) {
		return ErrAdmission
	}
	commit := func() error {
		// Durable cancellation may stop provider I/O immediately before or after
		// this closure, but never while SQLite is deciding whether an append won.
		if j.cancellationBoundary != nil {
			j.cancellationBoundary.Lock()
			defer j.cancellationBoundary.Unlock()
		}
		if j.runtimeHostAdmission != nil {
			if err := j.runtimeHostAdmission.validateAppend(expected, e, j.db); err != nil {
				return err
			}
			if expected == 0 {
				return j.runtimeHostAdmission.commit(ctx, e)
			}
		}
		if plan != nil {
			if token != "" {
				return j.db.AppendWorkerContextCompaction(ctx, expected, e, *plan, token, owner, j.submissionID, j.submissionToken)
			}
			if j.submissionID != "" {
				return j.db.AppendSubmissionContextCompaction(ctx, expected, e, *plan, j.submissionID, j.submissionToken)
			}
			return j.db.AppendContextCompaction(ctx, expected, e, *plan)
		}
		if finish {
			return j.db.FinishWorker(ctx, expected, e, token, owner, j.submissionID, j.submissionToken)
		}
		if token != "" {
			return j.db.AppendWorker(ctx, expected, e, token, owner, j.submissionID, j.submissionToken)
		}
		if j.submissionID != "" {
			return j.db.AppendSubmission(ctx, expected, e, j.submissionID, j.submissionToken)
		}
		return j.db.Append(ctx, expected, e)
	}
	var appendErr error
	if j.eventDelivery != nil {
		appendErr = j.eventDelivery.CommitAndDeliver(ctx, e, j.deliverPerCall, commit)
	} else {
		appendErr = commit()
	}
	if err := appendErr; err != nil {
		return err
	}
	if j.eventSink != nil {
		clone, err := e.Clone()
		if err == nil {
			j.eventSink(clone)
		}
	}
	if j.textDelivery != nil && (j.eventDelivery == nil || !j.eventDelivery.Failed()) {
		if e.Kind == runtime.TaskCompleted && e.Data.HarnessOutcome != nil {
			j.textDelivery.deliver(redact(rawText, j.secrets), true)
		} else {
			j.textDelivery.accept(e.Kind, rawText)
		}
	}
	return nil
}

func (a *runtimeHostAdmission) validateAppend(expected int64, event runtime.Event, store *telemetry.Store) error {
	if a == nil || a.state == nil || store == nil || store != a.store || expected < 0 || event.Sequence != expected+1 ||
		event.TaskID != a.taskID || event.SessionID != a.sessionID || event.CorrelationID != a.taskID || event.WorkerID != a.workerID || event.Validate() != nil {
		return ErrAdmission
	}
	if expected == 0 && (event.Kind != runtime.TaskStarted || event.Data.ParentTaskID != a.parentTaskID || event.TurnID != "" ||
		event.AttemptID != "" || event.RouteID != "" || event.CausationID != "") {
		return ErrAdmission
	}
	if expected > 0 {
		a.state.mu.Lock()
		committed := a.state.committed
		a.state.mu.Unlock()
		if !committed {
			return ErrAdmission
		}
	}
	return nil
}

func (a *runtimeHostAdmission) commit(ctx context.Context, event runtime.Event) error {
	if ctx == nil || ctx.Err() != nil {
		return ErrAdmission
	}
	a.state.mu.Lock()
	defer a.state.mu.Unlock()
	if a.state.attempted {
		return ErrAdmission
	}
	a.state.attempted = true
	if err := a.commitFirst(ctx, event); err != nil {
		return err
	}
	stored, err := a.store.Read(ctx, event.TaskID, 0, 2)
	if err != nil || len(stored) != 1 {
		return ErrAdmission
	}
	want, wantErr := event.Encode()
	got, gotErr := stored[0].Encode()
	if wantErr != nil || gotErr != nil || string(want) != string(got) {
		return ErrAdmission
	}
	a.state.committed = true
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
