package cli

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"go.yaml.in/yaml/v3"
)

func TestTraceExportCLI(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Telemetry.Database = filepath.Join(dir, "state.db")
	encoded, _ := yaml.Marshal(cfg)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := telemetry.Open(context.Background(), cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-time.Second)
	for i, kind := range []runtime.Kind{runtime.TaskStarted, runtime.TaskCompleted} {
		e := runtime.Event{Version: 1, ID: "private-event-" + string(rune('a'+i)), TaskID: "private-task", SessionID: "private-session", CorrelationID: "private-correlation", Sequence: int64(i + 1), Time: base.Add(time.Duration(i) * time.Millisecond), Kind: kind}
		if err := db.Append(context.Background(), int64(i), e); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/v1/traces" || !bytes.Contains(body, []byte("resourceSpans")) || bytes.Contains(body, []byte("private-")) {
			t.Error("invalid trace export")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, "{}")
	}))
	defer server.Close()
	var out, diagnostic bytes.Buffer
	args := []string{"traces", "export", "--config=" + path, "--endpoint=" + server.URL + "/v1/traces", "--limit=1"}
	if code := Run(args, &out, &diagnostic, "test"); code != 0 || out.String() != "{\"exported\":true}\n" || diagnostic.Len() != 0 {
		t.Fatal(code, out.String(), diagnostic.String())
	}
	for _, invalid := range [][]string{{"traces"}, {"traces", "export"}, append(args, "private-extra"), append(args, "--limit=2")} {
		out.Reset()
		diagnostic.Reset()
		if code := Run(invalid, &out, &diagnostic, "test"); code != 2 || out.Len() != 0 || strings.Contains(diagnostic.String(), "private-") {
			t.Fatal(code, diagnostic.String())
		}
	}
}
