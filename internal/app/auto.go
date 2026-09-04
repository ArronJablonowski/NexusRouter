package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"time"

	"darwinrouter/internal/config"
	"darwinrouter/internal/telemetry"
	"darwinrouter/policy"
	"darwinrouter/providers"
	"darwinrouter/resources"
	"darwinrouter/routing"
	"darwinrouter/runtime"
	"darwinrouter/sessions"
)

// Service shares local reservations across all concurrent automatic requests.
// Construct one per daemon. Resource estimates are operator supplied upper
// bounds including weights and context/KV memory; absent metadata fails closed.
type Service struct {
	settings config.Settings
	secret   func(string) string
	budget   *resources.Budget
	profile  func(context.Context) (resources.Snapshot, error)
	draw     func() float64
	mu       sync.Mutex
}

func NewService(s config.Settings, secret func(string) string) (*Service, error) {
	if s.Validate() != nil || s.Telemetry.OTEL {
		return nil, ErrAdmission
	}
	// Snapshot nested configuration so callers cannot mutate running admissions.
	body, err := json.Marshal(s)
	if err != nil {
		return nil, ErrAdmission
	}
	if json.Unmarshal(body, &s) != nil {
		return nil, ErrAdmission
	}
	concurrent := 1 // auto is deliberately conservative until adaptive sizing exists.
	if s.Hardware.Concurrent != "auto" {
		concurrent, _ = strconv.Atoi(s.Hardware.Concurrent)
	}
	b, err := resources.NewBudget(resources.Limits{MaxConcurrent: concurrent, RAMPercent: s.Hardware.MaxRAM, VRAMPercent: s.Hardware.MaxVRAM, MaxAge: 5 * time.Second})
	if err != nil {
		return nil, err
	}
	return &Service{settings: s, secret: secret, budget: b, profile: resources.Profile, draw: rand.Float64}, nil
}

// Run dispatches an explicit model or performs automatic admission and ranking.
func (s *Service) Run(ctx context.Context, r Request) (Result, error) {
	if r.ModelID != "" && r.ModelID != "auto" {
		return RunExplicit(ctx, s.settings, r, s.secret)
	}
	return s.runAuto(ctx, r)
}

// RunAuto is a one-shot convenience. Daemons must reuse Service.Run instead.
func RunAuto(ctx context.Context, s config.Settings, r Request, secret func(string) string) (Result, error) {
	svc, err := NewService(s, secret)
	if err != nil {
		return Result{}, err
	}
	return svc.runAuto(ctx, r)
}

func validateInput(r Request) error {
	if r.ContextTokens < 0 || len(r.Domain) > 128 || len(r.Profile) > 128 {
		return ErrAdmission
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
	if validateInput(r) != nil {
		return Result{}, ErrAdmission
	}
	cfg := s.settings
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		return Result{}, errors.New("cannot open routing storage")
	}
	defer db.Close()
	messages := r.Messages
	if len(messages) == 0 {
		messages = []providers.Message{{Role: "user", Content: r.Prompt}}
	}
	if r.ContinueTaskID != "" {
		history, e := sessions.Replay(ctx, db, r.ContinueTaskID)
		if e != nil || history.State != "completed" || history.InterruptedTurn || history.UncertainEffects || len(history.Pending) > 0 {
			return Result{}, ErrAdmission
		}
		if history.Privacy != "cloud_allowed" {
			r.LocalRequired = true
		}
		messages = append(history.Messages, messages...)
	}
	// Byte count is a conservative token estimate, with framing/output reserve.
	body, _ := json.Marshal(messages)
	contextTokens := len(body) + 1024
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
					models, e := adapter.Models(check)
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
		candidates = append(candidates, c)
	}
	// Serialize the snapshot/admission decision, while holding the reservation
	// (not the mutex) throughout inference. Capacity failures rerank safely.
	s.mu.Lock()
	snapshot, profileErr := s.profile(ctx)
	var selected routing.Selection
	var release func()
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
		release, err = s.budget.Reserve(snapshot, resources.Need{RAM: model.RAMBytes, VRAM: model.VRAMBytes}, time.Now())
		if err == nil {
			break
		}
		for i := range candidates {
			if candidates[i].Model == model.Model && candidates[i].Provider == model.Provider {
				candidates[i].CapacityAvailable = false
			}
		}
	}
	s.mu.Unlock()
	if err != nil {
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
		r.route.Resources = &snapshot
	}
	return RunExplicit(ctx, cfg, r, s.secret)
}
