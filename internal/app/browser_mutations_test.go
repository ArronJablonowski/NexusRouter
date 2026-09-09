package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/approvals"
	"github.com/ArronJablonowski/DarwinRouter/internal/browserops"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	contract "github.com/ArronJablonowski/DarwinRouter/webui"
)

const browserMutationTestSubject = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func browserApprovalRecord(state string, decisions []approvals.Decision, expires time.Time) approvals.Record {
	now := time.Now().UTC().Truncate(time.Second)
	request := approvals.Request{Version: 1, ID: "approval", TaskID: "task", TurnID: "turn", ToolCallID: "call", ToolName: "replace_file", ToolBehavior: runtime.BehaviorNonIdempotentWrite, Scope: "private-secret-scope", ArgumentsDigest: strings.Repeat("a", 64), SchemaDigest: strings.Repeat("b", 64), PolicyDigest: strings.Repeat("c", 64), CreatedAt: now.Add(-time.Minute), ExpiresAt: expires}
	return approvals.Record{Request: request, State: state, Decisions: decisions}
}

func TestBrowserFeedbackCoexistsWithEvaluationAndReplays(t *testing.T) {
	svc, cfg := autoFixture(t)
	ctx := context.Background()
	out, err := svc.Run(ctx, Request{Prompt: "hello", Domain: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	if err = RecordFeedback(ctx, cfg.Telemetry.Database, out.TaskID, true, 0); err != nil {
		t.Fatal(err)
	}
	store, err := browserops.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	mutations, err := NewBrowserMutations(svc, store)
	if err != nil {
		t.Fatal(err)
	}
	cost := .25
	request := contract.FeedbackRequest{Version: 1, IdempotencyKey: "browser-feedback-key-01", TaskID: out.TaskID, Action: contract.FeedbackRecord, Accepted: false, AttemptCost: &cost}
	first, err := mutations.Feedback(ctx, browserMutationTestSubject, request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := mutations.Feedback(ctx, browserMutationTestSubject, request)
	if err != nil || second != first {
		t.Fatal(first, second, err)
	}
	revision := first.Revision
	revise := contract.FeedbackRequest{Version: 1, IdempotencyKey: "browser-feedback-key-02", TaskID: out.TaskID, FeedbackID: first.FeedbackID, Action: contract.FeedbackRevise, Accepted: true, ExpectedRevision: &revision}
	revised, err := mutations.Feedback(ctx, browserMutationTestSubject, revise)
	if err != nil || revised.State != "revised" || revised.Revision != 2 || !revised.Accepted {
		t.Fatal(revised, err)
	}
	replayed, err := mutations.Feedback(ctx, browserMutationTestSubject, revise)
	if err != nil || replayed != revised {
		t.Fatal(replayed, err)
	}
	feedbackContext, err := mutations.FeedbackContext(ctx, out.TaskID)
	if err != nil || feedbackContext.Subjective == nil || feedbackContext.Revision != 2 || feedbackContext.Subjective.Outcome != contract.EvidenceAccepted {
		t.Fatal(feedbackContext, err)
	}
	history, err := FeedbackHistory(ctx, cfg.Telemetry.Database, out.TaskID)
	if err != nil || len(history) != 1 {
		t.Fatal(history, err)
	}
}

func TestBrowserRejectedFeedbackReplaysBeforeChangedRequestConflict(t *testing.T) {
	svc, cfg := autoFixture(t)
	ctx := context.Background()
	store, err := browserops.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	mutations, err := NewBrowserMutations(svc, store)
	if err != nil {
		t.Fatal(err)
	}
	cost := float64(0)
	request := contract.FeedbackRequest{Version: 1, IdempotencyKey: "browser-rejected-key-01", TaskID: "missing", Action: contract.FeedbackRecord, Accepted: true, AttemptCost: &cost}
	if _, err = mutations.Feedback(ctx, browserMutationTestSubject, request); !errors.Is(err, ErrAdmission) {
		t.Fatal("first rejection", err)
	}
	if _, err = mutations.Feedback(ctx, browserMutationTestSubject, request); !errors.Is(err, ErrAdmission) {
		t.Fatal("rejected replay", err)
	}
	page, err := mutations.Operations(ctx, browserMutationTestSubject, "", 10)
	if err != nil || len(page.Items) != 1 || page.Items[0].State != "rejected" || page.Items[0].SubjectType != "task" || page.Items[0].SubjectID != "missing" || page.Items[0].OperationID == "" {
		t.Fatal("rejected operation projection", page, err)
	}
	request.Accepted = false
	if _, err = mutations.Feedback(ctx, browserMutationTestSubject, request); !errors.Is(err, browserops.ErrConflict) {
		t.Fatal("changed request reused key", err)
	}
}

func TestBrowserApprovalProjectionIsBoundedAndCapabilityExact(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	service := &Service{secret: func(name string) string {
		if name == "DARWIN_API_TOKEN" {
			return "private-secret"
		}
		return ""
	}}
	mutations := &BrowserMutations{service: service}
	pending := browserApprovalRecord(approvals.Pending, nil, now.Add(time.Minute))
	summary, err := mutations.approvalSummary(pending)
	if err != nil || summary.Revision != 1 || !summary.CanAllow || !summary.CanDeny || summary.CanRevoke || summary.ScopeSummary == "" || strings.Contains(summary.ScopeSummary, "private-secret") || strings.Contains(summary.Prompt, pending.Request.Scope) || strings.Contains(summary.Prompt, pending.Request.ArgumentsDigest) {
		t.Fatal(summary, err)
	}
	decision := approvals.Decision{ID: "decision", Actor: "operator", Allowed: true, Time: now}
	consumedAt := now.Add(time.Second)
	consumed := browserApprovalRecord(approvals.Consumed, []approvals.Decision{decision}, now.Add(time.Minute))
	consumed.ConsumedAt = &consumedAt
	summary, err = mutations.approvalSummary(consumed)
	if err != nil || summary.Revision != 3 || summary.State != approvals.Consumed || summary.CanAllow || summary.CanDeny || summary.CanRevoke {
		t.Fatal(summary, err)
	}
}

func TestBrowserApprovalExpiredProjectionRemovesAuthority(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	record := browserApprovalRecord(approvals.Pending, nil, now.Add(-time.Second))
	mutations := &BrowserMutations{service: &Service{secret: func(string) string { return "" }}}
	summary, err := mutations.approvalSummary(record)
	if err != nil || summary.State != "expired" || summary.Revision != 1 || summary.CanAllow || summary.CanDeny || summary.CanRevoke {
		t.Fatal(summary, err)
	}
}
