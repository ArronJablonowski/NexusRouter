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
	"time"

	"darwinrouter/memory"
	"darwinrouter/resources"
)

type Settings struct {
	Version    int        `yaml:"version" json:"version"`
	Mode       string     `yaml:"mode" json:"mode"`
	Daemon     Daemon     `yaml:"daemon" json:"daemon"`
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
	Telemetry  Telemetry  `yaml:"telemetry" json:"telemetry"`
}
type Daemon struct {
	Listen string `yaml:"listen" json:"listen"`
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
	Max          int    `yaml:"max_in_process" json:"max_in_process"`
	Heartbeat    string `yaml:"heartbeat_interval" json:"heartbeat_interval"`
	Lease        string `yaml:"lease_timeout" json:"lease_timeout"`
	EffectPolicy string `yaml:"side_effect_policy" json:"side_effect_policy"`
}
type Provider struct {
	ID        string `yaml:"id" json:"id"`
	Kind      string `yaml:"kind" json:"kind"`
	Endpoint  string `yaml:"endpoint" json:"endpoint"`
	APIKeyEnv string `yaml:"api_key_env" json:"api_key_env,omitempty"`
}
type Model struct {
	ContextTokens int      `yaml:"context_tokens" json:"context_tokens"`
	EstimatedCost *float64 `yaml:"estimated_cost" json:"estimated_cost,omitempty"`
	RAMBytes      uint64   `yaml:"ram_bytes" json:"ram_bytes"`
	VRAMBytes     uint64   `yaml:"vram_bytes" json:"vram_bytes"`
	GPUDevice     string   `yaml:"gpu_device" json:"gpu_device,omitempty"`
	FailureDomain string   `yaml:"failure_domain" json:"failure_domain"`
	ID            string   `yaml:"id" json:"id"`
	Provider      string   `yaml:"provider" json:"provider"`
	Model         string   `yaml:"model" json:"model"`
	Locality      string   `yaml:"locality" json:"locality"`
	Capabilities  []string `yaml:"capabilities" json:"capabilities"`
}
type Routing struct {
	Exploration float64            `yaml:"exploration_rate" json:"exploration_rate"`
	MinSamples  int                `yaml:"minimum_samples" json:"minimum_samples"`
	HalfLife    string             `yaml:"decay_half_life" json:"decay_half_life"`
	Weights     map[string]float64 `yaml:"weights" json:"weights"`
}
type Skills struct {
	Enabled      bool   `yaml:"enabled" json:"enabled"`
	AutoDraft    bool   `yaml:"auto_draft" json:"auto_draft"`
	AutoActivate bool   `yaml:"auto_activate_after_validation" json:"auto_activate_after_validation"`
	Rollback     bool   `yaml:"rollback_on_regression" json:"rollback_on_regression"`
	Root         string `yaml:"root" json:"root"`
	Scope        string `yaml:"scope" json:"scope"`
	LocalOnly    bool   `yaml:"local_only" json:"local_only"`
	MaxSkills    int    `yaml:"max_skills" json:"max_skills"`
	MaxBytes     int    `yaml:"max_bytes" json:"max_bytes"`
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
	Egress     string `yaml:"local_only_egress" json:"local_only_egress"`
	ToolPolicy string `yaml:"default_tool_policy" json:"default_tool_policy"`
}
type Telemetry struct {
	Database string `yaml:"database" json:"database"`
	OTEL     bool   `yaml:"opentelemetry_enabled" json:"opentelemetry_enabled"`
}
type Tools struct {
	Enabled  bool   `yaml:"enabled" json:"enabled"`
	ReadRoot string `yaml:"read_root" json:"read_root"`
	MaxTurns int    `yaml:"max_turns" json:"max_turns"`
}

func Defaults() Settings {
	return Settings{Version: 1, Mode: "hybrid", Daemon: Daemon{"127.0.0.1:7788"},
		Hardware: Hardware{AutoProfile: true, MaxRAM: 80, MaxVRAM: 85, Concurrent: "auto", LocalPressurePolicy: "reject", LocalQueueTimeout: "30s"}, Workers: Workers{3, "5s", "30s", "single_writer"},
		Routing: Routing{0.05, 20, "30d", map[string]float64{"quality": 0.35, "schema_compliance": 0.15, "reliability": 0.20, "latency": 0.10, "cost": 0.10, "recency": 0.05, "uncertainty": 0.05}},
		Skills:  Skills{Enabled: true, AutoDraft: true, AutoActivate: true, Rollback: true, LocalOnly: true, MaxSkills: 3, MaxBytes: 16384}, Memory: Memory{Enabled: true, LocalOnly: true, MaxFacts: 8, MaxBytes: 16384},
		Evaluation: Evaluation{Judge: true, Precedence: []string{"deterministic", "tool_result", "user_feedback", "llm_judge"}},
		Security:   Security{"deny", "ask"}, Tools: Tools{MaxTurns: 8}, Telemetry: Telemetry{"darwin.db", false}}
}

