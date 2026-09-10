package webui

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestWorkboardReadContracts(t *testing.T) {
	queries := []interface{ Validate() error }{
		BoardListOptions{Limit: 25, State: "active"},
		BoardSnapshotOptions{Limit: 100, State: "ready", AssigneeID: "unassigned", OwnerID: "worker-a", ClaimState: "active"},
		DependencyOptions{Limit: 64, Direction: DependencyPrerequisites},
		BoardEventOptions{Limit: 100},
	}
	for _, query := range queries {
		if err := query.Validate(); err != nil {
			t.Fatalf("valid query rejected: %T: %v", query, err)
		}
	}
	invalid := []interface{ Validate() error }{
		BoardListOptions{},
		BoardListOptions{Limit: MaxBoardPageItems + 1},
		BoardSnapshotOptions{Limit: 1, State: "active"},
		BoardSnapshotOptions{Limit: 1, ClaimState: "released"},
		DependencyOptions{Limit: 1, Direction: "both"},
		BoardEventOptions{Limit: 1, After: strings.Repeat("x", MaxCursorBytes+1)},
	}
	for _, query := range invalid {
		if query.Validate() == nil {
			t.Fatalf("invalid query accepted: %#v", query)
		}
	}

	link := DependencyLink{Version: 1, BoardID: "board-a", CardID: "card-a", DependencyID: "card-b"}
	dependencyPage := DependencyPage{Version: 1, BoardID: "board-a", CardID: "card-a", Direction: DependencyPrerequisites, GraphRevision: 2, GraphDigest: strings.Repeat("a", 64), Items: []DependencyLink{link}}
	if err := dependencyPage.Validate(); err != nil {
		t.Fatal("valid dependency page rejected", err)
	}
	event := BoardEvent{Version: 1, ID: "event-a", BoardID: "board-a", Sequence: 4, OperationID: "operation-key-01", Kind: CardRevise, ActorID: "operator-a", ActorType: "operator", CardID: "card-a", CreatedAt: workboardTime()}
	eventPage := BoardEventPage{Version: 1, BoardID: "board-a", Items: []BoardEvent{event}, HighWaterSequence: 4}
	if err := eventPage.Validate(); err != nil {
		t.Fatal("valid event page rejected", err)
	}
	event.Kind = CardReorder
	if err := event.Validate(); err != nil {
		t.Fatal("card reorder event rejected", err)
	}
	dependencyPage.Items[0].BoardID = "other-board"
	if dependencyPage.Validate() == nil {
		t.Fatal("cross-board dependency accepted")
	}
	eventPage.Items = append(eventPage.Items, event)
	if eventPage.Validate() == nil {
		t.Fatal("duplicate event accepted")
	}
}

func TestNewBoardActionsRequireExactFences(t *testing.T) {
	revision := int64(2)
	digest := strings.Repeat("a", 64)
	title := "Renamed"
	description := ""
	valid := []BoardRequest{
		{Version: 1, Action: BoardRevise, IdempotencyKey: "operation-key-01", BoardID: "board-a", Title: &title, ExpectedBoardRevision: &revision},
		{Version: 1, Action: BoardArchive, IdempotencyKey: "operation-key-02", BoardID: "board-a", ExpectedBoardRevision: &revision},
		{Version: 1, Action: CardCancelFinalize, IdempotencyKey: "operation-key-03", BoardID: "board-a", CardID: "card-a", ExpectedCardRevision: &revision, StopProofID: "stop-proof", TaskHeadDigest: digest, ProcessProofDigest: digest, EffectEvidenceDigest: digest, EffectResolution: "effect_free"},
	}
	for _, request := range valid {
		if err := request.Validate(); err != nil {
			t.Fatalf("valid %s rejected: %v", request.Action, err)
		}
	}
	invalid := valid
	invalid[0].Title, invalid[0].Description = nil, nil
	invalid[1].Description = &description
	invalid[2].ClaimID = "claim-a"
	for _, request := range invalid {
		if request.Validate() == nil {
			t.Fatalf("invalid %s accepted", request.Action)
		}
	}
}

