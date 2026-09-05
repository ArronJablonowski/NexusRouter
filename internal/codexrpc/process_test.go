package codexrpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestProcessHelper is a subprocess fixture, never a real Codex invocation.
func TestProcessHelper(t *testing.T) {
	if os.Getenv("DARWIN_RPC_HELPER") != "1" {
		return
	}
	mode := os.Args[len(os.Args)-1]
	switch mode {
	case "echo":
		e, err := NewDecoder(os.Stdin, 0).Read()
		if err != nil || NewEncoder(os.Stdout, 0).Write(e) != nil {
			os.Exit(9)
		}
	case "blocked":
		_ = NewEncoder(os.Stdout, 0).Write(Envelope{Method: "ready"})
		time.Sleep(30 * time.Second)
	case "two-frames":
		_, _ = io.WriteString(os.Stdout, "{\"method\":\"first\"}\n{\"method\":\"second\"}\n")
	case "leave-output-open":
		executable, err := os.Executable()
		if err != nil {
			os.Exit(20)
		}
		child, err := os.StartProcess(executable, []string{executable, "-test.run=^TestProcessHelper$", "--", "bounded-descendant"}, &os.ProcAttr{
			Env:   []string{"DARWIN_RPC_HELPER=1", "GORACE=atexit_sleep_ms=0", "DARWIN_RPC_RECEIPT=" + os.Getenv("DARWIN_RPC_RECEIPT")},
			Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
		})
		if err != nil {
			os.Exit(21)
		}
		_ = child.Release()
	case "bounded-descendant":
		// This deliberately outlives its leader, but cannot remain as a
		// long-lived orphan even if the test fails before cancelling.
		time.Sleep(time.Second)
		_ = os.Stdout.Close()
		_ = os.Stdin.Close()
		if os.WriteFile(os.Getenv("DARWIN_RPC_RECEIPT"), []byte("descriptors-closed"), 0600) != nil {
			os.Exit(22)
		}
	case "failure":
		_, _ = fmt.Fprintln(os.Stderr, "fixture-secret-do-not-return")
		os.Exit(17)
	case "environment":
		dir, _ := os.Getwd()
		_, inherited := os.LookupEnv("DARWIN_RPC_PARENT_SECRET")
		body, _ := json.Marshal(map[string]any{"dir": dir, "inherited": inherited})
		_ = NewEncoder(os.Stdout, 0).Write(Envelope{ID: json.RawMessage("1"), Result: body})
	default:
		os.Exit(19)
	}
	os.Exit(0)
}

func processFixture(t *testing.T, mode string) ProcessSpec {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("process transport supported on Darwin and Linux")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return ProcessSpec{Executable: executable, Args: []string{"-test.run=^TestProcessHelper$", "--", mode}, Env: []string{"DARWIN_RPC_HELPER=1"}, Dir: t.TempDir()}
}

func startFixture(t *testing.T, ctx context.Context, mode string) *Process {
	t.Helper()
	p, err := StartProcess(ctx, processFixture(t, mode))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func TestProcessRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p := startFixture(t, ctx, "echo")
	want := Envelope{ID: json.RawMessage(`"opaque-id"`), Method: "fixture/echo", Params: json.RawMessage(`{"value":42}`)}
	if err := p.Write(want); err != nil {
		t.Fatal(err)
	}
	got, err := p.Read()
	if err != nil || got.Method != want.Method || string(got.ID) != string(want.ID) || string(got.Params) != string(want.Params) {
		t.Fatalf("round trip mismatch: %#v, %v", got, err)
	}
	if err := p.Wait(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.Done():
	default:
		t.Fatal("Wait returned before Done closed")
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal("second Close:", err)
	}
}

