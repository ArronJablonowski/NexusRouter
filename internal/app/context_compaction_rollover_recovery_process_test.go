package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	stdRuntime "runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/codexbridge"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

type rolloverCrashProvider struct {
	name, sidecar string
	taskID        *string
	control       *Service
	answer        string
}

func appendRolloverCrashObservation(path, value string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err = fmt.Fprintln(file, value); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}

func (p *rolloverCrashProvider) Models(context.Context) ([]string, error) {
	return []string{"gpt-5.6-sol"}, nil
}

func (p *rolloverCrashProvider) Stream(ctx context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
	if err := appendRolloverCrashObservation(p.sidecar, "stream:"+p.name); err != nil {
		return err
	}
	if p.name == "old" {
		if p.taskID == nil || *p.taskID == "" || p.control == nil {
			return errors.New("missing rollover steering identity")
		}
		if _, err := p.control.SteerTask(ctx, *p.taskID, "dar126-rollover-steering", "use the compacted context"); err != nil {
			return err
		}
	}
	return emit(providers.Chunk{Text: p.answer, Done: true, FinishReason: "stop"})
}

func (p *rolloverCrashProvider) CheckContextRollover(context.Context, providers.Request, providers.Request) error {
	return appendRolloverCrashObservation(p.sidecar, "check:"+p.name)
}

func (p *rolloverCrashProvider) Close() error {
	return appendRolloverCrashObservation(p.sidecar, "close:"+p.name)
}

