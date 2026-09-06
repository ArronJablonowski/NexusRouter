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
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/approvals"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/tools"
)

func replaceIntegrationConfig(t *testing.T) (config.Settings, *atomic.Int32, <-chan []string) {
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
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			http.Error(w, "fixture", 400)
			return
		}
		names := []string{}
		for _, tool := range request.Tools {
			names = append(names, tool.Function.Name)
		}
		catalogs <- names
		if calls.Add(1) == 1 {
			fmt.Fprintln(w, `{"message":{"tool_calls":[{"function":{"name":"replace_file","arguments":{"path":"result.txt","expected_content":"original","content":"replacement"}}}]},"done":true,"done_reason":"tool_calls"}`)
		} else {
			fmt.Fprintln(w, `{"message":{"content":"fixture finished"},"done":true,"done_reason":"stop"}`)
		}
	}))
	t.Cleanup(server.Close)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "result.txt"), []byte("original"), 0640); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Tools.Enabled = true
	cfg.Tools.ReadRoot = root
	cfg.Tools.ReplaceEnabled = true
	cfg.Tools.ReplaceRoot = root
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "replace.db")
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: server.URL}}
	zero := 0.0
	cfg.Models = []config.Model{{ID: "m", Provider: "local", Model: "m", Locality: "local", RAMBytes: 1, ContextTokens: 8192, Capabilities: []string{"chat"}, EstimatedCost: &zero}}
	return cfg, calls, catalogs
}

