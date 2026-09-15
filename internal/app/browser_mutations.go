package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/approvals"
	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/internal/browserops"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
	"github.com/ArronJablonowski/DarwinRouter/submissions"
	contract "github.com/ArronJablonowski/DarwinRouter/webui"
	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

var ErrBrowserMutation = errors.New("browser mutation unavailable")

// BrowserMutations owns browser-specific request binding and safe projections.
// It delegates execution to the same application primitives as other adapters.
type BrowserMutations struct {
	service *Service
	store   *browserops.Store
}

func NewBrowserMutations(service *Service, store *browserops.Store) (*BrowserMutations, error) {
	if service == nil || store == nil {
		return nil, ErrAdmission
	}
	return &BrowserMutations{service: service, store: store}, nil
}

func (b *BrowserMutations) Chat(ctx context.Context, subject string, request contract.ChatRequest) (contract.ChatMutationReceipt, error) {
	if b == nil || ctx == nil || request.Validate() != nil || !validBrowserSubject(subject) || (request.Action != contract.ChatSubmit && request.Action != contract.ChatResume) {
		return contract.ChatMutationReceipt{}, ErrAdmission
	}
	record, replay, err := b.begin(ctx, subject, string(request.Action), request.IdempotencyKey, request)
	if err != nil {
		return contract.ChatMutationReceipt{}, err
	}
	if replay {
		var receipt contract.ChatMutationReceipt
		if decodeReceipt(record.Response, &receipt) != nil || receipt.OperationID != record.OperationID {
			return contract.ChatMutationReceipt{}, ErrBrowserMutation
		}
		return receipt, nil
	}
	model := request.ModelID
	if model == "" {
		model = "auto"
	}
	input := Request{ModelID: model, Prompt: request.Text}
	var status submissions.Status
	if request.Action == contract.ChatSubmit {
		status, err = b.service.Submit(ctx, record.OperationID, input)
	} else {
		status, err = b.followUp(ctx, record.OperationID, request, input)
	}
	if err != nil {
		subjectType, subjectID := "", ""
		if request.TaskID != "" {
			subjectType, subjectID = "task", request.TaskID
		}
		return contract.ChatMutationReceipt{}, b.reject(ctx, subject, record, subjectType, subjectID, err)
	}
	receipt, err := b.chatReceipt(ctx, record.OperationID, status)
	if err != nil {
		return contract.ChatMutationReceipt{}, err
	}
	if request.Action == contract.ChatResume && receipt.ChatID != "" && receipt.ChatID != request.ChatID {
		return contract.ChatMutationReceipt{}, b.reject(ctx, subject, record, "task", request.TaskID, browserops.ErrConflict)
	}
	return receipt, b.commit(ctx, subject, record, receipt)
}

func (b *BrowserMutations) followUp(ctx context.Context, key string, request contract.ChatRequest, input Request) (submissions.Status, error) {
	snapshot, err := InspectTask(ctx, b.service.settings.Telemetry.Database, request.TaskID)
	if err != nil || snapshot.SessionID != request.ChatID || snapshot.Sequence != *request.ExpectedRevision {
		return submissions.Status{}, browserops.ErrConflict
	}
	continuation, err := InspectTaskContinuation(ctx, b.service.settings.Telemetry.Database, request.TaskID)
	if err != nil || !continuation.HistoryEligible || continuation.Sequence != snapshot.Sequence {
		return submissions.Status{}, ErrAdmission
	}
	recovered := continuation.State == "failed"
	source, err := b.sourceFence(ctx, request.ChatID, request.TaskID, *request.ExpectedRevision)
	if err != nil {
		return submissions.Status{}, err
	}
	if existing, existingErr := b.service.ExistingFollowUpSubmission(ctx, key, source, recovered, input); existingErr == nil {
		return existing, nil
	} else if !errors.Is(existingErr, sql.ErrNoRows) {
		return submissions.Status{}, existingErr
	}
	page, err := b.service.ListSessionTasks(ctx, request.ChatID, sessions.SessionTaskListOptions{Limit: 1})
	if err != nil || len(page.Items) != 1 {
		return submissions.Status{}, ErrAdmission
	}
	head := page.Items[0]
	if head.TaskID != request.TaskID || head.Fence.HeadSequence != *request.ExpectedRevision {
		return submissions.Status{}, browserops.ErrConflict
	}
	if continuation.State == "completed" {
		return b.service.SubmitBranch(ctx, key, head.Fence, input)
	}
	if continuation.State == "failed" {
		return b.service.SubmitResume(ctx, key, head.Fence, input)
	}
	return submissions.Status{}, ErrAdmission
}

func (b *BrowserMutations) sourceFence(ctx context.Context, chat, task string, revision int64) (sessions.TaskHeadFence, error) {
	var after string
	for pageNumber := 0; pageNumber < 100; pageNumber++ {
		page, err := b.service.ListSessionTasks(ctx, chat, sessions.SessionTaskListOptions{After: after, Limit: 100})
		if err != nil {
			return sessions.TaskHeadFence{}, err
		}
		for _, item := range page.Items {
			if item.TaskID == task && item.Fence.HeadSequence == revision {
				return item.Fence, nil
			}
		}
		if !page.HasMore {
			break
		}
		after = page.NextCursor
	}
	return sessions.TaskHeadFence{}, browserops.ErrConflict
}

