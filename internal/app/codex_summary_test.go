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

	"github.com/ArronJablonowski/DarwinRouter/internal/codexbridge"
	"github.com/ArronJablonowski/DarwinRouter/providers"
)

const codexSummaryFixtureOutput = `{"version":1,"summary":{"requirements":["Preserve the creative request private-token"]}}`

func TestCodexSummaryDurableStartSchemaAndUnchangedSource(t *testing.T) {
	svc, task, db := codexAuditSource(t)
	ctx := context.Background()
	before, err := db.Read(ctx, task, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	estimated := false
	svc.contextEstimator = auxiliaryContextEstimator(func(_ context.Context, r providers.Request) (int, error) {
		estimated = true
		if len(r.JSONSchema) == 0 {
			t.Error("schema missing at estimation")
		}
		return 1, nil
	})
	p := &codexAuditProviderFixture{}
	launches, streams := 0, 0
	dir := ""
	p.stream = func(_ context.Context, r providers.Request, emit func(providers.Chunk) error) error {
		streams++
		if r.Model != "gpt-5.6-sol" || len(r.Tools) != 0 || len(r.Messages) != 2 || r.Messages[0].Role != "system" || r.Messages[1].Role != "user" {
			t.Fatal("invalid summary authority envelope")
		}
		if strings.Contains(r.Messages[1].Content, "private-token") || !strings.Contains(r.Messages[1].Content, "[REDACTED]") {
			t.Fatal("source secret escaped")
		}
		var envelope map[string]json.RawMessage
		if json.Unmarshal([]byte(r.Messages[1].Content), &envelope) != nil || envelope["messages"] == nil || envelope["first_retained_message"] == nil {
			t.Fatal("missing source envelope")
		}
		var schema map[string]any
		if json.Unmarshal(r.JSONSchema, &schema) != nil || schema["additionalProperties"] != false {
			t.Fatal("missing closed summary schema")
		}
		return emit(providers.Chunk{Text: codexSummaryFixtureOutput, Done: true, FinishReason: "stop"})
	}
	svc.codexLauncher = func(_ context.Context, spec codexbridge.LaunchSpec) (taskProvider, error) {
		launches++
		dir = spec.CWD
		if !estimated || spec.Model != "gpt-5.6-sol" || spec.Privacy != "cloud_allowed" {
			t.Fatal("launch preceded admission")
		}
		attempts, e := db.ListSummaryAttempts(ctx, task, "", 100)
		if e != nil || len(attempts) != 1 || attempts[0].Status != "started" {
			t.Fatal("launch preceded durable start", e)
		}
		return p, nil
	}
	attempt, err := svc.SummarizeTask(ctx, task, "brain", 1, 0)
	if err != nil || attempt.Status != "drafted" || attempt.Draft == nil || launches != 1 || streams != 1 || p.closed != 1 {
		t.Fatal(attempt.Status, err, launches, streams, p.closed)
	}
	body, _ := json.Marshal(attempt)
	if strings.Contains(string(body), "private-token") {
		t.Fatal("output secret persisted")
	}
	saved, err := db.SummaryAttempt(ctx, attempt.ID)
	if err != nil || !reflect.DeepEqual(saved, attempt) {
		t.Fatal("proposal not durable", err)
	}
	reviews, err := db.SummaryReviews(ctx, attempt.ID)
	if err != nil || len(reviews) != 0 {
		t.Fatal("summary was automatically approved", err)
	}
	after, err := db.Read(ctx, task, 0, 100)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("source changed", err)
	}
	if _, err = os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("owned cwd retained", err)
	}
}

