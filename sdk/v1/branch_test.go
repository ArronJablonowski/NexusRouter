package v1_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

func TestSDKSubmitBranchGuardsBeforeStorage(t *testing.T) {
	ctx := context.Background()
	validFence := sdk.TaskHeadFence{Version: 1, TaskID: "source", SessionID: "session", HeadSequence: 2, HeadEventID: "source-terminal"}
	validRequest := sdk.Request{Version: 1, ModelID: "model", Prompt: "branch prompt"}
	for _, client := range []*sdk.Client{nil, {}} {
		if _, err := client.SubmitBranch(ctx, "fixture-branch-key", validFence, validRequest); !errors.Is(err, sdk.ErrAdmission) {
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
	bad := []struct {
		ctx     context.Context
		fence   sdk.TaskHeadFence
		request sdk.Request
	}{
		{nil, validFence, validRequest},
		{canceled, validFence, validRequest},
		{ctx, sdk.TaskHeadFence{}, validRequest},
		{ctx, validFence, sdk.Request{}},
		{ctx, validFence, sdk.Request{Version: 1, ModelID: "model", Prompt: "branch prompt", ContinueTaskID: "source"}},
	}
	for _, tc := range bad {
		if _, err = client.SubmitBranch(tc.ctx, "fixture-branch-key", tc.fence, tc.request); err == nil {
			t.Fatal("invalid branch accepted", tc)
		}
	}
	if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("guard created storage", err)
	}
}

func TestSDKSubmitBranchQueuesIdempotentChildFromExactFence(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "branch.db")
	db, err := telemetry.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	started := runtime.Event{Version: 1, ID: "source-start", TaskID: "source", SessionID: "session", CorrelationID: "source", Sequence: 1, Time: time.Unix(100, 0).UTC(), Kind: runtime.TaskStarted, Data: runtime.Data{Messages: []providers.Message{{Role: "user", Content: "private source"}}}}
	completed := runtime.Event{Version: 1, ID: "source-terminal", TaskID: "source", SessionID: "session", CorrelationID: "source", Sequence: 2, Time: time.Unix(101, 0).UTC(), Kind: runtime.TaskCompleted, Data: runtime.Data{Text: "private answer"}}
	if err = db.Append(ctx, 0, started); err != nil {
		t.Fatal(err)
	}
	if err = db.Append(ctx, 1, completed); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	client, err := sdk.New(sdk.ConfigOptions{Overrides: map[string]string{"telemetry.database": path}})
	if err != nil {
		t.Fatal(err)
	}
	fence := sdk.TaskHeadFence{Version: 1, TaskID: "source", SessionID: "session", HeadSequence: 2, HeadEventID: "source-terminal"}
	request := sdk.Request{Version: 1, ModelID: "model", Prompt: "private branch"}
	first, err := client.SubmitBranch(ctx, "fixture-branch-key", fence, request)
	if err != nil || first.State != "queued" || first.ID == "" {
		t.Fatal(first, err)
	}
	retry, err := client.SubmitBranch(ctx, "fixture-branch-key", fence, request)
	if err != nil || retry.ID != first.ID {
		t.Fatal(retry, err)
	}
	changed := request
	changed.Prompt = "different branch"
	if _, err = client.SubmitBranch(ctx, "fixture-branch-key", fence, changed); !errors.Is(err, submissions.ErrConflict) {
		t.Fatal("conflicting idempotency reuse accepted", err)
	}
	stale := fence
	stale.HeadEventID = "other-terminal"
	if _, err = client.SubmitBranch(ctx, "different-branch-key", stale, request); !errors.Is(err, sdk.ErrAdmission) {
		t.Fatal("stale source fence accepted", err)
	}
}
