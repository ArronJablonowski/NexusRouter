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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/codexbridge"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

// This supervised probe spends one native inference call. Source and approved
// summary are entirely synthetic; it does not infer human review from a model.
func TestLiveCodexCompactedContinuation(t *testing.T) {
	if os.Getenv("DARWIN_CODEX_LIVE_COMPACTION") != "1" {
		t.Skip("explicit supervised signed-in Sol compacted continuation only")
	}
	bin, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal("CLI unavailable")
	}
	bin, err = filepath.Abs(bin)
	if err != nil {
		t.Fatal("CLI path unavailable")
	}
	const marker = "AMBER-OTTER-7319"
	const original = "Synthetic source requirement: the recall marker is " + marker + ". This source-only sentence must be compacted away."
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		text := "Acknowledged the synthetic requirement."
		if n == 2 {
			text = `{"version":1,"summary":{"requirements":["The synthetic recall marker is ` + marker + `."]}}`
		}
		if n > 2 {
			t.Error("unexpected loopback inference")
		}
		body, _ := json.Marshal(text)
		fmt.Fprintf(w, "{\"message\":{\"content\":%s},\"done\":true,\"done_reason\":\"stop\"}\n", body)
	}))
	defer server.Close()
	cfg := codexTaskConfig(t)
	cfg.Providers[0].Executable = bin
	zero := 0.0
	cfg.Providers = append(cfg.Providers, config.Provider{ID: "synthetic", Kind: "ollama", Endpoint: server.URL})
	cfg.Models = append(cfg.Models, config.Model{ID: "synthetic", Provider: "synthetic", Model: "fixture", Locality: "cloud", ContextTokens: 4096, EstimatedCost: &zero, Capabilities: []string{"chat"}})
	svc, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal("fixture configuration invalid")
	}
	svc.profile = healthProfile
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	source, err := svc.Run(ctx, Request{ModelID: "synthetic", Prompt: original})
	if err != nil {
		t.Fatal("source fixture failed")
	}
	db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal("source inspection failed")
	}
	defer db.Close()
	before, err := db.Read(ctx, source.TaskID, 0, 100)
	if err != nil {
		t.Fatal("source journal unavailable")
	}
	attempt, err := svc.SummarizeTask(ctx, source.TaskID, "synthetic", 1, 0)
	if err != nil || attempt.Draft == nil || calls.Load() != 2 {
		t.Fatal("synthetic summary failed")
	}
	// Explicit trusted-host review fixture, not an automatic/model approval.
	review, err := svc.ReviewSummary(ctx, attempt.ID, "", "approved", "Synthetic human-review fixture: compared the exact marker against the source requirement.")
	if err != nil {
		t.Fatal("fixture review failed")
	}
	observed := &compactionLiveObservedProvider{t: t, marker: marker, original: original}
	launches, directory := 0, ""
	svc.codexLauncher = func(call context.Context, spec codexbridge.LaunchSpec) (taskProvider, error) {
		launches++
		directory = spec.CWD
		if launches != 1 || spec.Model != "gpt-5.6-sol" || spec.Privacy != "cloud_allowed" {
			t.Fatal("unexpected launch metadata")
		}
		p, err := codexbridge.LaunchChecked(call, spec)
		if err != nil {
			return nil, err
		}
		observed.taskProvider = p
		return observed, nil
	}
	result, runErr := svc.Run(ctx, Request{ModelID: "brain", ContinueTaskID: source.TaskID, SummaryAttemptID: attempt.ID, Prompt: "Reply with only the synthetic recall marker preserved in the approved summary. Do not use tools."})
	t.Logf("live compaction metadata: launches=%d streams=%d closes=%d done=%t", launches, observed.streams, observed.closes, observed.done)
	if directory != "" {
		if _, err := os.Stat(directory); !os.IsNotExist(err) {
			t.Fatal("owned continuation directory retained")
		}
	}
	if runErr != nil {
		t.Fatal("live continuation failed; payload withheld")
	}
	if launches != 1 || observed.streams != 1 || observed.closes != 1 || !observed.done || directory == "" || strings.TrimSpace(result.Text) != marker {
		t.Fatal("continuation protocol or recall failed; payload withheld")
	}
	replayed, err := sessions.Replay(ctx, db, result.TaskID)
	if err != nil || replayed.Compaction == nil || replayed.ParentTaskID != source.TaskID || replayed.Privacy != "cloud_allowed" {
		t.Fatal("continuation provenance missing")
	}
	checkpoint := *replayed.Compaction
	if checkpoint.SummaryAttemptID != attempt.ID || checkpoint.SummaryReviewID != review.ID {
		t.Fatal("approved summary attribution missing")
	}
	checkpoint.SummaryAttemptID, checkpoint.SummaryReviewID = "", ""
	if !reflect.DeepEqual(&checkpoint, attempt.Draft.Checkpoint) {
		t.Fatal("approved checkpoint changed")
	}
	after, err := db.Read(ctx, source.TaskID, 0, 100)
	if err != nil || !reflect.DeepEqual(before, after) || calls.Load() != 2 {
		t.Fatal("source changed or redispatched")
	}
	t.Log("live compaction result: recalled=true checkpoint_exact=true source_unchanged=true; import RPC count not observed")
}

type compactionLiveObservedProvider struct {
	taskProvider
	t                *testing.T
	marker, original string
	streams, closes  int
	done             bool
}

func (p *compactionLiveObservedProvider) Stream(ctx context.Context, r providers.Request, emit func(providers.Chunk) error) error {
	p.streams++
	encoded, _ := json.Marshal(r.Messages)
	if p.streams != 1 || r.Model != "gpt-5.6-sol" || len(r.Tools) != 0 || len(r.Messages) < 3 || !strings.Contains(string(encoded), p.marker) || strings.Contains(string(encoded), p.original) || strings.Contains(r.Messages[len(r.Messages)-1].Content, p.marker) || codexbridge.ValidateInitialMessages(r.Messages) != nil {
		p.t.Fatal("invalid compacted continuation envelope")
	}
	return p.taskProvider.Stream(ctx, r, func(c providers.Chunk) error {
		if c.ToolCall != nil {
			p.t.Error("unexpected continuation tool call")
		}
		p.done = p.done || c.Done
		return emit(c)
	})
}
func (p *compactionLiveObservedProvider) Close() error { p.closes++; return p.taskProvider.Close() }
