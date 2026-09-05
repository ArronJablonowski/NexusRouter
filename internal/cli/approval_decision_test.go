package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/approvals"
)

func TestCLIApprovalDecisionAndRetry(t *testing.T) {
	path, request := cliApprovalFixture(t)
	config := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(config, []byte(fmt.Sprintf("telemetry:\n  database: %q\n", path)), 0600); err != nil {
		t.Fatal(err)
	}
	command := approvals.Command{Expected: request, ID: "cli_decision", Allowed: true}
	body, _ := json.Marshal(command)
	var first approvals.Record
	for range 2 {
		var out, errout bytes.Buffer
		if code := RunWithInput([]string{"approval-decision", "--config", config}, bytes.NewReader(body), &out, &errout, "test"); code != 0 {
			t.Fatal(code, errout.String())
		}
		var r approvals.Record
		if json.Unmarshal(out.Bytes(), &r) != nil || r.State != approvals.Approved || len(r.Decisions) != 1 || !strings.HasPrefix(r.Decisions[0].Actor, "local_uid:") {
			t.Fatal(out.String())
		}
		if first.Request.ID != "" && !first.Decisions[0].Time.Equal(r.Decisions[0].Time) {
			t.Fatal("retry changed time")
		}
		first = r
	}
	command.ID = "revoke"
	command.Allowed = false
	body, _ = json.Marshal(command)
	var out, errout bytes.Buffer
	if code := runApprovalDecision([]string{"--config", config}, bytes.NewReader(body), &out, &errout); code != 0 {
		t.Fatal(code, errout.String())
	}
	var r approvals.Record
	if json.Unmarshal(out.Bytes(), &r) != nil || r.State != approvals.Revoked {
		t.Fatal(out.String())
	}
}

func TestCLIApprovalInputBoundsAndCanceledPipe(t *testing.T) {
	for _, body := range []string{"{}", `{"actor":"injected"}`, strings.Repeat("x", (16<<10)+1)} {
		var out, errout bytes.Buffer
		if code := runApprovalDecision([]string{"--config", "missing"}, strings.NewReader(body), &out, &errout); code != 1 || out.Len() != 0 {
			t.Fatal(code, out.String())
		}
	}
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	defer write.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err = readApprovalDecisionInput(ctx, read); err == nil {
		t.Fatal("blocked pipe ignored cancellation")
	}
}
