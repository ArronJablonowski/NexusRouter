package toolgate

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
	goruntime "runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/approvals"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/tools"
)

type crashSignal struct {
	Stage   string            `json:"stage"`
	Request approvals.Request `json:"request"`
}

// Limit retained diagnostics even if a faulty helper produces unexpected output.
type crashDiagnostics struct{ data []byte }

func (b *crashDiagnostics) Write(p []byte) (int, error) {
	n := len(p)
	if remaining := 8192 - len(b.data); remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		b.data = append(b.data, p...)
	}
	return n, nil
}

func TestToolGateSIGKILLLeavesSpentAuthorityAndOrphanWriter(t *testing.T) {
	if goruntime.GOOS != "darwin" && goruntime.GOOS != "linux" {
		t.Skip("SIGKILL qualification requires Unix")
	}
	for _, stage := range []string{"before_effect", "after_effect"} {
		t.Run(stage, func(t *testing.T) {
			dir, err := os.MkdirTemp(t.TempDir(), "toolgate-crash-")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestToolGateCrashChild$", "-test.count=1")
			cmd.Env = []string{"DARWIN_PROCESS_OWNER_DIR=" + filepath.Join(t.TempDir(), "owners"), "DARWIN_TOOLGATE_CRASH_DIR=" + dir, "DARWIN_TOOLGATE_CRASH_STAGE=" + stage}
			var stderr crashDiagnostics
			cmd.Stderr = &stderr
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			waited := false
			defer func() {
				if !waited {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			}()
			type signalResult struct {
				line string
				err  error
			}
			ready := make(chan signalResult, 1)
			go func() {
				line, err := bufio.NewReader(io.LimitReader(stdout, 8193)).ReadString('\n')
				ready <- signalResult{line, err}
			}()
			var signal crashSignal
			select {
			case r := <-ready:
				if r.err != nil || len(r.line) > 8192 || json.Unmarshal([]byte(r.line), &signal) != nil || signal.Stage != stage || signal.Request.Validate() != nil {
					t.Fatalf("invalid child boundary signal: %v", r.err)
				}
			case <-ctx.Done():
				t.Fatal("child did not reach crash boundary")
			}
			// Only the process created above is killed. Wait verifies its actual fate.
			if err = cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			err = cmd.Wait()
			waited = true
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				t.Fatalf("child was not killed: %v", err)
			}
			status, ok := exit.Sys().(syscall.WaitStatus)
			if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
				t.Fatalf("unexpected child termination: %v", err)
			}
			if ctx.Err() != nil {
				t.Fatal("qualification deadline elapsed")
			}
			qualifyCrashState(t, dir, stage, signal.Request)
		})
	}
}

func qualifyCrashState(t *testing.T, dir, stage string, req approvals.Request) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, err := telemetry.Open(ctx, filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	r, err := db.ReadApproval(ctx, req.ID)
	if err != nil || r.State != approvals.Consumed || !r.Request.Matches(req) || len(r.Decisions) != 1 {
		t.Fatal(r, err)
	}
	snapshot, err := db.TaskSnapshot(ctx, "task")
	pending, ok := snapshot.Pending["call"]
	if err != nil || snapshot.State != "running" || !snapshot.UncertainEffects || !ok || !pending.Dispatched {
		t.Fatal(snapshot, err)
	}
	before, err := db.Read(ctx, "task", 0, 100)
	if err != nil || len(before) == 0 || before[len(before)-1].Kind != runtime.ToolStarted {
		t.Fatal(before, err)
	}
	turns := 0
	for _, e := range before {
		if e.Kind == runtime.TurnStarted {
			turns++
		}
		if e.Kind == runtime.ToolCompleted || e.Kind == runtime.TaskCompleted {
			t.Fatal("unexpected completion after killed handler")
		}
	}
	if turns != 1 {
		t.Fatal("unexpected extra inference", turns)
	}
	leases, err := db.InspectLeases(ctx, "test-artifact")
	if err != nil || len(leases) != 1 || !leases[0].Writer || leases[0].Released {
		t.Fatal(leases, err)
	}
	lease := leases[0]
	observed, err := db.ApprovalExecutionStatus(ctx, req.TaskID, req.ID, lease.Expires.Add(time.Second))
	if err != nil || observed.Approval.State != approvals.Consumed || observed.CallState != "open" || observed.RecordedEffect != "" || observed.ScopeWriterState != "expired" {
		t.Fatal("invalid crash observation", observed, err)
	}
	// No wall-clock sleep: ask at a time beyond expiry. A stale writer still
	// blocks takeover because process/side-effect recovery is not automatic.
	if _, err = db.AcquireLease(ctx, "task", "new-owner", "test-artifact", true, lease.Expires.Add(time.Second), time.Minute); !errors.Is(err, telemetry.ErrLeaseBusy) {
		t.Fatal("orphan writer allowed takeover", err)
	}
	if _, err = db.ConsumeApproval(ctx, req, lease.Token, lease.Owner, time.Now().UTC()); !errors.Is(err, approvals.ErrConflict) {
		t.Fatal("spent authority was reusable", err)
	}
	artifact, err := os.ReadFile(filepath.Join(dir, "artifact.txt"))
	if stage == "before_effect" {
		if !os.IsNotExist(err) {
			t.Fatal("effect happened before boundary", err)
		}
	} else if err != nil || string(artifact) != "synced approved effect" {
		t.Fatal("synced effect not preserved", err)
	}
	after, err := db.Read(ctx, "task", 0, 100)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("inspection replayed or changed task events", err)
	}
	remaining, err := db.InspectLeases(ctx, "test-artifact")
	if err != nil || !reflect.DeepEqual(leases, remaining) {
		t.Fatal("inspection released orphan writer", err)
	}
}

