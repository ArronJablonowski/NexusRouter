package webuiapp

import (
	"context"
	"errors"
	"github.com/ArronJablonowski/NexusRouter/attachments"
	"github.com/ArronJablonowski/NexusRouter/internal/chatfiles"
	"github.com/ArronJablonowski/NexusRouter/remote"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestRemoteUploadOriginalBindingConcurrent(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	routes, e := remote.OpenRouteStore(filepath.Join(dir, "routes"))
	if e != nil {
		t.Fatal(e)
	}
	var preparations atomic.Int32
	d := &RecordedRemoteDispatcher{Client: &remote.Client{}, Store: routes, UploadDirectory: filepath.Join(dir, "uploads"), PrepareUploads: func(_ context.Context, p string, fs []attachments.File) (string, error) {
		preparations.Add(1)
		return attachments.Encode(p, []attachments.Input{{Name: fs[0].Name, Text: "same extracted text"}}), nil
	}}
	task := remote.Task{Version: 1, ModelID: "model-a", Prompt: "Read", Domain: "general", Profile: "default", ContextTokens: 8192, Private: true}
	var wg sync.WaitGroup
	var rejected atomic.Int32
	for _, b := range []string{"first original", "second original"} {
		wg.Add(1)
		go func(b string) {
			defer wg.Done()
			_, e := d.DispatchFiles(ctx, "node-a", "concurrent-upload-0001", task, []attachments.File{{Name: "document.txt", Data: []byte(b)}})
			if errors.Is(e, errRemoteUpload) {
				rejected.Add(1)
			}
		}(b)
	}
	wg.Wait()
	if rejected.Load() != 1 || preparations.Load() != 1 {
		t.Fatal("original binding race", rejected.Load(), preparations.Load())
	}
	if _, e = d.Dispatch(ctx, "node-a", "concurrent-upload-0001", task); !errors.Is(e, errRemoteUpload) {
		t.Fatal("omitted originals bypassed binding")
	}
	store, e := chatfiles.Open(ctx, d.UploadDirectory)
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	scope := remoteUploadScope("node-a", "concurrent-upload-0001")
	files, e := store.List(ctx, scope)
	if e != nil || len(files) != 1 {
		t.Fatal(files, e)
	}
	if other, _ := store.List(ctx, remoteUploadScope("node-b", "concurrent-upload-0001")); len(other) != 0 {
		t.Fatal("host scope leaked")
	}
}

func TestRemoteUploadEncodedTransportBound(t *testing.T) {
	task := remote.Task{Version: 1, ModelID: "model-a", Prompt: strings.Repeat("<", remote.MaxBody/2), Domain: "general", Profile: "default", ContextTokens: 8192, Private: true}
	if task.Validate() != nil || remoteTaskFits(task) {
		t.Fatal("JSON escaping bypassed transport bound")
	}
	task.Prompt = "Read document"
	if !remoteTaskFits(task) {
		t.Fatal("ordinary remote task rejected")
	}
}