func (b *BrowserMutations) chatReceipt(ctx context.Context, operationID string, status submissions.Status) (contract.ChatMutationReceipt, error) {
	state := status.State
	if state == "succeeded" {
		state = "completed"
	}
	receipt := contract.ChatMutationReceipt{Version: 1, OperationID: operationID, SubmissionID: status.ID, State: state}
	var task string
	if status.Result != nil && status.Result.TaskID != "" {
		task = status.Result.TaskID
	} else if len(status.TaskIDs) > 0 {
		task = status.TaskIDs[len(status.TaskIDs)-1]
	}
	if task != "" {
		snapshot, err := InspectTask(ctx, b.service.settings.Telemetry.Database, task)
		if err == nil {
			receipt.TaskID, receipt.ChatID = task, snapshot.SessionID
			revision := snapshot.Sequence
			receipt.Revision = &revision
		} else if state == "running" || state == "completed" {
			return contract.ChatMutationReceipt{}, ErrBrowserMutation
		}
	}
	if state == "running" && receipt.TaskID == "" {
		// Submission execution ownership may precede creation of its first task.
		receipt.State = "queued"
	}
	if receipt.Validate() != nil {
		return contract.ChatMutationReceipt{}, ErrBrowserMutation
	}
	return receipt, nil
}

func (b *BrowserMutations) Cancel(ctx context.Context, subject string, request contract.ChatRequest) (contract.CancellationReceipt, error) {
	if b == nil || ctx == nil || request.Validate() != nil || !validBrowserSubject(subject) || (request.Action != contract.ChatCancel && request.Action != contract.ChatCancelSubmission) {
		return contract.CancellationReceipt{}, ErrAdmission
	}
	record, replay, err := b.begin(ctx, subject, string(request.Action), request.IdempotencyKey, request)
	if err != nil {
		return contract.CancellationReceipt{}, err
	}
	if replay {
		var receipt contract.CancellationReceipt
		if decodeReceipt(record.Response, &receipt) != nil || receipt.OperationID != record.OperationID {
			return contract.CancellationReceipt{}, ErrBrowserMutation
		}
		return receipt, nil
	}
	var receipt contract.CancellationReceipt
	if request.Action == contract.ChatCancelSubmission {
		status, cancelErr := b.service.CancelSubmission(ctx, request.SubmissionID)
		if cancelErr != nil {
			return receipt, b.reject(ctx, subject, record, "submission", request.SubmissionID, cancelErr)
		}
		receipt = contract.CancellationReceipt{Version: 1, OperationID: record.OperationID, TargetKind: "submission", TargetID: request.SubmissionID, State: submissionPresentationState(status.State), Requested: status.CancelRequested}
	} else {
		status, cancelErr := b.service.CancelTaskAtRevision(ctx, request.TaskID, *request.ExpectedRevision)
		if cancelErr != nil {
			return receipt, b.reject(ctx, subject, record, "task", request.TaskID, cancelErr)
		}
		snapshot, inspectErr := InspectTask(ctx, b.service.settings.Telemetry.Database, request.TaskID)
		if inspectErr != nil {
			return receipt, inspectErr
		}
		revision := snapshot.Sequence
		receipt = contract.CancellationReceipt{Version: 1, OperationID: record.OperationID, TargetKind: "task", TargetID: request.TaskID, State: status.State, Requested: status.Requested, RequestID: status.RequestID, RequestedAt: status.RequestedAt, Revision: &revision}
	}
	if receipt.Validate() != nil {
		return contract.CancellationReceipt{}, ErrBrowserMutation
	}
	return receipt, b.commit(ctx, subject, record, receipt)
}

func (b *BrowserMutations) Steer(ctx context.Context, subject string, request contract.ChatRequest) (contract.SteeringReceipt, error) {
	if b == nil || ctx == nil || request.Validate() != nil || !validBrowserSubject(subject) || request.Action != contract.ChatSteer {
		return contract.SteeringReceipt{}, ErrAdmission
	}
	record, replay, err := b.begin(ctx, subject, string(request.Action), request.IdempotencyKey, request)
	if err != nil {
		return contract.SteeringReceipt{}, err
	}
	if replay {
		var receipt contract.SteeringReceipt
		if decodeReceipt(record.Response, &receipt) != nil || receipt.OperationID != record.OperationID {
			return contract.SteeringReceipt{}, ErrBrowserMutation
		}
		return receipt, nil
	}
	message, err := b.service.SteerTaskAtRevision(ctx, request.TaskID, record.OperationID, request.Text, *request.ExpectedRevision)
	if err != nil {
		return contract.SteeringReceipt{}, b.reject(ctx, subject, record, "task", request.TaskID, err)
	}
	receipt := contract.SteeringReceipt{Version: 1, OperationID: record.OperationID, ID: message.ID, TaskID: message.TaskID, State: message.State, CreatedAt: message.CreatedAt, AppliedRevision: message.AppliedSequence}
	if receipt.Validate() != nil {
		return contract.SteeringReceipt{}, ErrBrowserMutation
	}
	return receipt, b.commit(ctx, subject, record, receipt)
}

