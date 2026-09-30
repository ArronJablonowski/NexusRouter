package v1_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/resources"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
	"github.com/ArronJablonowski/NexusRouter/submissions"
	"go.yaml.in/yaml/v3"
)

func TestSDKSubmitResumeGuardsBeforeStorage(t *testing.T) {
	ctx := context.Background()
	fence := sdk.TaskHeadFence{Version: 1, TaskID: "source", SessionID: "session", HeadSequence: 4, HeadEventID: "recovery-terminal"}
	request := sdk.Request{Version: 1, ModelID: "model", Prompt: "resume prompt"}
	for _, client := range []*sdk.Client{nil, {}} {
		if _, err := client.SubmitResume(ctx, "fixture-resume-key", fence, request); !errors.Is(err, sdk.ErrAdmission) {
			t.Fatal(err)
		}
	}
	path := filepath.Join(t.TempDir(), "absent.db")
	client, err := sdk.New(sdk.ConfigOptions{Overrides: map[string]string{"telemetry.database": path}})
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	for _, tc := range []struct {
		ctx     context.Context
		fence   sdk.TaskHeadFence
		request sdk.Request
	}{
		{nil, fence, request},
		{canceled, fence, request},
		{ctx, sdk.TaskHeadFence{}, request},
		{ctx, fence, sdk.Request{}},
		{ctx, fence, sdk.Request{Version: 1, ModelID: "model", Prompt: "   "}},
		{ctx, fence, sdk.Request{Version: 1, ModelID: "model", Prompt: "resume", Messages: []providers.Message{{Role: "user", Content: "alternate"}}}},
		{ctx, fence, sdk.Request{Version: 1, ModelID: "model", Prompt: "resume", ContinueTaskID: "source"}},
	} {
		if _, err = client.SubmitResume(tc.ctx, "fixture-resume-key", tc.fence, tc.request); err == nil {
			t.Fatal("invalid resume accepted", tc)
		}
	}
	if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("resume guard created storage", err)
	}
}

func sdkRecoveredResumeSource(t *testing.T, path string) sdk.TaskHeadFence {
	t.Helper()
	ctx := context.Background()
	db, err := telemetry.Open(ctx, path)
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
	return sdk.TaskHeadFence{Version: 1, TaskID: fence.TaskID, SessionID: fence.SessionID, HeadSequence: fence.HeadSequence, HeadEventID: fence.HeadEventID}
}

func TestSDKSubmitResumeDurableIdempotentAndSourceImmutable(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	database := filepath.Join(dir, "resume.db")
	configuration := filepath.Join(dir, "config.yaml")
	fence := sdkRecoveredResumeSource(t, database)
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Hardware.AutoProfile = false
	cfg.Hardware.Concurrent = "1"
	cfg.Telemetry.Database = database
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: "http://127.0.0.1:1"}}
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
	var builds atomic.Int32
	client, err := sdk.New(sdk.ConfigOptions{
		ProjectFile: configuration,
		ResourceProfiler: sdkFixtureProfiler(func(context.Context) (resources.Measurement, error) {
			return sdkGoodMeasurement(), nil
		}),
		ProviderFactory: sdkProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
			builds.Add(1)
			return nil, errors.New("must not construct provider")
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	request := sdk.Request{Version: 1, ModelID: "chat", Prompt: "new resume prompt"}
	first, err := client.SubmitResume(ctx, "fixture-resume-key", fence, request)
	if err != nil || first.State != "queued" || first.ID == "" || builds.Load() != 0 {
		t.Fatal(first, builds.Load(), err)
	}
	retry, err := client.SubmitResume(ctx, "fixture-resume-key", fence, request)
	if err != nil || retry.ID != first.ID || builds.Load() != 0 {
		t.Fatal(retry, builds.Load(), err)
	}
	changed := request
	changed.Prompt = "changed intent"
	if _, err = client.SubmitResume(ctx, "fixture-resume-key", fence, changed); !errors.Is(err, submissions.ErrConflict) {
		t.Fatal("changed request did not conflict", err)
	}
	stale := fence
	stale.HeadEventID = "different-terminal"
	if _, err = client.SubmitResume(ctx, "different-resume-key", stale, request); !errors.Is(err, sdk.ErrAdmission) {
		t.Fatal("stale resume fence admitted", err)
	}
	db, err = telemetry.Open(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	after, err := db.Read(ctx, fence.TaskID, 0, 100)
	if err != nil || !reflect.DeepEqual(before, after) || builds.Load() != 0 {
		t.Fatal("SDK intake mutated source or constructed provider", err, builds.Load())
	}
}
