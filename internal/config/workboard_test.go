package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"math"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

func TestWorkboardDecompositionDefaultsLayersDisplayAndFingerprint(t *testing.T) {
	defaults := Defaults()
	if defaults.Workboard.Decomposition != (WorkboardDecomposition{Version: 1, MaxDepth: 4, MaxChildrenPerParent: 8}) {
		t.Fatalf("unexpected decomposition defaults: %+v", defaults.Workboard.Decomposition)
	}

	settings, err := Load(Options{
		UserFile:    file(t, "workboard:\n  decomposition:\n    max_depth: 6\n    max_children_per_parent: 12\n"),
		ProjectFile: file(t, "workboard:\n  decomposition:\n    max_depth: 5\n"),
		Env:         Environment([]string{"DARWIN__WORKBOARD__DECOMPOSITION__MAX_CHILDREN_PER_PARENT=10"}),
		Flags:       map[string]string{"workboard.decomposition.max_depth": "3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if settings.Workboard.Decomposition != (WorkboardDecomposition{Version: 1, MaxDepth: 3, MaxChildrenPerParent: 10}) {
		t.Fatalf("decomposition layers decoded incorrectly: %+v", settings.Workboard.Decomposition)
	}

	display, err := settings.RedactedJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(display, []byte(`"decomposition"`)) || !bytes.Contains(display, []byte(`"max_depth": 3`)) ||
		!bytes.Contains(display, []byte(`"version": 1`)) || !bytes.Contains(display, []byte(`"max_children_per_parent": 10`)) {
		t.Fatalf("effective decomposition limits absent from redacted display: %s", display)
	}
	before := sha256.Sum256(display)
	for name, mutate := range map[string]func(*Settings){
		"depth":    func(s *Settings) { s.Workboard.Decomposition.MaxDepth++ },
		"children": func(s *Settings) { s.Workboard.Decomposition.MaxChildrenPerParent++ },
	} {
		changedSettings := settings
		mutate(&changedSettings)
		changed, err := changedSettings.RedactedJSON()
		if err != nil {
			t.Fatal(err)
		}
		if before == sha256.Sum256(changed) {
			t.Fatalf("decomposition %s change absent from configuration fingerprint", name)
		}
	}
	if settings.Workboard.Decomposition != (WorkboardDecomposition{Version: 1, MaxDepth: 3, MaxChildrenPerParent: 10}) {
		t.Fatal("redacted display mutated effective settings")
	}
}

func TestWorkboardDecompositionValidationBoundaries(t *testing.T) {
	for _, limits := range []WorkboardDecomposition{
		{Version: 1, MaxDepth: 1, MaxChildrenPerParent: 1},
		{Version: 1, MaxDepth: workboard.MaxGraphDepth, MaxChildrenPerParent: workboard.MaxChildrenPerParent},
	} {
		s := Defaults()
		s.Workboard.Decomposition = limits
		if err := s.Validate(); err != nil {
			t.Fatalf("valid decomposition boundary rejected: %+v: %v", limits, err)
		}
	}
	for _, limits := range []WorkboardDecomposition{
		{Version: 0, MaxDepth: 1, MaxChildrenPerParent: 1},
		{Version: 2, MaxDepth: 1, MaxChildrenPerParent: 1},
		{Version: 1, MaxDepth: 0, MaxChildrenPerParent: 1},
		{Version: 1, MaxDepth: -1, MaxChildrenPerParent: 1},
		{Version: 1, MaxDepth: workboard.MaxGraphDepth + 1, MaxChildrenPerParent: 1},
		{Version: 1, MaxDepth: 1, MaxChildrenPerParent: 0},
		{Version: 1, MaxDepth: 1, MaxChildrenPerParent: -1},
		{Version: 1, MaxDepth: 1, MaxChildrenPerParent: workboard.MaxChildrenPerParent + 1},
	} {
		s := Defaults()
		s.Workboard.Decomposition = limits
		if err := s.Validate(); err == nil {
			t.Fatalf("unsafe decomposition limits accepted: %+v", limits)
		}
	}
}

func TestWorkboardDecompositionStrictConfiguration(t *testing.T) {
	for _, body := range []string{
		"workboard:\n  decomposition:\n    max_depth: many\n",
		"workboard:\n  decomposition:\n    max_children_per_parent: 2.5\n",
		"workboard:\n  decomposition:\n    max_child_cards: 8\n",
	} {
		if _, err := Load(Options{ProjectFile: file(t, body)}); err == nil {
			t.Fatalf("invalid decomposition configuration accepted: %s", body)
		}
	}
}

func TestWorkboardSchedulerDefaultsAndOverrides(t *testing.T) {
	defaults := Defaults()
	if !defaults.Workboard.Enabled || defaults.Workboard.Scheduler.Enabled || defaults.Workboard.Scheduler.Interval != "5s" ||
		defaults.Workboard.Scheduler.MaxActiveClaims != 3 || defaults.Workboard.Scheduler.CardScanLimit != 10000 ||
		defaults.Workboard.Scheduler.WorkerModel != "" || defaults.Workboard.Scheduler.AcceptanceJudge.Enabled ||
		defaults.Workboard.Scheduler.AcceptanceJudge.ReviewerModel != "" || defaults.Workboard.Scheduler.AcceptanceJudge.MaxCost != 0 ||
		defaults.Workboard.Scheduler.AcceptanceJudge.MaxInputTokens != 0 ||
		defaults.Workboard.Scheduler.AcceptanceJudge.MaxOutputTokens != 0 ||
		defaults.Workboard.Scheduler.AcceptanceJudge.Timeout != "30s" {
		t.Fatalf("unexpected workboard defaults: %+v", defaults.Workboard)
	}
	settings, err := Load(Options{ProjectFile: file(t, "workboard:\n  scheduler:\n    interval: 250ms\n    max_active_claims: 2\n    card_scan_limit: 1\n")})
	if err != nil || settings.Workboard.Scheduler.Interval != "250ms" || settings.Workboard.Scheduler.MaxActiveClaims != 2 || settings.Workboard.Scheduler.CardScanLimit != 1 {
		t.Fatalf("valid workboard override rejected: %+v %v", settings.Workboard, err)
	}
	settings, err = Load(Options{Env: map[string]string{"workboard.scheduler.enabled": "false", "workboard.scheduler.card_scan_limit": "25"}})
	if err != nil || settings.Workboard.Scheduler.Enabled || settings.Workboard.Scheduler.CardScanLimit != 25 {
		t.Fatalf("valid workboard scalar overrides rejected: %+v %v", settings.Workboard, err)
	}
	settings, err = Load(Options{Env: map[string]string{
		"workboard.scheduler.acceptance_judge.enabled":           "false",
		"workboard.scheduler.acceptance_judge.max_cost":          "1",
		"workboard.scheduler.acceptance_judge.max_input_tokens":  "2048",
		"workboard.scheduler.acceptance_judge.max_output_tokens": "4096",
		"workboard.scheduler.acceptance_judge.timeout":           "45s",
	}})
	if err != nil || settings.Workboard.Scheduler.AcceptanceJudge.Enabled || settings.Workboard.Scheduler.AcceptanceJudge.MaxCost != 1 || settings.Workboard.Scheduler.AcceptanceJudge.MaxInputTokens != 2048 || settings.Workboard.Scheduler.AcceptanceJudge.MaxOutputTokens != 4096 || settings.Workboard.Scheduler.AcceptanceJudge.Timeout != "45s" {
		t.Fatalf("valid nested scalar overrides rejected: %+v %v", settings.Workboard.Scheduler.AcceptanceJudge, err)
	}
}

func TestEnabledWorkboardSchedulerRequiresExplicitIndependentLocalJudge(t *testing.T) {
	s := validWorkboardSchedulerSettings()
	if err := s.Validate(); err != nil {
		t.Fatalf("valid scheduler configuration rejected: %v", err)
	}

	tests := map[string]func(*Settings){
		"missing worker":         func(s *Settings) { s.Workboard.Scheduler.WorkerModel = "" },
		"unknown worker":         func(s *Settings) { s.Workboard.Scheduler.WorkerModel = "missing" },
		"worker lacks context":   func(s *Settings) { s.Models[0].ContextTokens = 0 },
		"worker lacks estimate":  func(s *Settings) { s.Models[0].EstimatedCost = nil },
		"worker lacks chat":      func(s *Settings) { s.Models[0].Capabilities = []string{"reasoning"} },
		"cloud worker zero cost": func(s *Settings) { *s.Models[0].EstimatedCost = 0 },
		"worker WIP exceeds execution admission": func(s *Settings) {
			s.Workers.Max = workboard.MaxExecutionWIPLimit + 1
		},
		"worker provider lacks output ceiling": func(s *Settings) {
			s.Providers[0] = Provider{ID: "cloud", Kind: "codex_app_server", Executable: "/fixture/codex"}
			s.Models[0].Model = "gpt-5.6-sol"
		},
		"judge disabled":        func(s *Settings) { s.Workboard.Scheduler.AcceptanceJudge.Enabled = false },
		"global judge disabled": func(s *Settings) { s.Evaluation.Judge = false },
		"missing reviewer":      func(s *Settings) { s.Workboard.Scheduler.AcceptanceJudge.ReviewerModel = "" },
		"unknown reviewer":      func(s *Settings) { s.Workboard.Scheduler.AcceptanceJudge.ReviewerModel = "missing" },
		"same resolved model": func(s *Settings) {
			s.Models[0].Locality = "local"
			s.Workboard.Scheduler.AcceptanceJudge.ReviewerModel = "worker"
		},
		"cloud reviewer":          func(s *Settings) { s.Models[1].Locality = "cloud" },
		"reviewer lacks context":  func(s *Settings) { s.Models[1].ContextTokens = 0 },
		"reviewer lacks estimate": func(s *Settings) { s.Models[1].EstimatedCost = nil },
		"reviewer lacks audit":    func(s *Settings) { s.Models[1].Capabilities = []string{"chat"} },
		"reviewer over budget":    func(s *Settings) { *s.Models[1].EstimatedCost = .26 },
		"zero judge cost":         func(s *Settings) { s.Workboard.Scheduler.AcceptanceJudge.MaxCost = 0 },
		"negative judge cost":     func(s *Settings) { s.Workboard.Scheduler.AcceptanceJudge.MaxCost = -1 },
		"non-finite judge cost":   func(s *Settings) { s.Workboard.Scheduler.AcceptanceJudge.MaxCost = math.Inf(1) },
		"excess judge cost":       func(s *Settings) { s.Workboard.Scheduler.AcceptanceJudge.MaxCost = maxWorkboardJudgeCost + 1 },
		"zero input ceiling":      func(s *Settings) { s.Workboard.Scheduler.AcceptanceJudge.MaxInputTokens = 0 },
		"negative input ceiling":  func(s *Settings) { s.Workboard.Scheduler.AcceptanceJudge.MaxInputTokens = -1 },
		"large input ceiling":     func(s *Settings) { s.Workboard.Scheduler.AcceptanceJudge.MaxInputTokens = 1_000_000_001 },
		"zero output ceiling":     func(s *Settings) { s.Workboard.Scheduler.AcceptanceJudge.MaxOutputTokens = 0 },
		"negative output ceiling": func(s *Settings) { s.Workboard.Scheduler.AcceptanceJudge.MaxOutputTokens = -1 },
		"large output ceiling":    func(s *Settings) { s.Workboard.Scheduler.AcceptanceJudge.MaxOutputTokens = 1_000_000_001 },
		"combined token overflow": func(s *Settings) {
			s.Workboard.Scheduler.AcceptanceJudge.MaxInputTokens = 1_000_000_000
			s.Workboard.Scheduler.AcceptanceJudge.MaxOutputTokens = 1
			s.Models[1].ContextTokens = 1_000_000_000
		},
		"model context overflow": func(s *Settings) { s.Workboard.Scheduler.AcceptanceJudge.MaxInputTokens = 4097 },
		"short judge timeout":    func(s *Settings) { s.Workboard.Scheduler.AcceptanceJudge.Timeout = "99ms" },
		"long judge timeout":     func(s *Settings) { s.Workboard.Scheduler.AcceptanceJudge.Timeout = "5m1ns" },
		"invalid judge timeout":  func(s *Settings) { s.Workboard.Scheduler.AcceptanceJudge.Timeout = "later" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			s := validWorkboardSchedulerSettings()
			mutate(&s)
			if err := s.Validate(); err == nil {
				t.Fatal("unsafe scheduler configuration accepted")
			}
		})
	}
}

func TestEnabledWorkboardSchedulerLoadsFromYAML(t *testing.T) {
	body := `providers:
  - id: local
    kind: ollama
    endpoint: http://127.0.0.1:11434
models:
  - id: worker
    provider: local
    model: worker-native
    locality: local
    capabilities: [chat]
    context_tokens: 8192
    estimated_cost: 0
  - id: reviewer
    provider: local
    model: reviewer-native
    locality: local
    capabilities: [chat, audit]
    context_tokens: 8192
    estimated_cost: 0
workboard:
  scheduler:
    enabled: true
    worker_model: worker
    acceptance_judge:
      enabled: true
      reviewer_model: reviewer
      max_cost: 0.25
      max_input_tokens: 4096
      max_output_tokens: 4096
      timeout: 30s
`
	s, err := Load(Options{ProjectFile: file(t, body)})
	if err != nil {
		t.Fatalf("valid scheduler YAML rejected: %v", err)
	}
	if s.Workboard.Scheduler.WorkerModel != "worker" || s.Workboard.Scheduler.AcceptanceJudge.ReviewerModel != "reviewer" || s.Workboard.Scheduler.AcceptanceJudge.MaxCost != .25 || s.Workboard.Scheduler.AcceptanceJudge.MaxInputTokens != 4096 || s.Workboard.Scheduler.AcceptanceJudge.MaxOutputTokens != 4096 {
		t.Fatalf("scheduler YAML decoded incorrectly: %+v", s.Workboard.Scheduler)
	}
}

func TestWorkboardSchedulerJudgeCostAndTimeoutBoundaries(t *testing.T) {
	for _, test := range []struct {
		cost    float64
		timeout string
	}{{math.SmallestNonzeroFloat64, "100ms"}, {maxWorkboardJudgeCost, "5m"}} {
		s := validWorkboardSchedulerSettings()
		s.Workboard.Scheduler.AcceptanceJudge.MaxCost = test.cost
		*s.Models[1].EstimatedCost = 0
		s.Workboard.Scheduler.AcceptanceJudge.Timeout = test.timeout
		if err := s.Validate(); err != nil {
			t.Fatalf("valid acceptance boundary rejected: cost=%g timeout=%q: %v", test.cost, test.timeout, err)
		}
	}
}

func TestWorkboardAcceptanceJudgeCannotRunWithoutScheduler(t *testing.T) {
	s := validWorkboardSchedulerSettings()
	s.Workboard.Scheduler.Enabled = false
	if err := s.Validate(); err == nil {
		t.Fatal("active acceptance judge without scheduler accepted")
	}
}

func TestCloudOnlyModeRejectsLocalOnlyAcceptanceReviewer(t *testing.T) {
	s := validWorkboardSchedulerSettings()
	s.Mode = "cloud_only"
	if err := s.Validate(); err == nil {
		t.Fatal("cloud-only scheduler accepted despite mandatory local reviewer")
	}
}

func validWorkboardSchedulerSettings() Settings {
	s := Defaults()
	workerCost, reviewerCost := .5, .25
	s.Providers = []Provider{
		{ID: "cloud", Kind: "openai_compatible", Endpoint: "https://example.invalid/v1"},
		{ID: "local", Kind: "ollama", Endpoint: DefaultOllamaEndpoint},
	}
	s.Models = []Model{
		{ID: "worker", Provider: "cloud", Model: "worker-native", Locality: "cloud", ContextTokens: 8192, EstimatedCost: &workerCost, Capabilities: []string{"chat"}},
		{ID: "reviewer", Provider: "local", Model: "reviewer-native", Locality: "local", ContextTokens: 8192, EstimatedCost: &reviewerCost, Capabilities: []string{"chat", "audit"}},
	}
	s.Workboard.Scheduler.Enabled = true
	s.Workboard.Scheduler.WorkerModel = "worker"
	s.Workboard.Scheduler.AcceptanceJudge = WorkboardAcceptanceJudge{
		Enabled: true, ReviewerModel: "reviewer", MaxCost: .25, MaxInputTokens: 4096, MaxOutputTokens: 4096, Timeout: "30s",
	}
	return s
}

func TestWorkboardAcceptanceJudgeJSONDigestCompatibility(t *testing.T) {
	judge := Defaults().Workboard.Scheduler.AcceptanceJudge
	body, err := json.Marshal(judge)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte(`{"enabled":false,"reviewer_model":"","max_cost":0,"timeout":"30s"}`)
	if !bytes.Equal(body, want) {
		t.Fatalf("disabled acceptance judge JSON changed: %s", body)
	}
	judge.MaxInputTokens = 2048
	body, err = json.Marshal(judge)
	if err != nil || !bytes.Contains(body, []byte(`"max_input_tokens":2048`)) {
		t.Fatalf("input-token reservation absent from configuration fingerprint: %s %v", body, err)
	}
}

func TestWorkboardSchedulerValidation(t *testing.T) {
	tests := map[string]func(*Settings){
		"feature disabled": func(s *Settings) { s.Workboard.Enabled = false },
		"short interval":   func(s *Settings) { s.Workboard.Scheduler.Interval = "249ms" },
		"long interval":    func(s *Settings) { s.Workboard.Scheduler.Interval = "24h1ns" },
		"invalid interval": func(s *Settings) { s.Workboard.Scheduler.Interval = "invalid" },
		"zero claims":      func(s *Settings) { s.Workboard.Scheduler.MaxActiveClaims = 0 },
		"too many claims":  func(s *Settings) { s.Workboard.Scheduler.MaxActiveClaims = 65 },
		"worker overflow": func(s *Settings) {
			s.Workboard.Scheduler.Enabled = true
			s.Workboard.Scheduler.MaxActiveClaims = s.Workers.Max + 1
		},
		"zero scan":  func(s *Settings) { s.Workboard.Scheduler.CardScanLimit = 0 },
		"large scan": func(s *Settings) { s.Workboard.Scheduler.CardScanLimit = 10001 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			s := Defaults()
			mutate(&s)
			if err := s.Validate(); err == nil {
				t.Fatal("invalid workboard scheduler settings accepted")
			}
		})
	}
	for _, interval := range []string{"250ms", "24h"} {
		s := Defaults()
		s.Workboard.Scheduler.Interval = interval
		if err := s.Validate(); err != nil {
			t.Fatalf("boundary interval %q rejected: %v", interval, err)
		}
	}
}

func TestWorkboardSchedulerSchemaIsStrict(t *testing.T) {
	for _, body := range []string{
		"workboard:\n  unknown: true\n",
		"workboard:\n  scheduler:\n    unknown: true\n",
		"workboard:\n  scheduler:\n    max_active_claims: 2.5\n",
		"workboard:\n  scheduler:\n    card_scan_limit: \"10\"\n",
		"workboard:\n  scheduler:\n    worker_model: 3\n",
		"workboard:\n  scheduler:\n    acceptance_judge:\n      unknown: true\n",
		"workboard:\n  scheduler:\n    acceptance_judge:\n      enabled: yes\n",
		"workboard:\n  scheduler:\n    acceptance_judge:\n      max_cost: \"1\"\n",
		"workboard:\n  scheduler:\n    acceptance_judge:\n      max_input_tokens: 1.5\n",
	} {
		if _, err := Load(Options{ProjectFile: file(t, body)}); err == nil {
			t.Fatalf("invalid workboard schema accepted: %q", body)
		}
	}
}
