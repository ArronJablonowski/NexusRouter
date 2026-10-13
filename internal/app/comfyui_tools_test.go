package app

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/ArronJablonowski/NexusRouter/approvals"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/tools"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestComfyChatApprovalBeforeDispatch(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		t.Run(fmt.Sprint(allowed), func(t *testing.T) {
			var remoteCalls, modelCalls atomic.Int32
			comfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				remoteCalls.Add(1)
				w.Write([]byte(`{"prompt_id":"job-1","number":0}`))
			}))
			defer comfy.Close()
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/tags" {
					w.Write([]byte(`{"models":[{"name":"m"}]}`))
					return
				}
				if modelCalls.Add(1) == 1 {
					w.Write([]byte(`{"message":{"tool_calls":[{"function":{"name":"comfy_submit","arguments":{"key":"chat-job","workflow":{"1":{"class_type":"EmptyImage","inputs":{}}}}}}]},"done":true,"done_reason":"tool_calls"}`))
				} else {
					w.Write([]byte(`{"message":{"content":"finished"},"done":true,"done_reason":"stop"}`))
				}
			}))
			defer model.Close()
			cfg, _, _ := createIntegrationConfig(t, `{}`)
			cfg.Tools.Enabled = false
			cfg.Tools.CreateEnabled = false
			cfg.Providers[0].Endpoint = model.URL
			cfg.Security.ToolPolicy = "allow"
			root := t.TempDir()
			os.Chmod(root, 0700)
			cfg.ComfyUI = config.ComfyUI{Enabled: true, Endpoint: comfy.URL, ReceiptDirectory: root}
			reviewed := false
			svc, e := NewServiceWithToolApproval(cfg, nil, nil, nil, nil, nil, nil, func(ctx context.Context, p tools.ApprovalPrompt) (string, bool, error) {
				reviewed = true
				if p.Request.ToolName != "comfy_submit" || !strings.Contains(string(p.Arguments), "chat-job") || !json.Valid(p.Arguments) {
					t.Error("missing exact workflow approval")
				}
				if remoteCalls.Load() != 0 {
					t.Error("dispatched before approval")
				}
				return "operator", allowed, nil
			})
			if e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, e = svc.Run(ctx, Request{ModelID: "m", Prompt: "run this image workflow"})
			if !reviewed {
				t.Fatal("approval not requested", e)
			}
			if allowed && (e != nil || remoteCalls.Load() != 1) {
				t.Fatal("approved flow failed", e, remoteCalls.Load())
			}
			if !allowed && remoteCalls.Load() != 0 {
				t.Fatal("denied job dispatched")
			}
		})
	}
}

func TestComfyCloudCoordinatorCannotReceiveConnector(t *testing.T) {
	cfg, _, _ := createIntegrationConfig(t, `{}`)
	cfg.Mode = "hybrid"
	cfg.Tools.CreateEnabled = false
	cfg.Tools.Enabled = false
	cfg.Models[0].Locality = "cloud"
	root := t.TempDir()
	os.Chmod(root, 0700)
	cfg.ComfyUI = config.ComfyUI{Enabled: true, Endpoint: "http://127.0.0.1:1", ReceiptDirectory: root}
	reviewed := false
	svc, e := NewServiceWithToolApproval(cfg, nil, nil, nil, nil, nil, nil, func(context.Context, tools.ApprovalPrompt) (string, bool, error) {
		reviewed = true
		return "operator", true, nil
	})
	if e != nil {
		t.Fatal(e)
	}
	_, e = svc.Run(context.Background(), Request{ModelID: "m", Prompt: "inspect ComfyUI"})
	if e == nil || reviewed {
		t.Fatal("cloud connector authority admitted", e)
	}
}

func TestComfyBrowserChatShowsExactProposalAndWaits(t *testing.T) {
	var remoteCalls, modelCalls atomic.Int32
	comfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remoteCalls.Add(1)
		w.Write([]byte(`{"prompt_id":"job-1","number":0}`))
	}))
	defer comfy.Close()
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			w.Write([]byte(`{"models":[{"name":"m"}]}`))
			return
		}
		if modelCalls.Add(1) == 1 {
			w.Write([]byte(`{"message":{"tool_calls":[{"function":{"name":"comfy_submit","arguments":{"key":"browser-chat","workflow":{"1":{"class_type":"EmptyImage","inputs":{}}}}}}]},"done":true,"done_reason":"tool_calls"}`))
		} else {
			w.Write([]byte(`{"message":{"content":"finished"},"done":true,"done_reason":"stop"}`))
		}
	}))
	defer model.Close()
	cfg, _, _ := createIntegrationConfig(t, `{}`)
	cfg.Tools.Enabled = false
	cfg.Tools.CreateEnabled = false
	cfg.Providers[0].Endpoint = model.URL
	root := t.TempDir()
	os.Chmod(root, 0700)
	cfg.ComfyUI = config.ComfyUI{Enabled: true, Endpoint: comfy.URL, ReceiptDirectory: root}
	presented := make(chan tools.ApprovalPrompt, 1)
	svc, e := NewServiceWithToolControls(cfg, nil, nil, nil, nil, nil, nil, nil, func(_ context.Context, p tools.ApprovalPrompt) error { presented <- p; return nil })
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	dispatcher, e := StartDispatcher(ctx, svc)
	if e != nil {
		t.Fatal(e)
	}
	defer dispatcher.Close()
	done := make(chan error, 1)
	go func() {
		_, e := svc.RunSubmission(ctx, "comfy-browser-submission", Request{ModelID: "m", Prompt: "run workflow"})
		done <- e
	}()
	var prompt tools.ApprovalPrompt
	select {
	case prompt = <-presented:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if remoteCalls.Load() != 0 {
		t.Fatal("sent before browser approval")
	}
	bridge := &BrowserMutations{service: svc}
	page, e := bridge.Approvals(ctx, prompt.Request.TaskID, "", 10)
	if e != nil || len(page.Items) != 1 || page.Items[0].Proposal == nil || page.Items[0].Proposal.Kind != "comfy_workflow" || !strings.Contains(string(page.Items[0].Proposal.Arguments), "EmptyImage") {
		t.Fatal("exact browser preview missing", page, e)
	}
	_, e = svc.DecideApproval(ctx, approvals.Command{ID: "browser-decision", Expected: prompt.Request, Allowed: true}, "browser_operator")
	if e != nil {
		t.Fatal(e)
	}
	if e = <-done; e != nil || remoteCalls.Load() != 1 {
		t.Fatal("browser-approved job failed", e, remoteCalls.Load())
	}
}