func (b *BrowserMutations) Feedback(ctx context.Context, subject string, request contract.FeedbackRequest) (contract.FeedbackReceipt, error) {
	if b == nil || ctx == nil || request.Validate() != nil || !validBrowserSubject(subject) {
		return contract.FeedbackReceipt{}, ErrAdmission
	}
	record, replay, err := b.begin(ctx, subject, string(request.Action), request.IdempotencyKey, request)
	if err != nil {
		return contract.FeedbackReceipt{}, err
	}
	if replay {
		var receipt contract.FeedbackReceipt
		if decodeReceipt(record.Response, &receipt) != nil || receipt.OperationID != record.OperationID {
			return contract.FeedbackReceipt{}, ErrBrowserMutation
		}
		return receipt, nil
	}
	snapshot, err := InspectTask(ctx, b.service.settings.Telemetry.Database, request.TaskID)
	if err != nil || snapshot.State != "completed" || snapshot.UncertainEffects || snapshot.InterruptedTurn || len(snapshot.Pending) != 0 {
		return contract.FeedbackReceipt{}, b.reject(ctx, subject, record, "task", request.TaskID, ErrAdmission)
	}
	db, err := telemetry.Open(ctx, b.service.settings.Telemetry.Database)
	if err != nil {
		return contract.FeedbackReceipt{}, ErrBrowserMutation
	}
	defer db.Close()
	history, err := db.BrowserFeedbackHistory(ctx, request.TaskID)
	if err != nil {
		return contract.FeedbackReceipt{}, ErrBrowserMutation
	}
	expected := int64(0)
	supersedes := ""
	cost := float64(0)
	if request.Action == contract.FeedbackRecord {
		cost = *request.AttemptCost
	}
	if request.Action == contract.FeedbackRevise {
		expected = *request.ExpectedRevision
		if len(history) < int(expected) || history[expected-1].ID != request.FeedbackID {
			return contract.FeedbackReceipt{}, b.reject(ctx, subject, record, "feedback", request.FeedbackID, browserops.ErrConflict)
		}
		supersedes = request.FeedbackID
		cost = history[expected-1].AttemptCost
	}
	feedbackID := browserFeedbackID(record.OperationID)
	entry := telemetry.BrowserFeedback{Version: 1, ID: feedbackID, TaskID: request.TaskID, Supersedes: supersedes, Accepted: request.Accepted, AttemptCost: cost, CreatedAt: time.Now().UTC()}
	if err = db.AppendBrowserFeedback(ctx, entry, expected); err != nil {
		if errors.Is(err, telemetry.ErrBrowserOperationConflict) {
			return contract.FeedbackReceipt{}, b.reject(ctx, subject, record, "task", request.TaskID, browserops.ErrConflict)
		}
		return contract.FeedbackReceipt{}, err
	}
	history, err = db.BrowserFeedbackHistory(ctx, request.TaskID)
	if err != nil || len(history) == 0 {
		return contract.FeedbackReceipt{}, ErrBrowserMutation
	}
	current := history[len(history)-1]
	if current.ID != feedbackID || current.Accepted != request.Accepted {
		return contract.FeedbackReceipt{}, ErrBrowserMutation
	}
	state := "recorded"
	if request.Action == contract.FeedbackRevise {
		state = "revised"
	}
	receipt := contract.FeedbackReceipt{Version: 1, OperationID: record.OperationID, TaskID: request.TaskID, FeedbackID: current.ID, Revision: int64(len(history)), State: state, Accepted: request.Accepted, EvidenceClass: contract.SubjectiveEvidence, Source: "user_feedback"}
	if receipt.Validate() != nil {
		return contract.FeedbackReceipt{}, ErrBrowserMutation
	}
	return receipt, b.commit(ctx, subject, record, receipt)
}

func (b *BrowserMutations) Approvals(ctx context.Context, task, after string, limit int) (contract.ApprovalPage, error) {
	page, err := ListApprovals(ctx, b.service.settings.Telemetry.Database, approvals.ListOptions{TaskID: task, AfterCallID: after, Limit: limit})
	if err != nil {
		return contract.ApprovalPage{}, err
	}
	out := contract.ApprovalPage{Version: 1, TaskID: task, Items: make([]contract.ApprovalSummary, 0, len(page.Records)), NextCursor: page.NextAfterCallID, HasMore: page.NextAfterCallID != ""}
	for _, record := range page.Records {
		summary, projectErr := b.approvalSummary(ctx, record)
		if projectErr != nil {
			return contract.ApprovalPage{}, projectErr
		}
		out.Items = append(out.Items, summary)
	}
	if out.Validate() != nil {
		return contract.ApprovalPage{}, ErrBrowserMutation
	}
	return out, nil
}

func (b *BrowserMutations) Operations(ctx context.Context, subject, after string, limit int) (contract.OperationPage, error) {
	if b == nil || ctx == nil || !validBrowserSubject(subject) {
		return contract.OperationPage{}, ErrAdmission
	}
	records, next, err := b.store.List(ctx, subject, after, limit)
	if err != nil {
		return contract.OperationPage{}, err
	}
	out := contract.OperationPage{Version: 1, Items: make([]contract.OperationSummary, 0, len(records)), NextCursor: next, HasMore: next != ""}
	for _, record := range records {
		summary := contract.OperationSummary{Version: 1, OperationID: record.OperationID, Action: record.Kind, State: record.State, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt}
		if record.State == "committed" {
			summary.SubjectType, summary.SubjectID = operationSubject(record.Kind, record.Response)
			if summary.SubjectType == "" {
				return contract.OperationPage{}, ErrBrowserMutation
			}
		} else if record.State == "rejected" {
			var rejected browserRejection
			if json.Unmarshal(record.Response, &rejected) != nil || rejected.Version != 1 {
				return contract.OperationPage{}, ErrBrowserMutation
			}
			summary.SubjectType, summary.SubjectID = rejected.SubjectType, rejected.SubjectID
		}
		if summary.Validate() != nil {
			return contract.OperationPage{}, ErrBrowserMutation
		}
		out.Items = append(out.Items, summary)
	}
	if out.Validate() != nil {
		return contract.OperationPage{}, ErrBrowserMutation
	}
	return out, nil
}

