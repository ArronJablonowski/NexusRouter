// Package config loads and validates versioned runtime settings without fetching credentials.
package config

import (
	"encoding/json"
	"errors"
	"math"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ArronJablonowski/DarwinRouter/memory"
	"github.com/ArronJablonowski/DarwinRouter/resources"
)

type Settings struct {
	Version    int        `yaml:"version" json:"version"`
	Mode       string     `yaml:"mode" json:"mode"`
	Daemon     Daemon     `yaml:"daemon" json:"daemon"`
	WebUI      WebUI      `yaml:"web_ui" json:"web_ui"`
	Workboard  Workboard  `yaml:"workboard" json:"workboard"`
	Hardware   Hardware   `yaml:"hardware" json:"hardware"`
	Workers    Workers    `yaml:"workers" json:"workers"`
	Providers  []Provider `yaml:"providers" json:"providers"`
	Models     []Model    `yaml:"models" json:"models"`
	Routing    Routing    `yaml:"routing" json:"routing"`
	Skills     Skills     `yaml:"skills" json:"skills"`
	Memory     Memory     `yaml:"memory" json:"memory"`
	Evaluation Evaluation `yaml:"evaluation" json:"evaluation"`
	Security   Security   `yaml:"security" json:"security"`
	Tools      Tools      `yaml:"tools" json:"tools"`
	Runtime    Runtime    `yaml:"runtime" json:"runtime"`
	Telemetry  Telemetry  `yaml:"telemetry" json:"telemetry"`
}
type Daemon struct {
	Listen string `yaml:"listen" json:"listen"`
}
type WebUI struct {
	Enabled                       bool     `yaml:"enabled" json:"enabled"`
	PathPrefix                    string   `yaml:"path_prefix" json:"path_prefix"`
	AllowedOrigins                []string `yaml:"allowed_origins,omitempty" json:"allowed_origins,omitempty"`
	BrowserSessionTTL             string   `yaml:"browser_session_ttl" json:"browser_session_ttl"`
	ModelInventoryRefreshInterval string   `yaml:"model_inventory_refresh_interval" json:"model_inventory_refresh_interval"`
	DefaultModel                  string   `yaml:"default_model,omitempty" json:"default_model,omitempty"`
	CommanderFallbackModel        string   `yaml:"commander_fallback_model,omitempty" json:"commander_fallback_model,omitempty"`
	SpecialistsAllowCloud         bool     `yaml:"specialists_allow_cloud" json:"specialists_allow_cloud"`
}
type Workboard struct {
	Enabled       bool                   `yaml:"enabled" json:"enabled"`
	Decomposition WorkboardDecomposition `yaml:"decomposition" json:"decomposition"`
	Scheduler     WorkboardScheduler     `yaml:"scheduler" json:"scheduler"`
}
type WorkboardDecomposition struct {
	Version              int `yaml:"version" json:"version"`
	MaxDepth             int `yaml:"max_depth" json:"max_depth"`
	MaxChildrenPerParent int `yaml:"max_children_per_parent" json:"max_children_per_parent"`
}
type WorkboardScheduler struct {
	Enabled         bool                     `yaml:"enabled" json:"enabled"`
	Interval        string                   `yaml:"interval" json:"interval"`
	MaxActiveClaims int                      `yaml:"max_active_claims" json:"max_active_claims"`
	CardScanLimit   int                      `yaml:"card_scan_limit" json:"card_scan_limit"`
	WorkerModel     string                   `yaml:"worker_model" json:"worker_model"`
	AcceptanceJudge WorkboardAcceptanceJudge `yaml:"acceptance_judge" json:"acceptance_judge"`
}
type WorkboardAcceptanceJudge struct {
	Enabled         bool    `yaml:"enabled" json:"enabled"`
	ReviewerModel   string  `yaml:"reviewer_model" json:"reviewer_model"`
	MaxCost         float64 `yaml:"max_cost" json:"max_cost"`
	MaxInputTokens  int64   `yaml:"max_input_tokens" json:"max_input_tokens,omitempty"`
	MaxOutputTokens int64   `yaml:"max_output_tokens" json:"max_output_tokens,omitempty"`
	Timeout         string  `yaml:"timeout" json:"timeout"`
}
type Hardware struct {
	AutoProfile         bool    `yaml:"auto_profile" json:"auto_profile"`
	MaxRAM              float64 `yaml:"max_ram_usage_pct" json:"max_ram_usage_pct"`
	MaxVRAM             float64 `yaml:"max_vram_usage_pct" json:"max_vram_usage_pct"`
	Concurrent          string  `yaml:"max_concurrent_local_models" json:"max_concurrent_local_models"`
	LocalPressurePolicy string  `yaml:"local_pressure_policy" json:"local_pressure_policy"`
	LocalQueueTimeout   string  `yaml:"local_queue_timeout" json:"local_queue_timeout"`
}
type Workers struct {
	Max               int     `yaml:"max_in_process" json:"max_in_process"`
	Heartbeat         string  `yaml:"heartbeat_interval" json:"heartbeat_interval"`
	Lease             string  `yaml:"lease_timeout" json:"lease_timeout"`
	EffectPolicy      string  `yaml:"side_effect_policy" json:"side_effect_policy"`
	DelegateModel     string  `yaml:"delegate_model" json:"delegate_model"`
	DelegateMaxCalls  int     `yaml:"delegate_max_calls" json:"delegate_max_calls"`
	DelegateMaxCost   float64 `yaml:"delegate_max_cost" json:"delegate_max_cost"`
	DelegateReadTools bool    `yaml:"delegate_read_tools" json:"delegate_read_tools"`
	DelegateMaxTurns  int     `yaml:"delegate_max_turns" json:"delegate_max_turns"`
}
type Provider struct {
	ManageResidency bool   `yaml:"manage_residency" json:"manage_residency,omitempty"`
	ID              string `yaml:"id" json:"id"`
	Kind            string `yaml:"kind" json:"kind"`
	Endpoint        string `yaml:"endpoint" json:"endpoint"`
	RequestTimeout  string `yaml:"request_timeout,omitempty" json:"request_timeout,omitempty"`
	APIKeyEnv       string `yaml:"api_key_env" json:"api_key_env,omitempty"`
	Executable      string `yaml:"executable,omitempty" json:"executable,omitempty"`
}
type Model struct {
	ContextTokens        int      `yaml:"context_tokens" json:"context_tokens"`
	DefaultContextTokens int      `yaml:"default_context_tokens,omitempty" json:"default_context_tokens,omitempty"`
	EstimatedCost        *float64 `yaml:"estimated_cost" json:"estimated_cost,omitempty"`
	RAMBytes             uint64   `yaml:"ram_bytes" json:"ram_bytes"`
	VRAMBytes            uint64   `yaml:"vram_bytes" json:"vram_bytes"`
	GPUDevice            string   `yaml:"gpu_device" json:"gpu_device,omitempty"`
	FailureDomain        string   `yaml:"failure_domain" json:"failure_domain"`
	ID                   string   `yaml:"id" json:"id"`
	Provider             string   `yaml:"provider" json:"provider"`
	Model                string   `yaml:"model" json:"model"`
	ReasoningEffort      string   `yaml:"reasoning_effort,omitempty" json:"reasoning_effort,omitempty"`
	Locality             string   `yaml:"locality" json:"locality"`
	Capabilities         []string `yaml:"capabilities" json:"capabilities"`
}

