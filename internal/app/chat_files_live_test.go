package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/tools"
)

// Explicit opt-in; submits one tiny solid-color image, never model downloads,
// inference-service changes, cancellation, queue clearing, or retries.
func TestChatFilesSparkLive(t *testing.T) {
	endpoint := os.Getenv("NEXUS_COMFY_LIVE_ENDPOINT")
	if endpoint == "" {
		t.Skip("explicit live ComfyUI endpoint required")
	}
	key := "chat-file-test-" + rand.Text()
	workflow := map[string]any{"1": map[string]any{"class_type": "EmptyImage", "inputs": map[string]any{"width": 32, "height": 32, "batch_size": 1, "color": 65280}}, "2": map[string]any{"class_type": "SaveImage", "inputs": map[string]any{"images": []any{"1", 0}, "filename_prefix": "NexusRouterTests/chat-files-" + key}}}
	args := map[string]any{"key": key, "workflow": workflow}
	var calls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			w.Write([]byte(`{"models":[{"name":"m"}]}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1) == 1 {
			json.NewEncoder(w).Encode(map[string]any{"message": map[string]any{"tool_calls": []any{map[string]any{"function": map[string]any{"name": "comfy_submit", "arguments": args}}}}, "done": true, "done_reason": "tool_calls"})
		} else {
			w.Write([]byte(`{"message":{"content":"Your generated image is ready to observe."},"done":true,"done_reason":"stop"}`))
		}
	}))
	defer model.Close()
	cfg, _, _ := createIntegrationConfig(t, `{}`)
	cfg.Tools.Enabled = false
	cfg.Tools.CreateEnabled = false
	cfg.Providers[0].Endpoint = model.URL
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	cfg.ComfyUI = config.ComfyUI{Enabled: true, Endpoint: endpoint, ReceiptDirectory: dir}
	svc, e := NewServiceWithToolApproval(cfg, nil, nil, nil, nil, nil, nil, func(_ context.Context, p tools.ApprovalPrompt) (string, bool, error) {
		if p.Request.ToolName != "comfy_submit" || !strings.Contains(string(p.Arguments), key) {
			t.Error("unexpected proposal")
			return "", false, nil
		}
		return "live-test-operator", true, nil
	})
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	result, e := svc.Run(ctx, Request{ModelID: "m", Prompt: "Create one tiny green image to verify chat attachments"})
	if e != nil {
		t.Fatal("submission failed; never resend", result, e)
	}
	for {
		page, e := svc.ChatFiles(ctx, result.TaskID)
		if e != nil {
			t.Fatal(e)
		}
		if len(page.Files) == 1 {
			f, b, e := svc.ChatFile(ctx, result.TaskID, page.Files[0].ID)
			if e != nil {
				t.Fatal(e)
			}
			image, e := png.Decode(bytes.NewReader(b))
			if e != nil || image.Bounds().Dx() != 32 || image.Bounds().Dy() != 32 {
				t.Fatal("live PNG", e)
			}
			r, g, blue, _ := image.At(16, 16).RGBA()
			if g != 65535 || r != 0 || blue != 0 {
				t.Fatal("live pixel mismatch", r, g, blue)
			}
			if path := os.Getenv("NEXUS_CHAT_FILE_LIVE_OUTPUT"); path != "" {
				if e = os.WriteFile(path, b, 0600); e != nil {
					t.Fatal(e)
				}
			}
			// Cached downloads remain usable with the connector disabled.
			svc.settings.ComfyUI.Enabled = false
			_, saved, e := svc.ChatFile(ctx, result.TaskID, f.ID)
			if e != nil || !bytes.Equal(saved, b) {
				t.Fatal("cached live download", e)
			}
			t.Log("real Spark output collected into chat; exact PNG download and offline snapshot verified", f.Size)
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("observation timeout; no replay", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	if calls.Load() != 2 {
		t.Fatal("unexpected model turns", calls.Load())
	}
}