func (b *BrowserMutations) Submission(ctx context.Context, subject, id string) (contract.SubmissionStatus, error) {
	if b == nil || ctx == nil || !validBrowserSubject(subject) {
		return contract.SubmissionStatus{}, ErrAdmission
	}
	owned := false
	after := ""
	for page := 0; page < 100; page++ {
		records, next, listErr := b.store.List(ctx, subject, after, 100)
		if listErr != nil {
			return contract.SubmissionStatus{}, listErr
		}
		for _, record := range records {
			if record.State == "committed" && operationSubmission(record.Kind, record.Response) == id {
				owned = true
				break
			}
		}
		if owned || next == "" {
			break
		}
		after = next
	}
	if !owned {
		return contract.SubmissionStatus{}, sql.ErrNoRows
	}
	status, err := b.service.SubmissionStatus(ctx, id)
	if err != nil {
		return contract.SubmissionStatus{}, err
	}
	out := contract.SubmissionStatus{Version: 1, SubmissionID: status.ID, State: submissionPresentationState(status.State), CreatedAt: status.CreatedAt, UpdatedAt: status.UpdatedAt, CanCancel: (status.State == "queued" || status.State == "running") && !status.CancelRequested}
	task := ""
	if status.Result != nil {
		task = status.Result.TaskID
	} else if len(status.TaskIDs) > 0 {
		task = status.TaskIDs[len(status.TaskIDs)-1]
	}
	if task != "" {
		snapshot, inspectErr := InspectTask(ctx, b.service.settings.Telemetry.Database, task)
		if inspectErr != nil {
			return contract.SubmissionStatus{}, ErrBrowserMutation
		}
		out.TaskID, out.ChatID = task, snapshot.SessionID
		revision := snapshot.Sequence
		out.Revision = &revision
	}
	if out.Validate() != nil {
		return contract.SubmissionStatus{}, ErrBrowserMutation
	}
	return out, nil
}

func (b *BrowserMutations) DecideApproval(ctx context.Context, subject string, request contract.ApprovalRequest) (contract.ApprovalDecisionReceipt, error) {
	if b == nil || ctx == nil || request.Validate() != nil || !validBrowserSubject(subject) {
		return contract.ApprovalDecisionReceipt{}, ErrAdmission
	}
	record, replay, err := b.begin(ctx, subject, "approval."+string(request.Action), request.IdempotencyKey, request)
	if err != nil {
		return contract.ApprovalDecisionReceipt{}, err
	}
	if replay {
		var receipt contract.ApprovalDecisionReceipt
		if decodeReceipt(record.Response, &receipt) != nil || receipt.OperationID != record.OperationID {
			return contract.ApprovalDecisionReceipt{}, ErrBrowserMutation
		}
		return receipt, nil
	}
	current, err := InspectApproval(ctx, b.service.settings.Telemetry.Database, request.TaskID, request.ApprovalID)
	if err != nil {
		return contract.ApprovalDecisionReceipt{}, b.reject(ctx, subject, record, "approval", request.ApprovalID, err)
	}
	allowed := request.Action == contract.ApprovalAllow
	result := current
	alreadyDecided := false
	for _, decision := range current.Decisions {
		if decision.ID == record.OperationID {
			if decision.Allowed != allowed {
				return contract.ApprovalDecisionReceipt{}, b.reject(ctx, subject, record, "approval", request.ApprovalID, browserops.ErrConflict)
			}
			alreadyDecided = true
		}
	}
	if !alreadyDecided {
		summary, summaryErr := b.approvalSummary(ctx, current)
		if summaryErr != nil || summary.Revision != request.ExpectedRevision || !approvalActionAllowed(summary, request.Action) {
			return contract.ApprovalDecisionReceipt{}, b.reject(ctx, subject, record, "approval", request.ApprovalID, browserops.ErrConflict)
		}
		result, err = b.service.DecideApproval(ctx, approvals.Command{Expected: current.Request, ID: record.OperationID, Allowed: allowed}, "browser_operator")
		if err != nil {
			return contract.ApprovalDecisionReceipt{}, b.reject(ctx, subject, record, "approval", request.ApprovalID, err)
		}
	}
	var decided time.Time
	for _, decision := range result.Decisions {
		if decision.ID == record.OperationID {
			decided = decision.Time
		}
	}
	resultSummary, err := b.approvalSummary(ctx, result)
	if err != nil || decided.IsZero() {
		return contract.ApprovalDecisionReceipt{}, ErrBrowserMutation
	}
	receipt := contract.ApprovalDecisionReceipt{Version: 1, OperationID: record.OperationID, TaskID: request.TaskID, ApprovalID: request.ApprovalID, State: resultSummary.State, Revision: resultSummary.Revision, DecidedAt: decided}
	if receipt.Validate() != nil {
		return contract.ApprovalDecisionReceipt{}, ErrBrowserMutation
	}
	return receipt, b.commit(ctx, subject, record, receipt)
}

