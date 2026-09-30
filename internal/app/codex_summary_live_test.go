package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/codexbridge"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

// Only an explicitly supervised invocation may launch signed-in Sol. Source
// content is a synthetic loopback fixture; no user files or session are read.
func TestLiveCodexSummaryDraft(t *testing.T) {
	if os.Getenv("DARWIN_CODEX_LIVE_SUMMARY") != "1" {
		t.Skip("explicit supervised signed-in Sol summary only")
	}
	bin, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal("CLI unavailable")
	}
	bin, err = filepath.Abs(bin)
	if err != nil {
		t.Fatal("CLI path unavailable")
	}
	var sourceCalls atomic.Int32
	loopback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sourceCalls.Add(1)
		fmt.Fprintln(w, `{"message":{"content":"Decision: implement a pure Go Square(n int) int function returning n * n. Requirement: package arithmetic, no dependencies, no file writes. Pending work: add table-driven tests covering zero, positive, and negative inputs. No tests have been run and no artifacts were created."},"done":true,"done_reason":"stop"}`)
	}))
	defer loopback.Close()
	cfg := codexTaskConfig(t)
	cfg.Providers[0].Executable = bin
	cost, zero := 0.1, 0.0
	cfg.Models[0].EstimatedCost = &cost
	cfg.Providers = append(cfg.Providers, config.Provider{ID: "synthetic-summary-source", Kind: "ollama", Endpoint: loopback.URL})
	cfg.Models = append(cfg.Models, config.Model{ID: "synthetic", Provider: "synthetic-summary-source", Model: "synthetic-plan", Locality: "cloud", Capabilities: []string{"chat"}, RAMBytes: 1, ContextTokens: 4096, EstimatedCost: &zero})
	svc, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal("synthetic summary configuration invalid")
	}
	svc.profile = healthProfile
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	source, err := svc.Run(ctx, Request{ModelID: "synthetic", Prompt: "Plan a dependency-free Go Square function in package arithmetic. Do not write files or claim tests ran. Preserve the decision and list pending test cases for zero, positive, and negative inputs.", Domain: "code"})
	if err != nil || sourceCalls.Load() != 1 {
		t.Fatal("synthetic source execution failed")
	}
	db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal("source inspection unavailable")
	}
	defer db.Close()
	before, err := db.Read(ctx, source.TaskID, 0, 100)
	if err != nil {
		t.Fatal("source history unavailable")
	}
	snapshot, err := sessions.Replay(ctx, db, source.TaskID)
	if err != nil || snapshot.State != "completed" || snapshot.Privacy != "cloud_allowed" {
		t.Fatal("synthetic source not cloud eligible")
	}
	observed := &summaryLiveObservedProvider{t: t}
	launches := 0
	directory := ""
	svc.codexLauncher = func(call context.Context, spec codexbridge.LaunchSpec) (taskProvider, error) {
		launches++
		directory = spec.CWD
		if launches != 1 || spec.Model != "gpt-5.6-sol" || spec.Privacy != "cloud_allowed" {
			t.Fatal("unexpected native launch metadata")
		}
		attempts, err := db.ListSummaryAttempts(call, source.TaskID, "", 100)
		if err != nil || len(attempts) != 1 || attempts[0].Status != "started" {
			t.Fatal("summary launch preceded durable claim")
		}
		p, err := codexbridge.LaunchChecked(call, spec)
		if err != nil {
			return nil, err
		}
		observed.taskProvider = p
		return observed, nil
	}
	attempt, summaryErr := svc.SummarizeTask(ctx, source.TaskID, "brain", 1, cost)
	t.Logf("live summary protocol metadata: launches=%d streams=%d done=%t bytes=%d closes=%d", launches, observed.streams, observed.done, observed.bytes, observed.closes)
	if directory != "" {
		if _, err := os.Stat(directory); !os.IsNotExist(err) {
			t.Fatal("owned summary directory retained")
		}
	}
	if summaryErr != nil {
		t.Fatal("live Sol summary failed; payload withheld")
	}
	if launches != 1 || observed.streams != 1 || observed.closes != 1 || !observed.done || directory == "" || attempt.Status != "drafted" || attempt.Draft == nil {
		t.Fatal("summary metadata does not prove one durable draft")
	}
	_, expected, err := sessions.PrepareContinuation(snapshot, attempt.Draft.Request)
	if err != nil || !reflect.DeepEqual(expected, attempt.Draft.Checkpoint) || attempt.Draft.SourceSequence != snapshot.Sequence || attempt.Draft.SourceDigest != expected.SourceDigest {
		t.Fatal("summary provenance mismatch; payload withheld")
	}
	saved, err := db.SummaryAttempt(ctx, attempt.ID)
	if err != nil || !reflect.DeepEqual(saved, attempt) {
		t.Fatal("summary draft not durable")
	}
	after, err := db.Read(ctx, source.TaskID, 0, 100)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("summary applied itself or changed source")
	}
	if sourceCalls.Load() != 1 {
		t.Fatal("summary redispatched source")
	}
	t.Log("live summary result: drafted=true durable=true source_unchanged=true applied=false")
}

type summaryLiveObservedProvider struct {
	taskProvider
	t                      *testing.T
	streams, bytes, closes int
	done                   bool
}

func (p *summaryLiveObservedProvider) Stream(ctx context.Context, r providers.Request, emit func(providers.Chunk) error) error {
	p.streams++
	var schema map[string]any
	if p.streams != 1 || r.Model != "gpt-5.6-sol" || len(r.Messages) != 2 || r.Messages[0].Role != "system" || r.Messages[1].Role != "user" || len(r.Tools) != 0 || json.Unmarshal(r.JSONSchema, &schema) != nil || schema["additionalProperties"] != false {
		p.t.Fatal("invalid native summary request envelope")
	}
	return p.taskProvider.Stream(ctx, r, func(c providers.Chunk) error { p.bytes += len(c.Text); p.done = p.done || c.Done; return emit(c) })
}

func (p *summaryLiveObservedProvider) Close() error { p.closes++; return p.taskProvider.Close() }