func TestProcessExplicitEnvironmentAndDirectory(t *testing.T) {
	t.Setenv("DARWIN_RPC_PARENT_SECRET", "must-not-inherit")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	spec := processFixture(t, "environment")
	p, err := StartProcess(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	e, err := p.Read()
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Dir       string `json:"dir"`
		Inherited bool   `json:"inherited"`
	}
	if json.Unmarshal(e.Result, &result) != nil || result.Inherited {
		t.Fatal("child inherited ambient environment or returned invalid result")
	}
	wantInfo, err := os.Stat(spec.Dir)
	if err != nil {
		t.Fatal(err)
	}
	gotInfo, err := os.Stat(result.Dir)
	if err != nil || !os.SameFile(wantInfo, gotInfo) {
		t.Fatal("child did not use explicit working directory")
	}
	if err := p.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestProcessCancellationUnblocksIO(t *testing.T) {
	for _, operation := range []string{"read", "write"} {
		t.Run(operation, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			p := startFixture(t, ctx, "blocked")
			if e, err := p.Read(); err != nil || e.Method != "ready" {
				t.Fatalf("helper readiness: %v", err)
			}
			result := make(chan error, 1)
			go func() {
				if operation == "read" {
					_, err := p.Read()
					result <- err
					return
				}
				// Exceeds pipe capacity but remains below the bounded frame limit.
				result <- p.Write(Envelope{Method: "blocked", Params: json.RawMessage(`{"payload":"` + strings.Repeat("x", 512<<10) + `"}`)})
			}()
			select {
			case err := <-result:
				t.Fatalf("I/O unexpectedly finished before cancellation: %v", err)
			case <-time.After(50 * time.Millisecond):
			}
			cancel()
			select {
			case err := <-result:
				if err == nil {
					t.Fatal("cancelled I/O succeeded")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("cancellation did not unblock I/O")
			}
			if err := p.Wait(); !errors.Is(err, ErrProcessExit) {
				t.Fatalf("cancelled process exit: %v", err)
			}
		})
	}
}

func TestProcessNonzeroExitIsSanitized(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p := startFixture(t, ctx, "failure")
	if err := p.Wait(); err != ErrProcessExit {
		t.Fatalf("expected static process exit error, got %v", err)
	}
	if _, err := p.Read(); err == nil || (err != io.EOF && err != ErrRead) {
		t.Fatalf("expected sanitized terminal read error, got %v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestProcessEmptyExplicitEnvironment(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	spec := processFixture(t, "echo")
	spec.Env = []string{}
	spec.Args = []string{"-test.run=^$"}
	p, err := StartProcess(ctx, spec)
	if err != nil {
		t.Fatal("explicit empty environment rejected:", err)
	}
	defer p.Close()
	if err := p.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestProcessCloseUnblocksRead(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p := startFixture(t, ctx, "blocked")
	if _, err := p.Read(); err != nil {
		t.Fatal(err)
	}
	readDone := make(chan error, 1)
	go func() { _, err := p.Read(); readDone <- err }()
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal("repeated Close:", err)
	}
	select {
	case err := <-readDone:
		if err == nil {
			t.Fatal("read succeeded after closing transport")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not unblock read")
	}
	if err := p.Wait(); err != ErrProcessExit {
		t.Fatalf("closed live process: %v", err)
	}
}

func TestProcessCancelAfterLeaderExitClosesTransport(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	spec := processFixture(t, "leave-output-open")
	receipt := filepath.Join(spec.Dir, "descendant-receipt")
	spec.Env = append(spec.Env, "GORACE=atexit_sleep_ms=0", "DARWIN_RPC_RECEIPT="+receipt)
	p, err := StartProcess(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	// Even a failed assertion waits for the bounded descendant to release
	// its inherited descriptors before TempDir cleanup removes its receipt.
	t.Cleanup(func() {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if body, err := os.ReadFile(receipt); err == nil && string(body) == "descriptors-closed" {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Error("bounded descendant did not close descriptors and record completion")
	})
	if err := p.Wait(); err != nil {
		t.Fatal(err)
	}
	readDone := make(chan error, 1)
	go func() { _, err := p.Read(); readDone <- err }()
	select {
	case err := <-readDone:
		t.Fatalf("descendant did not retain stdout: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-readDone:
		if err == nil {
			t.Fatal("cancelled read succeeded")
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("cancellation after leader exit did not promptly close retained stdout")
	}
}

func TestProcessCloseDiscardsBufferedFrame(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p := startFixture(t, ctx, "two-frames")
	if err := p.Wait(); err != nil {
		t.Fatal(err)
	}
	if e, err := p.Read(); err != nil || e.Method != "first" {
		t.Fatalf("first frame: %#v %v", e, err)
	}
	if p.decoder.reader.Buffered() == 0 {
		t.Fatal("fixture did not buffer second frame")
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if e, err := p.Read(); err != ErrRead || e.Method != "" {
		t.Fatalf("closed process leaked buffered frame: %#v %v", e, err)
	}
}

func TestProcessRejectsInvalidSpec(t *testing.T) {
	base := processFixture(t, "echo")
	cases := map[string]func(*ProcessSpec){
		"relative executable":   func(s *ProcessSpec) { s.Executable = "codex" },
		"missing executable":    func(s *ProcessSpec) { s.Executable = s.Dir + "/missing" },
		"relative directory":    func(s *ProcessSpec) { s.Dir = "." },
		"missing directory":     func(s *ProcessSpec) { s.Dir += "/missing" },
		"nil environment":       func(s *ProcessSpec) { s.Env = nil },
		"duplicate environment": func(s *ProcessSpec) { s.Env = []string{"X=1", "X=2"} },
		"malformed environment": func(s *ProcessSpec) { s.Env = []string{"MISSING_EQUALS"} },
		"empty environment key": func(s *ProcessSpec) { s.Env = []string{"=value"} },
		"nul environment":       func(s *ProcessSpec) { s.Env = []string{"KEY=value\x00"} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			spec := base
			mutate(&spec)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			p, err := StartProcess(ctx, spec)
			if p != nil {
				_ = p.Close()
			}
			if p != nil || err != ErrProcessStart {
				t.Fatalf("invalid spec accepted or error not sanitized: %v", err)
			}
		})
	}
	if p, err := StartProcess(nil, base); p != nil || err != ErrProcessStart {
		if p != nil {
			_ = p.Close()
		}
		t.Fatalf("nil context accepted: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if p, err := StartProcess(ctx, base); p != nil || err != ErrProcessStart {
		if p != nil {
			_ = p.Close()
		}
		t.Fatalf("pre-cancelled context accepted: %v", err)
	}
}
