package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/contextengine"
	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/memory"
	"github.com/ArronJablonowski/DarwinRouter/policy"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/resources"
	"github.com/ArronJablonowski/DarwinRouter/routing"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
	"github.com/ArronJablonowski/DarwinRouter/skills"
	"github.com/ArronJablonowski/DarwinRouter/tools"
)

// Service shares local reservations across all concurrent explicit and automatic requests.
// Construct one per daemon. Resource estimates are operator supplied upper
// bounds including weights and context/KV memory; absent metadata fails closed.
type Service struct {
	toolExtension              *tools.Extension
	toolReviewer               tools.ApprovalReviewer
	toolPresenter              tools.ApprovalPresenter
	providerFactory            providers.Factory
	codexLauncher              codexLaunch
	contextEstimator           providers.ContextEstimator
	contextEngine              contextengine.Engine
	evaluator                  evaluation.Evaluator
	eventSink                  runtime.EventSink
	eventSinkSequencer         *configuredSinkSequencer
	presentationTextSink       PresentationTextSink
	memoryStore                memory.Store
	skillStore                 skills.Store
	execution                  chan struct{}
	discovery                  *modelHealthCache
	settings                   config.Settings
	secret                     func(string) string
	budget                     *resources.Budget
	resourceLimits             resources.Limits
	resourceCoordinatorMu      sync.Mutex
	resourceCoordinator        resources.Coordinator
	resourceOwner              resources.ReservationOwner
	resourceReservationStarted bool
	resourceReservationTTL     time.Duration
	resourceRenewTimeout       time.Duration
	residencyMu                sync.Mutex
	residencies                map[string]*residencyEndpoint
	profile                    func(context.Context) (resources.Snapshot, error)
	draw                       func() float64
	now                        func() time.Time
	mu                         sync.Mutex
}

func NewService(s config.Settings, secret func(string) string) (*Service, error) {
	if s.Validate() != nil || s.Workers.Max > 64 {
		return nil, ErrAdmission
	}
	// Snapshot nested configuration so callers cannot mutate running admissions.
	body, err := json.Marshal(s)
	if err != nil {
		return nil, ErrAdmission
	}
	var snapshot config.Settings
	if json.Unmarshal(body, &snapshot) != nil {
		return nil, ErrAdmission
	}
	s = snapshot
	concurrent := s.Workers.Max
	if s.Hardware.Concurrent != "auto" {
		concurrent, _ = strconv.Atoi(s.Hardware.Concurrent)
	}
	limits := resources.Limits{MaxConcurrent: concurrent, RAMPercent: s.Hardware.MaxRAM, VRAMPercent: s.Hardware.MaxVRAM, MaxAge: 5 * time.Second}
	var b *resources.Budget
	if s.Hardware.Concurrent == "auto" {
		b, err = resources.NewAdaptiveBudget(limits)
	} else {
		b, err = resources.NewBudget(limits)
	}
	if err != nil {
		return nil, err
	}
	profile := resources.Profile
	if s.Mode != "cloud_only" {
		for _, m := range s.Models {
			if m.Locality == "local" && m.GPUDevice != "" {
				profile = resources.ProfileWithGPUs
				break
			}
		}
	}
	if !s.Hardware.AutoProfile {
		// No manual profile source is configured yet. Disabling measurement
		// must not invent capacity or allow local execution without admission.
		profile = func(context.Context) (resources.Snapshot, error) { return resources.Snapshot{}, resources.ErrProfile }
	}
	return &Service{execution: make(chan struct{}, s.Workers.Max), discovery: newHealthCache(), settings: s, secret: secret, budget: b, resourceLimits: limits, resourceReservationTTL: hostResourceTTL, resourceRenewTimeout: hostResourceRenewTimeout, profile: profile, draw: rand.Float64, now: time.Now}, nil
}

