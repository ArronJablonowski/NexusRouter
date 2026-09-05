package v1_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	run "github.com/ArronJablonowski/DarwinRouter/runtime"
)

// This compiles outside the repository's import tree, so internal-package
// access cannot accidentally substitute for a usable public SDK boundary.
func TestExternalModuleConsumer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Error(err)
			return
		}
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": "answer fake-sdk-private-marker"}, "done": true, "done_reason": "stop"})
	}))
	defer provider.Close()
	defer cancel()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("source location unavailable")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "../.."))
	module, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	goVersion := ""
	for _, line := range strings.Split(string(module), "\n") {
		if strings.HasPrefix(line, "go ") {
			goVersion = strings.TrimSpace(strings.TrimPrefix(line, "go "))
		}
	}
	if goVersion == "" {
		t.Fatal("missing Go version")
	}
	dir := t.TempDir()
	verify := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var event run.Event
		if json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&event) != nil {
			http.Error(w, "invalid event", 400)
			return
		}
		db, err := telemetry.OpenReadOnly(r.Context(), filepath.Join(dir, "task.db"))
		if err != nil {
			http.Error(w, "not committed", 500)
			return
		}
		defer db.Close()
		events, err := db.Read(r.Context(), event.TaskID, event.Sequence-1, 1)
		if err != nil || len(events) != 1 || events[0].ID != event.ID || events[0].Sequence != event.Sequence {
			http.Error(w, "not committed", 500)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer verify.Close()
	mod := fmt.Sprintf("module example.org/externalconsumer\n\ngo %s\n\nrequire github.com/ArronJablonowski/DarwinRouter v0.0.0\nreplace github.com/ArronJablonowski/DarwinRouter => %q\n", goVersion, root)
	config := fmt.Sprintf(`version: 1
mode: local_only
telemetry:
  database: %q
providers:
  - id: fixture
    kind: ollama
    endpoint: %q
    api_key_env: SDK_FIXTURE_SECRET
models:
  - id: chat
    provider: fixture
    model: fixture
    locality: local
    capabilities: [chat]
    context_tokens: 8192
    estimated_cost: 0
    ram_bytes: 1
`, filepath.Join(dir, "task.db"), provider.URL)
	for name, body := range map[string]string{"go.mod": mod, "main.go": externalConsumerSource, "config.yaml": config} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.CommandContext(ctx, "go", "run", "-mod=mod", ".", filepath.Join(dir, "config.yaml"), verify.URL)
	cmd.Dir = dir
	cmd.WaitDelay = time.Second
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key == "GOWORK" || key == "GOPROXY" || key == "GOSUMDB" || key == "GOTOOLCHAIN" || key == "GOFLAGS" || strings.HasPrefix(key, "DARWIN_") {
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	cmd.Env = append(cmd.Env, "GOWORK=off", "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "GOFLAGS=")
	output, err := cmd.CombinedOutput()
	if strings.Contains(string(output), "fake-sdk-private-marker") {
		t.Fatal("consumer exposed fixture credential")
	}
	if err != nil {
		t.Fatalf("external consumer failed: %v\n%s", err, output)
	}
	if strings.TrimSpace(string(output)) != "SDK_EXTERNAL_OK" || calls.Load() != 2 {
		t.Fatalf("unexpected consumer result: calls=%d output=%s", calls.Load(), output)
	}
}

const externalConsumerSource = `package main
import (
 "bytes"
 "context"
 "encoding/json"
 "errors"
 "fmt"
 "net/http"
 "os"
 "strings"
 "time"
 sdk "github.com/ArronJablonowski/DarwinRouter/sdk/v1"
 "github.com/ArronJablonowski/DarwinRouter/runtime"
 "github.com/ArronJablonowski/DarwinRouter/submissions"
)
func check(ok bool, label string) { if !ok { panic(label) } }
func main() {
 ctx,cancel:=context.WithTimeout(context.Background(),20*time.Second);defer cancel()
 client,err:=sdk.New(sdk.ConfigOptions{ProjectFile:os.Args[1],LookupSecret:func(name string)string{if name=="SDK_FIXTURE_SECRET" {return "fake-sdk-private-marker"};return ""}})
 check(err==nil,"construction failed")
 var seq int64
 var task string
 var terminal bool
 result,err:=client.RunStream(ctx,sdk.Request{Version:1,ModelID:"chat",Prompt:"first"},func(e runtime.Event)error{
  check(e.Sequence==seq+1,"unordered event");seq=e.Sequence
  if task=="" {check(e.Kind==runtime.TaskStarted,"missing start");task=e.TaskID}
  check(e.TaskID==task,"foreign event")
  check(!strings.Contains(e.Data.Text,"fake-sdk-private-marker"),"event secret")
  body,err:=json.Marshal(e);check(err==nil,"event encoding")
  req,err:=http.NewRequestWithContext(ctx,"POST",os.Args[2],bytes.NewReader(body));check(err==nil,"verification request")
  response,err:=http.DefaultClient.Do(req);check(err==nil,"verification unavailable")
  response.Body.Close();check(response.StatusCode==204,"event not committed before callback")
  if e.Kind==runtime.TaskCompleted {terminal=true}
  return nil
 })
 check(err==nil&&result.Version==1&&result.TaskID==task&&terminal&&seq>2,"stream failed")
 check(strings.Contains(result.Text,"answer")&&!strings.Contains(result.Text,"fake-sdk-private-marker"),"result secret")
 snapshot,err:=client.InspectTask(ctx,task)
 check(err==nil&&snapshot.Version==1&&snapshot.TaskID==task&&snapshot.State=="completed"&&len(snapshot.Messages)==2,"inspection failed")
 check(strings.Contains(snapshot.Messages[1].Content,"answer")&&!strings.Contains(snapshot.Messages[1].Content,"fake-sdk-private-marker"),"inspection secret")
 check(client.Feedback(ctx,task,false,0)==nil,"feedback failed")
 check(client.Feedback(ctx,task,false,0)==nil,"feedback retry failed")
 history,err:=client.FeedbackHistory(ctx,task)
 check(err==nil&&len(history)==1&&!history[0].Checks[0].Passed,"feedback history failed")
 check(client.ReviseFeedback(ctx,task,history[0].ID,true)==nil,"feedback revision failed")
 history,err=client.FeedbackHistory(ctx,task)
 check(err==nil&&len(history)==2&&history[1].Checks[0].Passed,"revision history failed")
 _,err=client.Run(ctx,sdk.Request{Version:2,ModelID:"chat",Prompt:"invalid"})
 check(err!=nil,"version accepted")
 _,err=client.RunStream(ctx,sdk.Request{Version:1,ModelID:"chat",Prompt:"do not dispatch"},func(runtime.Event)error{return errors.New("private callback detail")})
 check(err!=nil&&!strings.Contains(err.Error(),"private callback detail"),"delivery error unsafe")
 result,err=client.Run(ctx,sdk.Request{Version:1,ModelID:"chat",Prompt:"second"})
 check(err==nil&&result.Version==1&&result.TaskID!="","run failed")
 queuedRequest:=sdk.Request{Version:1,ModelID:"chat",Prompt:"queued only; do not execute"}
 queued,err:=client.Submit(ctx,"external-submission-key",queuedRequest)
 check(err==nil&&queued.Version==1&&queued.ID!=""&&queued.State=="queued"&&len(queued.TaskIDs)==0,"submission failed")
 retried,err:=client.Submit(ctx,"external-submission-key",queuedRequest)
 check(err==nil&&retried.ID==queued.ID&&retried.CreatedAt.Equal(queued.CreatedAt)&&retried.State=="queued","submission retry changed work")
 page,err:=client.ListSubmissions(ctx,submissions.ListOptions{State:"queued",Limit:10})
 check(err==nil&&page.Version==1&&len(page.Items)==1&&page.Items[0].ID==queued.ID&&page.Items[0].State=="queued","submission discovery failed")
 status,err:=client.SubmissionStatus(ctx,queued.ID)
 check(err==nil&&status.ID==queued.ID&&status.State=="queued"&&status.Result==nil,"submission status failed")
 canceled,err:=client.CancelSubmission(ctx,queued.ID)
 check(err==nil&&canceled.ID==queued.ID&&canceled.State=="canceled"&&canceled.CancelRequested&&len(canceled.TaskIDs)==0,"submission cancel failed")
 status,err=client.SubmissionStatus(ctx,queued.ID)
 check(err==nil&&status.State=="canceled"&&status.Result==nil,"cancellation not durable")
 fmt.Println("SDK_EXTERNAL_OK")
}
`