var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var skillScope = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

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
	if !finite(s.Routing.Exploration) || s.Routing.Exploration < 0 || s.Routing.Exploration > .25 || s.Routing.MinSamples < 1 {
		return errors.New("invalid routing exploration or sample count")
	}
	if _, err := Duration(s.Routing.HalfLife); err != nil {
		return errors.New("invalid routing decay duration")
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
	if s.Security.Egress != "deny" {
		return errors.New("local-only egress must be deny")
	}
	if s.Security.ToolPolicy != "ask" && s.Security.ToolPolicy != "deny" && s.Security.ToolPolicy != "allow" {
		return errors.New("invalid default tool policy")
	}
	if s.Tools.MaxTurns < 2 || s.Tools.MaxTurns > 32 {
		return errors.New("tool max turns must be between 2 and 32")
	}
	if s.Tools.Enabled && !filepath.IsAbs(s.Tools.ReadRoot) {
		return errors.New("enabled tools require an absolute read root")
	}
	if (s.Memory.Scope != "" && !memory.ValidKey(s.Memory.Scope)) || s.Memory.MaxFacts < 1 || s.Memory.MaxFacts > 64 || s.Memory.MaxBytes < 256 || s.Memory.MaxBytes > 65536 {
		return errors.New("invalid memory context settings")
	}
	if s.Skills.MaxSkills < 1 || s.Skills.MaxSkills > 16 || s.Skills.MaxBytes < 256 || s.Skills.MaxBytes > 65536 || (s.Skills.Root == "") != (s.Skills.Scope == "") {
		return errors.New("invalid skills context settings")
	}
	if s.Skills.Root != "" {
		if !filepath.IsAbs(s.Skills.Root) || filepath.Dir(filepath.Clean(s.Skills.Root)) == filepath.Clean(s.Skills.Root) || !skillScope.MatchString(s.Skills.Scope) {
			return errors.New("invalid skills root or scope")
		}
		if s.Mode == "local_only" && !s.Skills.LocalOnly {
			return errors.New("local-only mode requires local skills context")
		}
	}
	if s.Mode == "local_only" && (!s.Memory.LocalOnly || s.Telemetry.OTEL) {
		return errors.New("local-only mode requires local memory and disabled telemetry export")
	}
	if s.Telemetry.Database == "" {
		return errors.New("database path required")
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
	for _, p := range s.Providers {
		if !identifier.MatchString(p.ID) {
			return errors.New("invalid provider ID")
		}
		if _, ok := providers[p.ID]; ok {
			return errors.New("duplicate provider ID")
		}
		if p.Kind != "ollama" && p.Kind != "openai_compatible" {
			return errors.New("unsupported provider kind")
		}
		u, err := url.Parse(p.Endpoint)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("invalid provider endpoint; credentials and query strings are prohibited")
		}
		if p.APIKeyEnv != "" && !envName.MatchString(p.APIKeyEnv) {
			return errors.New("invalid credential environment reference")
		}
		providers[p.ID] = p
	}
	models := map[string]bool{}
	routes := map[[2]string]bool{}
	for _, m := range s.Models {
		route := [2]string{m.Provider, m.Model}
		if routes[route] {
			return errors.New("duplicate provider model route")
		}
		routes[route] = true
		if m.ContextTokens < 0 || (m.EstimatedCost != nil && (!finite(*m.EstimatedCost) || *m.EstimatedCost < 0)) || (m.FailureDomain != "" && !identifier.MatchString(m.FailureDomain)) {
			return errors.New("invalid model routing metadata")
		}
		if !identifier.MatchString(m.ID) || models[m.ID] || m.Model == "" {
			return errors.New("invalid or duplicate model identity")
		}
		models[m.ID] = true
		if _, ok := providers[m.Provider]; !ok {
			return errors.New("model references unknown provider")
		}
		if m.Locality != "local" && m.Locality != "cloud" {
			return errors.New("invalid model locality")
		}
		if m.GPUDevice != "" && (m.Locality != "local" || m.VRAMBytes == 0 || !resources.ValidGPUDeviceID(m.GPUDevice)) {
			return errors.New("invalid model GPU binding")
		}
		if len(m.Capabilities) == 0 {
			return errors.New("model capabilities required")
		}
		for _, c := range m.Capabilities {
			if !identifier.MatchString(c) {
				return errors.New("invalid capability name")
			}
		}
	}
	return nil
}
func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

// RedactedJSON deliberately hides provider endpoints and local filesystem paths.
// Credentials are never resolved into Settings in the first place.
func (s Settings) RedactedJSON() ([]byte, error) {
	s.Providers = append([]Provider(nil), s.Providers...)
	for i := range s.Providers {
		s.Providers[i].Endpoint = "[REDACTED]"
	}
	s.Telemetry.Database = "[REDACTED]"
	s.Tools.ReadRoot = "[REDACTED]"
	s.Skills.Root = "[REDACTED]"
	return json.MarshalIndent(s, "", "  ")
}
