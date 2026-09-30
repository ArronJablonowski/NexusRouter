//go:build darwin || linux

package app

import (
	"bufio"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/processguard"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/resources"
	"github.com/ArronJablonowski/NexusRouter/routing"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
	"modernc.org/sqlite"
)

const summaryCrashBoundaryLine = "summary-crash-boundary"

// Only this test-owned process installs the SQLite function. The trigger stops
// in the actual drafted-state transaction after the provider has completed but
// before either the attempt or its accounting record commits.
func TestSummaryRecoveryCrashProcessHelper(t *testing.T) {
	if os.Getenv("DARWIN_SUMMARY_CRASH_HELPER") != "1" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	body, err := os.ReadFile(os.Getenv("DARWIN_SUMMARY_CRASH_CONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg config.Settings
	if json.Unmarshal(body, &cfg) != nil {
		t.Fatal("invalid helper configuration")
	}
	boundary := os.Getenv("DARWIN_SUMMARY_CRASH_BOUNDARY")
	if boundary == "after_provider_before_commit" {
		if err = sqlite.RegisterScalarFunction("darwin_test_pause_summary_commit", 1, func(_ *sqlite.FunctionContext, _ []driver.Value) (driver.Value, error) {
			fmt.Println(summaryCrashBoundaryLine)
			<-ctx.Done()
			return nil, errors.New("fixture owner was not killed")
		}); err != nil {
			t.Fatal(err)
		}
	}
	svc, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc.profile = func(context.Context) (resources.Snapshot, error) {
		return resources.Snapshot{Time: time.Now(), TotalRAM: 1 << 30, AvailableRAM: 1 << 30}, nil
	}
	if boundary == "before_dispatch" {
		svc.contextEstimator = auxiliaryContextEstimator(func(context.Context, providers.Request) (int, error) {
			fmt.Println(summaryCrashBoundaryLine)
			<-ctx.Done()
			return 0, errors.New("fixture owner was not killed")
		})
	}
	if boundary == "after_provider_before_commit" {
		raw, openErr := sql.Open("sqlite", cfg.Telemetry.Database)
		if openErr != nil {
			t.Fatal(openErr)
		}
		defer raw.Close()
		_, err = raw.ExecContext(ctx, `CREATE TRIGGER crash_before_summary_commit
			BEFORE UPDATE OF body ON summary_attempts
			WHEN json_extract(OLD.body,'$.Status')='started'
			AND json_extract(NEW.body,'$.Status')='drafted'
			BEGIN SELECT darwin_test_pause_summary_commit(NEW.id); END`)
		if err != nil {
			t.Fatal(err)
		}
	}
	attempt, err := svc.SummarizeTask(ctx, os.Getenv("DARWIN_SUMMARY_CRASH_TASK"), "a", 1, 0)
	if boundary != "after_commit_before_ack" {
		t.Fatal("summary returned before SIGKILL", attempt.Status, err)
	}
	if err != nil || attempt.Status != "drafted" || attempt.Draft == nil {
		t.Fatal("summary did not durably commit before acknowledgement", err)
	}
	fmt.Println(summaryCrashBoundaryLine)
	<-ctx.Done()
	t.Fatal("fixture owner was not killed")
}

func TestSummaryAttemptsRecoverAcrossFourSIGKILLBoundaries(t *testing.T) {
	for _, boundary := range []string{
		"before_dispatch",
		"during_streaming",
		"after_provider_before_commit",
		"after_commit_before_ack",
	} {
		t.Run(boundary, func(t *testing.T) { qualifySummaryAttemptCrash(t, boundary) })
	}
}

func TestSummaryRecoveryRunsBeforeDispatcherWorkers(t *testing.T) {
	qualifySummaryAttemptCrash(t, "before_dispatch", true)
}

func qualifySummaryAttemptCrash(t *testing.T, boundary string, recoverWithDispatcher ...bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	svc, cfg := autoFixture(t)
	source, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "preserve the crash recovery requirement", Domain: "creative"})
	if err != nil {
		t.Fatal(err)
	}
	seed, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	sourceEvents, err := seed.Read(ctx, source.TaskID, 0, 100)
	if err != nil {
		seed.Close()
		t.Fatal(err)
	}
	sourceSnapshot, err := sessions.Replay(ctx, seed, source.TaskID)
	if err != nil {
		seed.Close()
		t.Fatal(err)
	}
	if err = seed.Close(); err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32
	streamReached := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if boundary == "during_streaming" {
			w.Header().Set("Content-Type", "application/x-ndjson")
			_, _ = io.WriteString(w, `{"message":{"content":"{\"version\":1,"},"done":false}`+"\n")
			if flush, ok := w.(http.Flusher); ok {
				flush.Flush()
			}
			select {
			case streamReached <- struct{}{}:
			default:
			}
			<-r.Context().Done()
			return
		}
		_, _ = fmt.Fprintln(w, `{"message":{"content":"{\"version\":1,\"summary\":{\"decisions\":[],\"requirements\":[\"preserve crash recovery\"],\"pending_work\":[],\"failures\":[],\"artifacts\":[],\"activity\":[]}}"},"done":true,"done_reason":"stop","prompt_eval_count":11,"eval_count":7}`)
	}))
	defer server.Close()
	cfg.Providers[0].Endpoint = server.URL
	configuration := filepath.Join(t.TempDir(), "summary-crash.json")
	body, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(configuration, body, 0600); err != nil {
		t.Fatal(err)
	}

	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSummaryRecoveryCrashProcessHelper$", "-test.timeout=45s")
	command.Env = []string{
		"PATH=/usr/bin:/bin",
		"DARWIN_PROCESS_OWNER_DIR=" + filepath.Join(t.TempDir(), "owners"),
		"DARWIN_SUMMARY_CRASH_HELPER=1",
		"DARWIN_SUMMARY_CRASH_CONFIG=" + configuration,
		"DARWIN_SUMMARY_CRASH_BOUNDARY=" + boundary,
		"DARWIN_SUMMARY_CRASH_TASK=" + source.TaskID,
	}
	command.WaitDelay = time.Second
	command.Stderr = io.Discard
	pipe, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()
	line := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(pipe)
		scanner.Buffer(make([]byte, 256), 4096)
		if scanner.Scan() {
			line <- strings.TrimSpace(scanner.Text())
		} else {
			line <- ""
		}
	}()
	if boundary == "during_streaming" {
		select {
		case <-streamReached:
		case <-ctx.Done():
			t.Fatal("summary provider stream was not reached")
		}
	} else {
		select {
		case got := <-line:
			if got != summaryCrashBoundaryLine {
				t.Fatal("invalid crash boundary acknowledgement", got)
			}
		case <-ctx.Done():
			t.Fatal("summary helper did not reach crash boundary")
		}
	}
	if err = command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	err = command.Wait()
	waited = true
	if err == nil || command.ProcessState == nil {
		t.Fatal("summary fixture exited without SIGKILL")
	}
	if status, ok := command.ProcessState.Sys().(syscall.WaitStatus); !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatal("summary fixture did not terminate by SIGKILL", command.ProcessState)
	}

	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	attempts, err := db.ListSummaryAttempts(ctx, source.TaskID, "", 100)
	if err != nil || len(attempts) != 1 {
		t.Fatal("missing crash-boundary attempt", attempts, err)
	}
	before := attempts[0]
	wantCalls := int32(1)
	if boundary == "before_dispatch" {
		wantCalls = 0
	}
	if calls.Load() != wantCalls {
		t.Fatal("wrong provider dispatch count before recovery", calls.Load())
	}
	if boundary == "after_commit_before_ack" {
		if before.Status != "drafted" || before.Draft == nil {
			t.Fatal("committed draft was not retained", before.Status)
		}
	} else if before.Status != "started" || before.Draft != nil {
		t.Fatal("uncertain output crossed its terminal commit", before.Status)
	}
	if boundary == "after_provider_before_commit" {
		raw, openErr := sql.Open("sqlite", cfg.Telemetry.Database)
		if openErr != nil {
			t.Fatal(openErr)
		}
		_, dropErr := raw.ExecContext(ctx, `DROP TRIGGER crash_before_summary_commit`)
		closeErr := raw.Close()
		if dropErr != nil || closeErr != nil {
			t.Fatal("cannot remove fixture trigger", dropErr, closeErr)
		}
	}
	assertSummaryOwnerStopped(t, ctx, cfg.Telemetry.Database, before.ID)

	recoveryTime := before.StartedAt.Add(2 * time.Second).UTC()
	next, recovered := "", 0
	if len(recoverWithDispatcher) != 0 && recoverWithDispatcher[0] {
		recoveryService, serviceErr := NewService(cfg, nil)
		if serviceErr != nil {
			t.Fatal(serviceErr)
		}
		dispatcher, startErr := StartDispatcher(ctx, recoveryService)
		if startErr != nil {
			t.Fatal("dispatcher startup recovery failed", startErr)
		}
		defer dispatcher.Close()
		// StartDispatcher must not return until its first bounded reconciliation
		// page is durable. Workers have no authority to dispatch this auxiliary
		// operation, and the provider must remain untouched.
		terminal, readErr := db.SummaryAttempt(ctx, before.ID)
		if readErr != nil || terminal.Status != "interrupted" || calls.Load() != 0 {
			t.Fatal("dispatcher returned before summary recovery", terminal, calls.Load(), readErr)
		}
		recovered = 1
	} else {
		next, recovered, err = db.ReconcileSummaryAttemptsPage(ctx, "", 100, recoveryTime)
	}
	wantRecovered := 1
	if boundary == "after_commit_before_ack" {
		wantRecovered = 0
	}
	if err != nil || next != "" || recovered != wantRecovered {
		t.Fatal("summary reconciliation failed", next, recovered, err)
	}
	after, err := db.SummaryAttempt(ctx, before.ID)
	if err != nil {
		t.Fatal(err)
	}
	recoveries, err := db.ListSummaryAttemptRecoveries(ctx, source.TaskID, "", 100)
	if err != nil || len(recoveries) != wantRecovered {
		t.Fatal("wrong summary recovery receipts", recoveries, err)
	}
	if wantRecovered == 1 {
		if after.Status != "interrupted" || after.Code != "owner_interrupted" || after.Draft != nil || after.Usage != nil || after.Elapsed != 0 || after.FinishedAt.Before(after.StartedAt) {
			t.Fatal("uncertain summary was not safely interrupted", after)
		}
		receipt, readErr := db.SummaryAttemptRecovery(ctx, before.ID)
		if readErr != nil || receipt != recoveries[0] || receipt.AttemptID != before.ID || receipt.TaskID != source.TaskID || receipt.SourceDigest != before.SourceDigest || receipt.SourceSequence != before.SourceSequence {
			t.Fatal("recovery receipt lost source binding", receipt, readErr)
		}
		encoded, _ := json.Marshal(receipt)
		if strings.Contains(string(encoded), "darwin-owner-") || strings.Contains(string(encoded), server.URL) {
			t.Fatal("recovery receipt exposed private process or provider metadata")
		}
	} else if !reflect.DeepEqual(after, before) {
		t.Fatal("reconciliation changed already committed draft")
	}
	assertSummaryRecoveryHasNoDerivedEvidence(t, ctx, db, source.TaskID, before.ID, sourceEvents, sourceSnapshot)
	stableAttempt, _ := json.Marshal(after)
	stableRecoveries, _ := json.Marshal(recoveries)
	for range 2 {
		next, recovered, err = db.ReconcileSummaryAttemptsPage(ctx, "", 100, recoveryTime.Add(time.Second))
		if err != nil || next != "" || recovered != 0 || calls.Load() != wantCalls {
			t.Fatal("repeated recovery was not inert", next, recovered, calls.Load(), err)
		}
		current, readErr := db.SummaryAttempt(ctx, before.ID)
		currentRecoveries, listErr := db.ListSummaryAttemptRecoveries(ctx, source.TaskID, "", 100)
		attemptBody, _ := json.Marshal(current)
		recoveryBody, _ := json.Marshal(currentRecoveries)
		if readErr != nil || listErr != nil || string(attemptBody) != string(stableAttempt) || string(recoveryBody) != string(stableRecoveries) {
			t.Fatal("repeated recovery changed durable projections", readErr, listErr)
		}
	}
}

