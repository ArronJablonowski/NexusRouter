package webui

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func decodeFixture[T any](t *testing.T, name string, target *T) {
	t.Helper()
	body, err := os.ReadFile("testdata/v1/" + name)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		t.Fatal("fixture has trailing JSON", err)
	}
}

func TestVersionOneRequestFixtures(t *testing.T) {
	var fixture struct {
		Version  int             `json:"version"`
		Chat     ChatRequest     `json:"chat"`
		Approval ApprovalRequest `json:"approval"`
		Feedback FeedbackRequest `json:"feedback"`
		Board    BoardRequest    `json:"board"`
	}
	decodeFixture(t, "requests.json", &fixture)
	if fixture.Version != ContractVersion {
		t.Fatal("wrong fixture version", fixture.Version)
	}
	for name, validate := range map[string]func() error{
		"chat": fixture.Chat.Validate, "approval": fixture.Approval.Validate,
		"feedback": fixture.Feedback.Validate, "board": fixture.Board.Validate,
	} {
		if err := validate(); err != nil {
			t.Fatal(name, err)
		}
	}
}

func TestVersionOneEventAndErrorFixtures(t *testing.T) {
	var events struct {
		Version int     `json:"version"`
		Events  []Event `json:"events"`
	}
	decodeFixture(t, "events.json", &events)
	if events.Version != ContractVersion || len(events.Events) < 1 {
		t.Fatal("invalid event fixture envelope")
	}
	for _, event := range events.Events {
		if err := event.Validate(); err != nil {
			t.Fatal(event.Kind, err)
		}
	}
	var failures struct {
		Version int     `json:"version"`
		Errors  []Error `json:"errors"`
	}
	decodeFixture(t, "errors.json", &failures)
	if failures.Version != ContractVersion || len(failures.Errors) < 1 {
		t.Fatal("invalid error fixture envelope")
	}
	for _, failure := range failures.Errors {
		if err := failure.Validate(); err != nil {
			t.Fatal(failure.Code, err)
		}
	}
}

func TestOperationMappingIsClosedOwnedAndBrowserScoped(t *testing.T) {
	first := Operations()
	second := Operations()
	if len(first) < 22 || !reflect.DeepEqual(first, second) {
		t.Fatal("operation mapping is incomplete or unstable")
	}
	first[0].Operation = "mutated"
	if reflect.DeepEqual(first, Operations()) {
		t.Fatal("caller mutated canonical operation mapping")
	}
	seen := map[string]bool{}
	existing := map[string]bool{}
	for _, spec := range Operations() {
		if !operationPattern.MatchString(spec.Operation) || seen[spec.Operation] ||
			(spec.Method != "GET" && spec.Method != "POST") ||
			!strings.HasPrefix(spec.BrowserPath, "/app/api/v1/") ||
			strings.TrimSpace(spec.ServicePrimitive) == "" {
			t.Fatalf("invalid operation mapping: %+v", spec)
		}
		if spec.Mutation != (spec.Method == "POST") {
			t.Fatalf("mutation method mismatch: %+v", spec)
		}
		if spec.Security == "" || (spec.Method == "GET" && spec.Security != SessionRead) {
			t.Fatalf("invalid operation security class: %+v", spec)
		}
		seen[spec.Operation] = true
		existing[spec.Operation] = spec.PrimitiveExists
	}
	for _, operation := range []string{"chat.submit", "chat.stream", "chat.steer", "chat.cancel", "approval.list", "model.list", "route.inspect", "health.inspect"} {
		if !existing[operation] {
			t.Fatal("existing application primitive mislabeled", operation)
		}
	}
	for _, operation := range []string{"session.challenge", "chat.history", "feedback.record", "approval.decide", "board.list", "board.create", "board.read", "board.mutate", "board.stream"} {
		if existing[operation] {
			t.Fatal("future application primitive mislabeled", operation)
		}
	}
}