func (s *Service) routingNow() time.Time {
	if s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

// Run dispatches an explicit model or performs automatic admission and ranking.
func (s *Service) Run(ctx context.Context, r Request) (result Result, runErr error) {
	if s == nil || ctx == nil {
		return Result{}, ErrAdmission
	}
	// Host-owned identities authorize one named execution attempt. Automatic
	// routing can create fallback task identities and is therefore not an
	// admissible host-runtime surface.
	if r.runtimeHostAdmission != nil && (r.ModelID == "" || r.ModelID == "auto") {
		return Result{}, ErrAdmission
	}
	if validateRuntimeHostStore(ctx, s.settings, r) != nil {
		return Result{}, ErrAdmission
	}
	if r.delegatedParent == "" {
		r.presentationTextSink = s.presentationTextSink
	}
	var classifyErr error
	r, classifyErr = classifyRequestIntent(r)
	if classifyErr != nil {
		return Result{}, classifyErr
	}
	if s.settings.Routing.Classifier.Enabled && r.intentAmbiguous && (r.ModelID == "" || r.ModelID == "auto") &&
		r.delegatedParent == "" && r.runtimeHostAdmission == nil && r.onlyModelID == "" &&
		r.Compaction == nil && r.SummaryAttemptID == "" {
		r.intentClassification = &intentClassificationState{}
	}
	if r.eventDelivery == nil && (s.eventSink != nil || r.eventSink != nil) {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		r.eventDelivery = newEventDelivery(cancel, s.eventSink, r.eventSink, s.eventSinkSequencer)
		r.deliverPerCall = true
		r.eventSink = nil
		defer func() {
			cancel()
			runErr = errors.Join(runErr, r.eventDelivery.Err())
		}()
	}
	if (s.settings.Tools.CreateEnabled || s.settings.Tools.ReplaceEnabled || s.settings.Tools.WorkboardWriteEnabled) && r.delegatedParent == "" && (s.toolReviewer == nil && s.toolPresenter == nil || r.submissionID != "") {
		return Result{}, ErrAdmission
	}
	// Process-local handlers have no durable identity yet. Never attach changed
	// authority to work admitted by an earlier process or host configuration.
	if r.submissionID != "" && len(s.toolExtension.Names()) > 0 {
		return Result{}, ErrAdmission
	}
	r = s.bindToolExtension(r)
	r.providerFactory = s.providerFactory
	r.contextEstimator = s.contextEstimator
	r.contextEngine = s.contextEngine
	r.memoryStore = s.memoryStore
	r.skillStore = s.skillStore
	if s.execution != nil {
		select {
		case s.execution <- struct{}{}:
			defer func() { <-s.execution }()
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
	}
	result, runErr = s.runRouteChain(ctx, r)
	if r.runtimeHostAdmission == nil {
		if recovered, ok := s.providerOverflowCompaction(ctx, r, result, runErr); ok {
			next, nextErr := s.runRouteChain(ctx, recovered)
			if next.TaskID != "" {
				previous := append([]string(nil), result.PreviousTaskIDs...)
				previous = append(previous, result.TaskID)
				next.PreviousTaskIDs = append(previous, next.PreviousTaskIDs...)
				next.RouteEstimatedCost = sumRouteEstimatedCost(result.RouteEstimatedCost, next.RouteEstimatedCost)
				next.Usage = sumCompleteRouteUsage(result.Usage, next.Usage)
				result, runErr = next, nextErr
			}
		}
	}
	if runErr == nil && r.runtimeHostAdmission == nil && s.settings.Evaluation.Judge && s.settings.Evaluation.AutoReviewModel != "" {
		audit, auditErr := s.AuditTask(ctx, result.TaskID, s.settings.Evaluation.AutoReviewModel, s.settings.Evaluation.AutoReviewMaxCost)
		result.AuditStatus = "failed"
		if auditErr == nil {
			result.AuditID = audit.ID
			result.AuditStatus = "recorded"
		}
	}
	return result, runErr
}

func (s *Service) runRouteChain(ctx context.Context, r Request) (Result, error) {
	var result Result
	var err error
	if r.ModelID != "" && r.ModelID != "auto" {
		result, err = s.runWithPressure(ctx, r, s.runExplicit)
		fallback := s.settings.WebUI.CommanderFallbackModel
		if err != nil && result.retryable && result.TaskID != "" && fallback != "" && r.ModelID == s.settings.WebUI.DefaultModel && r.runtimeHostAdmission == nil && r.delegatedParent == "" && ctx.Err() == nil {
			remaining := r.MaxCost
			if remaining > 0 {
				remaining -= result.reservedCost
			}
			if remaining >= 0 {
				nextRequest := r
				nextRequest.ModelID, nextRequest.retryOfTaskID, nextRequest.MaxCost = fallback, result.TaskID, remaining
				nextRequest.taskID, nextRequest.sessionID = "", ""
				next, nextErr := s.runWithPressure(ctx, nextRequest, s.runExplicit)
				if next.TaskID != "" {
					next.PreviousTaskIDs = []string{result.TaskID}
					next.RouteEstimatedCost = sumRouteEstimatedCost(result.RouteEstimatedCost, next.RouteEstimatedCost)
					next.Usage = sumCompleteRouteUsage(result.Usage, next.Usage)
					result, err = next, nextErr
				}
			}
		}
	} else {
		result, err = s.runWithPressure(ctx, r, s.runAuto)
		if err != nil && r.runtimeHostAdmission == nil && result.retryable && result.TaskID != "" && len(result.fallbackModelIDs) > 0 && ctx.Err() == nil {
			fallbacks := append([]string(nil), result.fallbackModelIDs...)
			if len(fallbacks) >= sessions.MaxTerminalRouteAttempts {
				fallbacks = fallbacks[:sessions.MaxTerminalRouteAttempts-1]
			}
			previous := []string{result.TaskID}
			remaining := r.MaxCost - result.reservedCost
			for _, modelID := range fallbacks {
				if !result.retryable || ctx.Err() != nil || remaining < 0 {
					break
				}
				r.onlyModelID = modelID
				r.retryOfTaskID = previous[len(previous)-1]
				r.MaxCost = remaining
				next, nextErr := s.runWithPressure(ctx, r, s.runAuto)
				if next.TaskID == "" {
					// This candidate became ineligible before dispatch. No durable
					// attempt or effect exists, so the original ordered chain may
					// continue to a different failure domain.
					if nextErr == nil {
						nextErr = ErrAdmission
					}
					if !errors.Is(nextErr, ErrAdmission) || ctx.Err() != nil {
						err = nextErr
						break
					}
					continue
				}
				next.PreviousTaskIDs = append([]string(nil), previous...)
				previous = append(previous, next.TaskID)
				remaining -= next.reservedCost
				next.RouteEstimatedCost = sumRouteEstimatedCost(result.RouteEstimatedCost, next.RouteEstimatedCost)
				next.Usage = sumCompleteRouteUsage(result.Usage, next.Usage)
				result, err = next, nextErr
				if nextErr == nil {
					break
				}
			}
		}
	}
	return result, err
}

func sumRouteEstimatedCost(a, b *float64) *float64 {
	if a == nil || b == nil || *a > math.MaxFloat64-*b {
		return nil
	}
	total := *a + *b
	if math.IsInf(total, 0) || math.IsNaN(total) {
		return nil
	}
	return &total
}

// A partial aggregate is more misleading than unavailable usage. Retryable
// failed streams have no durable completed-turn usage, so any fallback chain
// containing one intentionally reports nil rather than only the final route.
func sumCompleteRouteUsage(a, b *providers.Usage) *providers.Usage {
	if a == nil || b == nil || a.InputTokens > math.MaxInt64-b.InputTokens || a.OutputTokens > math.MaxInt64-b.OutputTokens {
		return nil
	}
	return &providers.Usage{InputTokens: a.InputTokens + b.InputTokens, OutputTokens: a.OutputTokens + b.OutputTokens}
}

// RunAuto is a one-shot convenience. Daemons must reuse Service.Run instead.
func RunAuto(ctx context.Context, s config.Settings, r Request, secret func(string) string) (Result, error) {
	svc, err := NewService(s, secret)
	if err != nil {
		return Result{}, err
	}
	r.ModelID = "auto"
	return svc.Run(ctx, r)
}

func validateInput(r Request) error {
	if r.SummaryAttemptID != "" && (r.ContinueTaskID == "" || r.Compaction != nil || len(r.SummaryAttemptID) > 128 || strings.TrimSpace(r.SummaryAttemptID) != r.SummaryAttemptID) {
		return ErrAdmission
	}
	if r.Compaction != nil && (r.ContinueTaskID == "" || sessions.ValidateCompactionRequest(r.Compaction) != nil) {
		return ErrAdmission
	}
	if r.Validation != "" && r.Validation != "go_source" {
		return ErrAdmission
	}
	if r.ContextTokens < 0 || len(r.Domain) > 128 || len(r.Profile) > 128 || r.MaxCost < 0 || math.IsNaN(r.MaxCost) || math.IsInf(r.MaxCost, 0) || len(r.Capabilities) > 128 {
		return ErrAdmission
	}
	seen := map[string]bool{}
	for _, capability := range r.Capabilities {
		if strings.TrimSpace(capability) == "" || len(capability) > 128 || seen[capability] {
			return ErrAdmission
		}
		seen[capability] = true
	}
	if len(r.Messages) > 0 {
		if r.Prompt != "" || r.ContinueTaskID != "" || providers.ValidateMessages(r.Messages) != nil {
			return ErrAdmission
		}
		b, err := json.Marshal(r.Messages)
		if err != nil || len(b) > 1<<20 {
			return ErrAdmission
		}
	} else if strings.TrimSpace(r.Prompt) == "" || len(r.Prompt) > 1<<20 {
		return ErrAdmission
	}
	return nil
}

func (s *Service) runAuto(ctx context.Context, r Request) (result Result, runErr error) {
	var classifyErr error
	r, classifyErr = classifyRequestIntent(r)
	if classifyErr != nil {
		return Result{}, classifyErr
	}
	r.contextEngine = s.contextEngine
	r = s.bindToolExtension(r)
	r.providerFactory = s.providerFactory
	r.contextEstimator = s.contextEstimator
	executionCtx := ctx
	if r.admissionContext != nil {
		ctx = r.admissionContext
	}
	if validateInput(r) != nil || ctx.Err() != nil {
		return Result{}, ErrAdmission
	}
	cfg := s.settings
	if cfg.Tools.WorkboardReadEnabled || cfg.Tools.CreateEnabled || cfg.Tools.ReplaceEnabled || len(r.toolExtension.Names()) > 0 {
		r.LocalRequired = true
	}
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		return Result{}, errors.New("cannot open routing storage")
	}
	defer db.Close()
	messages := []providers.Message{}
	if r.ContinueTaskID != "" {
		if r.continuation == nil {
			r.continuation, err = loadContinuation(ctx, db, r, memorySecrets(cfg, s.secret))
			if err != nil {
				return Result{}, err
			}
		}
		history := r.continuation
		if history.Privacy != "cloud_allowed" {
			r.LocalRequired = true
		}
	}
	r, err = s.prepareAuxiliaryIntent(ctx, db, r)
	if r.intentClassificationUse != nil {
		defer func() {
			result, runErr = persistUnroutedClassification(ctx, db, r, result, runErr)
		}()
	}
	if err != nil {
		return Result{}, err
	}
	if !r.memoryPrepared {
		// Freeze one retrieval snapshot for context admission and execution.
		// Hybrid local-only memory pins this task local; shareable mode excludes
		// private facts before ranking any possible cloud candidate.
		if cfg.Mode != "cloud_only" || !cfg.Memory.LocalOnly {
			r.memoryContext, err = loadMemoryContext(ctx, selectMemoryStore(r.memoryStore, db), cfg.Memory, cfg.Memory.LocalOnly && cfg.Mode != "cloud_only", memorySecrets(cfg, s.secret), memoryTaskQuery(r))
			if err != nil {
				return Result{}, ErrAdmission
			}
		}
		r.memoryPrepared = true
	}
	if r.memoryContext != nil {
		r.LocalRequired = r.LocalRequired || r.memoryContext.LocalOnly
	}
	if !r.skillPrepared {
		if cfg.Mode != "cloud_only" || !cfg.Skills.LocalOnly {
			r.skillContext, err = loadSkillContextFrom(ctx, r.skillStore, cfg.Skills, r.Domain, contextTools(cfg, r.toolExtension), memorySecrets(cfg, s.secret))
			if err != nil {
				return Result{}, ErrAdmission
			}
		}
		r.skillPrepared = true
	}
	if r.skillContext != nil {
		r.LocalRequired = r.LocalRequired || r.skillContext.LocalOnly
	}
	messages, err = prepareTaskContext(ctx, &r, memorySecrets(cfg, s.secret))
	if err != nil {
		return Result{}, ErrAdmission
	}
	// Byte count is a conservative token estimate, with framing/output reserve.
	inference := providers.Request{Messages: messages, Tools: initialTaskTools(cfg, r)}
	contextTokens, estimateErr := providers.EstimateContext(inference)
	if estimateErr != nil {
		return Result{}, ErrAdmission
	}
	if r.ContextTokens > contextTokens {
		contextTokens = r.ContextTokens
	}
	p := routing.Defaults()
	p.MinSamples = cfg.Routing.MinSamples
	p.Exploration = cfg.Routing.Exploration
	p.HalfLife, _ = config.Duration(cfg.Routing.HalfLife)
	decayResolver := routing.DecayResolverFunc(func(key routing.Key) (time.Duration, bool) {
		halfLife, err := cfg.Routing.DecayHalfLife(key.Domain, key.Profile)
		return halfLife, err == nil
	})
	w := cfg.Routing.Weights
	p.Weights = routing.Weights{Quality: w["quality"], Compliance: w["schema_compliance"], Reliability: w["reliability"], Latency: w["latency"], Cost: w["cost"], Recency: w["recency"], Uncertainty: w["uncertainty"]}
	candidates := []routing.Candidate{}
	evidence := map[routing.Key]routing.Evidence{}
	observationSets := map[routing.Key]routing.ObservationSet{}
	validitySets := map[routing.Key]routing.Validity{}
	// Custom estimates are model-specific. Bound the whole measurement batch
	// separately from provider health checks, and deny only unmeasurable models.
	contextFits := map[string]bool{}
	if r.contextEstimator != nil {
		measureCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		for _, m := range cfg.Models {
			local := m.Locality == "local"
			if m.ContextTokens < 1 || m.EstimatedCost == nil || (cfg.Mode == "local_only" && !local) || (cfg.Mode == "cloud_only" && local) || (r.LocalRequired && !local) || (r.onlyModelID != "" && r.onlyModelID != m.ID) {
				continue
			}
			candidateInput := inference
			candidateInput.Model = m.Model
			estimate, err := providers.EstimateWith(measureCtx, r.contextEstimator, candidateInput)
			contextFits[m.ID] = err == nil && estimate <= m.ContextTokens
		}
		cancel()
	}
	for _, m := range cfg.Models {
		c := routing.Candidate{Model: m.Model, Provider: m.Provider, FailureDomain: m.FailureDomain, Local: m.Locality == "local", Capabilities: m.Capabilities, ContextTokens: m.ContextTokens, CapacityAvailable: m.Locality != "local" || m.RAMBytes > 0}
		// routing validates positive windows, so an unknown window is represented
		// as a tiny window and additionally denied by policy.
		if c.ContextTokens == 0 {
			c.ContextTokens = 1
		}
		c.PolicyAllowed = m.ContextTokens > 0 && m.EstimatedCost != nil
		if r.contextEstimator != nil && !contextFits[m.ID] {
			c.PolicyAllowed = false
		}
		if r.onlyModelID != "" && m.ID != r.onlyModelID {
			c.PolicyAllowed = false
		}
		if m.EstimatedCost != nil {
			c.EstimatedCost = *m.EstimatedCost
		}
		allowed := c.PolicyAllowed && !(cfg.Mode == "local_only" && !c.Local) && !(cfg.Mode == "cloud_only" && c.Local) && !(r.LocalRequired && !c.Local)
		if allowed {
			for _, pr := range cfg.Providers {
				if pr.ID != m.Provider {
					continue
				}
				if r.runtimeHostAdmission != nil && pr.ManageResidency {
					c.PolicyAllowed = false
					break
				}
				key := ""
				c.CredentialRequired = pr.APIKeyEnv != ""
				if pr.APIKeyEnv != "" && s.secret != nil {
					key = s.secret(pr.APIKeyEnv)
				}
				c.CredentialAvailable = key != ""
				if pr.APIKeyEnv != "" && key == "" {
					break
				}
				endpoint := pr.ResolvedEndpoint()
				tr, e := policy.NewTransport(cfg.Mode == "local_only" || c.Local, []string{endpoint})
				if e != nil {
					c.PolicyAllowed = false
					break
				}
				check, cancel := context.WithTimeout(ctx, 2*time.Second)
				adapter, e := providers.Build(check, s.providerFactory, providers.Connection{Version: 1, ID: pr.ID, Endpoint: endpoint, Kind: pr.Kind, Purpose: providers.PurposeDiscovery, Timeout: httpProviderTimeout(pr), APIKey: key, Transport: tr})
				if e == nil {
					identity, _ := json.Marshal([]string{pr.ID, pr.Kind, endpoint, key, strconv.FormatBool(cfg.Mode == "local_only" || c.Local)})
					digest := sha256.Sum256(identity)
					models, e := s.discovery.models(check, hex.EncodeToString(digest[:]), adapter.Models)
					if e == nil {
						for _, name := range models {
							if name == m.Model {
								c.Healthy = true
							}
						}
					}
				}
				cancel()
				tr.CloseIdleConnections()
			}
		}
		key := routing.Key{Model: m.Model, Provider: m.Provider, Domain: r.Domain, Profile: r.Profile}
		observations, observationErr := db.ObservationSet(ctx, key, cfg.Evaluation.Judge)
		if observationErr != nil {
			return Result{}, errors.New("cannot read routing observations")
		}
		observationSets[key] = observations
		validity, verr := db.OutputValidity(ctx, key, r.Validation)
		if verr != nil {
			return Result{}, errors.New("cannot read output validity")
		}
		validitySets[key] = validity
		candidates = append(candidates, c)
	}
	// Capture one clock after the read snapshots. A concurrently committed
	// observation cannot appear newer merely because provider health checks ran
	// after an earlier clock sample. Every candidate still uses the same instant.
	routingNow := s.routingNow()
	for key, observations := range observationSets {
		var e routing.Evidence
		if len(observations.Fitness) > 0 || len(observations.Advisory) > 0 {
			var observationErr error
			e, observationErr = routing.AggregateEvidence(key, observations, routingNow, p, decayResolver)
			if observationErr != nil {
				return Result{}, errors.New("cannot aggregate routing observations")
			}
		}
		validity := validitySets[key]
		if validity.Samples > 0 {
			e.Validity = validity
			if e.Updated.IsZero() {
				e.Updated = validity.Updated
			}
		}
		if e.Samples > 0 || e.Advisory.Samples > 0 || e.Validity.Samples > 0 {
			evidence[key] = e
		}
	}
	// Serialize profiling and the exploration draw. Each selected local route
	// then performs fresh, serialized reservation admission; managed lifecycle
	// HTTP runs outside the global mutex. Capacity failures rerank safely.
	if err := s.lockResources(ctx); err != nil {
		return Result{}, err
	}
	snapshot, profileErr := s.resourceProfile(ctx)
	draw := s.draw()
	s.mu.Unlock()
	var selected routing.Selection
	var model config.Model
	var release func() error
	capacityDenied := false
	for {
		if profileErr != nil {
			for i := range candidates {
				if candidates[i].Local {
					candidates[i].CapacityAvailable = false
				}
			}
		}
		selected, err = routing.Select(routing.Request{Mode: cfg.Mode, Domain: r.Domain, Profile: r.Profile, LocalRequired: r.LocalRequired, Capabilities: r.Capabilities, ContextTokens: contextTokens, MaxCost: r.MaxCost}, p, candidates, evidence, routingNow, draw)
		if err != nil {
			break
		}
		model = config.Model{}
		for _, m := range cfg.Models {
			if m.Model == selected.Primary.Model && m.Provider == selected.Primary.Provider {
				model = m
				break
			}
		}
		candidateRequest := r
		candidateRequest.ModelID = model.ID
		contextEvidence, evidenceErr := db.ContextEvidence(ctx, model.Model, model.Provider)
		if evidenceErr != nil {
			return Result{}, ErrAdmission
		}
		candidateRequest.ContextTokens, err = chooseContextTier(ctx, model, candidateRequest, contextTokens, contextEvidence, draw < cfg.Routing.Exploration, cfg.Routing.MinSamples)
		if err != nil {
			for i := range candidates {
				if candidates[i].Model == model.Model && candidates[i].Provider == model.Provider {
					candidates[i].CapacityAvailable = false
				}
			}
			continue
		}
		if candidateRequest.continuation != nil {
			candidateRequest.sessionID = candidateRequest.continuation.SessionID
		}
		candidateRequest, err = s.prepareExplicitApprovedCompaction(ctx, candidateRequest, model)
		if err != nil {
			return Result{}, ErrAdmission
		}
		if model.Locality != "local" {
			r = candidateRequest
			break
		}
		var reservedContext context.Context
		reservedContext, release, err = s.reservePrimary(executionCtx, ctx, model, candidateRequest)
		if err == nil {
			r = candidateRequest
			executionCtx = reservedContext
			for _, provider := range cfg.Providers {
				if provider.ID == model.Provider && provider.ManageResidency {
					// Never attach a pre-unload observation to a managed route.
					snapshot, profileErr = s.residencyProfile(ctx)
				}
			}
			break
		}
		if errors.Is(err, resources.ErrCapacity) {
			capacityDenied = true
		}
		for i := range candidates {
			if candidates[i].Model == model.Model && candidates[i].Provider == model.Provider {
				candidates[i].CapacityAvailable = false
			}
		}
	}
	if err != nil {
		if !capacityDenied && cfg.Runtime.AutoApprovedCompaction && !r.autoCompactionTried && r.ContinueTaskID != "" && r.Compaction == nil && r.SummaryAttemptID == "" && errors.Is(err, routing.ErrNoRoute) {
			attempt, _, discoveryErr := db.LatestApprovedSummary(ctx, r.ContinueTaskID)
			if discoveryErr == nil {
				r.autoCompactionTried = true
				r.SummaryAttemptID = attempt.ID
				r.continuation = nil
				r.preparedContext = nil
				return s.runAuto(executionCtx, r)
			}
			if !errors.Is(discoveryErr, sql.ErrNoRows) {
				return Result{}, ErrAdmission
			}
		}
		if capacityDenied && errors.Is(err, routing.ErrNoRoute) {
			return Result{}, errors.Join(ErrAdmission, routing.ErrNoRoute, resources.ErrCapacity)
		}
		if errors.Is(err, routing.ErrNoRoute) || errors.Is(err, routing.ErrInvalid) {
			return Result{}, errors.Join(ErrAdmission, err)
		}
		return Result{}, err
	}
	if release != nil {
		defer func() {
			if releaseErr := release(); releaseErr != nil {
				result = Result{}
				runErr = errors.Join(runErr, releaseErr)
			}
		}()
	}
	// Fingerprint configuration without persisting endpoints or local paths.
	redacted, _ := cfg.RedactedJSON()
	hash := sha256.Sum256(redacted)
	r.route = &runtime.Data{ModelID: selected.Primary.Model, ProviderID: selected.Primary.Provider, Route: &selected, Domain: r.Domain, Profile: r.Profile, ConfigID: hex.EncodeToString(hash[:]), RouteCandidates: candidates, RoutePolicy: &p}
	if profileErr == nil {
		// Persist only the selected device's scalar observation, never the
		// full hardware inventory or identifiers from unrelated devices.
		for _, model := range cfg.Models {
			if model.ID == r.ModelID && model.GPUDevice != "" {
				if total, available, e := resources.DeviceMemory(snapshot, model.GPUDevice, time.Now(), 5*time.Second); e == nil {
					snapshot.VRAMTotal, snapshot.VRAMAvailable = &total, &available
				}
			}
		}
		snapshot.GPUs = nil
		r.route.Resources = &snapshot
	}
	if ctx.Err() != nil {
		return Result{}, ErrAdmission
	}
	r.delegate = s.bindDelegate(r)
	r.delegateAudit = s.bindDelegationAudit()
	result, runErr = runExplicitAdmitted(executionCtx, cfg, r, s.secret)
	if runErr != nil {
		s.discovery.clear()
	}
	for _, m := range cfg.Models {
		if m.ID == r.ModelID && m.EstimatedCost != nil {
			result.reservedCost = *m.EstimatedCost
		}
	}
	seenFallbacks := map[string]bool{}
	for _, fallback := range selected.Fallbacks {
		for _, m := range cfg.Models {
			if m.Model == fallback.Model && m.Provider == fallback.Provider && !seenFallbacks[m.ID] {
				result.fallbackModelIDs = append(result.fallbackModelIDs, m.ID)
				seenFallbacks[m.ID] = true
				break
			}
		}
	}
	return result, runErr
}