func TestDAR126CodexRolloverCrashProcessHelper(t *testing.T) {
	if os.Getenv("DARWIN_ROLLOVER_CRASH_HELPER") != "1" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	body, err := os.ReadFile(os.Getenv("DARWIN_ROLLOVER_CRASH_CONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg config.Settings
	if json.Unmarshal(body, &cfg) != nil {
		t.Fatal("invalid rollover crash configuration")
	}
	svc, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	control, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	boundary := os.Getenv("DARWIN_ROLLOVER_CRASH_BOUNDARY")
	sidecar := os.Getenv("DARWIN_ROLLOVER_CRASH_SIDECAR")
	request := Request{ModelID: "brain", ContinueTaskID: os.Getenv("DARWIN_ROLLOVER_CRASH_SOURCE"), Prompt: "complete the initial native turn"}
	status, err := svc.Submit(ctx, "dar126-rollover-"+boundary, request)
	if err != nil {
		t.Fatal(err)
	}
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	claim, err := db.ClaimSubmission(ctx, svc.submissionConfigDigest(), time.Now().UTC(), time.Minute)
	if err != nil || claim.Status.ID != status.ID {
		t.Fatal("rollover submission claim unavailable", err)
	}
	request.submissionID, request.submissionToken = status.ID, claim.Token
	var taskID string
	launches := 0
	var launchMu sync.Mutex
	svc.codexLauncher = func(context.Context, codexbridge.LaunchSpec) (taskProvider, error) {
		launchMu.Lock()
		defer launchMu.Unlock()
		launches++
		name, answer := "old", "initial native answer"
		if launches == 2 {
			name, answer = "new", "replacement native answer"
		} else if launches > 2 {
			return nil, errors.New("duplicate rollover generation")
		}
		if err := appendRolloverCrashObservation(sidecar, "open:"+name); err != nil {
			return nil, err
		}
		return &rolloverCrashProvider{name: name, sidecar: sidecar, taskID: &taskID, control: control, answer: answer}, nil
	}
	activated := false
	_, err = svc.RunStream(ctx, request, func(event runtime.Event) error {
		if event.Kind == runtime.TaskStarted {
			taskID = event.TaskID
		}
		marker := ""
		switch {
		case event.Kind == runtime.ContextCompacted:
			activated = true
			if boundary == "after_activation" {
				marker = "activation_committed"
			}
		case activated && event.Kind == runtime.TurnStarted && boundary == "after_retirement":
			marker = "replacement_turn_committed"
		case activated && event.Kind == runtime.TaskCompleted && boundary == "after_completion":
			marker = "task_completed"
		}
		if marker == "" {
			return nil
		}
		if err := appendRolloverCrashObservation(sidecar, marker); err != nil {
			return err
		}
		fmt.Println(status.ID)
		<-ctx.Done()
		return ctx.Err()
	})
	t.Fatal("rollover crash helper returned before SIGKILL", err)
}

func TestDAR126CodexRolloverRecoversAcrossSIGKILLBoundaries(t *testing.T) {
	if stdRuntime.GOOS != "darwin" && stdRuntime.GOOS != "linux" {
		t.Skip("SIGKILL qualification requires Unix")
	}
	for _, boundary := range []string{"after_activation", "after_retirement", "after_completion"} {
		t.Run(boundary, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			svc, attempt, _ := prepareCodexPlanEvidence(t, nil)
			request := Request{ModelID: "brain", ContinueTaskID: attempt.TaskID, Prompt: "complete the initial native turn"}
			read, err := telemetry.OpenReadOnly(ctx, svc.settings.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			_, inference, err := svc.prepareExplicitInference(ctx, read, request, svc.settings.Models[0], memorySecrets(svc.settings, svc.secret))
			read.Close()
			if err != nil {
				t.Fatal(err)
			}
			limit, err := providers.EstimateContext(inference)
			if err != nil {
				t.Fatal(err)
			}
			svc.settings.Models[0].ContextTokens = limit
			configuration := filepath.Join(t.TempDir(), "config.json")
			body, err := json.Marshal(svc.settings)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(configuration, body, 0600); err != nil {
				t.Fatal(err)
			}
			sidecar := filepath.Join(t.TempDir(), "rollover-observations.log")
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDAR126CodexRolloverCrashProcessHelper$", "-test.count=1")
			cmd.Env = []string{
				"PATH=/usr/bin:/bin",
				"DARWIN_PROCESS_OWNER_DIR=" + filepath.Join(t.TempDir(), "owners"),
				"DARWIN_ROLLOVER_CRASH_HELPER=1",
				"DARWIN_ROLLOVER_CRASH_CONFIG=" + configuration,
				"DARWIN_ROLLOVER_CRASH_BOUNDARY=" + boundary,
				"DARWIN_ROLLOVER_CRASH_SIDECAR=" + sidecar,
				"DARWIN_ROLLOVER_CRASH_SOURCE=" + attempt.TaskID,
			}
			cmd.WaitDelay, cmd.Stderr = time.Second, io.Discard
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			line := make(chan string, 1)
			scanned := make(chan struct{})
			go func() {
				defer close(scanned)
				scanner := bufio.NewScanner(stdout)
				if scanner.Scan() {
					line <- strings.TrimSpace(scanner.Text())
					return
				}
				line <- ""
			}()
			waited := false
			defer func() {
				if !waited {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
				<-scanned
			}()
			var submissionID string
			select {
			case submissionID = <-line:
			case <-ctx.Done():
				t.Fatal("rollover helper did not reach crash boundary")
			}
			if submissionID == "" || len(submissionID) > 128 || strings.ContainsAny(submissionID, " \t\r\n") {
				t.Fatal("invalid rollover crash acknowledgement")
			}
			if err = cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if err = cmd.Wait(); err == nil || cmd.ProcessState == nil {
				t.Fatal("rollover helper exited gracefully")
			}
			waited = true
			if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
				t.Fatal("rollover helper did not die by SIGKILL", cmd.ProcessState)
			}
			before, err := os.ReadFile(sidecar)
			if err != nil {
				t.Fatal(err)
			}
			observed := string(before)
			for _, required := range []string{"open:old\n", "stream:old\n", "check:old\n"} {
				if !strings.Contains(observed, required) {
					t.Fatal("missing pre-crash rollover observation", required, observed)
				}
			}
			switch boundary {
			case "after_activation":
				if !strings.Contains(observed, "activation_committed\n") || strings.Contains(observed, "close:old\n") || strings.Contains(observed, "open:new\n") {
					t.Fatal("wrong activation crash boundary", observed)
				}
			case "after_retirement":
				if !strings.Contains(observed, "close:old\n") || !strings.Contains(observed, "replacement_turn_committed\n") || strings.Contains(observed, "open:new\n") {
					t.Fatal("wrong retirement crash boundary", observed)
				}
			case "after_completion":
				if !strings.Contains(observed, "close:old\n") || !strings.Contains(observed, "open:new\n") || !strings.Contains(observed, "stream:new\n") || !strings.Contains(observed, "task_completed\n") {
					t.Fatal("wrong completion crash boundary", observed)
				}
			}

			recovered, err := NewService(svc.settings, nil)
			if err != nil {
				t.Fatal(err)
			}
			status, err := recovered.SubmissionStatus(ctx, submissionID)
			if err != nil || status.State != "running" || len(status.TaskIDs) != 1 {
				t.Fatal("wrong pre-recovery submission", status, err)
			}
			taskID := status.TaskIDs[0]
			db, err := telemetry.Open(ctx, svc.settings.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			beforePage, err := db.ReadEventPage(ctx, taskID, 0, 100)
			if err != nil || beforePage.HasMore || len(beforePage.Events) == 0 {
				t.Fatal("missing pre-recovery task history", beforePage, err)
			}
			expireRecoveryClaim(t, recovered, submissionID)
			dispatcher := &Dispatcher{db: db}
			if _, err = dispatcher.recoverPage(ctx, recovered.submissionConfigDigest(), ""); err != nil {
				t.Fatal(err)
			}
			status, err = recovered.SubmissionStatus(ctx, submissionID)
			if err != nil || status.Result == nil || status.Result.TaskID != taskID {
				t.Fatal("missing recovered rollover result", status, err)
			}
			wantState, wantText := "failed", ""
			if boundary == "after_completion" {
				wantState, wantText = "succeeded", "replacement native answer"
			}
			if status.State != wantState || status.Result.Text != wantText {
				t.Fatal("wrong recovered rollover outcome", status.State, status.Result.Text)
			}
			afterPage, err := db.ReadEventPage(ctx, taskID, 0, 100)
			if err != nil || afterPage.HasMore {
				t.Fatal("missing recovered task history", afterPage, err)
			}
			receipts, err := db.RecoveryHistory(ctx, submissionID)
			if err != nil || len(receipts) != 1 {
				t.Fatal("missing unique recovery receipt", receipts, err)
			}
			if boundary == "after_completion" {
				if !reflect.DeepEqual(beforePage, afterPage) || afterPage.Events[len(afterPage.Events)-1].Kind != runtime.TaskCompleted ||
					receipts[0].Action != "succeeded" || receipts[0].Reason != "terminal_history" {
					t.Fatal("terminal recovery changed completed task history", afterPage, receipts)
				}
			} else {
				if afterPage.HeadSequence != beforePage.HeadSequence+1 || len(afterPage.Events) != len(beforePage.Events)+1 ||
					!reflect.DeepEqual(beforePage.Events, afterPage.Events[:len(beforePage.Events)]) {
					t.Fatal("interrupted recovery changed committed task prefix", beforePage, afterPage)
				}
				terminal := afterPage.Events[len(afterPage.Events)-1]
				if terminal.Kind != runtime.TaskFailed || terminal.Data.Code != "interrupted_model" || terminal.Data.Text != "" ||
					receipts[0].Action != "failed" || receipts[0].Reason != "interrupted_model" {
					t.Fatal("wrong interrupted rollover recovery evidence", terminal, receipts)
				}
			}
			plan, err := db.ContextCompactionPlanForAttempt(ctx, attempt.ID)
			if err != nil || plan.Status != sessions.ContextCompactionActivated {
				t.Fatal("durable rollover activation missing after restart", plan.Status, err)
			}
			page, err := db.ReadEventPage(ctx, taskID, 0, 100)
			if err != nil || page.HasMore {
				t.Fatal(err, page.HasMore)
			}
			compactions := 0
			for _, event := range page.Events {
				if event.Kind == runtime.ContextCompacted {
					compactions++
				}
			}
			if compactions != 1 {
				t.Fatal("restart lost or duplicated rollover activation", compactions)
			}
			if _, err = dispatcher.recoverPage(ctx, recovered.submissionConfigDigest(), ""); err != nil {
				t.Fatal(err)
			}
			againStatus, err := recovered.SubmissionStatus(ctx, submissionID)
			if err != nil || !reflect.DeepEqual(status, againStatus) {
				t.Fatal("repeated recovery changed submission", againStatus, err)
			}
			againPage, err := db.ReadEventPage(ctx, taskID, 0, 100)
			if err != nil || !reflect.DeepEqual(afterPage, againPage) {
				t.Fatal("repeated recovery changed task history", againPage, err)
			}
			againReceipts, err := db.RecoveryHistory(ctx, submissionID)
			if err != nil || !reflect.DeepEqual(receipts, againReceipts) {
				t.Fatal("repeated recovery duplicated receipt", againReceipts, err)
			}
			after, err := os.ReadFile(sidecar)
			if err != nil || string(after) != observed {
				t.Fatal("recovery redispatched stateful provider", err, string(after))
			}
		})
	}
}
