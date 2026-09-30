package v1_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
)

func appendTraceTask(t *testing.T, path string) {
	t.Helper()
	db, err := telemetry.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-time.Second)
	for i, kind := range []runtime.Kind{runtime.TaskStarted, runtime.TaskCompleted} {
		e := runtime.Event{Version: 1, ID: "private-event-" + string(rune('a'+i)), TaskID: "private-task", SessionID: "private-session", CorrelationID: "private-correlation", Sequence: int64(i + 1), Time: base.Add(time.Duration(i) * time.Millisecond), Kind: kind}
		if err := db.Append(context.Background(), int64(i), e); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSDKTraceSnapshotAndExport(t *testing.T) {
	options, path := sdkToolOptions(t)
	options.LookupSecret = func(name string) string {
		if name == "TRACE_KEY" {
			return "fixture-token"
		}
		return ""
	}
	c, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.TraceSnapshot(context.Background(), 1); err == nil {
		t.Fatal("missing database accepted")
	}
	appendTraceTask(t, path)
	snapshot, err := c.TraceSnapshot(context.Background(), 1)
	if err != nil || snapshot.Validate() != nil || len(snapshot.Traces) != 1 {
		t.Fatal(snapshot, err)
	}
	before, _ := os.ReadFile(path)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/traces" || r.Header.Get("Authorization") != "Bearer fixture-token" || !bytes.Contains(body, []byte("resourceSpans")) || bytes.Contains(body, []byte("private-")) || bytes.Contains(body, []byte("fixture-token")) {
			t.Error("invalid trace request", string(body))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, "{}")
	}))
	defer server.Close()
	if err := c.ExportTraces(context.Background(), sdk.TraceExportOptions{Endpoint: server.URL + "/v1/traces", APIKeyEnv: "TRACE_KEY", Limit: 1}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("trace inspection mutated database")
	}
	for _, client := range []*sdk.Client{nil, {}} {
		if err := client.ExportTraces(context.Background(), sdk.TraceExportOptions{}); err != sdk.ErrAdmission {
			t.Fatal(err)
		}
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.ExportTraces(canceled, sdk.TraceExportOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if strings.Contains(string(before), "fixture-token") {
		t.Fatal("secret entered database")
	}
}
