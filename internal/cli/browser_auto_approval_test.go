package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/ArronJablonowski/NexusRouter/approvals"
	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/tools"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func autoApprovalFixture(t *testing.T, args string) (config.Settings, *atomic.Int32, <-chan []string) {
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

func TestBrowserAutoApprovalDurableCreationAndOff(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			cfg, _, _ := autoApprovalFixture(t, `{"path":"result.txt","content":"exact auto approved content"}`)
			cfg.WebUI.Enabled = true
			cfg.Tools.AutoApprove = enabled
			presented := make(chan struct{}, 1)
			present := browserToolPresenter(cfg)
			svc, err := app.NewServiceWithToolControls(cfg, nil, nil, nil, nil, nil, nil, nil, func(ctx context.Context, p tools.ApprovalPrompt) error {
				err := present(ctx, p)
				presented <- struct{}{}
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := svc.Run(ctx, app.Request{ModelID: "m", Prompt: "Create a document"}); done <- err }()
			select {
			case <-presented:
			case err := <-done:
				t.Fatal("stopped before proposal", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if !enabled {
				if _, err := os.Stat(filepath.Join(cfg.Tools.CreateRoot, "result.txt")); !os.IsNotExist(err) {
					t.Fatal("manual mode wrote without a click", err)
				}
				cancel()
				<-done
				return
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(filepath.Join(cfg.Tools.CreateRoot, "result.txt"))
			if err != nil || string(body) != "exact auto approved content" {
				t.Fatal(string(body), err)
			}
			db, err := telemetry.Open(context.Background(), cfg.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			// Inspect through the generated task rather than treating a callback's return as authority.
			// The presenter records its own actor and the normal gate consumes approval.
			raw, err := sql.Open("sqlite", cfg.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			var encoded []byte
			if err = raw.QueryRow("SELECT body FROM tool_approvals").Scan(&encoded); err != nil {
				t.Fatal(err)
			}
			var record approvals.Record
			if json.Unmarshal(encoded, &record) != nil || record.State != approvals.Consumed || len(record.Decisions) != 1 || record.Decisions[0].Actor != "auto_approval_setting" {
				t.Fatal("automatic authority not audited/consumed", record)
			}
		})
	}
}
