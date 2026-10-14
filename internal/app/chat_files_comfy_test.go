package app

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/connectors/comfyui"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func TestChatFilesCollectOnlyOwnedComfyOutputsAndRetainSnapshots(t *testing.T) {
	ctx := context.Background()
	s := submissionService(t)
	var body bytes.Buffer
	png.Encode(&body, image.NewRGBA(image.Rect(0, 0, 16, 16)))
	var submits, downloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/prompt":
			submits.Add(1)
			w.Write([]byte(`{"prompt_id":"owned-prompt","number":0}`))
		case "/history/owned-prompt":
			w.Write([]byte(`{"owned-prompt":{"status":{"completed":true,"status_str":"success"},"outputs":{"11":{"images":[{"filename":"car.png","subfolder":"test","type":"output"}]}}}}`))
		case "/view":
			downloads.Add(1)
			w.Header().Set("Content-Type", "image/png")
			w.Write(body.Bytes())
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	s.settings.ComfyUI = config.ComfyUI{Enabled: true, Endpoint: server.URL, ReceiptDirectory: dir}
	service, e := comfyui.OpenService(server.URL, dir)
	if e != nil {
		t.Fatal(e)
	}
	workflow, e := comfyui.ParseWorkflow([]byte(`{"1":{"class_type":"EmptyImage","inputs":{}}}`))
	if e != nil {
		t.Fatal(e)
	}
	job, e := service.Submit(ctx, "owned", workflow)
	if e != nil {
		t.Fatal(e)
	}
	encoded, _ := json.Marshal(job)
	recordChatFileTool(t, s, "image-task", "image-chat", "comfy_submit", `{"key":"owned"}`, string(encoded), runtime.ConfirmedEffect)
	// An unrelated inspection cannot grant chat access to another job.
	recordChatFileTool(t, s, "inspect-task", "other-chat", "comfy_inspect", `{"kind":"jobs"}`, string(encoded), runtime.NoEffect)
	page, e := s.ChatFiles(ctx, "image-chat")
	if e != nil || len(page.Files) != 1 || page.Pending {
		t.Fatal(page, e)
	}
	other, e := s.ChatFiles(ctx, "other-chat")
	if e != nil || len(other.Files) != 0 {
		t.Fatal("inspection exposed jobs", other, e)
	}
	again, e := s.ChatFiles(ctx, "image-chat")
	if e != nil || len(again.Files) != 1 || downloads.Load() != 1 || submits.Load() != 1 {
		t.Fatal("observation repeated effects", again, e, downloads.Load(), submits.Load())
	}
	server.Close()
	page, e = s.ChatFiles(ctx, "image-chat")
	if e != nil || len(page.Files) != 1 || !page.Pending {
		t.Fatal("outage hid snapshot", page, e)
	}
	_, b, e := s.ChatFile(ctx, "image-chat", page.Files[0].ID)
	if e != nil || !bytes.Equal(b, body.Bytes()) {
		t.Fatal("offline saved download", e)
	}
}