func (b *BrowserMutations) approvalSummary(ctx context.Context, record approvals.Record) (contract.ApprovalSummary, error) {
	if record.Validate() != nil {
		return contract.ApprovalSummary{}, ErrBrowserMutation
	}
	state := record.State
	if (state == approvals.Pending || state == approvals.Approved) && !time.Now().UTC().Before(record.Request.ExpiresAt) {
		state = "expired"
	}
	behavior := string(record.Request.ToolBehavior)
	if behavior == "" {
		behavior = string(runtime.BehaviorNonIdempotentWrite)
	}
	prompt := fmt.Sprintf("Allow the requested %s operation by %s?", strings.ReplaceAll(behavior, "_", " "), record.Request.ToolName)
	prompt = redact(prompt, memorySecrets(b.service.settings, b.service.secret))
	scopeSummary := redact(record.Request.Scope, memorySecrets(b.service.settings, b.service.secret))
	if len(scopeSummary) > contract.MaxApprovalScopeBytes {
		scopeSummary = scopeSummary[:contract.MaxApprovalScopeBytes]
	}
	revision := int64(len(record.Decisions) + 1)
	if record.State == approvals.Consumed {
		revision++
	}
	summary := contract.ApprovalSummary{ID: record.Request.ID, State: state, Revision: revision, Prompt: prompt, ScopeSummary: scopeSummary, ToolName: record.Request.ToolName, ToolBehavior: behavior, ExpiresAt: record.Request.ExpiresAt, CanAllow: state == approvals.Pending, CanDeny: state == approvals.Pending, CanRevoke: state == approvals.Approved}
	if state == approvals.Pending || state == approvals.Approved {
		proposal, proposalErr := b.approvalProposal(ctx, record)
		if proposalErr != nil {
			return contract.ApprovalSummary{}, proposalErr
		}
		summary.Proposal = proposal
	}
	if summary.Validate() != nil {
		return contract.ApprovalSummary{}, ErrBrowserMutation
	}
	return summary, nil
}

type criteriaApprovalArguments struct {
	IdempotencyKey           string                         `json:"idempotency_key"`
	BoardID                  string                         `json:"board_id"`
	CardID                   string                         `json:"card_id"`
	ExpectedBoardRevision    int64                          `json:"expected_board_revision"`
	ExpectedCardRevision     int64                          `json:"expected_card_revision"`
	ExpectedCriteriaRevision int64                          `json:"expected_criteria_revision"`
	ExpectedCriteriaDigest   string                         `json:"expected_criteria_digest"`
	Criteria                 []contract.AcceptanceCriterion `json:"criteria"`
}

type candidateDecisionApprovalArguments struct {
	IdempotencyKey          string `json:"idempotency_key"`
	BoardID                 string `json:"board_id"`
	CardID                  string `json:"card_id"`
	AttemptID               string `json:"attempt_id"`
	CandidateID             string `json:"candidate_id"`
	ExpectedBoardRevision   int64  `json:"expected_board_revision"`
	ExpectedCardRevision    int64  `json:"expected_card_revision"`
	ExpectedAttemptRevision int64  `json:"expected_attempt_revision"`
	CriteriaRevision        int64  `json:"criteria_revision"`
	EvidenceHeadRevision    int64  `json:"evidence_head_revision"`
	CandidateDigest         string `json:"candidate_digest"`
	CriteriaDigest          string `json:"criteria_digest"`
	EvidenceSetDigest       string `json:"evidence_set_digest"`
	PolicyDigest            string `json:"policy_digest"`
	Decision                string `json:"decision"`
	Rationale               string `json:"rationale"`
}

