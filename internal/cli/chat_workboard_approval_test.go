package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/approvals"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/tools"
)

func chatWorkboardPrompt(t *testing.T, name, scope string, arguments any) (config.Settings, tools.ApprovalPrompt) {
	t.Helper()
	body, err := json.Marshal(arguments)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	digest := sha256.Sum256(body)
	settings := config.Defaults()
	settings.Tools.WorkboardReadEnabled = true
	settings.Tools.WorkboardWriteEnabled = true
	return settings, tools.ApprovalPrompt{Arguments: body, Description: "Approved workboard operation", Request: approvals.Request{
		Version: approvals.Version, ID: "approval-workboard", TaskID: "task-workboard", TurnID: "turn-workboard", ToolCallID: "call-workboard",
		ToolName: name, ToolBehavior: tools.BehaviorIdempotentWrite, Scope: scope, ArgumentsDigest: hex.EncodeToString(digest[:]),
		SchemaDigest: strings.Repeat("a", 64), PolicyDigest: strings.Repeat("b", 64), CreatedAt: now, ExpiresAt: now.Add(time.Minute),
	}}
}

func TestChatWorkboardApprovalPreviewSupportsMutationCatalog(t *testing.T) {
	criteria := []map[string]any{{"version": 1, "id": "tests", "kind": "objective", "required_source": "deterministic", "validator_id": "go_test", "description": "Tests pass.", "required": true}}
	digest := strings.Repeat("d", 64)
	cases := map[string]struct {
		scope string
		args  map[string]any
	}{
		"workboard_create_board":               {"workboards", map[string]any{"idempotency_key": "approval-board-create-01", "title": "Board"}},
		"workboard_revise_board":               {"workboard:board_a", map[string]any{"idempotency_key": "approval-board-revise-01", "board_id": "board_a", "title": "Revised", "expected_board_revision": 1}},
		"workboard_archive_board":              {"workboard:board_a", map[string]any{"idempotency_key": "approval-board-archive-1", "board_id": "board_a", "expected_board_revision": 1}},
		"workboard_create_card":                {"workboard:board_a", map[string]any{"idempotency_key": "approval-card-create-001", "board_id": "board_a", "title": "Card", "criteria": criteria, "expected_board_revision": 1, "expected_graph_revision": 1}},
		"workboard_update_card":                {"workboard:board_a", map[string]any{"idempotency_key": "approval-card-update-001", "board_id": "board_a", "card_id": "card_a", "title": "Updated", "expected_card_revision": 1}},
		"workboard_transition_card":            {"workboard:board_a", map[string]any{"idempotency_key": "approval-card-move-0001", "board_id": "board_a", "card_id": "card_a", "target_state": "ready", "expected_board_revision": 1, "expected_layout_revision": 1, "expected_card_revision": 1}},
		"workboard_reorder_card":               {"workboard:board_a", map[string]any{"idempotency_key": "approval-card-order-001", "board_id": "board_a", "card_id": "card_a", "before_card_id": "card_b", "expected_board_revision": 1, "expected_layout_revision": 1, "expected_card_revision": 1}},
		"workboard_add_dependency":             {"workboard:board_a", map[string]any{"idempotency_key": "approval-dependency-add1", "board_id": "board_a", "card_id": "card_a", "dependency_id": "card_b", "expected_card_revision": 1, "expected_graph_revision": 1}},
		"workboard_remove_dependency":          {"workboard:board_a", map[string]any{"idempotency_key": "approval-dependency-rm01", "board_id": "board_a", "card_id": "card_a", "dependency_id": "card_b", "expected_card_revision": 1, "expected_graph_revision": 1}},
		"workboard_request_pause":              {"workboard:board_a", map[string]any{"idempotency_key": "approval-pause-request1", "board_id": "board_a", "card_id": "card_a", "expected_card_revision": 1}},
		"workboard_request_resume":             {"workboard:board_a", map[string]any{"idempotency_key": "approval-resume-request", "board_id": "board_a", "card_id": "card_a", "expected_card_revision": 1}},
		"workboard_request_cancel":             {"workboard:board_a", map[string]any{"idempotency_key": "approval-cancel-request", "board_id": "board_a", "card_id": "card_a", "expected_card_revision": 1}},
		"workboard_propose_criteria":           {"workboard:board_a", map[string]any{"idempotency_key": "approval-criteria-propose", "board_id": "board_a", "card_id": "card_a", "expected_board_revision": 1, "expected_card_revision": 1, "expected_criteria_revision": 1, "expected_criteria_digest": digest, "criteria": criteria}},
		"workboard_request_candidate_decision": {"workboard:board_a", map[string]any{"idempotency_key": "approval-candidate-decision", "board_id": "board_a", "card_id": "card_a", "attempt_id": "attempt_a", "candidate_id": "candidate_a", "expected_board_revision": 1, "expected_card_revision": 1, "expected_attempt_revision": 1, "criteria_revision": 1, "evidence_head_revision": 1, "candidate_digest": digest, "criteria_digest": digest, "evidence_set_digest": digest, "policy_digest": digest, "decision": "accepted", "rationale": "Deterministic evidence passed."}},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			settings, prompt := chatWorkboardPrompt(t, name, test.scope, test.args)
			preview, err := chatApprovalPreview(settings, nil, prompt)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{name, strconv.QuoteToASCII(test.scope), strconv.QuoteToASCII(string(prompt.Arguments)), "/approve " + prompt.Request.ID, "/deny " + prompt.Request.ID, "never automatically replayed"} {
				if !strings.Contains(preview, want) {
					t.Fatalf("preview missing %q: %s", want, preview)
				}
			}
			for _, r := range preview {
				if r > 127 || r < 32 && r != '\n' {
					t.Fatalf("preview contains unsafe terminal rune %U", r)
				}
			}
		})
	}
}