func (m Model) WorkingContextTokens() int {
	if m.DefaultContextTokens > 0 {
		return m.DefaultContextTokens
	}
	return m.ContextTokens
}

type Routing struct {
	Exploration    float64            `yaml:"exploration_rate" json:"exploration_rate"`
	MinSamples     int                `yaml:"minimum_samples" json:"minimum_samples"`
	HalfLife       string             `yaml:"decay_half_life" json:"decay_half_life"`
	DecayOverrides []DecayOverride    `yaml:"decay_overrides,omitempty" json:"decay_overrides,omitempty"`
	Weights        map[string]float64 `yaml:"weights" json:"weights"`
	Classifier     RoutingClassifier  `yaml:"classifier" json:"classifier"`
}

// RoutingClassifier configures one policy-bounded auxiliary call that may
// classify ambiguous automatic-routing requests. It does not select its own
// model through adaptive routing.
type RoutingClassifier struct {
	Enabled         bool    `yaml:"enabled" json:"enabled"`
	ModelID         string  `yaml:"model_id" json:"model_id"`
	MaxCost         float64 `yaml:"max_cost" json:"max_cost"`
	MaxInputTokens  int64   `yaml:"max_input_tokens" json:"max_input_tokens"`
	MaxOutputTokens int64   `yaml:"max_output_tokens" json:"max_output_tokens"`
	Timeout         string  `yaml:"timeout" json:"timeout"`
}