func TestOperationMappingRebasesToConfiguredShellPath(t *testing.T) {
	operations, err := OperationsAtBase("/darwin")
	if err != nil || len(operations) != len(Operations()) {
		t.Fatal("operation map rebase failed", err)
	}
	for _, operation := range operations {
		if !strings.HasPrefix(operation.BrowserPath, "/darwin/api/v1/") || strings.HasPrefix(operation.BrowserPath, "/app/") {
			t.Fatal("operation path not rebased", operation.BrowserPath)
		}
	}
	if _, err := OperationsAtBase("/bad/"); err == nil {
		t.Fatal("invalid operation base accepted")
	}
}

func TestContractValidationFailsClosed(t *testing.T) {
	key := "contract-key-0001"
	revision := int64(1)
	validChat := ChatRequest{Version: 1, Action: ChatSubmit, IdempotencyKey: key, ModelID: "auto", Text: "hello"}
	validApproval := ApprovalRequest{Version: 1, IdempotencyKey: key, TaskID: "task", ApprovalID: "approval", Action: ApprovalAllow, ExpectedRevision: 1}
	validFeedback := FeedbackRequest{Version: 1, IdempotencyKey: key, TaskID: "task", Action: FeedbackRevise, FeedbackID: "feedback", Accepted: true, ExpectedRevision: &revision}
	validBoard := BoardRequest{Version: 1, Action: ClaimHeartbeat, IdempotencyKey: key, BoardID: "board", CardID: "card", ClaimID: "claim", AttemptID: "attempt", ExpectedClaimRevision: &revision}
	validEvent := Event{Version: 1, Kind: ChatDelta, Durability: Provisional, Subject: "subject", Data: json.RawMessage(`{"task_id":"task","text":"partial"}`)}
	validError := Error{Version: 1, Code: "conflict", Message: "Refresh and retry.", Retryable: true, CurrentRevision: &revision}
	for name, validate := range map[string]func() error{
		"chat": validChat.Validate, "approval": validApproval.Validate,
		"feedback": validFeedback.Validate, "board": validBoard.Validate,
		"event": validEvent.Validate, "error": validError.Validate,
	} {
		if err := validate(); err != nil {
			t.Fatal("valid contract rejected", name, err)
		}
	}

	badChat := validChat
	badChat.Version = 2
	badApproval := validApproval
	badApproval.ExpectedRevision = 0
	badFeedback := validFeedback
	badFeedback.AttemptCost = math.NaN()
	badBoard := validBoard
	badBoard.ClaimID = ""
	title := "card"
	badCreate := BoardRequest{Version: 1, Action: CardCreate, IdempotencyKey: key, BoardID: "board", Title: &title}
	badEvent := validEvent
	badEvent.Durability = Committed
	badEventData := validEvent
	badEventData.Data = json.RawMessage(`["raw", "payload"]`)
	badEventField := validEvent
	badEventField.Data = json.RawMessage(`{"text":"partial","prompt":"secret"}`)
	badEventDuplicate := validEvent
	badEventDuplicate.Data = json.RawMessage(`{"text":"first","text":"second"}`)
	badError := validError
	badError.Message = "private\x00detail"
	for name, validate := range map[string]func() error{
		"chat": badChat.Validate, "approval": badApproval.Validate,
		"feedback": badFeedback.Validate, "board": badBoard.Validate,
		"card create without CAS": badCreate.Validate, "event": badEvent.Validate,
		"event data is not object": badEventData.Validate, "event unknown field": badEventField.Validate,
		"event duplicate field": badEventDuplicate.Validate, "error": badError.Validate,
	} {
		if !errors.Is(validate(), ErrContract) {
			t.Fatal("invalid contract accepted", name)
		}
	}
}

