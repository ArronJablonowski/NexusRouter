package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/approvals"
	"github.com/ArronJablonowski/NexusRouter/harness/goose"
	"github.com/ArronJablonowski/NexusRouter/harness/hermes"
	"github.com/ArronJablonowski/NexusRouter/harness/openhands"
	"github.com/ArronJablonowski/NexusRouter/harness/pi"
	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/tools"
)

func TestHTTPNativePiDurableOperatorApproval(t *testing.T) {
	if os.Getenv("NEXUS_PI_NATIVE") != "1" {
		t.Skip("requires installed Pi")
	}
	testHTTPNativeApproval(t, "pi", pi.AgentAdapterVersion)
}
func TestHTTPNativeOpenHandsDurableOperatorApproval(t *testing.T) {
	if os.Getenv("NEXUS_OPENHANDS_PYTHON") == "" {
		t.Skip("requires installed OpenHands")
	}
	testHTTPNativeApproval(t, "openhands", openhands.AgentAdapterVersion)
}
func testHTTPNativeApproval(t *testing.T, kind, adapter string) {
	registration := nativeHTTPRegistration(t, kind)
	for _, mode := range []string{"approve", "deny", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			var calls atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if !strings.Contains(string(body), "create_file") {
					t.Error("host write schema missing")
				}
				w.Header().Set("Content-Type", "application/x-ndjson")
				if calls.Add(1) == 1 {
					fmt.Fprintln(w, `{"model":"fixture","message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"create_file","arguments":{"path":"approved.txt","content":"operator authorized content"}}}]},"done":true,"done_reason":"stop"}`)
					return
				}
				fmt.Fprintln(w, `{"model":"fixture","message":{"role":"assistant","content":"approved write complete"},"done":true,"done_reason":"stop"}`)
			}))
			defer func() { cancel(); provider.Close() }()
			root := t.TempDir()
			artifactPath := filepath.Join(root, "approved.txt")
			cfg := config.Defaults()
			cfg.Mode = "local_only"
			cfg.Tools.Enabled = true
			cfg.Tools.ReadRoot = root
			cfg.Tools.CreateEnabled = true
			cfg.Tools.CreateRoot = root
			cfg.Tools.MaxTurns = 3
			cfg.Runtime.MaxTurns = 3
			cfg.Telemetry.Database = filepath.Join(t.TempDir(), "approvals.db")
			cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: provider.URL, RequestTimeout: "15s"}}
			cfg.Models = []config.Model{{ID: "chat", Provider: "local", Model: "fixture", Locality: "local", RAMBytes: 1, ContextTokens: 16384, Capabilities: []string{"chat"}}}
			cfg.NativeHarnesses = []config.NativeHarness{registration}
			proposals := make(chan tools.ApprovalPrompt, 1)
			svc, e := app.NewServiceWithToolControls(cfg, nil, apiFixtureProfiler{}, nil, nil, nil, nil, nil, func(c context.Context, p tools.ApprovalPrompt) error {
				select {
				case proposals <- p:
					return nil
				case <-c.Done():
					return c.Err()
				}
			})
			if e != nil {
				t.Fatal(e)
			}
			results := make(chan app.Result, 1)
			services := services()
			services.Run = svc.Run
			services.RunTextStream = func(c context.Context, r app.Request, emit func(string) error) (app.Result, error) {
				out, e := svc.RunTextStream(c, r, emit)
				results <- out
				return out, e
			}
			services.Approval = func(c context.Context, task, id string) (approvals.Record, error) {
				return app.InspectApproval(c, cfg.Telemetry.Database, task, id)
			}
			services.ApprovalExecution = func(c context.Context, task, id string) (approvals.ExecutionStatus, error) {
				return app.ApprovalExecutionStatus(c, cfg.Telemetry.Database, task, id)
			}
			services.DecideApproval = func(c context.Context, command approvals.Command) (approvals.Record, error) {
				return svc.DecideApproval(c, command, "api_operator")
			}
			handler, e := New(token, 1, services)
			if e != nil {
				t.Fatal(e)
			}
			server := httptest.NewServer(handler)
			defer func() { cancel(); server.Close() }()
			requestCtx, stop := context.WithCancel(ctx)
			defer stop()
			req, _ := http.NewRequestWithContext(requestCtx, "POST", server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"chat","harness_id":"native-tools","messages":[{"role":"user","content":"Create the requested file."}],"stream":true}`))
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			response, e := server.Client().Do(req)
			if e != nil {
				t.Fatal(e)
			}
			defer response.Body.Close()
			var proposal tools.ApprovalPrompt
			select {
			case proposal = <-proposals:
			case <-ctx.Done():
				t.Fatal("no operator proposal", ctx.Err())
			}
			if _, e = os.Stat(artifactPath); !os.IsNotExist(e) || calls.Load() != 1 {
				t.Fatal("effect before approval", e, calls.Load())
			}
			endpoint := "/v1/tasks/" + proposal.Request.TaskID + "/approvals/" + proposal.Request.ID
			control := func(method, path, body string, authenticated bool) (int, []byte) {
				t.Helper()
				r, _ := http.NewRequestWithContext(ctx, method, server.URL+path, strings.NewReader(body))
				r.Header.Set("Content-Type", "application/json")
				if authenticated {
					r.Header.Set("Authorization", "Bearer "+token)
				}
				resp, e := server.Client().Do(r)
				if e != nil {
					t.Fatal(e)
				}
				defer resp.Body.Close()
				b, e := io.ReadAll(resp.Body)
				if e != nil {
					t.Fatal(e)
				}
				return resp.StatusCode, b
			}
			status, body := control("GET", endpoint, "", true)
			var pending approvals.Record
			if status != 200 || json.Unmarshal(body, &pending) != nil || pending.State != approvals.Pending || !pending.Request.Matches(proposal.Request) || strings.Contains(string(body), "operator authorized content") {
				t.Fatal("invalid pending inspection", status, string(body))
			}
			command := approvals.Command{Expected: proposal.Request, ID: "operator-decision", Allowed: mode == "approve"}
			if status, _ := control("POST", endpoint+"/decision", decisionCommandBody(command), false); status != 401 {
				t.Fatal("unauthenticated decision", status)
			}
			forged := command
			forged.ID = "forged-decision"
			forged.Expected.ArgumentsDigest = strings.Repeat("0", 64)
			if status, _ := control("POST", endpoint+"/decision", decisionCommandBody(forged), true); status != 409 {
				t.Fatal("unbound decision", status)
			}
			if _, e = os.Stat(artifactPath); !os.IsNotExist(e) {
				t.Fatal("invalid decision wrote file", e)
			}
			if mode == "cancel" {
				stop()
			} else {
				for range 2 {
					status, body := control("POST", endpoint+"/decision", decisionCommandBody(command), true)
					if status != 200 {
						t.Fatal("decision retry", status, string(body))
					}
				}
			}
			output, _ := io.ReadAll(response.Body)
			var out app.Result
			select {
			case out = <-results:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			db, e := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
			if e != nil {
				t.Fatal(e)
			}
			defer db.Close()
			record, e := db.ReadApproval(ctx, proposal.Request.ID)
			if e != nil {
				t.Fatal(e)
			}
			events, e := db.Read(ctx, out.TaskID, 0, 100)
			if e != nil || len(events) == 0 {
				t.Fatal(out, e)
			}
			last := events[len(events)-1]
			if mode == "approve" {
				content, e := os.ReadFile(artifactPath)
				if e != nil || string(content) != "operator authorized content" || record.State != approvals.Consumed || len(record.Decisions) != 1 || record.Decisions[0].Actor != "api_operator" || calls.Load() != 2 || last.Kind != runtime.TaskCompleted || out.HarnessOutcome == nil || out.HarnessOutcome.Actual.AdapterVersion != adapter || !strings.Contains(string(output), "[DONE]") {
					t.Fatal("approved lifecycle", record, out, e, calls.Load())
				}
				status, body := control("GET", endpoint+"/execution", "", true)
				var observed approvals.ExecutionStatus
				if status != 200 || json.Unmarshal(body, &observed) != nil || observed.RecordedEffect != string(runtime.ConfirmedEffect) {
					t.Fatal("missing committed effect", status, string(body))
				}
			} else {
				if _, e = os.Stat(artifactPath); !os.IsNotExist(e) || calls.Load() != 1 || out.HarnessOutcome != nil || strings.Contains(string(output), "[DONE]") {
					t.Fatal("unapproved effect", e, out, calls.Load())
				}
				want := runtime.TaskFailed
				if mode == "cancel" {
					want = runtime.TaskCanceled
				}
				if last.Kind != want {
					t.Fatal("wrong terminal", last.Kind)
				}
				if mode == "deny" && (record.State != approvals.Denied || len(record.Decisions) != 1) {
					t.Fatal(record)
				}
				if mode == "cancel" {
					status, _ := control("POST", endpoint+"/decision", decisionCommandBody(command), true)
					if status != 409 {
						t.Fatal("late decision accepted", status)
					}
				}
			}
		})
	}
}

func TestHTTPNativeGooseDurableOperatorApproval(t *testing.T) {
	if os.Getenv("NEXUS_GOOSE_NATIVE") != "1" {
		t.Skip("requires installed Goose")
	}
	testHTTPNativeApproval(t, "goose", goose.AgentAdapterVersion)
}

func TestHTTPNativeHermesDurableOperatorApproval(t *testing.T) {
	if os.Getenv("NEXUS_HERMES_NATIVE") != "1" {
		t.Skip("requires installed Hermes")
	}
	testHTTPNativeApproval(t, "hermes", hermes.AgentAdapterVersion)
}