// DecayOverride selects an exact task domain and execution profile. Ordered
// lists merge predictably as one configuration value; duplicate selectors are
// rejected rather than depending on declaration order.
type DecayOverride struct {
	Domain   string `yaml:"domain" json:"domain"`
	Profile  string `yaml:"profile" json:"profile"`
	HalfLife string `yaml:"half_life" json:"half_life"`
}
type Skills struct {
	Learning                  Learning                  `yaml:"learning" json:"learning"`
	GenerationBudget          GenerationBudget          `yaml:"generation_budget" json:"generation_budget"`
	OutcomeRollbackSupervisor OutcomeRollbackSupervisor `yaml:"outcome_rollback_supervisor" json:"outcome_rollback_supervisor"`
	Enabled                   bool                      `yaml:"enabled" json:"enabled"`
	AutoDraft                 bool                      `yaml:"auto_draft" json:"auto_draft"`
	AutoActivate              bool                      `yaml:"auto_activate_after_validation" json:"auto_activate_after_validation"`
	Rollback                  bool                      `yaml:"rollback_on_regression" json:"rollback_on_regression"`
	OutcomeRollback           bool                      `yaml:"outcome_rollback" json:"outcome_rollback,omitempty"`
	Root                      string                    `yaml:"root" json:"root"`
	Scope                     string                    `yaml:"scope" json:"scope"`
	LocalOnly                 bool                      `yaml:"local_only" json:"local_only"`
	MaxSkills                 int                       `yaml:"max_skills" json:"max_skills"`
	MaxBytes                  int                       `yaml:"max_bytes" json:"max_bytes"`
}
type Memory struct {
	Enabled   bool   `yaml:"enabled" json:"enabled"`
	LocalOnly bool   `yaml:"local_only" json:"local_only"`
	Scope     string `yaml:"scope" json:"scope"`
	MaxFacts  int    `yaml:"max_facts" json:"max_facts"`
	MaxBytes  int    `yaml:"max_bytes" json:"max_bytes"`
}
type Evaluation struct {
	AutoReviewModel   string   `yaml:"auto_review_model" json:"auto_review_model"`
	AutoReviewMaxCost float64  `yaml:"auto_review_max_cost" json:"auto_review_max_cost"`
	Judge             bool     `yaml:"llm_judge_enabled" json:"llm_judge_enabled"`
	Precedence        []string `yaml:"evidence_precedence" json:"evidence_precedence"`
}
type Security struct {
	Egress     string   `yaml:"local_only_egress" json:"local_only_egress"`
	ToolPolicy string   `yaml:"default_tool_policy" json:"default_tool_policy"`
	RedactEnv  []string `yaml:"redact_env,omitempty" json:"redact_env,omitempty"`
}
type Telemetry struct {
	Database              string                `yaml:"database" json:"database"`
	OTEL                  bool                  `yaml:"opentelemetry_enabled" json:"opentelemetry_enabled"`
	ProviderHealthHistory ProviderHealthHistory `yaml:"provider_health_history" json:"provider_health_history"`
	MetricsExport         *MetricsExport        `yaml:"metrics_export,omitempty" json:"metrics_export,omitempty"`
	TraceExport           *TraceExport          `yaml:"trace_export,omitempty" json:"trace_export,omitempty"`
}
type Tools struct {
	CreateEnabled         bool   `yaml:"create_enabled" json:"create_enabled,omitempty"`
	CreateRoot            string `yaml:"create_root" json:"create_root,omitempty"`
	ReplaceEnabled        bool   `yaml:"replace_enabled" json:"replace_enabled,omitempty"`
	ReplaceRoot           string `yaml:"replace_root" json:"replace_root,omitempty"`
	WorkboardReadEnabled  bool   `yaml:"workboard_read_enabled" json:"workboard_read_enabled,omitempty"`
	WorkboardWriteEnabled bool   `yaml:"workboard_write_enabled" json:"workboard_write_enabled,omitempty"`
	Enabled               bool   `yaml:"enabled" json:"enabled"`
	ReadRoot              string `yaml:"read_root" json:"read_root"`
	MaxTurns              int    `yaml:"max_turns" json:"max_turns"`
}

