package openclaw

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func runnerFixture(t *testing.T, executable string) Config {
	t.Helper()
	b, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return Config{Executable: executable, ExecutableSHA256: hex.EncodeToString(sum[:]), Provider: "nexus-fixture", Model: "test-model", ModelRevision: "fixture-r1", BaseURL: "https://fixture.invalid/v1", TransportPolicySHA256: strings.Repeat("a", 64), ContextTokens: 32768, MaxOutputTokens: 128, Timeout: 30 * time.Second, Prices: &Prices{}, Admit: func(context.Context) (func(), error) { return func() {}, nil }, Transport: policyTransport(func(*http.Request) (*http.Response, error) { return nil, errors.New("fixture denied") })}
}

func fakeCLI(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nif [ \"$1\" = '--version' ]; then echo 'OpenClaw 2026.9.7 (c074824)'; exit 0; fi\necho '{\"ok\":true,\"status\":\"ok\",\"provider\":\"nexus-fixture\",\"model\":\"test-model\",\"sessionId\":\"fake\",\"assistantTurns\":1,\"final\":\"answer\",\"payloads\":[]}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunnerCannotTrustProjectionWithoutProvider(t *testing.T) {
	if !processSupported() {
		t.Skip("native process ownership unsupported")
	}
	c := runnerFixture(t, fakeCLI(t))
	released := false
	c.Admit = func(context.Context) (func(), error) { return func() { released = true }, nil }
	r, err := Run(context.Background(), c, "answer")
	if err == nil || r != (Result{}) || !released {
		t.Fatal("fabricated native output accepted", r, err, released)
	}
	c.ExecutableSHA256 = strings.Repeat("0", 64)
	released = false
	if r, err := Run(context.Background(), c, "answer"); err == nil || r != (Result{}) || !released {
		t.Fatal("wrong artifact accepted")
	}
	denied := errors.New("admission denied")
	c = runnerFixture(t, fakeCLI(t))
	c.Admit = func(context.Context) (func(), error) { return nil, denied }
	if _, err := Run(context.Background(), c, "answer"); !errors.Is(err, denied) {
		t.Fatal("admission bypass", err)
	}
}

func TestRunnerIdentityBindsPolicyAndExcludesCredentials(t *testing.T) {
	if !processSupported() {
		t.Skip("native process ownership unsupported")
	}
	c := runnerFixture(t, fakeCLI(t))
	first, err := c.Identity()
	if err != nil {
		t.Fatal(err)
	}
	c.APIKey = "rotated"
	same, err := c.Identity()
	if err != nil || same != first {
		t.Fatal("credential rotation changed identity")
	}
	c.TransportPolicySHA256 = strings.Repeat("b", 64)
	changed, err := c.Identity()
	if err != nil || changed == first {
		t.Fatal("policy not bound")
	}
	c.Transport = nil
	if _, err := c.Identity(); err == nil {
		t.Fatal("nil policy transport")
	}
}

func TestNativeRunner(t *testing.T) {
	if os.Getenv("NEXUS_OPENCLAW_NATIVE") != "1" {
		t.Skip("installed native runner qualification is opt-in")
	}
	for _, protocol := range []string{"openai_compatible", "ollama"} {
		t.Run(protocol, func(t *testing.T) {
			c := runnerFixture(t, nativeExecutable(t))
			c.UpstreamProtocol = protocol
			var calls, admitted, released atomic.Int32
			c.Admit = func(context.Context) (func(), error) { admitted.Add(1); return func() { released.Add(1) }, nil }
			c.Transport = policyTransport(func(r *http.Request) (*http.Response, error) {
				if admitted.Load() != 1 || released.Load() != 0 || calls.Add(1) != 1 {
					t.Error("reservation or single dispatch violated")
				}
				body := completionFixture("test-model")
				contentType := "text/event-stream"
				if protocol == "ollama" {
					body = `{"model":"test-model","message":{"role":"assistant","content":"answer"},"done":true,"done_reason":"stop"}` + "\n"
					contentType = "application/x-ndjson"
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			journal, err := telemetry.Open(context.Background(), filepath.Join(t.TempDir(), "native.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer journal.Close()
			task := Task{ID: "native-openclaw", SessionID: "fixture-session", Prompt: "Return answer.", Class: harness.TaskClass{Domain: "fixture", Profile: "exact-v1", Difficulty: "easy"}, MaxOutputBytes: 4096}
			taskResult, err := RunTask(context.Background(), journal, c, task)
			result := taskResult.Result
			if err == nil {
				events, e := journal.Read(context.Background(), task.ID, 0, 10)
				if e != nil || len(events) != 2 || events[1].Data.HarnessOutcome == nil || *events[1].Data.HarnessOutcome != taskResult.Execution {
					t.Fatal("canonical native outcome missing", e)
				}
			}
			if _, e := RunTask(context.Background(), journal, c, task); e == nil {
				t.Fatal("reused task ID executed")
			}
			identity, _ := c.Identity()
			if err != nil || result.Text != "answer" || result.Identity != identity || released.Load() != 1 || calls.Load() != 1 {
				t.Fatalf("run=%+v error=%v calls=%d released=%d", result, err, calls.Load(), released.Load())
			}
		})
	}
}

func TestNativeRunnerCancellationJoinsProvider(t *testing.T) {
	if os.Getenv("NEXUS_OPENCLAW_NATIVE") != "1" {
		t.Skip("installed native cancellation qualification is opt-in")
	}
	c := runnerFixture(t, nativeExecutable(t))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, joined := make(chan struct{}), make(chan struct{})
	c.Transport = policyTransport(func(r *http.Request) (*http.Response, error) {
		close(entered)
		<-r.Context().Done()
		close(joined)
		return nil, r.Context().Err()
	})
	released := false
	c.Admit = func(context.Context) (func(), error) {
		return func() {
			select {
			case <-joined:
				released = true
			default:
				t.Error("released before provider cleanup")
			}
		}, nil
	}
	go func() {
		select {
		case <-entered:
			cancel()
		case <-ctx.Done():
		}
	}()
	result, err := Run(ctx, c, "Return answer.")
	if !errors.Is(err, context.Canceled) || result != (Result{}) || !released {
		t.Fatal("canceled output accepted or cleanup incomplete", result, err, released)
	}
}
