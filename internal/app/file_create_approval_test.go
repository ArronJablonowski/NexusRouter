package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/approvals"
	"github.com/ArronJablonowski/NexusRouter/internal/browserops"
	"github.com/ArronJablonowski/NexusRouter/tools"
	contract "github.com/ArronJablonowski/NexusRouter/webui"
)

func TestBrowserFileCreateExactPreviewBeforeApproval(t *testing.T) {
	cfg, _, _ := createIntegrationConfig(t, `{"path":"chat-report.md","content":"# Reviewed document\nExact content <script>plain text</script>\n"}`)
	presented := make(chan tools.ApprovalPrompt, 1)
	svc, e := NewServiceWithToolControls(cfg, nil, nil, nil, nil, nil, nil, nil, func(_ context.Context, p tools.ApprovalPrompt) error { presented <- p; return nil })
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	dispatcher, e := StartDispatcher(ctx, svc)
	if e != nil {
		t.Fatal(e)
	}
	defer dispatcher.Close()
	done := make(chan error, 1)
	go func() {
		_, e := svc.RunSubmission(ctx, "file-browser-submission", Request{ModelID: "m", Prompt: "Create the reviewed document"})
		done <- e
	}()
	var prompt tools.ApprovalPrompt
	select {
	case prompt = <-presented:
	case e := <-done:
		t.Fatal("request stopped before review", e)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, e = os.Stat(filepath.Join(cfg.Tools.CreateRoot, "chat-report.md")); !os.IsNotExist(e) {
		t.Fatal("created before approval", e)
	}
	ops, e := browserops.Open(ctx, cfg.Telemetry.Database)
	if e != nil {
		t.Fatal(e)
	}
	defer ops.Close()
	bridge, e := NewBrowserMutations(svc, ops)
	if e != nil {
		t.Fatal(e)
	}
	page, e := bridge.Approvals(ctx, prompt.Request.TaskID, "", 10)
	if e != nil || len(page.Items) != 1 || page.Items[0].Proposal == nil || page.Items[0].Proposal.Kind != "file_create" {
		t.Fatal(page, e)
	}
	var a struct{ Path, Content string }
	if json.Unmarshal(page.Items[0].Proposal.Arguments, &a) != nil || a.Path != "chat-report.md" || a.Content != "# Reviewed document\nExact content <script>plain text</script>\n" {
		t.Fatal("inexact preview", a)
	}
	tampered := prompt.Request
	tampered.ArgumentsDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, e = bridge.fileCreateApprovalProposal(ctx, approvals.Record{Request: tampered}); e == nil {
		t.Fatal("digest mismatch accepted")
	}
	command := contract.ApprovalRequest{Version: 1, IdempotencyKey: "file-browser-allow", TaskID: prompt.Request.TaskID, ApprovalID: prompt.Request.ID, Action: contract.ApprovalAllow, ExpectedRevision: page.Items[0].Revision}
	raw, e := sql.Open("sqlite", cfg.Telemetry.Database)
	if e != nil {
		t.Fatal(e)
	}
	defer raw.Close()
	if _, e = raw.Exec(`CREATE TRIGGER fail_create_receipt BEFORE UPDATE ON browser_operations WHEN NEW.state='committed' BEGIN SELECT RAISE(ABORT,'injected receipt failure'); END`); e != nil {
		t.Fatal(e)
	}
	_, e = bridge.DecideApproval(ctx, browserMutationTestSubject, command)
	if !errors.Is(e, ErrBrowserMutation) {
		t.Fatal("receipt failure not surfaced", e)
	}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	// The write has finished and there is no pending tool preview. Recover the
	// same operation from its consumed durable decision, without another write.
	if _, e = raw.Exec(`DROP TRIGGER fail_create_receipt`); e != nil {
		t.Fatal(e)
	}
	receipt, e := bridge.DecideApproval(ctx, browserMutationTestSubject, command)
	if e != nil || receipt.State != approvals.Consumed || receipt.Revision != 3 {
		t.Fatal("consumed decision not recovered", receipt, e)
	}
	current, e := InspectApproval(ctx, cfg.Telemetry.Database, prompt.Request.TaskID, prompt.Request.ID)
	if e != nil || len(current.Decisions) != 1 {
		t.Fatal("recovery duplicated decision", current, e)
	}
	replay, e := bridge.DecideApproval(ctx, browserMutationTestSubject, command)
	if e != nil || replay != receipt {
		t.Fatal("completed write receipt replay changed", replay, e)
	}
	files, e := svc.ChatFiles(ctx, prompt.Request.TaskID)
	if e != nil || len(files.Files) != 1 {
		t.Fatal(files, e)
	}
	_, b, e := svc.ChatFile(ctx, prompt.Request.TaskID, files.Files[0].ID)
	if e != nil || string(b) != a.Content {
		t.Fatal("download differs from review", e)
	}
}

func TestQueuedFileCreateRejectsSynchronousReviewer(t *testing.T) {
	cfg, _, _ := createIntegrationConfig(t, `{"path":"headless.txt","content":"must not write"}`)
	svc, e := NewServiceWithToolApproval(cfg, nil, nil, nil, nil, nil, nil, func(context.Context, tools.ApprovalPrompt) (string, bool, error) {
		t.Error("background synchronous reviewer called")
		return "operator", true, nil
	})
	if e != nil {
		t.Fatal(e)
	}
	// Exercise the same admission boundary a queued dispatcher uses, without
	// waiting for an independently scheduled submission status.
	_, e = svc.Run(context.Background(), Request{ModelID: "m", Prompt: "create", submissionID: "queued-file"})
	if e == nil {
		t.Fatal("queued write admitted without presenter")
	}
	if _, e = os.Stat(filepath.Join(cfg.Tools.CreateRoot, "headless.txt")); !os.IsNotExist(e) {
		t.Fatal("headless output exists", e)
	}
}
