package app

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	stdRuntime "runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

func TestInterruptedModelCrashProcessHelper(t *testing.T) {
	if os.Getenv("DARWIN_MODEL_CRASH_HELPER") != "1" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	body, err := os.ReadFile(os.Getenv("DARWIN_MODEL_CRASH_CONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg config.Settings
	if json.Unmarshal(body, &cfg) != nil {
		t.Fatal("invalid helper config")
	}
	svc, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc.profile = delegateProcessProfile
	r := Request{ModelID: "chat", Prompt: "start model-only work"}
	status, err := svc.Submit(ctx, "interrupted-model-fixture", r)
	if err != nil {
		t.Fatal(err)
	}
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	claim, err := db.ClaimSubmission(ctx, svc.submissionConfigDigest(), time.Now(), time.Minute)
	if err != nil || claim.Status.ID != status.ID {
		t.Fatal("claim unavailable", err)
	}
	r.submissionID, r.submissionToken = status.ID, claim.Token
	_, err = svc.RunStream(ctx, r, func(e runtime.Event) error {
		if e.Kind == runtime.ModelDelta {
			// RunStream delivers only after the journal append commits. Pause
			// inside that callback so no graceful return can finish the task.
			fmt.Println(status.ID)
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	})
	t.Fatal("helper was not killed at committed model delta", err)
}

func TestInterruptedModelRecoveredAfterAbruptProcessDeath(t *testing.T) {
	if stdRuntime.GOOS != "darwin" && stdRuntime.GOOS != "linux" {
		t.Skip("SIGKILL qualification requires Unix")
	}
	for _, cancelRequested := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelRequested), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			var calls atomic.Int32
			requests := make(chan []providers.Message, 2)
			disconnected := make(chan struct{}, 1)
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Messages []providers.Message `json:"messages"`
				}
				if json.NewDecoder(r.Body).Decode(&request) != nil {
					t.Error("invalid owned provider request")
					return
				}
				select {
				case requests <- request.Messages:
				case <-ctx.Done():
					return
				}
				if calls.Add(1) > 1 {
					fmt.Fprintln(w, `{"message":{"content":"fresh continuation answer"},"done":true,"done_reason":"stop"}`)
					return
				}
				fmt.Fprintln(w, `{"message":{"content":"partial-not-a-final-answer"},"done":false}`)
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
				case <-ctx.Done():
				}
				select {
				case disconnected <- struct{}{}:
				default:
				}
			}))
			defer provider.Close()
			defer cancel()
			cfg := config.Defaults()
			cfg.Mode = "local_only"
			cfg.Workers.Max = 1
			cfg.Memory.Enabled = false
			cfg.Skills.Enabled = false
			cfg.Tools.Enabled = false
			cfg.Telemetry.Database = filepath.Join(t.TempDir(), "model-crash.db")
			cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: provider.URL}}
			zero := 0.0
			cfg.Models = []config.Model{{ID: "chat", Model: "fixture", Provider: "local", Locality: "local", RAMBytes: 1, ContextTokens: 16384, EstimatedCost: &zero, Capabilities: []string{"chat"}}}
			configuration := filepath.Join(t.TempDir(), "config.json")
			body, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(configuration, body, 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestInterruptedModelCrashProcessHelper$", "-test.count=1")
			cmd.Env = []string{"DARWIN_PROCESS_OWNER_DIR=" + filepath.Join(t.TempDir(), "owners"), "PATH=/usr/bin:/bin", "DARWIN_MODEL_CRASH_HELPER=1", "DARWIN_MODEL_CRASH_CONFIG=" + configuration}
			cmd.WaitDelay = time.Second
			cmd.Stderr = io.Discard
			pipe, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			line, scanned := make(chan string, 1), make(chan struct{})
			go func() {
				defer close(scanned)
				scanner := bufio.NewScanner(pipe)
				scanner.Buffer(make([]byte, 256), 4096)
				if scanner.Scan() {
					line <- scanner.Text()
				} else {
					line <- ""
				}
			}()
			waited := false
			defer func() {
				if !waited {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
				<-scanned
			}()
			var id string
			select {
			case id = <-line:
			case <-ctx.Done():
				t.Fatal("helper missed durable model delta")
			}
			if id == "" || len(id) > 128 || strings.ContainsAny(id, " \t\r\n") {
				t.Fatal("invalid crash acknowledgement")
			}
			if err = cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			err = cmd.Wait()
			waited = true
			if err == nil || cmd.ProcessState == nil {
				t.Fatal("helper exited gracefully")
			}
			if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
				t.Fatal("helper did not die by SIGKILL")
			}
			select {
			case <-disconnected:
			case <-ctx.Done():
				t.Fatal("killed provider request stayed connected")
			}
			svc, err := NewService(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			status, err := svc.SubmissionStatus(ctx, id)
			if err != nil || status.State != "running" || len(status.TaskIDs) != 1 || calls.Load() != 1 {
				t.Fatal(status, err, calls.Load())
			}
			task := status.TaskIDs[0]
			before, err := db.ReadEventPage(ctx, task, 0, 100)
			if err != nil || before.State != "running" || before.HasMore || len(before.Events) < 3 || before.Events[len(before.Events)-1].Kind != runtime.ModelDelta {
				t.Fatal("wrong crash journal boundary", before, err)
			}
			raw, err := sql.Open("sqlite", cfg.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			var oldToken string
			err = raw.QueryRowContext(ctx, `SELECT token FROM submissions WHERE id=?`, id).Scan(&oldToken)
			raw.Close()
			if err != nil || oldToken == "" {
				t.Fatal("missing old execution claim", err)
			}
			if cancelRequested {
				if _, err = svc.CancelSubmission(ctx, id); err != nil {
					t.Fatal(err)
				}
			}
			expireRecoveryClaim(t, svc, id)
			dispatcher := &Dispatcher{db: db}
			if _, err = dispatcher.recoverPage(ctx, svc.submissionConfigDigest(), ""); err != nil {
				t.Fatal(err)
			}
			want, code := "failed", "interrupted_model"
			if cancelRequested {
				want, code = "canceled", "canceled"
			}
			after, err := db.ReadEventPage(ctx, task, 0, 100)
			if err != nil || after.State != want || after.HeadSequence != before.HeadSequence+1 || !reflect.DeepEqual(before.Events, after.Events[:len(before.Events)]) {
				t.Fatal("recovery changed committed prefix", after.State, err)
			}
			end := after.Events[len(after.Events)-1]
			kind := runtime.TaskFailed
			if cancelRequested {
				kind = runtime.TaskCanceled
			}
			if end.Kind != kind || end.Data.Code != code || end.Data.Text != "" {
				t.Fatal("invented terminal output", end)
			}
			status, err = svc.SubmissionStatus(ctx, id)
			if err != nil || status.State != want || status.LeaseExpiresAt != nil || status.Result == nil || status.Result.TaskID != task || status.Result.Text != "" || calls.Load() != 1 {
				t.Fatal(status, err, calls.Load())
			}
			receipts, err := db.RecoveryHistory(ctx, id)
			if err != nil || len(receipts) != 1 || receipts[0].Reason != "interrupted_model" || receipts[0].Action != want {
				t.Fatal(receipts, err)
			}
			stale := end
			stale.ID = "old-owner-append"
			stale.Sequence = before.HeadSequence + 1
			if err = db.AppendSubmission(ctx, before.HeadSequence, stale, id, oldToken); err == nil {
				t.Fatal("stale owner appended")
			}
			if _, err = dispatcher.recoverPage(ctx, svc.submissionConfigDigest(), ""); err != nil {
				t.Fatal(err)
			}
			again, err := db.ReadEventPage(ctx, task, 0, 100)
			if err != nil || !reflect.DeepEqual(after, again) || calls.Load() != 1 {
				t.Fatal("recovery replayed work or appended twice", err)
			}
			againReceipts, err := db.RecoveryHistory(ctx, id)
			if err != nil || !reflect.DeepEqual(receipts, againReceipts) {
				t.Fatal("duplicate recovery receipt", err)
			}
			var firstRequest []providers.Message
			select {
			case firstRequest = <-requests:
			case <-ctx.Done():
				t.Fatal("original request missing")
			}
			if !reflect.DeepEqual(firstRequest, []providers.Message{{Role: "user", Content: "start model-only work"}}) {
				t.Fatal("wrong original request")
			}
			childTask := runRecoveredModelContinuationChild(t, ctx, configuration, task, cancelRequested)
			wantCalls := int32(2)
			if cancelRequested {
				wantCalls = 1
			}
			if calls.Load() != wantCalls {
				t.Fatal("unexpected continuation inference")
			}
			if cancelRequested {
				if childTask != "" {
					t.Fatal("canceled source continued")
				}
			} else {
				var next []providers.Message
				select {
				case next = <-requests:
				case <-ctx.Done():
					t.Fatal("continuation request missing")
				}
				if !reflect.DeepEqual(next, []providers.Message{{Role: "user", Content: "start model-only work"}, {Role: "user", Content: "explicit recovered follow-up"}}) {
					t.Fatal("partial output imported")
				}
				child, err := sessions.Replay(ctx, db, childTask)
				if err != nil || child.State != "completed" || child.ParentTaskID != task || child.SessionID != before.Events[0].SessionID || child.Privacy != "local_only" {
					t.Fatal("fresh subprocess continuation lineage", err)
				}
				if len(child.Messages) != 3 || child.Messages[2].Role != "assistant" || child.Messages[2].Content != "fresh continuation answer" {
					t.Fatal("durable continuation answer missing")
				}
				childEvents, err := db.Read(ctx, childTask, 0, 100)
				if err != nil || len(childEvents) == 0 || childEvents[0].Data.ModelID != "fixture" || childEvents[0].Data.ProviderID != "local" || childEvents[len(childEvents)-1].Kind != runtime.TaskCompleted {
					t.Fatal("continuation model binding", err)
				}
			}
			if _, err = dispatcher.recoverPage(ctx, svc.submissionConfigDigest(), ""); err != nil || calls.Load() != wantCalls {
				t.Fatal("recovery redispatched after continuation", err)
			}
			finalSource, err := db.ReadEventPage(ctx, task, 0, 100)
			if err != nil || !reflect.DeepEqual(after, finalSource) {
				t.Fatal("continuation changed old source")
			}
			finalStatus, err := svc.SubmissionStatus(ctx, id)
			if err != nil || !reflect.DeepEqual(status, finalStatus) {
				t.Fatal("continuation changed old submission")
			}
			finalReceipts, err := db.RecoveryHistory(ctx, id)
			if err != nil || !reflect.DeepEqual(receipts, finalReceipts) {
				t.Fatal("continuation changed recovery receipt")
			}
			counts, err := db.Metrics(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var total int64
			for _, count := range counts.Groups[0].Counts {
				total += count.Value
			}
			wantTasks := int64(2)
			if cancelRequested {
				wantTasks = 1
			}
			if total != wantTasks {
				t.Fatal("extra continuation tasks")
			}
		})
	}
}
