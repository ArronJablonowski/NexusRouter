package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
	"github.com/ArronJablonowski/DarwinRouter/submissions"
	"go.yaml.in/yaml/v3"
)

func resumeCLIArgs(config string) []string {
	return []string{"resume", "--config", config, "--key", "fixture-resume-key", "--task", "source", "--session", "session", "--sequence", "4", "--event", "recovery-terminal", "--model", "model"}
}

func cliRecoveredResumeSource(t *testing.T, database string) sessions.TaskHeadFence {
	t.Helper()
	ctx := context.Background()
	db, err := telemetry.Open(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	digest := func(value string) string {
		sum := sha256.Sum256([]byte(value))
		return hex.EncodeToString(sum[:])
	}
	body := []byte(`{"prompt":"private source request"}`)
	job, err := db.CreateSubmission(ctx, digest("source-key"), digest(string(body)), digest("source-config"), body)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	claim, err := db.ClaimSubmission(ctx, digest("source-config"), now, time.Minute)
	if err != nil || claim.Status.ID != job.ID {
		t.Fatal(claim.Status, err)
	}
	prefix := []runtime.Event{
		{Version: 1, ID: "source-start", TaskID: "source", SessionID: "session", CorrelationID: "source", Sequence: 1, Time: now, Kind: runtime.TaskStarted, Data: runtime.Data{SubmissionID: job.ID, Privacy: "local_only", Messages: []providers.Message{{Role: "user", Content: "private history"}}}},
		{Version: 1, ID: "source-turn", TaskID: "source", SessionID: "session", CorrelationID: "source", Sequence: 2, Time: now.Add(time.Second), Kind: runtime.TurnStarted, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{ProviderID: "local", ModelID: "chat"}},
		{Version: 1, ID: "source-delta", TaskID: "source", SessionID: "session", CorrelationID: "source", Sequence: 3, Time: now.Add(2 * time.Second), Kind: runtime.ModelDelta, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{Text: "private partial"}},
	}
	for _, event := range prefix {
		if err = db.AppendSubmission(ctx, event.Sequence-1, event, job.ID, claim.Token); err != nil {
			t.Fatal(err)
		}
	}
	if ok, recoverErr := db.RecoverInterruptedModel(ctx, job.ID, digest("source-config"), now.Add(2*time.Minute)); recoverErr != nil || !ok {
		t.Fatal(ok, recoverErr)
	}
	fence, err := db.ResumeSource(ctx, "source")
	if err != nil {
		t.Fatal(err)
	}
	return sessions.TaskHeadFence{Version: 1, TaskID: fence.TaskID, SessionID: fence.SessionID, HeadSequence: fence.HeadSequence, HeadEventID: fence.HeadEventID}
}

func TestResumeParserReusesStrictHistoryIntakeOptions(t *testing.T) {
	options, request, key, source, err := parseResumeArgs(append(resumeCLIArgs("config.yaml")[1:], "--local-required", "--capability", "chat"))
	if err != nil || options.ProjectFile != "config.yaml" || key != "fixture-resume-key" || request.ModelID != "model" || !request.LocalRequired || len(request.Capabilities) != 1 || source.TaskID != "source" || source.HeadSequence != 4 || source.HeadEventID != "recovery-terminal" {
		t.Fatal(options, request, key, source, err)
	}
	for _, extra := range [][]string{{"--task", "other"}, {"--sequence", "04"}, {"--continue-task", "source"}, {"--json"}, {"private-extra"}} {
		var out, diagnostic bytes.Buffer
		args := append(append([]string{}, resumeCLIArgs("config.yaml")...), extra...)
		if code := RunWithInput(args, strings.NewReader("prompt"), &out, &diagnostic, "dev"); code != 2 || out.Len() != 0 || strings.Contains(diagnostic.String(), "private") {
			t.Fatal(extra, code, diagnostic.String())
		}
	}
}

func TestResumeCLIRejectsInvalidPromptBeforeStorage(t *testing.T) {
	dir := t.TempDir()
	database := filepath.Join(dir, "absent.db")
	configuration := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configuration, []byte(fmt.Sprintf("telemetry:\n  database: %q\n", database)), 0600); err != nil {
		t.Fatal(err)
	}
	var out, diagnostic bytes.Buffer
	if code := RunWithInput(resumeCLIArgs(configuration), strings.NewReader(" \n"), &out, &diagnostic, "dev"); code != 1 || out.Len() != 0 {
		t.Fatal(code, out.String(), diagnostic.String())
	}
	if _, err := os.Stat(database); !os.IsNotExist(err) {
		t.Fatal("invalid resume prompt created storage", err)
	}
}