func TestReplaceToolApprovalAndStaleReview(t *testing.T) {
	for _, scenario := range []string{"allow", "deny", "changed"} {
		t.Run(scenario, func(t *testing.T) {
			cfg, calls, _ := replaceIntegrationConfig(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var reviewed approvals.Request
			svc, err := NewServiceWithToolApproval(cfg, nil, nil, nil, nil, nil, nil, func(ctx context.Context, p tools.ApprovalPrompt) (string, bool, error) {
				reviewed = p.Request
				if p.Request.ToolName != "replace_file" || p.Request.Scope != "workspace" {
					t.Error("incorrect write approval scope")
				}
				var args map[string]string
				if json.Unmarshal(p.Arguments, &args) != nil || args["expected_content"] != "original" || args["content"] != "replacement" {
					t.Error("approval omitted exact preimage")
				}
				db, e := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
				if e != nil {
					return "", false, e
				}
				defer db.Close()
				events, e := db.Read(ctx, p.Request.TaskID, 0, 100)
				if e != nil || len(events) == 0 || events[len(events)-1].Kind != runtime.ToolStarted {
					t.Error("approval preceded persisted dispatch")
				}
				body, _ := os.ReadFile(filepath.Join(cfg.Tools.ReplaceRoot, "result.txt"))
				if string(body) != "original" {
					t.Error("changed before approval")
				}
				if scenario == "changed" {
					if e = os.WriteFile(filepath.Join(cfg.Tools.ReplaceRoot, "result.txt"), []byte("external update"), 0640); e != nil {
						return "", false, e
					}
				}
				return "fixture-operator", scenario != "deny", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			result, runErr := svc.Run(ctx, Request{ModelID: "m", Prompt: "replace file"})
			if reviewed.ID == "" {
				t.Fatal("missing review", runErr)
			}
			body, _ := os.ReadFile(filepath.Join(cfg.Tools.ReplaceRoot, "result.txt"))
			want := "original"
			if scenario == "allow" {
				want = "replacement"
			}
			if scenario == "changed" {
				want = "external update"
			}
			if string(body) != want {
				t.Fatal("unexpected resulting bytes")
			}
			backups, _ := filepath.Glob(filepath.Join(cfg.Tools.ReplaceRoot, ".darwin-replace-*", "original"))
			if scenario == "allow" {
				if runErr != nil || calls.Load() != 2 || len(backups) != 1 {
					t.Fatal(runErr, calls.Load(), backups)
				}
				old, _ := os.ReadFile(backups[0])
				if string(old) != "original" {
					t.Fatal("backup lost")
				}
			} else if len(backups) != 0 {
				t.Fatal("failed/denied replacement staged backup")
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
			if scenario == "deny" {
				if record.State == approvals.Consumed {
					t.Fatal("denial consumed approval")
				}
				return
			}
			status, err := db.ApprovalExecutionStatus(ctx, result.TaskID, reviewed.ID, time.Now().UTC())
			if err != nil || record.State != approvals.Consumed || status.CallState != "completed" || status.ScopeWriterState != "none" {
				t.Fatal(status, err)
			}
			if scenario == "allow" && status.RecordedEffect != "confirmed" {
				t.Fatal(status)
			}
			if scenario == "changed" && (runErr == nil || status.RecordedEffect != "none" || calls.Load() != 1) {
				t.Fatal(status, runErr, calls.Load())
			}
			raw, err := sql.Open("sqlite", cfg.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			var leases int
			if err = raw.QueryRow("SELECT count(*) FROM resource_leases WHERE scope='workspace' AND writer=1 AND released=1").Scan(&leases); err != nil || leases != 1 {
				t.Fatal("writer evidence", leases, err)
			}
		})
	}
}

func TestReplaceToolMissingReviewerAndChildNoAuthority(t *testing.T) {
	cfg, calls, _ := replaceIntegrationConfig(t)
	svc, err := NewServiceWithToolApproval(cfg, nil, nil, nil, nil, nil, nil, nil)
	if err != ErrAdmission || svc != nil || calls.Load() != 0 {
		t.Fatal("missing reviewer admitted", err)
	}
	if _, err = os.Stat(cfg.Telemetry.Database); !os.IsNotExist(err) {
		t.Fatal("unauthorized admission wrote database")
	}
	cfg, calls, catalogs := replaceIntegrationConfig(t)
	cfg.Workers.DelegateModel = "m"
	var reviews atomic.Int32
	svc, err = NewServiceWithToolApproval(cfg, nil, nil, nil, nil, nil, nil, func(context.Context, tools.ApprovalPrompt) (string, bool, error) {
		reviews.Add(1)
		return "fixture-operator", true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = svc.runDelegate(ctx, "replace file", "", "fixture-parent", true, "", "")
	if err == nil || reviews.Load() != 0 {
		t.Fatal("child gained authority", err)
	}
	if calls.Load() > 0 {
		for _, name := range <-catalogs {
			if name == "replace_file" {
				t.Fatal("child advertised replace")
			}
		}
	}
	body, _ := os.ReadFile(filepath.Join(cfg.Tools.ReplaceRoot, "result.txt"))
	if string(body) != "original" {
		t.Fatal("child changed file")
	}
}

func TestReplaceToolUnrecordedCompletionRetainsBackupNeverReplays(t *testing.T) {
	cfg, calls, _ := replaceIntegrationConfig(t)
	fallback := cfg.Models[0]
	fallback.ID = "z"
	fallback.Model = "z"
	cfg.Models = append(cfg.Models, fallback)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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
	if _, err = raw.Exec(`CREATE TRIGGER deny_replace_completion BEFORE INSERT ON events WHEN json_extract(NEW.body,'$.kind')='tool.completed' AND json_extract(NEW.body,'$.data.tool_name')='replace_file' BEGIN SELECT RAISE(ABORT,'fixture completion interrupted'); END`); err != nil {
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
	_, err = svc.Run(ctx, Request{ModelID: "auto", Prompt: "replace file"})
	if err == nil || calls.Load() != 1 || reviewed.ID == "" {
		t.Fatal("uncertain completion retried", err, calls.Load())
	}
	body, _ := os.ReadFile(filepath.Join(cfg.Tools.ReplaceRoot, "result.txt"))
	backups, _ := filepath.Glob(filepath.Join(cfg.Tools.ReplaceRoot, ".darwin-replace-*", "original"))
	if string(body) != "replacement" || len(backups) != 1 {
		t.Fatal("publication/backup missing")
	}
	body, _ = os.ReadFile(backups[0])
	if string(body) != "original" {
		t.Fatal("original lost")
	}
	read, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	status, err := read.ApprovalExecutionStatus(ctx, reviewed.TaskID, reviewed.ID, time.Now().UTC())
	if err != nil || status.Approval.State != approvals.Consumed || status.CallState != "open" || status.RecordedEffect != "" {
		t.Fatal(status, err)
	}
}

func TestReplaceToolSubmissionAndCloudCannotGainAuthority(t *testing.T) {
	for _, scenario := range []string{"submission", "cloud"} {
		t.Run(scenario, func(t *testing.T) {
			cfg, calls, _ := replaceIntegrationConfig(t)
			if scenario == "cloud" {
				cfg.Mode = "hybrid"
				cfg.Models[0].Locality = "cloud"
			}
			var reviews atomic.Int32
			svc, err := NewServiceWithToolApproval(cfg, nil, nil, nil, nil, nil, nil, func(context.Context, tools.ApprovalPrompt) (string, bool, error) {
				reviews.Add(1)
				return "fixture-operator", true, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if scenario == "submission" {
				_, err = svc.Submit(ctx, "0123456789abcdef", Request{ModelID: "m", Prompt: "replace file"})
			} else {
				_, err = svc.Run(ctx, Request{ModelID: "m", Prompt: "replace file"})
			}
			if err == nil || calls.Load() != 0 || reviews.Load() != 0 {
				t.Fatal("unauthorized execution admitted", err, calls.Load(), reviews.Load())
			}
			body, _ := os.ReadFile(filepath.Join(cfg.Tools.ReplaceRoot, "result.txt"))
			entries, _ := os.ReadDir(cfg.Tools.ReplaceRoot)
			if string(body) != "original" || len(entries) != 1 {
				t.Fatal("unauthorized execution changed root")
			}
		})
	}
}