func (b *BrowserMutations) approvalProposal(ctx context.Context, record approvals.Record) (*contract.ApprovalProposal, error) {
	if record.Request.ToolName != "workboard_propose_criteria" && record.Request.ToolName != "workboard_request_candidate_decision" {
		return nil, nil
	}
	if b == nil || b.service == nil || ctx == nil {
		return nil, ErrBrowserMutation
	}
	snapshot, err := InspectTask(ctx, b.service.settings.Telemetry.Database, record.Request.TaskID)
	if err != nil {
		return nil, ErrBrowserMutation
	}
	pending, ok := snapshot.Pending[record.Request.ToolCallID]
	if !ok || !pending.Dispatched || pending.ToolBehavior != runtime.BehaviorIdempotentWrite || record.Request.ToolBehavior != runtime.BehaviorIdempotentWrite ||
		pending.TurnID != record.Request.TurnID || pending.Call.ID != record.Request.ToolCallID || pending.Call.Name != record.Request.ToolName {
		return nil, ErrBrowserMutation
	}
	digest := sha256.Sum256(pending.Call.Arguments)
	if hex.EncodeToString(digest[:]) != record.Request.ArgumentsDigest || contract.RejectDuplicateJSONFields(pending.Call.Arguments) != nil ||
		!selectionValueClean(pending.Call.Arguments, memorySecrets(b.service.settings, b.service.secret)) {
		return nil, ErrBrowserMutation
	}
	decode := func(target any) error {
		decoder := json.NewDecoder(strings.NewReader(string(pending.Call.Arguments)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(target) != nil || decoder.Decode(&struct{}{}) != io.EOF {
			return ErrBrowserMutation
		}
		return nil
	}
	var proposal contract.ApprovalProposal
	switch record.Request.ToolName {
	case "workboard_propose_criteria":
		var args criteriaApprovalArguments
		if decode(&args) != nil || !validApprovalProposalKey(args.IdempotencyKey) {
			return nil, ErrBrowserMutation
		}
		proposal = contract.ApprovalProposal{Version: 1, Kind: "criteria_change", BoardID: args.BoardID, CardID: args.CardID,
			ExpectedBoardRevision: args.ExpectedBoardRevision, ExpectedCardRevision: args.ExpectedCardRevision,
			ExpectedCriteriaRevision: args.ExpectedCriteriaRevision, ExpectedCriteriaDigest: args.ExpectedCriteriaDigest, Criteria: args.Criteria}
	case "workboard_request_candidate_decision":
		var args candidateDecisionApprovalArguments
		if decode(&args) != nil || !validApprovalProposalKey(args.IdempotencyKey) {
			return nil, ErrBrowserMutation
		}
		proposal = contract.ApprovalProposal{Version: 1, Kind: "candidate_decision", BoardID: args.BoardID, CardID: args.CardID,
			AttemptID: args.AttemptID, CandidateID: args.CandidateID, ExpectedBoardRevision: args.ExpectedBoardRevision,
			ExpectedCardRevision: args.ExpectedCardRevision, ExpectedAttemptRevision: args.ExpectedAttemptRevision,
			CriteriaRevision: args.CriteriaRevision, EvidenceHeadRevision: args.EvidenceHeadRevision, CandidateDigest: args.CandidateDigest,
			CriteriaDigest: args.CriteriaDigest, EvidenceSetDigest: args.EvidenceSetDigest, PolicyDigest: args.PolicyDigest,
			Decision: args.Decision, Rationale: args.Rationale}
	}
	if proposal.Validate() != nil {
		return nil, ErrBrowserMutation
	}
	return &proposal, nil
}

func validApprovalProposalKey(value string) bool {
	if len(value) < contract.MinIdempotencyBytes || len(value) > contract.MaxIdempotencyBytes {
		return false
	}
	for _, char := range []byte(value) {
		if char < '!' || char > '~' {
			return false
		}
	}
	return true
}

func (b *BrowserMutations) TaskControls(ctx context.Context, task string) (contract.TaskControlStatus, error) {
	snapshot, err := InspectTask(ctx, b.service.settings.Telemetry.Database, task)
	if err != nil {
		return contract.TaskControlStatus{}, err
	}
	continuation, _ := InspectTaskContinuation(ctx, b.service.settings.Telemetry.Database, task)
	cancellation, _ := b.service.CancellationStatus(ctx, task)
	cleanComplete := snapshot.State == "completed" && !snapshot.UncertainEffects && !snapshot.InterruptedTurn && len(snapshot.Pending) == 0
	out := contract.TaskControlStatus{Version: 1, TaskID: task, Revision: snapshot.Sequence, CanResume: continuation.HistoryEligible, CanSteer: snapshot.State == "running" && !cancellation.Requested, CanCancel: snapshot.State == "running" && !cancellation.Requested, CanFeedback: cleanComplete}
	if out.Validate() != nil {
		return contract.TaskControlStatus{}, ErrBrowserMutation
	}
	return out, nil
}

func (b *BrowserMutations) FeedbackContext(ctx context.Context, task string) (contract.FeedbackContext, error) {
	snapshot, err := InspectTask(ctx, b.service.settings.Telemetry.Database, task)
	if err != nil {
		return contract.FeedbackContext{}, err
	}
	out := contract.FeedbackContext{Version: 1, TaskID: task, Revision: 1, Objective: []contract.EvidenceSummary{}}
	clean := snapshot.State == "completed" && !snapshot.UncertainEffects && !snapshot.InterruptedTurn && len(snapshot.Pending) == 0
	if !clean {
		out.DenialCode = "task_ineligible"
	} else {
		out.FeedbackAllowed = true
	}
	history, historyErr := FeedbackHistory(ctx, b.service.settings.Telemetry.Database, task)
	if historyErr != nil && !errors.Is(historyErr, sql.ErrNoRows) {
		return contract.FeedbackContext{}, ErrBrowserMutation
	}
	if len(history) > 0 {
		current := history[len(history)-1]
		groups := map[evaluation.Source][]evaluation.Check{}
		for _, check := range current.Checks {
			groups[check.Source] = append(groups[check.Source], check)
		}
		for _, source := range []evaluation.Source{evaluation.Deterministic, evaluation.ToolResult, evaluation.UserFeedback, evaluation.LLMJudge} {
			checks := groups[source]
			if len(checks) == 0 {
				continue
			}
			summary := evidenceSummary(source, checks)
			switch source {
			case evaluation.Deterministic, evaluation.ToolResult:
				out.Objective = append(out.Objective, summary)
			case evaluation.UserFeedback:
				// Legacy user feedback remains inspectable but is not confused
				// with the independently revisioned browser feedback journal.
			case evaluation.LLMJudge:
				out.Advisory = &summary
			}
		}
	}
	db, openErr := telemetry.OpenReadOnly(ctx, b.service.settings.Telemetry.Database)
	if openErr != nil {
		return contract.FeedbackContext{}, ErrBrowserMutation
	}
	browserHistory, browserErr := db.BrowserFeedbackHistory(ctx, task)
	db.Close()
	if browserErr != nil {
		return contract.FeedbackContext{}, ErrBrowserMutation
	}
	if len(browserHistory) > 0 {
		current := browserHistory[len(browserHistory)-1]
		out.Revision = int64(len(browserHistory))
		out.FeedbackID = current.ID
		outcome := contract.EvidenceRejected
		if current.Accepted {
			outcome = contract.EvidenceAccepted
		}
		out.Subjective = &contract.EvidenceSummary{Class: contract.SubjectiveEvidence, Source: "user_feedback", Outcome: outcome, ReferenceCount: len(browserHistory)}
	}
	if out.Validate() != nil {
		return contract.FeedbackContext{}, ErrBrowserMutation
	}
	return out, nil
}

func (b *BrowserMutations) begin(ctx context.Context, subject, kind, key string, value any) (browserops.Record, bool, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return browserops.Record{}, false, ErrAdmission
	}
	record, err := b.store.Begin(ctx, subject, key, kind, body)
	if err != nil {
		return browserops.Record{}, false, err
	}
	if record.State == "rejected" {
		return browserops.Record{}, false, decodeBrowserRejection(record.OperationID, record.Response)
	}
	return record, record.State == "committed", nil
}

func (b *BrowserMutations) commit(ctx context.Context, subject string, record browserops.Record, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return ErrBrowserMutation
	}
	committed, err := b.store.Commit(ctx, subject, record.OperationID, record.RequestDigest, body)
	if err != nil || committed.State != "committed" {
		return ErrBrowserMutation
	}
	return nil
}