func TestResumeCLIQueuesIdempotentlyWithoutExecutingOrMutatingSource(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	database := filepath.Join(dir, "resume.db")
	configuration := filepath.Join(dir, "config.yaml")
	fence := cliRecoveredResumeSource(t, database)
	var providerRequests atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { providerRequests.Add(1) }))
	defer provider.Close()
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Hardware.AutoProfile = false
	cfg.Hardware.Concurrent = "1"
	cfg.Telemetry.Database = database
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: provider.URL}}
	zero := 0.0
	cfg.Models = []config.Model{{ID: "chat", Provider: "local", Model: "fixture", Locality: "local", RAMBytes: 1, ContextTokens: 8192, Capabilities: []string{"chat"}, EstimatedCost: &zero}}
	body, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(configuration, body, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := telemetry.Open(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	before, err := db.Read(ctx, fence.TaskID, 0, 100)
	if err != nil || db.Close() != nil {
		t.Fatal(err)
	}
	args := resumeCLIArgs(configuration)
	args[10] = fmt.Sprint(fence.HeadSequence)
	args[12] = fence.HeadEventID
	_, _, _, parsedFence, parseErr := parseResumeArgs(args[1:])
	if parseErr != nil || parsedFence != fence {
		t.Fatal("resume CLI changed source fence", parsedFence, fence, parseErr)
	}
	var firstID string
	for i := 0; i < 2; i++ {
		var out, diagnostic bytes.Buffer
		if code := RunWithInput(args, strings.NewReader("new resume prompt"), &out, &diagnostic, "dev"); code != 0 {
			t.Fatal(code, diagnostic.String())
		}
		var status submissions.Status
		if json.Unmarshal(out.Bytes(), &status) != nil || status.State != "queued" || status.ID == "" || len(status.TaskIDs) != 0 {
			t.Fatal(out.String())
		}
		if i == 0 {
			firstID = status.ID
		} else if status.ID != firstID {
			t.Fatal("resume was not idempotent", firstID, status.ID)
		}
	}
	var out, diagnostic bytes.Buffer
	if code := RunWithInput(args, strings.NewReader("changed intent"), &out, &diagnostic, "dev"); code != 1 || out.Len() != 0 || strings.Contains(diagnostic.String(), "changed") {
		t.Fatal("changed request did not fail safely", code, diagnostic.String())
	}
	stale := append([]string{}, args...)
	stale[4] = "different-resume-key"
	stale[12] = "different-terminal"
	out.Reset()
	diagnostic.Reset()
	if code := RunWithInput(stale, strings.NewReader("new resume prompt"), &out, &diagnostic, "dev"); code != 1 || out.Len() != 0 {
		t.Fatal("stale fence admitted", code, diagnostic.String())
	}
	db, err = telemetry.Open(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	after, err := db.Read(ctx, fence.TaskID, 0, 100)
	page, pageErr := db.ListSubmissions(ctx, submissions.ListOptions{Limit: 25})
	resumeCount := 0
	for _, item := range page.Items {
		if item.ID == firstID {
			resumeCount++
		}
	}
	if err != nil || pageErr != nil || !reflect.DeepEqual(before, after) || len(page.Items) != 2 || resumeCount != 1 || providerRequests.Load() != 0 {
		t.Fatal("CLI resume mutated source, duplicated work, or reached provider", err, pageErr, len(page.Items), providerRequests.Load())
	}
}