func TestCardMoveAndReorderRejectSelfAnchors(t *testing.T) {
	revision := int64(2)
	for _, request := range []BoardRequest{
		{Version: 1, Action: CardMove, IdempotencyKey: "operation-key-03", BoardID: "board-a", CardID: "card-a", TargetState: "ready", BeforeCardID: "card-a", ExpectedBoardRevision: &revision, ExpectedLayoutRevision: &revision, ExpectedCardRevision: &revision},
		{Version: 1, Action: CardMove, IdempotencyKey: "operation-key-04", BoardID: "board-a", CardID: "card-a", TargetState: "ready", AfterCardID: "card-a", ExpectedBoardRevision: &revision, ExpectedLayoutRevision: &revision, ExpectedCardRevision: &revision},
		{Version: 1, Action: CardReorder, IdempotencyKey: "operation-key-05", BoardID: "board-a", CardID: "card-a", BeforeCardID: "card-a", ExpectedBoardRevision: &revision, ExpectedLayoutRevision: &revision, ExpectedCardRevision: &revision},
		{Version: 1, Action: CardReorder, IdempotencyKey: "operation-key-06", BoardID: "board-a", CardID: "card-a", AfterCardID: "card-a", ExpectedBoardRevision: &revision, ExpectedLayoutRevision: &revision, ExpectedCardRevision: &revision},
	} {
		if request.Validate() == nil {
			t.Fatalf("%s accepted self anchor", request.Action)
		}
	}
}

func TestCardReviseClearSemanticsAndConditionalGraphFence(t *testing.T) {
	revision := int64(2)
	graph := int64(3)
	clear := true
	base := BoardRequest{Version: 1, Action: CardRevise, IdempotencyKey: "operation-key-04", BoardID: "board-a", CardID: "card-a", ExpectedCardRevision: &revision}
	title := "Updated"
	ordinary := base
	ordinary.Title = &title
	if ordinary.Validate() != nil {
		t.Fatal("ordinary revise unexpectedly requires graph fence")
	}
	clearParent := base
	clearParent.ClearParent, clearParent.ExpectedGraphRevision = &clear, &graph
	if clearParent.Validate() != nil {
		t.Fatal("clear parent with graph fence rejected")
	}
	clearAssignee := base
	clearAssignee.ClearAssignee = &clear
	if clearAssignee.Validate() != nil {
		t.Fatal("clear assignee rejected")
	}
	clearParent.ExpectedGraphRevision = nil
	if clearParent.Validate() == nil {
		t.Fatal("parent clear without graph fence accepted")
	}
	ordinary.ExpectedGraphRevision = &graph
	if ordinary.Validate() == nil {
		t.Fatal("unbound graph fence accepted")
	}
	clearAssignee.AssigneeID = "worker-a"
	if clearAssignee.Validate() == nil {
		t.Fatal("clear and replacement assignee accepted together")
	}
}

func TestCardReviseRejectsExplicitFalseClearFlags(t *testing.T) {
	for name, body := range map[string]string{
		"parent":   `{"version":1,"action":"card.revise","idempotency_key":"operation-key-05","board_id":"board-a","card_id":"card-a","expected_card_revision":2,"clear_parent":false}`,
		"assignee": `{"version":1,"action":"card.revise","idempotency_key":"operation-key-06","board_id":"board-a","card_id":"card-a","expected_card_revision":2,"clear_assignee":false}`,
	} {
		t.Run(name, func(t *testing.T) {
			var request BoardRequest
			if err := json.Unmarshal([]byte(body), &request); err != nil {
				t.Fatal(err)
			}
			if request.Validate() == nil {
				t.Fatal("explicit false clear flag accepted")
			}
		})
	}
}

