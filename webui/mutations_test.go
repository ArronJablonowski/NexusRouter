package webui

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func mutationTime() time.Time { return time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC) }

func TestMutationFixtureTypesValidate(t *testing.T) {
	var fixture struct {
		Chat            ChatMutationReceipt     `json:"chat"`
		Cancellation    CancellationReceipt     `json:"cancellation"`
		Steering        SteeringReceipt         `json:"steering"`
		Controls        TaskControlStatus       `json:"controls"`
		FeedbackContext FeedbackContext         `json:"feedback_context"`
		FeedbackReceipt FeedbackReceipt         `json:"feedback_receipt"`
		ApprovalPage    ApprovalPage            `json:"approval_page"`
		ApprovalReceipt ApprovalDecisionReceipt `json:"approval_receipt"`
		OperationPage   OperationPage           `json:"operation_page"`
		Submission      SubmissionStatus        `json:"submission_status"`
	}
	decodeFixture(t, "mutations.json", &fixture)
	validators := map[string]func() error{
		"chat": fixture.Chat.Validate, "cancellation": fixture.Cancellation.Validate,
		"steering": fixture.Steering.Validate, "controls": fixture.Controls.Validate,
		"feedback context": fixture.FeedbackContext.Validate, "feedback receipt": fixture.FeedbackReceipt.Validate,
		"approval page": fixture.ApprovalPage.Validate, "approval receipt": fixture.ApprovalReceipt.Validate,
		"operation page": fixture.OperationPage.Validate, "submission": fixture.Submission.Validate,
	}
	for name, validate := range validators {
		if err := validate(); err != nil {
			t.Fatal(name, err)
		}
	}
}

func TestChatMutationActionsHaveExactFields(t *testing.T) {
	key, revision := "contract-key-0001", int64(3)
	valid := []ChatRequest{
		{Version: 1, Action: ChatSubmit, IdempotencyKey: key, ModelID: "auto", Text: "new"},
		{Version: 1, Action: ChatResume, IdempotencyKey: key, ChatID: "chat", TaskID: "task", ModelID: "auto", Text: "next", ExpectedRevision: &revision},
		{Version: 1, Action: ChatSteer, IdempotencyKey: key, TaskID: "task", Text: "adjust", ExpectedRevision: &revision},
		{Version: 1, Action: ChatCancel, IdempotencyKey: key, TaskID: "task", ExpectedRevision: &revision},
		{Version: 1, Action: ChatCancelSubmission, IdempotencyKey: key, SubmissionID: "submission"},
	}
	for _, request := range valid {
		if err := request.Validate(); err != nil {
			t.Fatal("valid action rejected", request.Action, err)
		}
	}
	invalid := []ChatRequest{
		{Version: 1, Action: ChatSubmit, IdempotencyKey: key, ChatID: "chat", Text: "new"},
		{Version: 1, Action: ChatResume, IdempotencyKey: key, ChatID: "chat", TaskID: "task", SubmissionID: "submission", Text: "next", ExpectedRevision: &revision},
		{Version: 1, Action: ChatSteer, IdempotencyKey: key, TaskID: "task", Text: "adjust"},
		{Version: 1, Action: ChatCancel, IdempotencyKey: key, TaskID: "task"},
		{Version: 1, Action: ChatCancelSubmission, IdempotencyKey: key, SubmissionID: "submission", ExpectedRevision: &revision},
	}
	for _, request := range invalid {
		if !errors.Is(request.Validate(), ErrContract) {
			t.Fatal("inexact action accepted", request.Action, request)
		}
	}
}

