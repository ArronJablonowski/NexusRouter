package remotecli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestCentralLoggingCLIExplicitReadAndMetadataStatus(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	for _, args := range [][]string{{"logs-status", "--log-store", dir}, {"logs-read", "--log-store", dir, "--instance", "node-a", "--stream", "runtime"}} {
		var out, stderr bytes.Buffer
		if e := Run(context.Background(), args, strings.NewReader(""), &out, &stderr); e != nil || strings.TrimSpace(out.String()) != "[]" {
			t.Fatal(e, out.String(), stderr.String())
		}
	}
	for _, args := range [][]string{{"logs-read", "--log-store", dir, "--instance", "node-a", "--limit", "101"}, {"collect-logs", "--log-store", dir, "--watch", "--interval", "1ms"}, {"logs-read", "--log-store", dir, "--instance", "node-a", "--stream", "unknown"}} {
		var out, stderr bytes.Buffer
		if e := Run(context.Background(), args, strings.NewReader(""), &out, &stderr); e == nil || out.Len() != 0 {
			t.Fatal("invalid command accepted", args, e, out.String())
		}
	}
}