type Runtime struct {
	MaxTurns               int  `yaml:"max_turns" json:"max_turns"`
	AutoApprovedCompaction bool `yaml:"auto_use_approved_summary" json:"auto_use_approved_summary"`
}

func Defaults() Settings {
	return Settings{Version: 1, Mode: "hybrid", Daemon: Daemon{"127.0.0.1:7788"}, WebUI: WebUI{Enabled: true, PathPrefix: "/app", BrowserSessionTTL: "8h", ModelInventoryRefreshInterval: "10s"},
		Workboard: Workboard{Enabled: true, Decomposition: WorkboardDecomposition{Version: 1, MaxDepth: 4, MaxChildrenPerParent: 8}, Scheduler: WorkboardScheduler{Interval: "5s", MaxActiveClaims: 3, CardScanLimit: 10000, AcceptanceJudge: WorkboardAcceptanceJudge{Timeout: "30s"}}},
		Hardware:  Hardware{AutoProfile: true, MaxRAM: 80, MaxVRAM: 85, Concurrent: "auto", LocalPressurePolicy: "reject", LocalQueueTimeout: "30s"}, Workers: Workers{Max: 3, Heartbeat: "5s", Lease: "30s", EffectPolicy: "single_writer", DelegateMaxCalls: 4, DelegateMaxCost: 0, DelegateMaxTurns: 4},
		Routing: Routing{Exploration: 0.05, MinSamples: 20, HalfLife: "30d", Weights: map[string]float64{"quality": 0.35, "schema_compliance": 0.15, "reliability": 0.20, "latency": 0.10, "cost": 0.10, "recency": 0.05, "uncertainty": 0.05}, Classifier: RoutingClassifier{MaxInputTokens: 4096, MaxOutputTokens: 256, Timeout: "30s"}},
		Skills: Skills{Learning: Learning{Name: "default", Domain: "general", Interval: "1m", ScanLimit: 20}, GenerationBudget: GenerationBudget{Window: "24h", MaxAttempts: 10, MaxInFlight: 1, Cooldown: "1h"},
			OutcomeRollbackSupervisor: OutcomeRollbackSupervisor{Version: 1, Interval: "5m", Domain: "unknown", Profile: "default", Source: "user_feedback", Privacy: "local_only", MinSamples: 20, MinDrop: .1, TasksPerVersion: 20},
			Enabled:                   true, AutoDraft: true, AutoActivate: true, Rollback: true, LocalOnly: true, MaxSkills: 3, MaxBytes: 16384}, Memory: Memory{Enabled: true, LocalOnly: true, MaxFacts: 8, MaxBytes: 16384},
		Evaluation: Evaluation{Judge: true, Precedence: []string{"deterministic", "tool_result", "user_feedback", "llm_judge"}},
		Security:   Security{Egress: "deny", ToolPolicy: "ask"}, Tools: Tools{MaxTurns: 8}, Runtime: Runtime{MaxTurns: 8},
		Telemetry: Telemetry{Database: "darwin.db", ProviderHealthHistory: ProviderHealthHistory{Enabled: true, Interval: "30s", Retain: 2880}}}
}

var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var skillScope = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

const DefaultOllamaEndpoint = "http://127.0.0.1:11434"

// ResolvedEndpoint returns the standard loopback Ollama endpoint when it was
// omitted. It never probes the network or discovers a remote destination.
func (p Provider) ResolvedEndpoint() string {
	if p.Kind == "ollama" && p.Endpoint == "" {
		return DefaultOllamaEndpoint
	}
	return p.Endpoint
}