func TestMutationProjectionRejectsAuthorityAndInvalidState(t *testing.T) {
	now, revision := mutationTime(), int64(2)
	validApproval := ApprovalSummary{ID: "approval", State: "pending", Revision: 1, Prompt: "Review the bounded project scope.", ScopeSummary: "Project file: internal/router.go", ToolName: "create_file", ToolBehavior: "non_idempotent_write", ExpiresAt: now.Add(time.Minute), CanAllow: true, CanDeny: true}
	valid := []func() error{
		(ChatMutationReceipt{Version: 1, OperationID: "operation", SubmissionID: "submission", State: "queued"}).Validate,
		(CancellationReceipt{Version: 1, OperationID: "operation", TargetKind: "submission", TargetID: "submission", State: "queued", Requested: true}).Validate,
		(SteeringReceipt{Version: 1, OperationID: "operation", ID: "steering", TaskID: "task", State: "applied", CreatedAt: now, AppliedRevision: &revision}).Validate,
		validApproval.Validate,
		(OperationSummary{Version: 1, OperationID: "board-operation", Action: string(BoardCreate), State: "committed", SubjectType: "board", SubjectID: "board-a", CreatedAt: now, UpdatedAt: now}).Validate,
		(OperationSummary{Version: 1, OperationID: "card-operation", Action: string(CardMove), State: "committed", SubjectType: "card", SubjectID: "card-a", CreatedAt: now, UpdatedAt: now}).Validate,
	}
	for _, validate := range valid {
		if err := validate(); err != nil {
			t.Fatal(err)
		}
	}
	badApproval := validApproval
	badApproval.Prompt = strings.Repeat("x", MaxApprovalPromptBytes+1)
	badUTF8Bytes := validApproval
	badUTF8Bytes.Prompt = strings.Repeat("é", MaxApprovalPromptBytes/2+1)
	badScope := validApproval
	badScope.ScopeSummary = strings.Repeat("é", MaxApprovalScopeBytes/2+1)
	badCapabilities := validApproval
	badCapabilities.CanRevoke = true
	badFeedback := EvidenceSummary{Class: ObjectiveEvidence, Source: "user_feedback", Outcome: EvidenceAccepted, ReferenceCount: 1}
	badOperation := OperationSummary{Version: 1, OperationID: "operation", Action: "submit", State: "committed", CreatedAt: now, UpdatedAt: now}
	validRejectedOperation := OperationSummary{Version: 1, OperationID: "operation", Action: "submit", State: "rejected", CreatedAt: now, UpdatedAt: now}
	for name, validate := range map[string]func() error{
		"oversized approval":             badApproval.Validate,
		"oversized multibyte approval":   badUTF8Bytes.Validate,
		"oversized approval scope":       badScope.Validate,
		"pending revoke":                 badCapabilities.Validate,
		"misclassified user feedback":    badFeedback.Validate,
		"committed operation no subject": badOperation.Validate,
	} {
		if !errors.Is(validate(), ErrContract) {
			t.Fatal("unsafe projection accepted", name)
		}
	}
	if err := validRejectedOperation.Validate(); err != nil {
		t.Fatal("pre-entity rejected operation requires an unavailable subject", err)
	}
}

func TestApprovalProposalIsExactAndRequiredOnlyWhileActionable(t *testing.T) {
	now := mutationTime()
	criterion := AcceptanceCriterion{Version: 1, ID: "tests", Kind: "objective", RequiredSource: "deterministic", ValidatorID: "go.test", Description: "Focused tests pass.", Required: true}
	proposal := &ApprovalProposal{Version: 1, Kind: "criteria_change", BoardID: "board", CardID: "card", ExpectedBoardRevision: 2, ExpectedCardRevision: 3, ExpectedCriteriaRevision: 1, ExpectedCriteriaDigest: strings.Repeat("a", 64), Criteria: []AcceptanceCriterion{criterion}}
	summary := ApprovalSummary{ID: "approval", State: "pending", Revision: 1, Prompt: "Review the exact criteria proposal.", ScopeSummary: "Workboard: board", ToolName: "workboard_propose_criteria", ToolBehavior: "idempotent_write", ExpiresAt: now.Add(time.Minute), CanAllow: true, CanDeny: true, Proposal: proposal}
	if err := summary.Validate(); err != nil {
		t.Fatal(err)
	}
	missing := summary
	missing.Proposal = nil
	if !errors.Is(missing.Validate(), ErrContract) {
		t.Fatal("actionable proposal without exact projection accepted")
	}
	wrong := summary
	copyProposal := *proposal
	copyProposal.Kind = "candidate_decision"
	wrong.Proposal = &copyProposal
	if !errors.Is(wrong.Validate(), ErrContract) {
		t.Fatal("proposal kind mismatch accepted")
	}
	terminal := summary
	terminal.State, terminal.Revision, terminal.CanAllow, terminal.CanDeny = "denied", 2, false, false
	if !errors.Is(terminal.Validate(), ErrContract) {
		t.Fatal("terminal approval retained proposal payload")
	}
	candidate := &ApprovalProposal{Version: 1, Kind: "candidate_decision", BoardID: "board", CardID: "card", AttemptID: "attempt", CandidateID: "candidate", ExpectedBoardRevision: 2, ExpectedCardRevision: 3, ExpectedAttemptRevision: 4, CriteriaRevision: 1, EvidenceHeadRevision: 0, CandidateDigest: strings.Repeat("b", 64), CriteriaDigest: strings.Repeat("c", 64), EvidenceSetDigest: strings.Repeat("d", 64), PolicyDigest: strings.Repeat("e", 64), Decision: "rejected", Rationale: "Required objective evidence did not pass."}
	candidateSummary := summary
	candidateSummary.ToolName, candidateSummary.Proposal = "workboard_request_candidate_decision", candidate
	if err := candidateSummary.Validate(); err != nil {
		t.Fatal(err)
	}
	candidate.Decision = "accepted"
	candidate.Rationale = ""
	if !errors.Is(candidateSummary.Validate(), ErrContract) {
		t.Fatal("candidate decision without rationale accepted")
	}
}

