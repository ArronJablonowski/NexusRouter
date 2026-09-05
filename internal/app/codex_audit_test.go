package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/internal/codexbridge"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/routing"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

type codexAuditProviderFixture struct {
	stream func(context.Context, providers.Request, func(providers.Chunk) error) error
	closed int
}

func (p *codexAuditProviderFixture) Close() error { p.closed++; return nil }
func (p *codexAuditProviderFixture) Models(context.Context) ([]string, error) {
	return []string{"gpt-5.6-sol"}, nil
}
func (p *codexAuditProviderFixture) Stream(ctx context.Context, r providers.Request, emit func(providers.Chunk) error) error {
	return p.stream(ctx, r, emit)
}

func codexAuditSource(t *testing.T) (*Service, string, *telemetry.Store) {
	t.Helper()
	original, cfg := autoFixture(t)
	cfg.Mode = "hybrid"
	cfg.Models[0].Locality = "cloud"
	cfg.Memory.Enabled = false
	cfg.Skills.Enabled = false
	cfg.Tools.Enabled = false
	cfg.Evaluation.Judge = true
	cfg.Providers = append(cfg.Providers, config.Provider{ID: "codex-auditor", Kind: "codex_app_server", Executable: "/fixture/codex"})
	zero := 0.0
	cfg.Models = append(cfg.Models, config.Model{ID: "brain", Provider: "codex-auditor", Model: "gpt-5.6-sol", Locality: "cloud", ContextTokens: 16384, EstimatedCost: &zero, Capabilities: []string{"chat"}})
	svc, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc.profile = original.profile
	source, err := svc.Run(context.Background(), Request{ModelID: "a", Prompt: "creative task with private-token", Domain: "creative", Profile: "default"})
	if err != nil {
		t.Fatal(err)
	}
	db, err := telemetry.OpenReadOnly(context.Background(), cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	snapshot, err := db.TaskSnapshot(context.Background(), source.TaskID)
	if err != nil || snapshot.State != "completed" || snapshot.Privacy != "cloud_allowed" {
		t.Fatal("source not cloud-admitted", snapshot, err)
	}
	svc.secret = func(name string) string {
		if name == "DARWIN_API_TOKEN" {
			return "private-token"
		}
		return ""
	}
	return svc, source.TaskID, db
}

func TestCodexAuditPersistsIndependentReviewAfterAdmission(t *testing.T) {
	svc, task, db := codexAuditSource(t)
	ctx := context.Background()
	before, err := db.Read(ctx, task, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	key := routing.Key{Model: "a", Provider: "local", Domain: "creative", Profile: "default"}
	// Explicit execution does not itself populate routing fitness. Seed known
	// evidence for this actual completed attempt to detect accidental mutation.
	writer, err := telemetry.Open(ctx, svc.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	attemptID := ""
	for _, e := range before {
		if e.Kind == runtime.TurnStarted {
			attemptID = e.AttemptID
		}
	}
	err = writer.RecordEvaluation(ctx, evaluation.Record{Version: 1, ID: "source-evidence", TaskID: task, AttemptID: attemptID, Key: key, Checks: []evaluation.Check{{Source: evaluation.Deterministic, Reference: "fixture.nonempty", Passed: true}}, ExecutionSucceeded: true, Time: time.Now()})
	writer.Close()
	if err != nil {
		t.Fatal(err)
	}
	fitness, fitnessErr := db.Fitness(ctx, key)
	if fitnessErr != nil || fitness.Samples < 1 {
		t.Fatal("source fitness fixture missing", fitness, fitnessErr)
	}
	estimated := false
	svc.contextEstimator = auxiliaryContextEstimator(func(context.Context, providers.Request) (int, error) { estimated = true; return 1, nil })
	launches, streams := 0, 0
	dir := ""
	p := &codexAuditProviderFixture{}
	p.stream = func(ctx context.Context, r providers.Request, emit func(providers.Chunk) error) error {
		streams++
		if len(r.Tools) != 0 || len(r.Messages) != 2 || r.Messages[0].Role != "system" || r.Messages[1].Role != "user" || r.Model != "gpt-5.6-sol" {
			t.Fatal("invalid audit envelope")
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > time.Minute || time.Until(deadline) <= 0 {
			t.Fatal("unbounded review")
		}
		if strings.Contains(r.Messages[1].Content, "private-token") || !strings.Contains(r.Messages[1].Content, "[REDACTED]") {
			t.Fatal("audit disclosed current secret")
		}
		a := evaluation.Audit{Version: 1, EvaluatorID: "brain", RubricVersion: "darwin-review-v2", Domain: "creative", Verdict: "reject", Confidence: .4, Findings: []evaluation.AuditFinding{{Summary: "private-token advisory", EvidenceRefs: []string{"candidate"}}}}
		body, _ := json.Marshal(a)
		return emit(providers.Chunk{Text: string(body), Done: true, FinishReason: "stop", Usage: &providers.Usage{InputTokens: 10, OutputTokens: 20}})
	}
	svc.codexLauncher = func(_ context.Context, spec codexbridge.LaunchSpec) (taskProvider, error) {
		launches++
		dir = spec.CWD
		if !estimated || spec.Privacy != "cloud_allowed" || spec.Model != "gpt-5.6-sol" {
			t.Fatal("launch preceded admission")
		}
		attempts, err := db.ReviewAttempts(ctx, task, "", 100)
		if err != nil || len(attempts) != 1 || attempts[0].Status != "started" {
			t.Fatal("launch preceded durable start", attempts, err)
		}
		return p, nil
	}
	record, err := svc.AuditTask(ctx, task, "brain", 0)
	if err != nil || launches != 1 || streams != 1 || p.closed != 1 || record.EvaluatorModel != "gpt-5.6-sol" || record.EvaluatorProvider != "codex-auditor" || record.Audit.Verdict != "reject" {
		t.Fatal(record, err, launches, streams, p.closed)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("audit directory retained", err)
	}
	if strings.Contains(record.Audit.Findings[0].Summary, "private-token") {
		t.Fatal("finding secret not redacted")
	}
	saved, err := db.Audit(ctx, record.ID)
	if err != nil || saved.ID != record.ID || saved.TaskID != task {
		t.Fatal(saved, err)
	}
	attempts, err := db.ReviewAttempts(ctx, task, "", 100)
	if err != nil || len(attempts) != 1 || attempts[0].Status != "completed" || attempts[0].AuditID != record.ID {
		t.Fatal(attempts, err)
	}
	after, err := db.Read(ctx, task, 0, 100)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("audit changed source journal", err)
	}
	afterFitness, afterErr := db.Fitness(ctx, key)
	if !reflect.DeepEqual(fitness, afterFitness) || !reflect.DeepEqual(fitnessErr, afterErr) {
		t.Fatal("audit changed fitness")
	}
}

func TestCodexAuditFailureLifecycleAndCleanup(t *testing.T) {
	for _, mode := range []string{"malformed", "tool", "cancel", "panic"} {
		t.Run(mode, func(t *testing.T) {
			svc, task, db := codexAuditSource(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			p := &codexAuditProviderFixture{}
			dir := ""
			p.stream = func(_ context.Context, r providers.Request, emit func(providers.Chunk) error) error {
				if len(r.Tools) != 0 {
					t.Fatal("audit tools enabled")
				}
				switch mode {
				case "cancel":
					cancel()
					return ctx.Err()
				case "panic":
					panic("private provider payload")
				case "tool":
					return emit(providers.Chunk{ToolCall: &providers.ToolCall{ID: "call", Name: "delegate", Arguments: json.RawMessage(`{}`)}})
				default:
					return emit(providers.Chunk{Text: "private invalid review", Done: true, FinishReason: "stop"})
				}
			}
			svc.codexLauncher = func(_ context.Context, spec codexbridge.LaunchSpec) (taskProvider, error) {
				dir = spec.CWD
				return p, nil
			}
			if _, err := svc.AuditTask(ctx, task, "brain", 0); err == nil {
				t.Fatal("failed review accepted")
			}
			if p.closed != 1 {
				t.Fatal("provider not closed", p.closed)
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatal("audit directory retained")
			}
			attempts, err := db.ReviewAttempts(context.Background(), task, "", 100)
			code := "review_failed"
			if mode == "cancel" {
				code = "canceled"
			}
			if err != nil || len(attempts) != 1 || attempts[0].Status != "failed" || attempts[0].Code != code {
				t.Fatal(attempts, err)
			}
			body, _ := json.Marshal(attempts)
			if strings.Contains(string(body), "private") {
				t.Fatal("failure detail persisted")
			}
			audits, err := db.Audits(context.Background(), task, "", 100)
			if err != nil || len(audits) != 0 {
				t.Fatal("failed review persisted audit", audits, err)
			}
		})
	}
}

func TestCodexAuditDeniedBeforeLaunch(t *testing.T) {
	for _, mode := range []string{"privacy", "local_mode", "self_review", "budget", "no_context", "context_budget", "estimator"} {
		t.Run(mode, func(t *testing.T) {
			svc, task, db := codexAuditSource(t)
			model := &svc.settings.Models[len(svc.settings.Models)-1]
			switch mode {
			case "privacy":
				// Produce a genuinely local second source through the existing local fixture.
				out, err := svc.Run(context.Background(), Request{ModelID: "z", Prompt: "local", Domain: "creative"})
				if err != nil {
					t.Fatal(err)
				}
				task = out.TaskID
			case "local_mode":
				svc.settings.Mode = "local_only"
			case "self_review":
				model.Provider = "local"
				model.Model = "a"
			case "budget":
				cost := 1.0
				model.EstimatedCost = &cost
			case "no_context":
				model.ContextTokens = 0
			case "context_budget":
				model.ContextTokens = 1
			case "estimator":
				svc.contextEstimator = auxiliaryContextEstimator(func(context.Context, providers.Request) (int, error) { return 0, errors.New("private estimator") })
			}
			svc.codexLauncher = func(context.Context, codexbridge.LaunchSpec) (taskProvider, error) {
				t.Fatal("denial launched Codex")
				return nil, nil
			}
			if _, err := svc.AuditTask(context.Background(), task, "brain", 0); err == nil {
				t.Fatal("denied audit admitted")
			}
			attempts, err := db.ReviewAttempts(context.Background(), task, "", 100)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "context_budget" || mode == "estimator" {
				if len(attempts) != 1 || attempts[0].Status != "failed" {
					t.Fatal("estimation failure not durable", attempts)
				}
			} else if len(attempts) != 0 {
				t.Fatal("preflight denial created attempt", attempts)
			}
		})
	}
}

func TestCodexAutomaticAuditPreservesCandidateAndDoesNotRecurse(t *testing.T) {
	for _, mode := range []string{"valid", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			svc, prior, db := codexAuditSource(t)
			svc.settings.Evaluation.AutoReviewModel = "brain"
			launches, streams := 0, 0
			p := &codexAuditProviderFixture{}
			p.stream = func(ctx context.Context, r providers.Request, emit func(providers.Chunk) error) error {
				streams++
				if len(r.Tools) != 0 || r.Model != "gpt-5.6-sol" {
					t.Fatal("invalid automatic audit provider request")
				}
				text := "malformed-private-audit"
				if mode == "valid" {
					body, _ := json.Marshal(evaluation.Audit{Version: 1, EvaluatorID: "brain", RubricVersion: "darwin-review-v2", Domain: "creative", Verdict: "abstain", Confidence: 0, Findings: []evaluation.AuditFinding{}})
					text = string(body)
				}
				return emit(providers.Chunk{Text: text, Done: true, FinishReason: "stop"})
			}
			svc.codexLauncher = func(context.Context, codexbridge.LaunchSpec) (taskProvider, error) { launches++; return p, nil }
			out, err := svc.Run(context.Background(), Request{ModelID: "a", Prompt: "new creative task", Domain: "creative"})
			if err != nil || out.Text != "a" || launches != 1 || streams != 1 || p.closed != 1 || out.TaskID == prior {
				t.Fatal("audit changed candidate or recursed", out, err, launches, streams, p.closed)
			}
			wantStatus, wantAttempt := "recorded", "completed"
			if mode == "malformed" {
				wantStatus, wantAttempt = "failed", "failed"
			}
			if out.AuditStatus != wantStatus || (out.AuditID != "") != (mode == "valid") {
				t.Fatal(out)
			}
			snapshot, err := db.TaskSnapshot(context.Background(), out.TaskID)
			if err != nil || snapshot.State != "completed" || snapshot.Privacy != "cloud_allowed" {
				t.Fatal("audit changed durable candidate status", snapshot, err)
			}
			attempts, err := db.ReviewAttempts(context.Background(), out.TaskID, "", 100)
			if err != nil || len(attempts) != 1 || attempts[0].Status != wantAttempt {
				t.Fatal(attempts, err)
			}
			audits, err := db.Audits(context.Background(), out.TaskID, "", 100)
			wantCount := 1
			if mode == "malformed" {
				wantCount = 0
			}
			if err != nil || len(audits) != wantCount {
				t.Fatal(audits, err)
			}
			if mode == "valid" && (audits[0].ID != out.AuditID || attempts[0].AuditID != out.AuditID) {
				t.Fatal("unlinked automatic audit")
			}
			priorAttempts, err := db.ReviewAttempts(context.Background(), prior, "", 100)
			if err != nil || len(priorAttempts) != 0 {
				t.Fatal("automatic audit reached prior task", priorAttempts, err)
			}
		})
	}
}

func TestCodexAuditBeginReviewFailureDoesNotLaunch(t *testing.T) {
	svc, task, db := codexAuditSource(t)
	raw, err := sql.Open("sqlite", svc.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err = raw.Exec(`CREATE TRIGGER reject_codex_review_start BEFORE INSERT ON review_attempts BEGIN SELECT RAISE(ABORT,'fixture review start failed'); END`); err != nil {
		t.Fatal(err)
	}
	launches := 0
	svc.codexLauncher = func(context.Context, codexbridge.LaunchSpec) (taskProvider, error) {
		launches++
		return nil, errors.New("unexpected launch")
	}
	if _, err := svc.AuditTask(context.Background(), task, "brain", 0); err == nil || launches != 0 {
		t.Fatal("review dispatched before durable start", err, launches)
	}
	attempts, err := db.ReviewAttempts(context.Background(), task, "", 100)
	if err != nil || len(attempts) != 0 {
		t.Fatal(attempts, err)
	}
	audits, err := db.Audits(context.Background(), task, "", 100)
	if err != nil || len(audits) != 0 {
		t.Fatal(audits, err)
	}
}
