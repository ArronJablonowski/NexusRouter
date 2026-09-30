package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

func TestSkillComparisonSelectionHTTPActualIndex(t *testing.T) {
	t.Setenv("DARWIN_PROCESS_OWNER_DIR", "")
	ctx := context.Background()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.Skills.Root = filepath.Join(dir, "absent-skills")
	cfg.Skills.Scope = "project"
	cfg.Skills.Enabled = false
	cfg.Telemetry.Database = filepath.Join(dir, "tasks.db")
	input, _ := selectionAPIFixture(t)
	zero := 0.0
	cfg.Providers = []config.Provider{{ID: "provider", Kind: "ollama", Endpoint: "http://127.0.0.1:1"}}
	cfg.Models = []config.Model{{ID: input.ModelID, Provider: "provider", Model: "model:tag", Locality: "local", RAMBytes: 1, ContextTokens: 4096, EstimatedCost: &zero, Capabilities: []string{"general"}}}
	svc, err := app.NewService(cfg, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	s := services()
	s.SelectSkillComparison = svc.SelectSkillComparison
	h, _ := New(token, 1, s)
	body, _ := json.Marshal(input)
	call := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request("POST", "/v1/skills/comparison/select", string(body)))
		return w
	}
	if w := call(); w.Code != 503 {
		t.Fatal("missing store admitted", w.Code)
	}
	if _, err := os.Stat(cfg.Telemetry.Database); !os.IsNotExist(err) {
		t.Fatal("created database")
	}
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("private-task-%d", i)
		version := input.BaselineVersion
		privacy := "local_only"
		if i == 1 {
			version = input.CandidateVersion
		}
		if i == 2 {
			privacy = "cloud_allowed"
		}
		for j, kind := range []runtime.Kind{runtime.TaskStarted, runtime.TaskCanceled} {
			e := runtime.Event{Version: 1, ID: fmt.Sprintf("event-%d-%d", i, j), TaskID: id, SessionID: id, CorrelationID: id, Sequence: int64(j + 1), Time: time.Now().UTC(), Kind: kind}
			if j == 0 {
				e.Data.Privacy = privacy
				e.Data.Messages = []providers.Message{{Role: "user", Content: "private-prompt"}}
				e.Data.SkillContext = &runtime.SkillContextUse{Version: 1, Complete: true, References: []runtime.SkillReference{{Scope: "project", Name: input.Name, Version: version, Digest: strings.Repeat("c", 64)}}}
			}
			if err := db.Append(ctx, int64(j), e); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	w := call()
	var got skills.ComparisonSelectionReport
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Validate() != nil || got.Baseline.Selected != 1 || got.Candidate.Selected != 1 || got.Watermark != 3 || got.Comparison == nil || got.Comparison.Excluded["nonfinal_outcome"] != 2 {
		t.Fatal("index selection failed", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "private-prompt") || strings.Contains(w.Body.String(), "private-task-2") || !got.Comparison.AdvisoryOnly {
		t.Fatal("payload, excluded source or authority escaped")
	}
	if got.Sources == nil || len(got.Sources.Tasks) != 2 || got.Sources.Tasks[0] != "private-task-0" || got.Sources.Tasks[1] != "private-task-1" {
		t.Fatal("exact selected source metadata missing")
	}
	w = call()
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	after, err := os.ReadFile(cfg.Telemetry.Database)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("inspection mutated database", err)
	}
	if _, err := os.Stat(cfg.Skills.Root); !os.IsNotExist(err) {
		t.Fatal("catalog created")
	}
}
