package openhands

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
	return Config{RuntimeSHA256: strings.Repeat("b", 64), Executable: executable, ExecutableSHA256: hex.EncodeToString(sum[:]), Provider: "nexus-fixture", Model: "test-model", ModelRevision: "fixture-r1", BaseURL: "https://fixture.invalid/v1", TransportPolicySHA256: strings.Repeat("a", 64), ContextTokens: 32768, MaxOutputTokens: 128, Timeout: 30 * time.Second, Prices: &Prices{}, Admit: func(context.Context) (func(), error) { return func() {}, nil }, Transport: policyTransport(func(*http.Request) (*http.Response, error) { return nil, errors.New("fixture denied") })}
}

func TestNativeRunner(t *testing.T) {
	if os.Getenv("NEXUS_OPENHANDS_PYTHON") == "" {
		t.Skip("installed native runner qualification is opt-in")
	}
	for _, protocol := range []string{"openai_compatible", "ollama"} {
		t.Run(protocol, func(t *testing.T) {
			c := runnerFixture(t, os.Getenv("NEXUS_OPENHANDS_PYTHON"))
			c.UpstreamProtocol = protocol
			var calls, admitted, released atomic.Int32
			c.Admit = func(context.Context) (func(), error) { admitted.Add(1); return func() { released.Add(1) }, nil }
			c.Transport = policyTransport(func(r *http.Request) (*http.Response, error) {
				if admitted.Load() != 1 || released.Load() != 0 || calls.Add(1) != 1 {
					t.Error("reservation or single dispatch violated")
				}
				body := strings.Replace(completionFixture("test-model"), `"finish_reason":"stop"}]}`, `"finish_reason":"stop"}],"usage":{"prompt_tokens":17,"completion_tokens":4,"total_tokens":21}}`, 1)
				contentType := "text/event-stream"
				if protocol == "ollama" {
					body = `{"model":"test-model","message":{"role":"assistant","content":"answer"},"done":true,"done_reason":"stop","prompt_eval_count":17,"eval_count":4}` + "\n"
					contentType = "application/x-ndjson"
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			journal, err := telemetry.Open(context.Background(), filepath.Join(t.TempDir(), "native.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer journal.Close()
			task := Task{ID: "native-openhands", SessionID: "fixture-session", Prompt: "Return answer.", Class: harness.TaskClass{Domain: "fixture", Profile: "exact-v1", Difficulty: "easy"}, MaxOutputBytes: 4096}
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
			if err != nil || result.Text != "answer" || result.Usage == nil || result.Usage.InputTokens != 17 || result.Usage.OutputTokens != 4 || result.Identity != identity || released.Load() != 1 || calls.Load() != 1 {
				t.Fatalf("run=%+v error=%v calls=%d released=%d", result, err, calls.Load(), released.Load())
			}
		})
	}
}

func TestNativeRunnerCancellationJoinsProvider(t *testing.T) {
	if os.Getenv("NEXUS_OPENHANDS_PYTHON") == "" {
		t.Skip("installed native cancellation qualification is opt-in")
	}
	c := runnerFixture(t, os.Getenv("NEXUS_OPENHANDS_PYTHON"))
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

func TestNativeRunnerRejectsTruncation(t *testing.T) {
	if os.Getenv("NEXUS_OPENHANDS_PYTHON") == "" {
		t.Skip("native Goose required")
	}
	c := runnerFixture(t, os.Getenv("NEXUS_OPENHANDS_PYTHON"))
	var calls atomic.Int32
	c.Transport = policyTransport(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(strings.Replace(completionFixture("test-model"), `"stop"`, `"length"`, 1)))}, nil
	})
	result, err := Run(context.Background(), c, "answer")
	if err == nil || result != (Result{}) || calls.Load() != 1 {
		t.Fatal("truncated run accepted or repeated inference", err, calls.Load())
	}
}

func TestRunnerCannotTrustProjectionWithoutProvider(t *testing.T) {
	if !processSupported() {
		t.Skip("unsupported process ownership")
	}
	b, err := os.ReadFile("testdata/native-1.50.1.json")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "fake-python")
	body := "#!/bin/sh\ncat <<'JSON'\n" + strings.ReplaceAll(string(b), "fixture-model", "test-model") + "JSON\n"
	if err := os.WriteFile(path, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	c := runnerFixture(t, path)
	var released bool
	c.Admit = func(context.Context) (func(), error) { return func() { released = true }, nil }
	result, err := Run(context.Background(), c, "answer")
	if err == nil || result != (Result{}) || !released {
		t.Fatal("fabricated success accepted", result, err, released)
	}
	c.ExecutableSHA256 = strings.Repeat("0", 64)
	if _, err := Run(context.Background(), c, "answer"); err == nil {
		t.Fatal("wrong executable accepted")
	}
	denied := errors.New("denied")
	c.Admit = func(context.Context) (func(), error) { return nil, denied }
	if _, err := Run(context.Background(), c, "answer"); !errors.Is(err, denied) {
		t.Fatal("admission bypass", err)
	}
}

func TestIdentityBindsRuntimeAndBridgePolicy(t *testing.T) {
	if !processSupported() {
		t.Skip("unsupported process ownership")
	}
	c := runnerFixture(t, "/bin/sh")
	first, err := c.Identity()
	if err != nil {
		t.Fatal(err)
	}
	c.APIKey = "rotated"
	same, err := c.Identity()
	if err != nil || same != first {
		t.Fatal("credential affected identity")
	}
	c.RuntimeSHA256 = strings.Repeat("c", 64)
	changed, err := c.Identity()
	if err != nil || changed == first {
		t.Fatal("runtime attestation not bound")
	}
	c.Transport = nil
	if _, err := c.Identity(); err == nil {
		t.Fatal("missing policy transport accepted")
	}
}

func TestFailedHarnessKeepsOnlyVerifiedConsumption(t *testing.T) {
	python := os.Getenv("NEXUS_OPENHANDS_PYTHON")
	if python == "" {
		t.Skip("pinned Python fixture required")
	}
	for _, mode := range []string{"exit", "mismatch", "truncated"} {
		t.Run(mode, func(t *testing.T) {
			shim := filepath.Join(t.TempDir(), "fixture-python")
			code := "#!" + python + "\n" + `import json,sys,urllib.request
c=json.load(sys.stdin)
request=urllib.request.Request(c['base_url']+'/chat/completions',data=json.dumps({'model':'test-model','messages':[{'role':'user','content':'answer'}],'stream':True,'max_tokens':128}).encode(),headers={'Authorization':'Bearer '+c['api_key'],'Content-Type':'application/json'})
try:
 urllib.request.urlopen(request,timeout=10).read()
except Exception:
 sys.exit(3)
`
			if mode == "mismatch" {
				code += `print(json.dumps({'sdk_version':'1.50.1','model':'openai/test-model','status':'finished','events':[{'kind':'MessageEvent','source':'agent','llm_response_id':'one','llm_message':{'role':'assistant','content':[{'type':'text','text':'fabricated'}],'tool_calls':None,'reasoning_content':None}}]}))` + "\n"
			} else {
				code += "sys.exit(2)\n"
			}
			if err := os.WriteFile(shim, []byte(code), 0700); err != nil {
				t.Fatal(err)
			}
			c := runnerFixture(t, shim)
			var calls atomic.Int32
			c.Transport = policyTransport(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				body := strings.Replace(completionFixture("test-model"), `"finish_reason":"stop"}]}`, `"finish_reason":"stop"}],"usage":{"prompt_tokens":17,"completion_tokens":4,"total_tokens":21}}`, 1)
				if mode == "truncated" {
					body = strings.Replace(body, "data: [DONE]\n\n", "", 1)
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			result, err := Run(context.Background(), c, "answer")
			if err == nil || result.Text != "" || calls.Load() != 1 {
				t.Fatal("failure became success or repeated inference", result, err)
			}
			if mode == "truncated" {
				if result.Usage != nil {
					t.Fatal("unverified usage accepted")
				}
			} else if result.Usage == nil || result.Usage.InputTokens != 17 || result.Usage.OutputTokens != 4 || result.Identity.Model != "test-model" {
				t.Fatal("verified consumption lost", result)
			}
		})
	}
}
