package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/approvals"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/tools"
)

func createIntegrationConfig(t *testing.T, args string) (config.Settings, *atomic.Int32, <-chan []string) {
	t.Helper()
	calls := new(atomic.Int32)
	catalogs := make(chan []string, 10)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			fmt.Fprintln(w, `{"models":[{"name":"m"},{"name":"z"}]}`)
			return
		}
		var request struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
			Messages []providers.Message `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			http.Error(w, "fixture", 400)
			return
		}
		var names []string
		for _, tool := range request.Tools {
			names = append(names, tool.Function.Name)
		}
		catalogs <- names
		if calls.Add(1) == 1 {
			fmt.Fprintf(w, "{\"message\":{\"tool_calls\":[{\"function\":{\"name\":\"create_file\",\"arguments\":%s}}]},\"done\":true,\"done_reason\":\"tool_calls\"}\n", args)
		} else {
			fmt.Fprintln(w, `{"message":{"content":"fixture finished"},"done":true,"done_reason":"stop"}`)
		}
	}))
	t.Cleanup(server.Close)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Tools.Enabled = true
	cfg.Tools.ReadRoot = root
	cfg.Tools.CreateEnabled = true
	cfg.Tools.CreateRoot = root
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "create.db")
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: server.URL}}
	zero := 0.0
	cfg.Models = []config.Model{{ID: "m", Provider: "local", Model: "m", Locality: "local", RAMBytes: 1, ContextTokens: 8192, Capabilities: []string{"chat"}, EstimatedCost: &zero}}
	return cfg, calls, catalogs
}

func TestCreateToolHTTPApprovalAndDurableEffect(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		t.Run(fmt.Sprint(allowed), func(t *testing.T) {
			cfg, calls, _ := createIntegrationConfig(t, `{"path":"result.txt","content":"approved exact content 世界\n"}`)
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			var reviewed approvals.Request
			svc, err := NewServiceWithToolApproval(cfg, nil, nil, nil, nil, nil, nil, func(ctx context.Context, p tools.ApprovalPrompt) (string, bool, error) {
				reviewed = p.Request
				if p.Request.ToolName != "create_file" || !strings.Contains(string(p.Arguments), "result.txt") {
					t.Error("missing exact create preview")
				}
				db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
				if err != nil {
					return "", false, err
				}
				defer db.Close()
				events, err := db.Read(ctx, p.Request.TaskID, 0, 100)
				if err != nil || len(events) == 0 || events[len(events)-1].Kind != runtime.ToolStarted {
					t.Error("approval preceded durable tool start")
				}
				if _, err = os.Stat(filepath.Join(cfg.Tools.CreateRoot, "result.txt")); !os.IsNotExist(err) {
					t.Error("file created before approval")
				}
				return "fixture-operator", allowed, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			result, runErr := svc.Run(ctx, Request{ModelID: "m", Prompt: "create the file"})
			body, fileErr := os.ReadFile(filepath.Join(cfg.Tools.CreateRoot, "result.txt"))
			if allowed && (runErr != nil || string(body) != "approved exact content 世界\n" || fileErr != nil || calls.Load() != 2) {
				t.Fatal(result, runErr, fileErr, calls.Load())
			}
			if !allowed && (!os.IsNotExist(fileErr) || reviewed.ID == "") {
				t.Fatal("denied create produced file", runErr, fileErr)
			}
			db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			record, err := db.ReadApproval(ctx, reviewed.ID)
			if err != nil {
				t.Fatal(err)
			}
			if allowed {
				status, err := db.ApprovalExecutionStatus(ctx, result.TaskID, reviewed.ID, time.Now().UTC())
				if err != nil || record.State != approvals.Consumed || status.CallState != "completed" || status.RecordedEffect != "confirmed" || status.ScopeWriterState != "none" {
					t.Fatal(status, record.State, err)
				}
				raw, err := sql.Open("sqlite", cfg.Telemetry.Database)
				if err != nil {
					t.Fatal(err)
				}
				defer raw.Close()
				var leases int
				if err = raw.QueryRow("SELECT count(*) FROM resource_leases WHERE scope=? AND writer=1 AND released=1", reviewed.Scope).Scan(&leases); err != nil || leases != 1 {
					t.Fatal("missing released writer evidence", leases, err)
				}
			} else if record.State == approvals.Consumed {
				t.Fatal("denied approval consumed")
			}
		})
	}
}

func TestCreateToolHTTPRejectsUnsafeAndExistingTargets(t *testing.T) {
	for _, scenario := range []string{"existing", "escape", "absolute", "unknown_argument", "missing_content", "child"} {
		t.Run(scenario, func(t *testing.T) {
			args := `{"path":"result.txt","content":"new"}`
			switch scenario {
			case "escape":
				args = `{"path":"../escape.txt","content":"new"}`
			case "absolute":
				body, err := json.Marshal(map[string]string{"path": filepath.Join(t.TempDir(), "outside.txt"), "content": "new"})
				if err != nil {
					t.Fatal(err)
				}
				args = string(body)
			case "unknown_argument":
				args = `{"path":"result.txt","content":"new","overwrite":true}`
			case "missing_content":
				args = `{"path":"result.txt"}`
			}
			cfg, calls, catalogs := createIntegrationConfig(t, args)
			if scenario == "existing" {
				if err := os.WriteFile(filepath.Join(cfg.Tools.CreateRoot, "result.txt"), []byte("original"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "child" {
				cfg.Workers.DelegateModel = "m"
			}
			var reviews atomic.Int32
			svc, err := NewServiceWithToolApproval(cfg, nil, nil, nil, nil, nil, nil, func(context.Context, tools.ApprovalPrompt) (string, bool, error) {
				reviews.Add(1)
				return "fixture-operator", true, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			var result Result
			if scenario == "child" {
				_, err = svc.runDelegate(ctx, "create file", "", "fixture-parent", true, "", "")
			} else {
				result, err = svc.Run(ctx, Request{ModelID: "m", Prompt: "create file"})
			}
			if scenario == "existing" || scenario == "escape" || scenario == "absolute" {
				if err == nil || calls.Load() != 1 {
					t.Fatal("certain create failure continued successfully", result, err, calls.Load())
				}
				db, readErr := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
				if readErr != nil {
					t.Fatal(readErr)
				}
				events, readErr := db.Read(ctx, result.TaskID, 0, 100)
				db.Close()
				if readErr != nil {
					t.Fatal(readErr)
				}
				found := false
				for _, event := range events {
					if event.Kind == runtime.ToolCompleted {
						if event.Data.Code != "tool_failed" || event.Data.Effect != runtime.NoEffect {
							t.Fatal("lost certain failure evidence", event)
						}
						found = true
					}
					if event.Kind == runtime.TaskCompleted {
						t.Fatal("failed file operation completed task")
					}
				}
				if !found {
					t.Fatal("missing durable failed tool completion")
				}
			}
			body, fileErr := os.ReadFile(filepath.Join(cfg.Tools.CreateRoot, "result.txt"))
			if scenario == "existing" {
				if fileErr != nil || string(body) != "original" {
					t.Fatal("existing file altered")
				}
			} else if !os.IsNotExist(fileErr) {
				t.Fatal("unsafe proposal created file", fileErr)
			}
			if scenario == "escape" || scenario == "absolute" {
				var proposal struct {
					Path string `json:"path"`
				}
				if err := json.Unmarshal([]byte(args), &proposal); err != nil {
					t.Fatal(err)
				}
				target := proposal.Path
				if !filepath.IsAbs(target) {
					target = filepath.Join(cfg.Tools.CreateRoot, target)
				}
				if _, err := os.Stat(target); !os.IsNotExist(err) {
					t.Fatal("out-of-root target created", err)
				}
			}
			if scenario == "child" {
				if err == nil || reviews.Load() != 0 {
					t.Fatal("child gained create authority", err, reviews.Load())
				}
				for _, name := range <-catalogs {
					if name == "create_file" {
						t.Fatal("create advertised to child")
					}
				}
			}
			if (scenario == "unknown_argument" || scenario == "missing_content") && reviews.Load() != 0 {
				t.Fatal("invalid schema reached approval")
			}
		})
	}
}

func TestCreateToolMissingReviewerDeniesBeforeIO(t *testing.T) {
	cfg, calls, _ := createIntegrationConfig(t, `{"path":"result.txt","content":"new"}`)
	svc, err := NewServiceWithToolApproval(cfg, nil, nil, nil, nil, nil, nil, nil)
	if err != ErrAdmission || svc != nil {
		t.Fatal("missing reviewer admitted", err)
	}
	if calls.Load() != 0 {
		t.Fatal("missing reviewer invoked provider")
	}
	if _, err = os.Stat(cfg.Telemetry.Database); !os.IsNotExist(err) {
		t.Fatal("missing reviewer created storage")
	}
	if entries, err := os.ReadDir(cfg.Tools.CreateRoot); err != nil || len(entries) != 0 {
		t.Fatal("missing reviewer touched create root", err)
	}
}

func TestCreateToolUnrecordedCompletionNeverFallsBack(t *testing.T) {
	cfg, calls, _ := createIntegrationConfig(t, `{"path":"result.txt","content":"one effect"}`)
	fallback := cfg.Models[0]
	fallback.ID = "z"
	fallback.Model = "z"
	cfg.Models = append(cfg.Models, fallback)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	raw, err := sql.Open("sqlite", cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err = raw.Exec(`CREATE TRIGGER deny_create_completion BEFORE INSERT ON events WHEN json_extract(NEW.body,'$.kind')='tool.completed' AND json_extract(NEW.body,'$.data.tool_name')='create_file' BEGIN SELECT RAISE(ABORT,'fixture completion interrupted'); END`); err != nil {
		t.Fatal(err)
	}
	var reviewed approvals.Request
	svc, err := NewServiceWithToolApproval(cfg, nil, nil, nil, nil, nil, nil, func(_ context.Context, p tools.ApprovalPrompt) (string, bool, error) {
		reviewed = p.Request
		return "fixture-operator", true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Run(ctx, Request{ModelID: "auto", Prompt: "create file"})
	if err == nil || calls.Load() != 1 || reviewed.ID == "" {
		t.Fatal("uncertain completion retried", err, calls.Load())
	}
	body, err := os.ReadFile(filepath.Join(cfg.Tools.CreateRoot, "result.txt"))
	if err != nil || string(body) != "one effect" {
		t.Fatal("expected original effect absent", err)
	}
	read, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	status, err := read.ApprovalExecutionStatus(ctx, reviewed.TaskID, reviewed.ID, time.Now().UTC())
	if err != nil || status.Approval.State != approvals.Consumed || status.CallState != "open" || status.RecordedEffect != "" {
		t.Fatal("lost uncertain execution evidence", status, err)
	}
}