type browserRejection struct {
	Version     int    `json:"version"`
	Code        string `json:"code"`
	SubjectType string `json:"subject_type,omitempty"`
	SubjectID   string `json:"subject_id,omitempty"`
	Field       string `json:"field,omitempty"`
}

type BrowserOperationError struct {
	OperationID string
	Cause       error
}

func (e *BrowserOperationError) Error() string { return e.Cause.Error() }
func (e *BrowserOperationError) Unwrap() error { return e.Cause }

func (b *BrowserMutations) reject(ctx context.Context, subject string, record browserops.Record, subjectType, subjectID string, cause error) error {
	code, definitive := browserRejectionCode(cause)
	if !definitive {
		return cause
	}
	if !validBrowserRejectionSubject(subjectType, subjectID) {
		return ErrBrowserMutation
	}
	field := ""
	var violation *workboard.Violation
	if errors.As(cause, &violation) {
		field = violation.Field
	}
	body, err := json.Marshal(browserRejection{Version: 1, Code: code, SubjectType: subjectType, SubjectID: subjectID, Field: field})
	if err != nil {
		return ErrBrowserMutation
	}
	if _, err = b.store.Reject(ctx, subject, record.OperationID, record.RequestDigest, body); err != nil {
		return ErrBrowserMutation
	}
	return &BrowserOperationError{OperationID: record.OperationID, Cause: cause}
}

func browserRejectionCode(err error) (string, bool) {
	var violation *workboard.Violation
	switch {
	case errors.As(err, &violation) && violation.Code == workboard.CodeStaleRevision:
		return "workboard_stale", true
	case errors.As(err, &violation) && violation.Code == workboard.CodeMissingNode:
		return "workboard_missing", true
	case errors.As(err, &violation):
		return "workboard_rejected", true
	case errors.Is(err, telemetry.ErrWorkboardNotFound):
		return "workboard_missing", true
	case errors.Is(err, browserops.ErrConflict), errors.Is(err, telemetry.ErrConflict), errors.Is(err, submissions.ErrConflict), errors.Is(err, approvals.ErrConflict), errors.Is(err, runtime.ErrSteeringClosed):
		return "conflict", true
	case errors.Is(err, sql.ErrNoRows):
		return "not_found", true
	case errors.Is(err, browserops.ErrCapacity), errors.Is(err, submissions.ErrCapacity), errors.Is(err, runtime.ErrSteeringLimit):
		return "capacity", true
	case errors.Is(err, ErrAdmission), errors.Is(err, approvals.ErrInvalid), errors.Is(err, submissions.ErrInvalid):
		return "admission", true
	default:
		return "", false
	}
}