// Invoked only by the narrowly scoped subprocess test above. The helper never
// performs network inference and writes only into its parent-created directory.
func TestToolGateCrashChild(t *testing.T) {
	dir, stage := os.Getenv("DARWIN_TOOLGATE_CRASH_DIR"), os.Getenv("DARWIN_TOOLGATE_CRASH_STAGE")
	if dir == "" && stage == "" {
		return
	}
	if !filepath.IsAbs(dir) || !strings.HasPrefix(filepath.Base(dir), "toolgate-crash-") || (stage != "before_effect" && stage != "after_effect") {
		t.Fatal("invalid helper setup")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	db, err := telemetry.Open(ctx, filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var req approvals.Request
	g := &Gate{Store: db, Review: func(_ context.Context, r approvals.Request) (string, bool, error) {
		req = r
		return "crash-test-operator", true, nil
	}}
	registry := &tools.Registry{}
	if err = registry.Register(tools.Definition{Tool: providers.Tool{Name: "write_artifact", Parameters: json.RawMessage(`{"type":"object","additionalProperties":false}`)}, Scope: "test-artifact", Handler: func(ctx context.Context, _ json.RawMessage) (runtime.ToolResult, error) {
		r, err := db.ReadApproval(ctx, req.ID)
		if err != nil || r.State != approvals.Consumed {
			t.Fatal("unconsumed crash boundary", err)
		}
		if stage == "after_effect" {
			f, err := os.OpenFile(filepath.Join(dir, "artifact.txt"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.WriteString("synced approved effect"); err != nil {
				t.Fatal(err)
			}
			if err = f.Sync(); err != nil {
				t.Fatal(err)
			}
			if err = f.Close(); err != nil {
				t.Fatal(err)
			}
			d, err := os.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			if err = d.Sync(); err != nil {
				t.Fatal(err)
			}
			if err = d.Close(); err != nil {
				t.Fatal(err)
			}
		}
		body, err := json.Marshal(crashSignal{Stage: stage, Request: req})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = fmt.Fprintln(os.Stdout, string(body)); err != nil {
			t.Fatal(err)
		}
		<-ctx.Done()
		return runtime.ToolResult{Effect: runtime.UncertainEffect}, ctx.Err()
	}}); err != nil {
		t.Fatal(err)
	}
	turns := 0
	loop := runtime.Loop{Journal: db, Tools: tools.Executor{Registry: registry, Policy: &tools.Policy{Default: tools.Ask}, Authority: g}, Provider: fixtureModel(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
		turns++
		if turns != 1 {
			t.Fatal("unexpected inference retry")
		}
		if err := emit(providers.Chunk{ToolCall: &providers.ToolCall{ID: "call", Name: "write_artifact", Arguments: json.RawMessage(`{}`)}}); err != nil {
			return err
		}
		return emit(providers.Chunk{Done: true, FinishReason: "tool_calls"})
	})}
	_, err = loop.Run(ctx, runtime.RunRequest{TaskID: "task", SessionID: "session", ProviderID: "fixture", Inference: providers.Request{Model: "fixture", Messages: []providers.Message{{Role: "user", Content: "approved write"}}, Tools: registry.Catalog()}, MaxTurns: 2, MaxOutputBytes: 4096})
	t.Fatalf("helper survived requested crash boundary: %v", err)
}