func TestCodexSummaryFailureLifecycle(t *testing.T) {
	for _, mode := range []string{"malformed", "tool", "cancel", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			svc, task, db := codexAuditSource(t)
			before, _ := db.Read(context.Background(), task, 0, 100)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			p := &codexAuditProviderFixture{}
			dir := ""
			streams := 0
			p.stream = func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
				streams++
				switch mode {
				case "cancel":
					cancel()
					return ctx.Err()
				case "tool":
					return emit(providers.Chunk{ToolCall: &providers.ToolCall{ID: "call", Name: "delegate", Arguments: json.RawMessage(`{}`)}})
				default:
					return emit(providers.Chunk{Text: "private malformed source", Done: true, FinishReason: "stop"})
				}
			}
			svc.codexLauncher = func(_ context.Context, spec codexbridge.LaunchSpec) (taskProvider, error) {
				dir = spec.CWD
				if mode == "unavailable" {
					return nil, errors.New("private launch detail")
				}
				return p, nil
			}
			a, err := svc.SummarizeTask(ctx, task, "brain", 1, 0)
			if err == nil || a.Status != "failed" || a.Draft != nil {
				t.Fatal("invalid summary admitted", a.Status, err)
			}
			code := "summary_failed"
			if mode == "cancel" {
				code = "canceled"
			}
			attempts, err := db.ListSummaryAttempts(context.Background(), task, "", 100)
			if err != nil || len(attempts) != 1 || attempts[0].Status != "failed" || attempts[0].Code != code {
				t.Fatal(attempts, err)
			}
			body, _ := json.Marshal(attempts)
			if strings.Contains(string(body), "private") {
				t.Fatal("failure leaked raw detail")
			}
			if mode == "unavailable" {
				if streams != 0 {
					t.Fatal("unavailable CLI streamed")
				}
			} else if p.closed != 1 || streams != 1 {
				t.Fatal("provider lifecycle", p.closed, streams)
			}
			if _, err = os.Stat(dir); !os.IsNotExist(err) {
				t.Fatal("cwd retained")
			}
			after, err := db.Read(context.Background(), task, 0, 100)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("failure changed source")
			}
		})
	}
}

func TestCodexSummaryDeniedBeforeLaunch(t *testing.T) {
	for _, mode := range []string{"privacy", "local_only", "context", "estimator", "begin"} {
		t.Run(mode, func(t *testing.T) {
			svc, task, db := codexAuditSource(t)
			switch mode {
			case "privacy":
				out, err := svc.Run(context.Background(), Request{ModelID: "z", Prompt: "local source"})
				if err != nil {
					t.Fatal(err)
				}
				task = out.TaskID
			case "local_only":
				svc.settings.Mode = "local_only"
			case "context":
				svc.settings.Models[len(svc.settings.Models)-1].ContextTokens = 1
			case "estimator":
				svc.contextEstimator = auxiliaryContextEstimator(func(context.Context, providers.Request) (int, error) { return 0, errors.New("private estimation") })
			case "begin":
				raw, err := sql.Open("sqlite", svc.settings.Telemetry.Database)
				if err != nil {
					t.Fatal(err)
				}
				defer raw.Close()
				if _, err = raw.Exec(`CREATE TRIGGER deny_native_summary BEFORE INSERT ON summary_attempts BEGIN SELECT RAISE(ABORT,'fixture start denied'); END`); err != nil {
					t.Fatal(err)
				}
			}
			svc.codexLauncher = func(context.Context, codexbridge.LaunchSpec) (taskProvider, error) {
				t.Fatal("denied summary launched")
				return nil, nil
			}
			if _, err := svc.SummarizeTask(context.Background(), task, "brain", 1, 0); err == nil {
				t.Fatal("denied summary admitted")
			}
			attempts, err := db.ListSummaryAttempts(context.Background(), task, "", 100)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "context" || mode == "estimator" {
				if len(attempts) != 1 || attempts[0].Status != "failed" {
					t.Fatal("context denial not recorded", attempts)
				}
			} else if len(attempts) != 0 {
				t.Fatal("preflight denial recorded attempt")
			}
		})
	}
}

func TestCodexSummaryNativeConfigurationRequiresExactOptIn(t *testing.T) {
	for _, mode := range []string{"model", "executable"} {
		t.Run(mode, func(t *testing.T) {
			svc, _, _ := codexAuditSource(t)
			cfg := svc.settings
			if mode == "model" {
				cfg.Models[len(cfg.Models)-1].Model = "other-model"
			} else {
				cfg.Providers[len(cfg.Providers)-1].Executable = "relative/codex"
			}
			if next, err := NewService(cfg, nil); err == nil || next != nil {
				t.Fatal("invalid native configuration accepted")
			}
		})
	}
}