func assertSummaryOwnerStopped(t *testing.T, ctx context.Context, path, attempt string) {
	t.Helper()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var body []byte
	if err = raw.QueryRowContext(ctx, `SELECT p.body FROM summary_attempts a JOIN lease_processes p ON p.id=a.process_id WHERE a.id=?`, attempt).Scan(&body); err != nil {
		t.Fatal("missing summary process binding", err)
	}
	var reference processguard.Reference
	if json.Unmarshal(body, &reference) != nil || reference.Validate() != nil {
		t.Fatal("invalid summary process binding")
	}
	observation, err := processguard.Probe(ctx, reference)
	if err != nil || observation == nil || observation.State != processguard.Unlocked || observation.ConfirmUnlocked(ctx) != nil {
		if observation != nil {
			_ = observation.Close()
		}
		t.Fatal("killed summary owner was not independently proven stopped", err)
	}
	if err = observation.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertSummaryRecoveryHasNoDerivedEvidence(t *testing.T, ctx context.Context, db *telemetry.Store, task, attempt string, sourceEvents []runtime.Event, sourceSnapshot sessions.Snapshot) {
	t.Helper()
	reviews, err := db.SummaryReviews(ctx, attempt)
	if err != nil || len(reviews) != 0 {
		t.Fatal("summary recovery created a review", reviews, err)
	}
	if _, _, err = db.LatestApprovedSummary(ctx, task); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("summary recovery created an approved draft", err)
	}
	events, err := db.Read(ctx, task, 0, 100)
	if err != nil || !reflect.DeepEqual(events, sourceEvents) {
		t.Fatal("summary recovery changed source journal", err)
	}
	for _, event := range events {
		if event.Data.Compaction != nil {
			t.Fatal("summary recovery activated compaction")
		}
	}
	snapshot, err := sessions.Replay(ctx, db, task)
	if err != nil || !reflect.DeepEqual(snapshot, sourceSnapshot) {
		t.Fatal("summary recovery changed source replay", err)
	}
	if _, err = db.Fitness(ctx, routing.Key{Model: "a", Provider: "local", Domain: "creative", Profile: "default"}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("summary recovery created fitness evidence", err)
	}
}