func TestPublishedSchemaHasEveryContractDefinition(t *testing.T) {
	body, err := os.ReadFile("schema/v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Definitions map[string]json.RawMessage `json:"$defs"`
	}
	if err := json.Unmarshal(body, &schema); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"chat_request", "approval_request", "feedback_request", "board_request", "event", "error"} {
		if len(schema.Definitions[name]) == 0 {
			t.Fatal("missing schema definition", name)
		}
	}
}

func TestSchemaBoardFieldMatrixMatchesGoValidator(t *testing.T) {
	body, err := os.ReadFile("schema/v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Definitions map[string]struct {
			Allowed map[string][]string `json:"x-allowedFieldsByAction"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(body, &schema); err != nil {
		t.Fatal(err)
	}
	allowed := schema.Definitions["board_request"].Allowed
	actions := []BoardAction{BoardCreate, CardCreate, CardRevise, CardMove, DependencyAdd, DependencyRemove,
		CardClaim, ClaimHeartbeat, ClaimRecover, CriteriaRevise, CheckpointAppend, CandidateSubmit,
		AcceptanceAccept, AcceptanceReject, CardPauseRequest, CardCancelRequest, CardBlock, CardUnblock}
	if len(allowed) != len(actions) {
		t.Fatal("schema action matrix is incomplete", len(allowed), len(actions))
	}
	for _, action := range actions {
		got := append([]string(nil), allowed[string(action)]...)
		want := append([]string(nil), allowedBoardFields(action)...)
		slices.Sort(got)
		slices.Sort(want)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("schema/Go field mismatch for %s: %v != %v", action, got, want)
		}
	}
}

func TestPublishedSchemaAcceptsFixturesAndRejectsUnsafeShapes(t *testing.T) {
	body, err := os.ReadFile("schema/v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var document any
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	const location = "https://darwinrouter.local/schema/webui/v1"
	if err := compiler.AddResource(location, document); err != nil {
		t.Fatal(err)
	}

	var requests map[string]json.RawMessage
	decodeFixture(t, "requests.json", &requests)
	for _, name := range []string{"chat", "approval", "feedback", "board"} {
		validateSchemaValue(t, compiler, location+"#/$defs/"+map[string]string{
			"chat": "chat_request", "approval": "approval_request", "feedback": "feedback_request", "board": "board_request",
		}[name], requests[name], true)
	}
	var events struct {
		Version int               `json:"version"`
		Events  []json.RawMessage `json:"events"`
	}
	decodeFixture(t, "events.json", &events)
	for _, event := range events.Events {
		validateSchemaValue(t, compiler, location+"#/$defs/event", event, true)
	}
	var failures struct {
		Version int               `json:"version"`
		Errors  []json.RawMessage `json:"errors"`
	}
	decodeFixture(t, "errors.json", &failures)
	for _, failure := range failures.Errors {
		validateSchemaValue(t, compiler, location+"#/$defs/error", failure, true)
	}

	negative := map[string]struct {
		definition string
		body       string
	}{
		"empty submit":           {"chat_request", `{"version":1,"action":"submit","idempotency_key":"fixture-key-0001"}`},
		"space in key":           {"chat_request", `{"version":1,"action":"submit","idempotency_key":"fixture key 0001","text":"hello"}`},
		"incomplete heartbeat":   {"board_request", `{"version":1,"action":"claim.heartbeat","idempotency_key":"fixture-key-0001","board_id":"board"}`},
		"cursor on provisional":  {"event", `{"version":1,"cursor":"chat:1","kind":"chat.delta","durability":"provisional","subject":"chat","revision":0,"data":{"task_id":"task","text":"partial"}}`},
		"committed delta":        {"event", `{"version":1,"cursor":"chat:1","kind":"chat.delta","durability":"committed","subject":"chat","revision":1,"data":{"task_id":"task","text":"partial"}}`},
		"raw prompt escape":      {"event", `{"version":1,"kind":"chat.delta","durability":"provisional","subject":"chat","revision":0,"data":{"task_id":"task","text":"partial","prompt":"secret"}}`},
		"move with title":        {"board_request", `{"version":1,"action":"card.move","idempotency_key":"fixture-key-0001","board_id":"board","card_id":"card","expected_card_revision":1,"target_state":"ready","title":"irrelevant"}`},
		"zero feedback revision": {"feedback_request", `{"version":1,"action":"revise","idempotency_key":"fixture-key-0001","task_id":"task","feedback_id":"feedback","accepted":true,"attempt_cost":0,"expected_revision":0}`},
	}
	for name, item := range negative {
		t.Run(name, func(t *testing.T) {
			validateSchemaValue(t, compiler, location+"#/$defs/"+item.definition, json.RawMessage(item.body), false)
		})
	}
}

func validateSchemaValue(t *testing.T, compiler *jsonschema.Compiler, location string, body json.RawMessage, wantValid bool) {
	t.Helper()
	schema, err := compiler.Compile(location)
	if err != nil {
		t.Fatal(err)
	}
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatal(err)
	}
	err = schema.Validate(value)
	if (err == nil) != wantValid {
		t.Fatalf("schema validity = %t, want %t: %v", err == nil, wantValid, err)
	}
}

func TestRequestAndEventBounds(t *testing.T) {
	chat := ChatRequest{Version: 1, Action: ChatSubmit, IdempotencyKey: "contract-key-0001", Text: strings.Repeat("x", MaxRequestBytes)}
	if !errors.Is(chat.Validate(), ErrContract) {
		t.Fatal("oversized chat accepted")
	}
	event := Event{Version: 1, Cursor: "subject:1", Kind: LifecycleEvent, Durability: Committed, Subject: "subject", Data: json.RawMessage(`{"ok":true}`)}
	event.Cursor = strings.Repeat("c", MaxCursorBytes+1)
	if !errors.Is(event.Validate(), ErrContract) {
		t.Fatal("oversized cursor accepted")
	}
}

func TestWorkboardCommandsRequireLifecycleAndAuthorityFences(t *testing.T) {
	key := "contract-key-0001"
	revision := int64(2)
	digest := strings.Repeat("a", 64)
	criteria := []AcceptanceCriterion{criterion("tests", "objective")}
	commands := []BoardRequest{
		{Version: 1, Action: CardMove, IdempotencyKey: key, BoardID: "board", CardID: "card", TargetState: "ready", ExpectedBoardRevision: &revision, ExpectedLayoutRevision: &revision, ExpectedCardRevision: &revision},
		{Version: 1, Action: CriteriaRevise, IdempotencyKey: key, BoardID: "board", CardID: "card", Criteria: criteria, ExpectedCardRevision: &revision, ExpectedCriteriaRevision: &revision},
		{Version: 1, Action: ClaimRecover, IdempotencyKey: key, BoardID: "board", CardID: "card", ClaimID: "claim", AttemptID: "attempt", ExpectedCardRevision: &revision, ExpectedClaimRevision: &revision, StopProofID: "stop-proof", TaskHeadDigest: digest, ProcessProofDigest: digest, EffectEvidenceDigest: digest, EffectResolution: "effect_free"},
	}
	for _, command := range commands {
		if err := command.Validate(); err != nil {
			t.Fatal("valid fenced command rejected", command.Action, err)
		}
	}
	badMove := commands[0]
	badMove.TargetState = "review"
	if badMove.Validate() == nil {
		t.Fatal("generic move entered review")
	}
	badRecovery := commands[2]
	badRecovery.EffectResolution = "uncertain"
	if badRecovery.Validate() == nil {
		t.Fatal("uncertain recovery accepted")
	}
	title := "board"
	boardCreate := BoardRequest{Version: 1, Action: BoardCreate, IdempotencyKey: key, Title: &title, Labels: []string{"unrepresented"}}
	if boardCreate.Validate() == nil {
		t.Fatal("unrepresented board labels accepted")
	}
}
