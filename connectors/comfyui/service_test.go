package comfyui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestComfyServiceOwnedJobsAndRecovery(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Write([]byte(`{"prompt_id":"job-1","number":0}`))
	}))
	defer s.Close()
	root := t.TempDir()
	os.Chmod(root, 0700)
	svc, e := OpenService(s.URL, root)
	if e != nil {
		t.Fatal(e)
	}
	a, e := svc.Submit(context.Background(), "owned", workflow())
	if e != nil || a.State != "accepted" {
		t.Fatal(a, e)
	}
	again, e := svc.Submit(context.Background(), "owned", workflow())
	if e != nil || again.PromptID != a.PromptID || calls.Load() != 1 {
		t.Fatal("replayed")
	}
	w := workflow()
	w["1"] = Node{ClassType: "Other", Inputs: map[string]any{}}
	if _, e = svc.Submit(context.Background(), "owned", w); e == nil {
		t.Fatal("graph conflict accepted")
	}
	if _, e = svc.Submit(context.Background(), "../escape", workflow()); e == nil {
		t.Fatal("escaped")
	}
	jobs, e := svc.List()
	if e != nil || len(jobs) != 1 {
		t.Fatal(jobs, e)
	}
	os.Symlink(filepath.Join(root, "owned.json"), filepath.Join(root, "linked.json"))
	if _, e = svc.Status(context.Background(), "linked"); e == nil {
		t.Fatal("symlink accepted")
	}
	os.Chmod(root, 0755)
	if _, e = OpenService(s.URL, root); e == nil {
		t.Fatal("public receipts allowed")
	}
}