func decodeBrowserRejection(operationID string, body []byte) error {
	var rejected browserRejection
	if json.Unmarshal(body, &rejected) != nil || rejected.Version != 1 || !validBrowserRejectionSubject(rejected.SubjectType, rejected.SubjectID) ||
		(rejected.Field != "" && !contract.ValidID(rejected.Field)) {
		return ErrBrowserMutation
	}
	switch rejected.Code {
	case "workboard_stale":
		return &BrowserOperationError{OperationID: operationID, Cause: &workboard.Violation{Code: workboard.CodeStaleRevision, Field: rejected.Field}}
	case "workboard_missing":
		return &BrowserOperationError{OperationID: operationID, Cause: &workboard.Violation{Code: workboard.CodeMissingNode, Field: rejected.Field}}
	case "workboard_rejected":
		return &BrowserOperationError{OperationID: operationID, Cause: &workboard.Violation{Code: workboard.CodeInvalid, Field: rejected.Field}}
	case "conflict":
		if rejected.Field != "" {
			return ErrBrowserMutation
		}
		return &BrowserOperationError{OperationID: operationID, Cause: browserops.ErrConflict}
	case "not_found":
		if rejected.Field != "" {
			return ErrBrowserMutation
		}
		return &BrowserOperationError{OperationID: operationID, Cause: sql.ErrNoRows}
	case "capacity":
		if rejected.Field != "" {
			return ErrBrowserMutation
		}
		return &BrowserOperationError{OperationID: operationID, Cause: browserops.ErrCapacity}
	case "admission":
		if rejected.Field != "" {
			return ErrBrowserMutation
		}
		return &BrowserOperationError{OperationID: operationID, Cause: ErrAdmission}
	default:
		return ErrBrowserMutation
	}
}

func validBrowserRejectionSubject(subjectType, subjectID string) bool {
	if subjectType == "" || subjectID == "" {
		return subjectType == "" && subjectID == ""
	}
	if !contract.ValidID(subjectID) {
		return false
	}
	switch subjectType {
	case "chat", "task", "submission", "feedback", "approval", "board", "card":
		return true
	default:
		return false
	}
}

func validBrowserSubject(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func decodeReceipt(body []byte, value interface{ Validate() error }) error {
	if json.Unmarshal(body, value) != nil || value.Validate() != nil {
		return ErrBrowserMutation
	}
	return nil
}

func submissionPresentationState(state string) string {
	if state == "succeeded" {
		return "completed"
	}
	return state
}

func approvalActionAllowed(summary contract.ApprovalSummary, action contract.ApprovalAction) bool {
	return action == contract.ApprovalAllow && summary.CanAllow || action == contract.ApprovalDeny && summary.CanDeny || action == contract.ApprovalRevoke && summary.CanRevoke
}

func evidenceSummary(source evaluation.Source, checks []evaluation.Check) contract.EvidenceSummary {
	accepted := true
	for _, check := range checks {
		accepted = accepted && check.Passed
	}
	outcome := contract.EvidenceRejected
	if accepted {
		outcome = contract.EvidenceAccepted
	}
	class := contract.ObjectiveEvidence
	if source == evaluation.UserFeedback {
		class = contract.SubjectiveEvidence
	} else if source == evaluation.LLMJudge {
		class = contract.AdvisoryEvidence
	}
	return contract.EvidenceSummary{Class: class, Source: string(source), Outcome: outcome, ReferenceCount: len(checks)}
}

func browserFeedbackID(operationID string) string {
	hash := sha256.Sum256([]byte("browser-feedback:" + operationID))
	return "fb_" + hex.EncodeToString(hash[:])
}

func operationSubject(action string, body []byte) (string, string) {
	switch action {
	case "submit", "resume":
		var receipt contract.ChatMutationReceipt
		if decodeReceipt(body, &receipt) == nil {
			if receipt.ChatID != "" {
				return "chat", receipt.ChatID
			}
			return "submission", receipt.SubmissionID
		}
	case "steer":
		var receipt contract.SteeringReceipt
		if decodeReceipt(body, &receipt) == nil {
			return "task", receipt.TaskID
		}
	case "cancel", "cancel_submission":
		var receipt contract.CancellationReceipt
		if decodeReceipt(body, &receipt) == nil {
			return receipt.TargetKind, receipt.TargetID
		}
	case "record", "revise":
		var receipt contract.FeedbackReceipt
		if decodeReceipt(body, &receipt) == nil {
			return "feedback", receipt.FeedbackID
		}
	case "approval.allow", "approval.deny", "approval.revoke":
		var receipt contract.ApprovalDecisionReceipt
		if decodeReceipt(body, &receipt) == nil {
			return "approval", receipt.ApprovalID
		}
	case string(contract.BoardCreate), string(contract.BoardRevise), string(contract.BoardArchive),
		string(contract.CardCreate), string(contract.CardRevise), string(contract.CardMove), string(contract.CardReorder),
		string(contract.DependencyAdd), string(contract.DependencyRemove), string(contract.CardClaim), string(contract.ClaimHeartbeat),
		string(contract.ClaimRecover), string(contract.CriteriaRevise), string(contract.CheckpointAppend), string(contract.CandidateSubmit),
		string(contract.AcceptanceAccept), string(contract.AcceptanceReject), string(contract.CardPauseRequest), string(contract.CardCancelRequest),
		string(contract.CardCancelFinalize), string(contract.CardBlock), string(contract.CardUnblock):
		var receipt contract.OperationReceipt
		if decodeReceipt(body, &receipt) == nil {
			if receipt.CardID != "" {
				return "card", receipt.CardID
			}
			return "board", receipt.BoardID
		}
	}
	return "", ""
}

func operationSubmission(action string, body []byte) string {
	if action == "submit" || action == "resume" {
		var receipt contract.ChatMutationReceipt
		if decodeReceipt(body, &receipt) == nil {
			return receipt.SubmissionID
		}
	}
	if action == "cancel_submission" {
		var receipt contract.CancellationReceipt
		if decodeReceipt(body, &receipt) == nil && receipt.TargetKind == "submission" {
			return receipt.TargetID
		}
	}
	return ""
}