func TestWorkboardReadSchemaParity(t *testing.T) {
	compiler := jsonschema.NewCompiler()
	for _, item := range []struct{ location, path string }{
		{"https://darwinrouter.local/schema/webui/v1", "schema/v1.schema.json"},
		{"https://darwinrouter.local/schema/webui/workboard-v1", "schema/workboard-v1.schema.json"},
	} {
		body, err := os.ReadFile(item.path)
		if err != nil {
			t.Fatal(err)
		}
		var document any
		if json.Unmarshal(body, &document) != nil || compiler.AddResource(item.location, document) != nil {
			t.Fatal("invalid schema", item.path)
		}
	}
	link := DependencyLink{Version: 1, BoardID: "board-a", CardID: "card-a", DependencyID: "card-b"}
	event := BoardEvent{Version: 1, ID: "event-a", BoardID: "board-a", Sequence: 1, OperationID: "operation-key-01", Kind: CardCreate, ActorID: "operator-a", ActorType: "operator", CardID: "card-a", CreatedAt: workboardTime()}
	values := map[string]any{
		"board_list_options":     BoardListOptions{Limit: 25},
		"board_snapshot_options": BoardSnapshotOptions{Limit: 25, ClaimState: "unclaimed"},
		"dependency_options":     DependencyOptions{Limit: 25, Direction: DependencyPrerequisites},
		"dependency_link":        link,
		"dependency_page":        DependencyPage{Version: 1, BoardID: "board-a", CardID: "card-a", Direction: DependencyPrerequisites, GraphRevision: 1, GraphDigest: strings.Repeat("a", 64), Items: []DependencyLink{link}},
		"board_event_options":    BoardEventOptions{Limit: 25},
		"board_event":            event,
		"board_event_page":       BoardEventPage{Version: 1, BoardID: "board-a", Items: []BoardEvent{event}, HighWaterSequence: 1},
	}
	for definition, value := range values {
		body, _ := json.Marshal(value)
		validateSchemaValue(t, compiler, "https://darwinrouter.local/schema/webui/workboard-v1#/$defs/"+definition, body, true)
	}
	invalidation := BoardChangedData{BoardID: "board-a", CardID: "card-a", Change: "card_changed"}
	if invalidation.Validate() != nil {
		t.Fatal("state-free durable card invalidation rejected")
	}
	invalidationBody, _ := json.Marshal(invalidation)
	validateSchemaValue(t, compiler, "https://darwinrouter.local/schema/webui/v1#/$defs/board_changed_data", invalidationBody, true)
	for definition, body := range map[string]string{
		"board_snapshot_options": `{"limit":1,"state":"active"}`,
		"dependency_page":        `{"version":1,"board_id":"board-a","card_id":"card-a","direction":"prerequisites","graph_revision":1,"graph_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","items":[],"has_more":true}`,
		"board_event":            `{"version":1,"id":"event-a","board_id":"board-a","sequence":1,"operation_id":"operation-key-01","kind":"secret.dump","actor_id":"operator-a","actor_type":"operator","created_at":"2026-09-09T12:00:00Z"}`,
		"board_event_page":       `{"version":1,"board_id":"board-a","items":[],"has_more":false,"high_water_sequence":0,"raw_payload":"secret"}`,
	} {
		validateSchemaValue(t, compiler, "https://darwinrouter.local/schema/webui/workboard-v1#/$defs/"+definition, json.RawMessage(body), false)
	}
}

func TestPublishedWorkboardReadFixtures(t *testing.T) {
	compiler := jsonschema.NewCompiler()
	for _, item := range []struct{ location, path string }{
		{"https://darwinrouter.local/schema/webui/v1", "schema/v1.schema.json"},
		{"https://darwinrouter.local/schema/webui/workboard-v1", "schema/workboard-v1.schema.json"},
	} {
		body, err := os.ReadFile(item.path)
		if err != nil {
			t.Fatal(err)
		}
		var document any
		if json.Unmarshal(body, &document) != nil || compiler.AddResource(item.location, document) != nil {
			t.Fatal("invalid schema", item.path)
		}
	}
	body, err := os.ReadFile("testdata/v1/workboard-reads.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture map[string]json.RawMessage
	if json.Unmarshal(body, &fixture) != nil {
		t.Fatal("invalid fixture")
	}
	for _, definition := range []string{"board_list_options", "board_snapshot_options", "dependency_options", "dependency_page", "board_event_options", "board_event_page"} {
		validateSchemaValue(t, compiler, "https://darwinrouter.local/schema/webui/workboard-v1#/$defs/"+definition, fixture[definition], true)
	}
}