func TestCodexSummaryRotatingSecretFailsClosedOrRedactsOutput(t *testing.T) {
	for _, phase := range []string{"estimation", "startup", "output"} {
		t.Run(phase, func(t *testing.T) {
			svc, task, db := codexAuditSource(t)
			// The source already contains private-token. It becomes a configured
			// credential only after the initial redacted snapshot was assembled.
			secret := ""
			svc.secret = func(name string) string {
				if name == "DARWIN_API_TOKEN" {
					return secret
				}
				return ""
			}
			svc.contextEstimator = auxiliaryContextEstimator(func(context.Context, providers.Request) (int, error) {
				if phase == "estimation" {
					secret = "private-token"
				}
				return 1, nil
			})
			launches, streams := 0, 0
			p := &codexAuditProviderFixture{}
			p.stream = func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
				streams++
				secret = "new-output-credential"
				return emit(providers.Chunk{Text: `{"version":1,"summary":{"requirements":["Preserve new-output-credential requirement"]}}`, Done: true, FinishReason: "stop"})
			}
			dir := ""
			svc.codexLauncher = func(_ context.Context, spec codexbridge.LaunchSpec) (taskProvider, error) {
				launches++
				dir = spec.CWD
				if phase == "startup" {
					secret = "private-token"
				}
				return p, nil
			}
			a, err := svc.SummarizeTask(context.Background(), task, "brain", 1, 0)
			if phase == "output" {
				if err != nil || a.Status != "drafted" || streams != 1 || launches != 1 {
					t.Fatal(a.Status, err, streams, launches)
				}
				body, _ := json.Marshal(a)
				if strings.Contains(string(body), secret) || !strings.Contains(string(body), "[REDACTED]") {
					t.Fatal("fresh output credential persisted")
				}
			} else {
				if err == nil || a.Status != "failed" || streams != 0 {
					t.Fatal("rotated source credential dispatched", a.Status, err, streams)
				}
				want := 0
				if phase == "startup" {
					want = 1
				}
				if launches != want {
					t.Fatal("incorrect launch boundary", launches)
				}
			}
			if launches != 0 {
				if p.closed != 1 {
					t.Fatal("provider not closed")
				}
				if _, err := os.Stat(dir); !os.IsNotExist(err) {
					t.Fatal("cwd retained")
				}
			}
			saved, readErr := db.SummaryAttempt(context.Background(), a.ID)
			if readErr != nil || saved.Status != a.Status {
				t.Fatal("terminal not durable", readErr)
			}
		})
	}
}

func TestCodexSummaryMetadataRotationRejectsDraft(t *testing.T) {
	for _, metadata := range []string{"model", "task", "provider"} {
		t.Run(metadata, func(t *testing.T) {
			svc, task, db := codexAuditSource(t)
			ctx := context.Background()
			before, err := db.Read(ctx, task, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			secret := ""
			svc.secret = func(name string) string {
				if name == "DARWIN_API_TOKEN" {
					return secret
				}
				return ""
			}
			p := &codexAuditProviderFixture{}
			streams := 0
			p.stream = func(_ context.Context, r providers.Request, emit func(providers.Chunk) error) error {
				streams++
				switch metadata {
				case "model":
					secret = r.Model
				case "task":
					secret = task
				case "provider":
					secret = "codex-auditor"
				}
				return emit(providers.Chunk{Text: codexSummaryFixtureOutput, Done: true, FinishReason: "stop"})
			}
			svc.codexLauncher = func(context.Context, codexbridge.LaunchSpec) (taskProvider, error) { return p, nil }
			a, err := svc.SummarizeTask(ctx, task, "brain", 1, 0)
			if err == nil || a.Status != "failed" || a.Draft != nil || streams != 1 || p.closed != 1 {
				t.Fatal("newly secret metadata published a draft", a.Status, err, streams, p.closed)
			}
			attempts, err := db.ListSummaryAttempts(ctx, task, "", 100)
			if err != nil || len(attempts) != 1 || attempts[0].Status != "failed" || attempts[0].Draft != nil {
				t.Fatal("failed terminal not durable", err)
			}
			// Already-persisted start correlation is historical evidence, not
			// rewritten metadata: publication of a new draft is what must stop.
			if attempts[0].TaskID != task || attempts[0].Model != "gpt-5.6-sol" || attempts[0].Provider != "codex-auditor" {
				t.Fatal("durable attempt correlation rewritten")
			}
			after, err := db.Read(ctx, task, 0, 100)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("source changed", err)
			}
		})
	}
}
