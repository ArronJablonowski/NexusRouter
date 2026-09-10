package cli

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServeRejectsUnwiredWorkboardSchedulerBeforeStorageOrListen(t *testing.T) {
	t.Setenv("DARWIN_API_TOKEN", strings.Repeat("test-token-", 4))
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := probe.Addr().String()
	probe.Close()
	database := filepath.Join(t.TempDir(), "must-not-exist.db")
	configuration := filepath.Join(t.TempDir(), "config.yaml")
	body := "daemon:\n  listen: \"" + address + "\"\n" +
		"providers:\n  - id: local\n    kind: ollama\n    endpoint: http://127.0.0.1:11434\n" +
		"models:\n  - id: worker\n    provider: local\n    model: worker-native\n    locality: local\n    capabilities: [chat]\n    context_tokens: 8192\n    estimated_cost: 0\n" +
		"  - id: reviewer\n    provider: local\n    model: reviewer-native\n    locality: local\n    capabilities: [chat]\n    context_tokens: 8192\n    estimated_cost: 0\n" +
		"evaluation:\n  llm_judge_enabled: true\nworkboard:\n  enabled: true\n  scheduler:\n    enabled: true\n    worker_model: worker\n" +
		"    acceptance_judge:\n      enabled: true\n      reviewer_model: reviewer\n      max_cost: 0.01\n      timeout: 30s\n" +
		"telemetry:\n  database: \"" + database + "\"\n"
	if err := os.WriteFile(configuration, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := runServe([]string{"--config", configuration}, &stdout, &stderr); code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "workboard scheduler is not available") {
		t.Fatalf("unexpected serve result: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(database); !os.IsNotExist(err) {
		t.Fatal("refused scheduler configuration touched durable storage")
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal("refused scheduler configuration acquired the listener", err)
	}
	listener.Close()
}
