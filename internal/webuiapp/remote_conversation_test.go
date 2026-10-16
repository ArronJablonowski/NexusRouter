package webuiapp

import (
	"context"
	"encoding/json"
	"github.com/ArronJablonowski/NexusRouter/attachments"
	"github.com/ArronJablonowski/NexusRouter/remote"
	"github.com/ArronJablonowski/NexusRouter/submissions"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type conversationBackend struct{ *browserRemoteExecution }

func (b *conversationBackend) Info(ctx context.Context) (remote.Info, error) {
	v, e := b.browserRemoteExecution.Info(ctx)
	v.TargetingVersion = 1
	v.ConversationVersion = 1
	return v, e
}
func TestRemoteConversationPinnedHistoryUploadsAndRecovery(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	creds, pin := browserRemoteCredentials(t, dir)
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	peer := remote.Peer{ID: "node-a", Endpoint: "https://" + listener.Addr().String(), ServerName: "node-a", Pins: []string{pin}, Operations: []string{"info", "dispatch", "inspect"}, Models: []string{"chat"}, AllowPrivate: true, MaxContextTokens: 32768}
	st := remote.TrustFile(filepath.Join(dir, "server.json"))
	caller := peer
	caller.ID = "caller"
	if e = st.Replace(remote.Registry{Version: 1, Peers: []remote.Peer{caller}}, "absent"); e != nil {
		t.Fatal(e)
	}
	ct := remote.TrustFile(filepath.Join(dir, "client.json"))
	if e = ct.Replace(remote.Registry{Version: 1, Peers: []remote.Peer{peer}}, "absent"); e != nil {
		t.Fatal(e)
	}
	journal, e := remote.OpenJournal(filepath.Join(dir, "journal"), "node-a")
	if e != nil {
		t.Fatal(e)
	}
	defer journal.Close()
	b := &conversationBackend{&browserRemoteExecution{tasks: map[string]submissions.Status{}}}
	server, e := remote.NewServer("node-a", st, journal, b)
	if e != nil {
		t.Fatal(e)
	}
	httpServer, e := server.HTTPServer(listener.Addr().String(), creds)
	if e != nil {
		t.Fatal(e)
	}
	go httpServer.ServeTLS(listener, "", "")
	defer httpServer.Close()
	routes, e := remote.OpenRouteStore(filepath.Join(dir, "routes"))
	if e != nil {
		t.Fatal(e)
	}
	d := &RecordedRemoteDispatcher{Client: &remote.Client{Trust: ct, Credentials: creds}, Store: routes, UploadDirectory: filepath.Join(dir, "uploads"), PrepareUploads: func(_ context.Context, p string, files []attachments.File) (string, error) {
		return attachments.Encode(p, []attachments.Input{{Name: files[0].Name, Text: string(files[0].Data)}}), nil
	}}
	task := remote.Task{Version: 1, ModelID: "chat", ExpectedModel: "model", Prompt: "Remember this", Domain: "general", Profile: "default", ContextTokens: 8192, Private: true}
	key := "conversation-parent-01"
	initial, e := d.DispatchFiles(ctx, "node-a", key, task, []attachments.File{{Name: "facts.txt", Data: []byte("FACT_ONE")}})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = d.DispatchContinuation(ctx, "node-a", "conversation-child-01", key, task, nil); e == nil {
		t.Fatal("continued queued task")
	}
	b.mu.Lock()
	status := b.tasks[initial.ID]
	status.State = "succeeded"
	status.TaskIDs = []string{"task-one"}
	status.Result = &submissions.Result{TaskID: "task-one", Text: "I remember FACT_ONE"}
	b.tasks[initial.ID] = status
	b.mu.Unlock()
	// Reopen all local evidence to simulate a commanding-process restart.
	d.Store, e = remote.OpenExistingRouteStore(filepath.Join(dir, "routes"))
	if e != nil {
		t.Fatal(e)
	}
	task.Prompt = "What were both facts?"
	if _, e = d.conversation(ctx, "node-a", key); e != nil {
		t.Fatal("stored parent", e)
	}
	if _, e = d.Client.InspectRecorded(ctx, d.Store, key); e != nil {
		t.Fatal("original status", e)
	}
	if v, e := d.Client.Info(ctx, "node-a"); e != nil || v.ConversationVersion != 1 {
		t.Fatal("conversation capability", v, e)
	}
	if _, e = d.continuation(ctx, "node-a", key, task); e != nil {
		t.Fatal("parent context", e)
	}
	changed := task
	changed.ModelID = "other"
	if _, e = d.DispatchContinuation(ctx, "node-a", "conversation-wrong-01", key, changed, nil); e == nil {
		t.Fatal("model switch accepted")
	}
	if _, e = d.DispatchContinuation(ctx, "node-a", "conversation-child-02", key, task, []attachments.File{{Name: "second.txt", Data: []byte("FACT_TWO")}}); e != nil {
		t.Fatal(e)
	}
	b.mu.Lock()
	received := b.received
	b.mu.Unlock()
	if len(received.Messages) != 3 || !strings.Contains(received.Messages[0].Content, "FACT_ONE") || received.Messages[1].Content != "I remember FACT_ONE" || !strings.Contains(received.Messages[2].Content, "FACT_TWO") || received.ExpectedModel != "model" {
		t.Fatal("history or exact model lost", received)
	}
	c, e := d.conversation(ctx, "node-a", "conversation-child-02")
	if e != nil || len(c.Messages) != 3 || c.Messages[0].Text != "Remember this" {
		t.Fatal(c, e)
	}
	if _, e = d.conversation(ctx, "node-b", "conversation-child-02"); e == nil {
		t.Fatal("cross-host history accepted")
	}
	if _, e = d.DispatchContinuation(ctx, "node-a", "conversation-child-02", key, task, nil); e == nil {
		t.Fatal("omitted files accepted")
	}
	if b.creates.Load() != 2 {
		t.Fatal("rejected request dispatched", b.creates.Load())
	}
	// Payloads remain separate from route records and original download scopes.
	record, _ := json.Marshal(mustConversationBinding(t, d, key))
	if strings.Contains(string(record), "FACT_ONE") {
		t.Fatal("route contains content")
	}
	// Upgrade old snapshots into one sidebar row, persist a bound final answer,
	// then reopen while the remote host is unavailable. Reads must not dispatch.
	h := mutationHandlerFixture(t, MutationServices{})
	h.remoteDispatcher = d
	h.remoteTaskController = d.Client
	cookie, _ := authenticateBrowser(t, h)
	get := func(path string, want int) []byte {
		t.Helper()
		r := browserRequest("GET", path, "")
		r.Body = http.NoBody
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		return w.Body.Bytes()
	}
	page := get("/app/api/v1/remote-chats", 200)
	if strings.Count(string(page), `"request_id"`) != 1 || !strings.Contains(string(page), "conversation-child-02") || !strings.Contains(string(page), "Remember this") || strings.Contains(string(page), "FACT_ONE") {
		t.Fatal("sidebar projection", string(page))
	}
	get("/app/api/v1/remote-chats?after=invalid", 400)
	get("/app/api/v1/remote-chats/node-b/conversation-child-02", 404)
	b.mu.Lock()
	var child submissions.Status
	for _, v := range b.tasks {
		if v.ID != initial.ID {
			child = v
		}
	}
	child.State = "succeeded"
	child.TaskIDs = []string{"task-two"}
	child.Result = &submissions.Result{TaskID: "task-two", Text: "FACT_ONE and FACT_TWO"}
	b.tasks[child.ID] = child
	b.mu.Unlock()
	r := authorizedMutationRequest(t, h, "/app/api/v1/remote-task-control", `{"version":1,"instance":"node-a","request_id":"conversation-child-02","action":"status"}`)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "FACT_ONE and FACT_TWO") {
		t.Fatal(w.Code, w.Body.String())
	}
	httpServer.Close()
	d.indexAfter = 0
	saved := get("/app/api/v1/remote-chats/node-a/conversation-child-02", 200)
	if !strings.Contains(string(saved), `"cached":true`) || !strings.Contains(string(saved), "FACT_ONE and FACT_TWO") || strings.Contains(string(saved), `"continuation"`) {
		t.Fatal("cached authority/history", string(saved))
	}
	get("/app/api/v1/remote-chats", 200)
	if b.creates.Load() != 2 {
		t.Fatal("sidebar replayed execution")
	}

	replacement, _ := browserRemoteCredentials(t, t.TempDir())
	d.Client.Credentials = replacement
	if _, e = d.DispatchContinuation(ctx, "node-a", "conversation-rotated-01", key, task, nil); e == nil {
		t.Fatal("rotated caller continued")
	}
}
func mustConversationBinding(t *testing.T, d *RecordedRemoteDispatcher, key string) remote.RouteBinding {
	t.Helper()
	b, e := d.Store.Lookup(key)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