// Duration accepts Go duration syntax plus positive integer days (e.g. 30d).
func Duration(value string) (time.Duration, error) {
	if len(value) > 1 && value[len(value)-1] == 'd' {
		days, err := strconv.ParseUint(value[:len(value)-1], 10, 32)
		if err != nil || days == 0 || days > 106751 {
			return 0, errors.New("invalid duration")
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 {
		return 0, errors.New("invalid duration")
	}
	return d, nil
}

func (s Settings) Validate() error {
	if s.Version != 1 {
		return errors.New("unsupported configuration version")
	}
	if s.Mode != "local_only" && s.Mode != "cloud_only" && s.Mode != "hybrid" {
		return errors.New("invalid deployment mode")
	}
	host, port, err := net.SplitHostPort(s.Daemon.Listen)
	n, pe := strconv.Atoi(port)
	if err != nil || pe != nil || n < 1 || n > 65535 || host == "" {
		return errors.New("invalid daemon listen address")
	}
	if err := s.WebUI.Validate(s.Daemon.Listen); err != nil {
		return err
	}
	for _, p := range []float64{s.Hardware.MaxRAM, s.Hardware.MaxVRAM} {
		if !finite(p) || p <= 0 || p > 100 {
			return errors.New("hardware percentages must be in (0,100]")
		}
	}
	if s.Hardware.Concurrent != "auto" {
		n, err := strconv.Atoi(s.Hardware.Concurrent)
		if err != nil || n < 1 || n > 64 {
			return errors.New("invalid local model concurrency")
		}
	}
	if s.Hardware.LocalPressurePolicy != "reject" && s.Hardware.LocalPressurePolicy != "wait" {
		return errors.New("invalid local pressure policy")
	}
	queueTimeout, queueErr := Duration(s.Hardware.LocalQueueTimeout)
	if queueErr != nil || queueTimeout < 100*time.Millisecond || queueTimeout > 5*time.Minute {
		return errors.New("invalid local queue timeout")
	}
	h, he := Duration(s.Workers.Heartbeat)
	l, le := Duration(s.Workers.Lease)
	if s.Workers.Max < 1 || he != nil || le != nil || l <= h || s.Workers.EffectPolicy != "single_writer" {
		return errors.New("invalid worker limits or lease policy")
	}
	if s.Workers.DelegateMaxCalls < 1 || s.Workers.DelegateMaxCalls > 16 || s.Workers.DelegateMaxTurns < 1 || s.Workers.DelegateMaxTurns > 8 || !finite(s.Workers.DelegateMaxCost) || s.Workers.DelegateMaxCost < 0 {
		return errors.New("invalid delegation limits")
	}
	if s.Workers.DelegateReadTools && (s.Workers.DelegateModel == "" || !s.Tools.Enabled || s.Workers.DelegateMaxTurns < 2) {
		return errors.New("invalid delegate read tools configuration")
	}
	workboardInterval, workboardIntervalErr := Duration(s.Workboard.Scheduler.Interval)
	if !s.Workboard.Enabled || workboardIntervalErr != nil || workboardInterval < 250*time.Millisecond || workboardInterval > 24*time.Hour ||
		s.Workboard.Scheduler.MaxActiveClaims < 1 || s.Workboard.Scheduler.MaxActiveClaims > 64 || (s.Workboard.Scheduler.Enabled && s.Workboard.Scheduler.MaxActiveClaims > s.Workers.Max) ||
		s.Workboard.Scheduler.CardScanLimit < 1 || s.Workboard.Scheduler.CardScanLimit > 10000 {
		return errors.New("invalid workboard scheduler configuration")
	}
	if err := s.validateWorkboardDecomposition(); err != nil {
		return err
	}
	if err := s.validateWorkboardSchedulerModels(); err != nil {
		return err
	}
	if s.Workers.DelegateModel != "" {
		if !identifier.MatchString(s.Workers.DelegateModel) || h < time.Millisecond || l > 10*time.Minute || l <= 2*h {
			return errors.New("invalid delegation model or lease policy")
		}
		found := false
		for _, model := range s.Models {
			if model.ID == s.Workers.DelegateModel {
				turns := 1
				if s.Workers.DelegateReadTools {
					turns = s.Workers.DelegateMaxTurns
				}
				if model.ContextTokens < 1 || model.EstimatedCost == nil || !finite(*model.EstimatedCost) || *model.EstimatedCost < 0 || *model.EstimatedCost > s.Workers.DelegateMaxCost/float64(turns) || (s.Workers.DelegateReadTools && model.Locality != "local") || (s.Mode == "local_only" && model.Locality != "local") || (s.Mode == "cloud_only" && model.Locality != "cloud") {
					return errors.New("delegation model unavailable within configured limits")
				}
				found = true
				break
			}
		}
		if !found {
			return errors.New("delegation model unavailable within configured limits")
		}
	}
	if !finite(s.Routing.Exploration) || s.Routing.Exploration < 0 || s.Routing.Exploration > .25 || s.Routing.MinSamples < 1 {
		return errors.New("invalid routing exploration or sample count")
	}
	if _, err := Duration(s.Routing.HalfLife); err != nil {
		return errors.New("invalid routing decay duration")
	}
	if len(s.Routing.DecayOverrides) > 128 {
		return errors.New("too many routing decay overrides")
	}
	decaySelectors := map[[2]string]bool{}
	for _, override := range s.Routing.DecayOverrides {
		selector := [2]string{override.Domain, override.Profile}
		if !identifier.MatchString(override.Domain) || !identifier.MatchString(override.Profile) || decaySelectors[selector] {
			return errors.New("invalid or duplicate routing decay override")
		}
		if _, err := Duration(override.HalfLife); err != nil {
			return errors.New("invalid routing decay override duration")
		}
		decaySelectors[selector] = true
	}
	known := Defaults().Routing.Weights
	total := 0.0
	if len(s.Routing.Weights) != len(known) {
		return errors.New("routing weights must specify seven components")
	}
	for k, w := range s.Routing.Weights {
		if _, ok := known[k]; !ok || !finite(w) || w < 0 {
			return errors.New("invalid routing weight")
		}
		total += w
	}
	if math.Abs(total-1) > 1e-9 {
		return errors.New("routing weights must sum to one")
	}
	if err := s.validateRoutingClassifier(); err != nil {
		return err
	}
	if s.Security.Egress != "deny" {
		return errors.New("local-only egress must be deny")
	}
	if s.Security.ToolPolicy != "ask" && s.Security.ToolPolicy != "deny" && s.Security.ToolPolicy != "allow" {
		return errors.New("invalid default tool policy")
	}
	if len(s.Security.RedactEnv) > 64 {
		return errors.New("too many sensitive environment references")
	}
	redactEnv := map[string]bool{}
	for _, name := range s.Security.RedactEnv {
		if !envName.MatchString(name) || redactEnv[name] {
			return errors.New("invalid sensitive environment reference")
		}
		redactEnv[name] = true
	}
	if s.Tools.MaxTurns < 2 || s.Tools.MaxTurns > 32 {
		return errors.New("tool max turns must be between 2 and 32")
	}
	if s.Runtime.MaxTurns < 1 || s.Runtime.MaxTurns > 32 {
		return errors.New("runtime max turns must be between 1 and 32")
	}
	if s.Tools.Enabled && !filepath.IsAbs(s.Tools.ReadRoot) {
		return errors.New("enabled tools require an absolute read root")
	}
	if s.Tools.CreateRoot != "" && (len(s.Tools.CreateRoot) > 4096 || !utf8.ValidString(s.Tools.CreateRoot) || strings.ContainsRune(s.Tools.CreateRoot, 0)) {
		return errors.New("invalid create root")
	}
	if s.Tools.CreateEnabled && (!s.Tools.Enabled || !filepath.IsAbs(s.Tools.CreateRoot)) {
		return errors.New("create tool requires enabled tools and absolute create root")
	}
	if s.Tools.ReplaceRoot != "" && (len(s.Tools.ReplaceRoot) > 4096 || !utf8.ValidString(s.Tools.ReplaceRoot) || strings.ContainsRune(s.Tools.ReplaceRoot, 0)) {
		return errors.New("invalid replace tool root")
	}
	if s.Tools.ReplaceEnabled && (!s.Tools.Enabled || !filepath.IsAbs(s.Tools.ReplaceRoot)) {
		return errors.New("replace tool requires enabled local tools and an absolute root")
	}
	if s.Tools.WorkboardWriteEnabled && !s.Tools.WorkboardReadEnabled {
		return errors.New("workboard write tools require workboard reads")
	}
	if (s.Memory.Scope != "" && !memory.ValidKey(s.Memory.Scope)) || s.Memory.MaxFacts < 1 || s.Memory.MaxFacts > 64 || s.Memory.MaxBytes < 256 || s.Memory.MaxBytes > 65536 {
		return errors.New("invalid memory context settings")
	}
	if s.Skills.MaxSkills < 1 || s.Skills.MaxSkills > 16 || s.Skills.MaxBytes < 256 || s.Skills.MaxBytes > 65536 || (s.Skills.Root == "") != (s.Skills.Scope == "") {
		return errors.New("invalid skills context settings")
	}
	if s.Skills.OutcomeRollback && (!s.Skills.Enabled || !s.Skills.Rollback || s.Skills.Root == "" || s.Skills.Scope == "") {
		return errors.New("outcome rollback requires enabled skills, rollback policy, and configured root and scope")
	}
	if err := s.validateOutcomeRollbackSupervisor(); err != nil {
		return err
	}
	if err := s.Skills.GenerationBudget.Validate(); err != nil {
		return err
	}
	if err := s.validateLearning(); err != nil {
		return err
	}
	if s.Skills.Root != "" {
		if !filepath.IsAbs(s.Skills.Root) || filepath.Dir(filepath.Clean(s.Skills.Root)) == filepath.Clean(s.Skills.Root) || !skillScope.MatchString(s.Skills.Scope) {
			return errors.New("invalid skills root or scope")
		}
		if s.Mode == "local_only" && !s.Skills.LocalOnly {
			return errors.New("local-only mode requires local skills context")
		}
	}
	if s.Mode == "local_only" && !s.Memory.LocalOnly {
		return errors.New("local-only mode requires local memory")
	}
	if s.Telemetry.Database == "" {
		return errors.New("database path required")
	}
	if err := s.Telemetry.ProviderHealthHistory.validate(); err != nil {
		return err
	}
	if err := s.Telemetry.MetricsExport.validate(s.Mode, s.Telemetry.OTEL); err != nil {
		return err
	}
	if err := s.Telemetry.TraceExport.validate(s.Mode); err != nil {
		return err
	}
	want := Defaults().Evaluation.Precedence
	if s.Evaluation.AutoReviewMaxCost < 0 || math.IsNaN(s.Evaluation.AutoReviewMaxCost) || math.IsInf(s.Evaluation.AutoReviewMaxCost, 0) {
		return errors.New("invalid audit cost ceiling")
	}
	if s.Evaluation.AutoReviewModel != "" {
		found := false
		for _, m := range s.Models {
			if m.ID == s.Evaluation.AutoReviewModel {
				found = true
			}
		}
		if !found {
			return errors.New("unknown automatic reviewer")
		}
	}
	if len(s.Evaluation.Precedence) != len(want) {
		return errors.New("invalid evidence precedence")
	}
	for i, v := range want {
		if s.Evaluation.Precedence[i] != v {
			return errors.New("invalid evidence precedence")
		}
	}
	providers := map[string]Provider{}
	if err := s.validateResidency(); err != nil {
		return err
	}
	for _, p := range s.Providers {
		if !identifier.MatchString(p.ID) {
			return errors.New("invalid provider ID")
		}
		if _, ok := providers[p.ID]; ok {
			return errors.New("duplicate provider ID")
		}
		if p.Kind != "ollama" && p.Kind != "openai_compatible" && p.Kind != "codex_app_server" {
			return errors.New("unsupported provider kind")
		}
		if p.RequestTimeout != "" {
			timeout, err := Duration(p.RequestTimeout)
			if err != nil || timeout < 100*time.Millisecond || timeout > 5*time.Minute || p.Kind == "codex_app_server" {
				return errors.New("invalid provider request timeout")
			}
		}
		if p.Kind == "codex_app_server" {
			if !filepath.IsAbs(p.Executable) || len(p.Executable) > 4096 || !utf8.ValidString(p.Executable) || strings.ContainsRune(p.Executable, 0) || p.Endpoint != "" || p.APIKeyEnv != "" {
				return errors.New("invalid Codex app-server provider settings")
			}
		} else {
			if p.Executable != "" {
				return errors.New("HTTP providers cannot configure an executable")
			}
			u, err := url.Parse(p.ResolvedEndpoint())
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
				return errors.New("invalid provider endpoint; credentials and query strings are prohibited")
			}
		}
		if p.APIKeyEnv != "" && !envName.MatchString(p.APIKeyEnv) {
			return errors.New("invalid credential environment reference")
		}
		providers[p.ID] = p
	}
	models := map[string]bool{}
	modelSettings := map[string]Model{}
	routes := map[[2]string]bool{}
	if len(s.Models) > 256 {
		return errors.New("too many configured models")
	}
	for _, m := range s.Models {
		route := [2]string{m.Provider, m.Model}
		if routes[route] {
			return errors.New("duplicate provider model route")
		}
		routes[route] = true
		if m.ContextTokens < 0 || m.DefaultContextTokens < 0 || m.DefaultContextTokens > m.ContextTokens || (m.EstimatedCost != nil && (!finite(*m.EstimatedCost) || *m.EstimatedCost < 0)) || (m.FailureDomain != "" && !identifier.MatchString(m.FailureDomain)) {
			return errors.New("invalid model routing metadata")
		}
		if !identifier.MatchString(m.ID) || models[m.ID] || m.Model == "" || len(m.Model) > 512 || !utf8.ValidString(m.Model) || strings.TrimSpace(m.Model) != m.Model || strings.ContainsFunc(m.Model, unicode.IsControl) {
			return errors.New("invalid or duplicate model identity")
		}
		models[m.ID] = true
		modelSettings[m.ID] = m
		if _, ok := providers[m.Provider]; !ok {
			return errors.New("model references unknown provider")
		}
		if m.Locality != "local" && m.Locality != "cloud" {
			return errors.New("invalid model locality")
		}
		if providers[m.Provider].Kind == "codex_app_server" && (m.Locality != "cloud" || m.Model != "gpt-5.6-sol" || m.ContextTokens < 1 || !validReasoningEffort(m.ReasoningEffort)) {
			return errors.New("invalid Codex coordinator model settings")
		}
		if providers[m.Provider].Kind != "codex_app_server" && m.ReasoningEffort != "" {
			return errors.New("reasoning effort requires Codex coordinator provider")
		}
		if m.GPUDevice != "" && (m.Locality != "local" || m.VRAMBytes == 0 || !resources.ValidGPUDeviceID(m.GPUDevice)) {
			return errors.New("invalid model GPU binding")
		}
		if len(m.Capabilities) == 0 || len(m.Capabilities) > 128 {
			return errors.New("model capabilities required")
		}
		capabilities := map[string]bool{}
		for _, c := range m.Capabilities {
			if !identifier.MatchString(c) || capabilities[c] {
				return errors.New("invalid capability name")
			}
			capabilities[c] = true
		}
	}
	if s.WebUI.DefaultModel != "" && !models[s.WebUI.DefaultModel] {
		return errors.New("unknown web UI default model")
	}
	if fallback := s.WebUI.CommanderFallbackModel; fallback != "" {
		model, ok := modelSettings[fallback]
		if !ok || fallback == s.WebUI.DefaultModel || s.WebUI.DefaultModel == "" || model.Locality != "local" {
			return errors.New("invalid web UI commander fallback model")
		}
	}
	return nil
}
func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

func validReasoningEffort(value string) bool {
	switch value {
	case "", "none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra":
		return true
	default:
		return false
	}
}

// RedactedJSON deliberately hides provider endpoints and local filesystem paths.
// Credentials are never resolved into Settings in the first place.
func (s Settings) RedactedJSON() ([]byte, error) {
	s.Providers = append([]Provider(nil), s.Providers...)
	for i := range s.Providers {
		s.Providers[i].Endpoint = "[REDACTED]"
		if s.Providers[i].Executable != "" {
			s.Providers[i].Executable = "[REDACTED]"
		}
	}
	s.Telemetry.Database = "[REDACTED]"
	if s.Telemetry.MetricsExport != nil {
		copy := *s.Telemetry.MetricsExport
		if copy.Endpoint != "" {
			copy.Endpoint = "[REDACTED]"
		}
		s.Telemetry.MetricsExport = &copy
	}
	if s.Telemetry.TraceExport != nil {
		copy := *s.Telemetry.TraceExport
		if copy.Endpoint != "" {
			copy.Endpoint = "[REDACTED]"
		}
		s.Telemetry.TraceExport = &copy
	}
	s.Tools.ReadRoot = "[REDACTED]"
	if s.Tools.CreateRoot != "" {
		s.Tools.CreateRoot = "[REDACTED]"
	}
	if s.Tools.ReplaceRoot != "" {
		s.Tools.ReplaceRoot = "[REDACTED]"
	}
	s.Skills.Root = "[REDACTED]"
	return json.MarshalIndent(s, "", "  ")
}
