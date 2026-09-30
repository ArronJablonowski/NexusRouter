package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/approvals"
)

func TestApprovalExecutionCLIReadsBoundStatus(t *testing.T) {
	path, r := cliApprovalFixture(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out, errout bytes.Buffer
	code := Run([]string{"approvals", "execution", "--db", path, "--task", r.TaskID, "--id", r.ID}, &out, &errout, "test")
	var status approvals.ExecutionStatus
	if code != 0 || errout.Len() != 0 || json.Unmarshal(out.Bytes(), &status) != nil || status.Validate() != nil || !status.Approval.Request.Matches(r) || status.Approval.State != approvals.Pending || status.TaskState != "running" || status.CallState != "open" || status.ScopeWriterState != "none" || status.Sequence != 4 {
		t.Fatal(code, out.String(), errout.String())
	}
	if strings.Contains(out.String(), "sensitive-arguments") || strings.Contains(out.String(), `"token"`) {
		t.Fatal("private tool data exposed")
	}
	if status.ScopeLeases == nil || *status.ScopeLeases != (approvals.ScopeLeaseObservation{Version: 1, OverlapPolicyVersion: 1}) {
		t.Fatal("missing explicit empty blocker observation", status.ScopeLeases)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("status inspection changed database", err)
	}
	for _, args := range [][]string{{"execution", "--db", path, "--task", "other", "--id", r.ID}, {"execution", "--db", path, "--task", r.TaskID, "--id", "missing"}} {
		out.Reset()
		errout.Reset()
		if code := runApprovals(args, &out, &errout); code != 1 || out.Len() != 0 {
			t.Fatal(code, out.String(), errout.String())
		}
	}
	if code := runApprovals([]string{"execution", "--db", path, "--task", r.TaskID, "--id", r.ID}, steeringFailedWriter{}, &errout); code != 1 {
		t.Fatal(code)
	}
}

func TestApprovalExecutionCLIRejectsInvalidOrMissingStorage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")
	for _, extra := range [][]string{nil, {"--id", "bad:id"}, {"--id", "id", "--limit", "1"}, {"--id", "id", "--after", "call"}, {"--id", "id", "--id", "again"}} {
		args := append([]string{"execution", "--db", path, "--task", "task"}, extra...)
		var out, errout bytes.Buffer
		if code := runApprovals(args, &out, &errout); code != 2 || out.Len() != 0 || strings.Contains(errout.String(), path) {
			t.Fatal(code, out.String(), errout.String())
		}
	}
	var out, errout bytes.Buffer
	if code := runApprovals([]string{"execution", "--db", path, "--task", "task", "--id", "approval"}, &out, &errout); code != 1 || out.Len() != 0 {
		t.Fatal(code, out.String(), errout.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("inspection created missing storage", err)
	}
}
