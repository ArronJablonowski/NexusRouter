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

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/memory"
	"github.com/ArronJablonowski/DarwinRouter/policy"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/resources"
	"github.com/ArronJablonowski/DarwinRouter/routing"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

// Service shares local reservations across all concurrent explicit and automatic requests.
// Construct one per daemon. Resource estimates are operator supplied upper
// bounds including weights and context/KV memory; absent metadata fails closed.
type Service struct {
	memoryStore memory.Store
	execution   chan struct{}
	discovery   *modelHealthCache
	settings    config.Settings
	secret      func(string) string
	budget      *resources.Budget
	profile     func(context.Context) (resources.Snapshot, error)
	draw        func() float64
	mu          sync.Mutex
}

func NewService(s config.Settings, secret func(string) string) (*Service, error) {
	if s.Validate() != nil || s.Telemetry.OTEL || s.Workers.Max > 64 {
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
	return &Service{execution: make(chan struct{}, s.Workers.Max), discovery: newHealthCache(), settings: s, secret: secret, budget: b, profile: profile, draw: rand.Float64}, nil
}

// Run dispatches an explicit model or performs automatic admission and ranking.
func (s *Service) Run(ctx context.Context, r Request) (Result, error) {
	r.memoryStore = s.memoryStore
	if s.execution != nil {
		select {
		case s.execution <- struct{}{}:
			defer func() { <-s.execution }()
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
	}
	var result Result
	var err error
	if r.ModelID != "" && r.ModelID != "auto" {
		result, err = s.runWithPressure(ctx, r, s.runExplicit)
	} else {
		result, err = s.runWithPressure(ctx, r, s.runAuto)
		if err != nil && result.retryable && result.fallbackModelID != "" && ctx.Err() == nil {
			first := result
			r.onlyModelID = first.fallbackModelID
			r.retryOfTaskID = first.TaskID
			r.LocalRequired = r.LocalRequired || first.retryLocalOnly
			r.MaxCost -= first.reservedCost
			if r.MaxCost >= 0 {
				next, nextErr := s.runWithPressure(ctx, r, s.runAuto)
				if next.TaskID != "" {
					next.PreviousTaskIDs = []string{first.TaskID}
					result, err = next, nextErr
				}
			}
		}
	}
	if err == nil && s.settings.Evaluation.Judge && s.settings.Evaluation.AutoReviewModel != "" {
		audit, auditErr := s.AuditTask(ctx, result.TaskID, s.settings.Evaluation.AutoReviewModel, s.settings.Evaluation.AutoReviewMaxCost)
		result.AuditStatus = "failed"
		if auditErr == nil {
			result.AuditID = audit.ID
			result.AuditStatus = "recorded"
		}
	}
	return result, err
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

func (s *Service) runAuto(ctx context.Context, r Request) (Result, error) {
	executionCtx := ctx
	if r.admissionContext != nil {
		ctx = r.admissionContext
	}
	if validateInput(r) != nil || ctx.Err() != nil {
		return Result{}, ErrAdmission
	}
	cfg := s.settings
	if cfg.Tools.Enabled {
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
		messages = append(messages, history.Messages...)
	}
	if !r.memoryPrepared {
		// Freeze one retrieval snapshot for context admission and execution.
		// Hybrid local-only memory pins this task local; shareable mode excludes
		// private facts before ranking any possible cloud candidate.
		if cfg.Mode != "cloud_only" || !cfg.Memory.LocalOnly {
			r.memoryContext, err = loadMemoryContext(ctx, selectMemoryStore(r.memoryStore, db), cfg.Memory, cfg.Memory.LocalOnly && cfg.Mode != "cloud_only", memorySecrets(cfg, s.secret))
			if err != nil {
				return Result{}, ErrAdmission
			}
		}
		r.memoryPrepared = true
	}
	if r.memoryContext != nil {
		messages = append(messages, r.memoryContext.Messages...)
		r.LocalRequired = r.LocalRequired || r.memoryContext.LocalOnly
	}
	if !r.skillPrepared {
		if cfg.Mode != "cloud_only" || !cfg.Skills.LocalOnly {
			r.skillContext, err = loadSkillContext(ctx, cfg.Skills, r.Domain, contextTools(cfg), memorySecrets(cfg, s.secret))
			if err != nil {
				return Result{}, ErrAdmission
			}
		}
		r.skillPrepared = true
	}
	if r.skillContext != nil {
		messages = append(messages, r.skillContext.Messages...)
		r.LocalRequired = r.LocalRequired || r.skillContext.LocalOnly
	}
	if len(r.Messages) > 0 {
		messages = append(messages, r.Messages...)
	} else {
		messages = append(messages, providers.Message{Role: "user", Content: r.Prompt})
	}
	// Byte count is a conservative token estimate, with framing/output reserve.
	inference := providers.Request{Messages: messages}
	if cfg.Tools.Enabled {
		inference.Tools = []providers.Tool{readFileSpec()}
	}
	contextTokens, estimateErr := providers.EstimateContext(inference)
	if estimateErr != nil {
		return Result{}, ErrAdmission
	}
	if r.ContextTokens > contextTokens {
		contextTokens = r.ContextTokens
	}
	if r.Domain == "" {
		r.Domain = "general"
	}
	if r.Profile == "" {
		r.Profile = "default"
	}
	if len(r.Capabilities) == 0 {
		r.Capabilities = []string{"chat"}
	}
	p := routing.Defaults()
	p.MinSamples = cfg.Routing.MinSamples
	p.Exploration = cfg.Routing.Exploration
	p.HalfLife, _ = config.Duration(cfg.Routing.HalfLife)
	w := cfg.Routing.Weights
	p.Weights = routing.Weights{Quality: w["quality"], Compliance: w["schema_compliance"], Reliability: w["reliability"], Latency: w["latency"], Cost: w["cost"], Recency: w["recency"], Uncertainty: w["uncertainty"]}
	candidates := []routing.Candidate{}
	evidence := map[routing.Key]routing.Evidence{}
	for _, m := range cfg.Models {
		c := routing.Candidate{Model: m.Model, Provider: m.Provider, FailureDomain: m.FailureDomain, Local: m.Locality == "local", Capabilities: m.Capabilities, ContextTokens: m.ContextTokens, CapacityAvailable: m.Locality != "local" || m.RAMBytes > 0}
		// routing validates positive windows, so an unknown window is represented
		// as a tiny window and additionally denied by policy.
		if c.ContextTokens == 0 {
			c.ContextTokens = 1
		}
		c.PolicyAllowed = m.ContextTokens > 0 && m.EstimatedCost != nil
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
				key := ""
				if pr.APIKeyEnv != "" && s.secret != nil {
					key = s.secret(pr.APIKeyEnv)
				}
				if pr.APIKeyEnv != "" && key == "" {
					c.PolicyAllowed = false
					break
				}
				tr, e := policy.NewTransport(cfg.Mode == "local_only" || c.Local, []string{pr.Endpoint})
				if e != nil {
					c.PolicyAllowed = false
					break
				}
				adapter, e := providers.NewHTTP(pr.Endpoint, pr.Kind, key, tr)
				if e == nil {
					check, cancel := context.WithTimeout(ctx, 2*time.Second)
					identity, _ := json.Marshal([]string{pr.ID, pr.Kind, pr.Endpoint, key, strconv.FormatBool(cfg.Mode == "local_only" || c.Local)})
					digest := sha256.Sum256(identity)
					models, e := s.discovery.models(check, hex.EncodeToString(digest[:]), adapter.Models)
					cancel()
					if e == nil {
						for _, name := range models {
							if name == m.Model {
								c.Healthy = true
							}
						}
					}
				}
				tr.CloseIdleConnections()
			}
		}
		key := routing.Key{Model: m.Model, Provider: m.Provider, Domain: r.Domain, Profile: r.Profile}
		e, eerr := db.Fitness(ctx, key)
		if eerr == nil {
			evidence[key] = e
		} else if !errors.Is(eerr, sql.ErrNoRows) {
			return Result{}, errors.New("cannot read routing fitness")
		}
		if cfg.Evaluation.Judge {
			advisory, aerr := db.AuditQuality(ctx, key)
			if aerr != nil {
				return Result{}, errors.New("cannot read advisory evidence")
			}
			if advisory.Samples > 0 {
				e.Advisory = advisory
				if e.Updated.IsZero() {
					e.Updated = advisory.Updated
				}
				evidence[key] = e
			}
		}
		validity, verr := db.OutputValidity(ctx, key, r.Validation)
		if verr != nil {
			return Result{}, errors.New("cannot read output validity")
		}
		if validity.Samples > 0 {
			e.Validity = validity
			if e.Updated.IsZero() {
				e.Updated = validity.Updated
			}
			evidence[key] = e
		}
		candidates = append(candidates, c)
	}
	// Serialize the snapshot/admission decision, while holding the reservation
	// (not the mutex) throughout inference. Capacity failures rerank safely.
	if err := s.lockResources(ctx); err != nil {
		return Result{}, err
	}
	snapshot, profileErr := s.resourceProfile(ctx)
	var selected routing.Selection
	var release func()
	capacityDenied := false
	draw := s.draw()
	for {
		if profileErr != nil {
			for i := range candidates {
				if candidates[i].Local {
					candidates[i].CapacityAvailable = false
				}
			}
		}
		selected, err = routing.Select(routing.Request{Mode: cfg.Mode, Domain: r.Domain, Profile: r.Profile, LocalRequired: r.LocalRequired, Capabilities: r.Capabilities, ContextTokens: contextTokens, MaxCost: r.MaxCost}, p, candidates, evidence, time.Now(), draw)
		if err != nil {
			break
		}
		var model config.Model
		for _, m := range cfg.Models {
			if m.Model == selected.Primary.Model && m.Provider == selected.Primary.Provider {
				model = m
				break
			}
		}
		r.ModelID = model.ID
		if model.Locality != "local" {
			break
		}
		release, err = s.budget.Reserve(snapshot, modelResources(model), time.Now())
		if err == nil {
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
	s.mu.Unlock()
	if err != nil {
		if capacityDenied && errors.Is(err, routing.ErrNoRoute) {
			return Result{}, errors.Join(ErrAdmission, routing.ErrNoRoute, resources.ErrCapacity)
		}
		if errors.Is(err, routing.ErrNoRoute) || errors.Is(err, routing.ErrInvalid) {
			return Result{}, errors.Join(ErrAdmission, err)
		}
		return Result{}, err
	}
	if release != nil {
		defer release()
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
	result, runErr := runExplicitAdmitted(executionCtx, cfg, r, s.secret)
	if runErr != nil {
		s.discovery.clear()
	}
	for _, m := range cfg.Models {
		if m.ID == r.ModelID && m.EstimatedCost != nil {
			result.reservedCost = *m.EstimatedCost
			result.retryLocalOnly = m.Locality == "local"
		}
	}
	for _, fallback := range selected.Fallbacks {
		for _, m := range cfg.Models {
			if m.Model == fallback.Model && m.Provider == fallback.Provider && (!result.retryLocalOnly || m.Locality == "local") {
				result.fallbackModelID = m.ID
				break
			}
		}
		if result.fallbackModelID != "" {
			break
		}
	}
	return result, runErr
}