func TestFeedbackRevisionForbidsAttemptCost(t *testing.T) {
	revision := int64(1)
	request := FeedbackRequest{Version: 1, IdempotencyKey: "contract-key-0001", TaskID: "task", FeedbackID: "feedback", Action: FeedbackRevise, Accepted: true, ExpectedRevision: &revision}
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	cost := .01
	request.AttemptCost = &cost
	if !errors.Is(request.Validate(), ErrContract) {
		t.Fatal("revision accepted a cost override")
	}
}

func TestFeedbackRecordRequiresAttemptCost(t *testing.T) {
	request := FeedbackRequest{Version: 1, IdempotencyKey: "contract-key-0001", TaskID: "task", Action: FeedbackRecord, Accepted: true}
	if !errors.Is(request.Validate(), ErrContract) {
		t.Fatal("record accepted an absent cost")
	}
	cost := 0.0
	request.AttemptCost = &cost
	if err := request.Validate(); err != nil {
		t.Fatal("record rejected an explicit zero cost", err)
	}
}

func TestMutationSchemaFixturesAndGoldenFailures(t *testing.T) {
	body, err := os.ReadFile("schema/v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var document any
	if json.Unmarshal(body, &document) != nil {
		t.Fatal("invalid schema")
	}
	compiler := jsonschema.NewCompiler()
	const location = "https://nexusrouter.local/schema/webui/v1"
	if err = compiler.AddResource(location, document); err != nil {
		t.Fatal(err)
	}
	var fixtures map[string]json.RawMessage
	decodeFixture(t, "mutations.json", &fixtures)
	definitions := map[string]string{
		"chat": "chat_mutation_receipt", "cancellation": "cancellation_receipt", "steering": "steering_receipt",
		"controls": "task_control_status", "feedback_context": "feedback_context", "feedback_receipt": "feedback_receipt",
		"approval_page": "approval_page", "approval_receipt": "approval_decision_receipt",
		"operation_page": "operation_page", "submission_status": "submission_status",
	}
	for name, definition := range definitions {
		validateSchemaValue(t, compiler, location+"#/$defs/"+definition, fixtures[name], true)
	}
	var negative []struct {
		Name       string          `json:"name"`
		Definition string          `json:"definition"`
		Payload    json.RawMessage `json:"payload"`
	}
	decodeFixture(t, "mutation-errors.json", &negative)
	for _, item := range negative {
		t.Run(item.Name, func(t *testing.T) {
			validateSchemaValue(t, compiler, location+"#/$defs/"+item.Definition, item.Payload, false)
		})
	}
	for name, raw := range map[string]json.RawMessage{
		"duplicate": json.RawMessage(`{"version":1,"operation_id":"operation","operation_id":"other","submission_id":"submission","state":"queued"}`),
		"unknown":   json.RawMessage(`{"version":1,"operation_id":"operation","submission_id":"submission","state":"queued","prompt":"raw"}`),
	} {
		t.Run(name, func(t *testing.T) {
			if name == "duplicate" && RejectDuplicateJSONFields(raw) == nil {
				t.Fatal("duplicate field accepted")
			}
			validateSchemaValue(t, compiler, location+"#/$defs/chat_mutation_receipt", raw, name == "duplicate")
		})
	}
}