func TestChatWorkboardApprovalPreviewFailsClosed(t *testing.T) {
	settings, prompt := chatWorkboardPrompt(t, "workboard_update_card", "workboard:board_a", map[string]any{
		"idempotency_key": "approval-failure-case1", "board_id": "board_a", "card_id": "card_a", "title": "Updated", "expected_card_revision": 1,
	})
	tests := map[string]func(*config.Settings, *tools.ApprovalPrompt){
		"disabled":    func(s *config.Settings, _ *tools.ApprovalPrompt) { s.Tools.WorkboardWriteEnabled = false },
		"wrong scope": func(_ *config.Settings, p *tools.ApprovalPrompt) { p.Request.Scope = "workboard:board_b" },
		"wrong behavior": func(_ *config.Settings, p *tools.ApprovalPrompt) {
			p.Request.ToolBehavior = tools.BehaviorNonIdempotentWrite
		},
		"changed arguments": func(_ *config.Settings, p *tools.ApprovalPrompt) {
			p.Arguments = append([]byte(nil), []byte(`{"board_id":"board_a"}`)...)
		},
		"unknown tool": func(_ *config.Settings, p *tools.ApprovalPrompt) { p.Request.ToolName = "workboard_unknown" },
		"unknown field": func(_ *config.Settings, p *tools.ApprovalPrompt) {
			p.Arguments = []byte(`{"idempotency_key":"approval-failure-case1","board_id":"board_a","card_id":"card_a","title":"Updated","expected_card_revision":1,"actor":"operator"}`)
			digest := sha256.Sum256(p.Arguments)
			p.Request.ArgumentsDigest = hex.EncodeToString(digest[:])
		},
		"duplicate field": func(_ *config.Settings, p *tools.ApprovalPrompt) {
			p.Arguments = []byte(`{"idempotency_key":"approval-failure-case1","board_id":"board_a","board_id":"board_a","card_id":"card_a","title":"Updated","expected_card_revision":1}`)
			digest := sha256.Sum256(p.Arguments)
			p.Request.ArgumentsDigest = hex.EncodeToString(digest[:])
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			cfg, candidate := settings, prompt
			candidate.Arguments = append(json.RawMessage(nil), prompt.Arguments...)
			mutate(&cfg, &candidate)
			if preview, err := chatApprovalPreview(cfg, nil, candidate); err != tools.ErrDenied || preview != "" {
				t.Fatalf("unsafe proposal accepted: preview=%q err=%v", preview, err)
			}
		})
	}
}

func TestChatWorkboardApprovalPreviewRejectsDecodedSecret(t *testing.T) {
	secret := "secret-\"-\\-\n-世界"
	settings, prompt := chatWorkboardPrompt(t, "workboard_revise_board", "workboard:board_a", map[string]any{
		"idempotency_key": "approval-secret-case01", "board_id": "board_a", "description": "prefix " + secret + " suffix", "expected_board_revision": 1,
	})
	settings.Providers = append(settings.Providers, config.Provider{ID: "fixture", Kind: "ollama", Endpoint: "http://127.0.0.1:11434", APIKeyEnv: "WORKBOARD_SECRET"})
	preview, err := chatApprovalPreview(settings, func(name string) string {
		if name == "WORKBOARD_SECRET" {
			return secret
		}
		return ""
	}, prompt)
	if err != tools.ErrDenied || preview != "" {
		t.Fatalf("decoded secret appeared in preview: %q %v", preview, err)
	}
	if strings.Contains(fmt.Sprint(preview), secret) {
		t.Fatal("secret leaked")
	}
}
