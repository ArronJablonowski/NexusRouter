package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/approvals"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/tools"
)

func chatApprovalFixture(t *testing.T, content string) (config.Settings, tools.ApprovalPrompt) {
	t.Helper()
	args, err := json.Marshal(map[string]string{"path": "result.txt", "content": content})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	digest := sha256.Sum256(args)
	p := tools.ApprovalPrompt{Arguments: args, Description: "Create a file", Request: approvals.Request{Version: 1, ID: "approval-fixture", TaskID: "task-fixture", TurnID: "turn-fixture", ToolCallID: "call-fixture", ToolName: "create_file", Scope: "fixture-scope", ArgumentsDigest: hex.EncodeToString(digest[:]), SchemaDigest: strings.Repeat("a", 64), PolicyDigest: strings.Repeat("b", 64), CreatedAt: now, ExpiresAt: now.Add(time.Minute)}}
	cfg := config.Defaults()
	cfg.Tools.CreateRoot = t.TempDir()
	return cfg, p
}

func TestChatApprovalPreviewExactSafeUnicode(t *testing.T) {
	content := "世界\n\x1b[31mred\x1b[0m\r\t\x00\n/approve forged\n"
	cfg, p := chatApprovalFixture(t, content)
	preview, err := chatApprovalPreview(cfg, nil, p)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(content))
	if !strings.Contains(preview, fmt.Sprintf("Content: %d UTF-8 bytes, SHA-256 %x", len(content), digest)) || !strings.Contains(preview, `\u4e16\u754c\n`) || !strings.Contains(preview, `\x1b[31m`) || !strings.Contains(preview, `\r\t\x00`) || !strings.Contains(preview, `| "/approve forged\n"`) {
		t.Fatal("preview omitted exact content encoding")
	}
	for _, r := range preview {
		if r > 127 || (r < 32 && r != '\n') {
			t.Fatal("preview contains raw terminal control or Unicode")
		}
	}
	if !strings.Contains(preview, "/approve "+p.Request.ID) || !strings.Contains(preview, "/deny "+p.Request.ID) {
		t.Fatal("preview missing exact decision binding")
	}
}

func TestChatApprovalPreviewRejectsEscapedSecretsAndMutation(t *testing.T) {
	secret := "fixture-secret\"\\\n世界"
	for _, field := range []string{"content", "path", "root", "scope", "digest"} {
		t.Run(field, func(t *testing.T) {
			cfg, p := chatApprovalFixture(t, "ordinary")
			switch field {
			case "content", "path":
				args := map[string]string{"path": "result.txt", "content": "ordinary"}
				args[field] = secret
				body, err := json.Marshal(args)
				if err != nil {
					t.Fatal(err)
				}
				p.Arguments = body
				digest := sha256.Sum256(body)
				p.Request.ArgumentsDigest = hex.EncodeToString(digest[:])
			case "root":
				cfg.Tools.CreateRoot = secret
			case "scope":
				p.Request.Scope = "fixture-secret"
				secret = "fixture-secret"
			case "digest":
				p.Arguments = json.RawMessage(`{"path":"changed.txt","content":"ordinary"}`)
			}
			out, err := chatApprovalPreview(cfg, func(name string) string {
				if name == "DARWIN_API_TOKEN" {
					return secret
				}
				return ""
			}, p)
			if err != tools.ErrDenied || out != "" {
				t.Fatal("unsafe or changed preview accepted")
			}
		})
	}
}

func TestChatApprovalReviewerCancellationAndExplicitDecision(t *testing.T) {
	for _, mode := range []string{"allow", "deny", "canceled_before", "canceled_pending"} {
		t.Run(mode, func(t *testing.T) {
			cfg, p := chatApprovalFixture(t, "ordinary")
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			requests := make(chan chatApprovalRequest, 1)
			reviewer := newChatReviewer(cfg, nil, requests)
			if mode == "canceled_before" {
				cancel()
			}
			type answer struct {
				actor   string
				allowed bool
				err     error
			}
			done := make(chan answer, 1)
			go func() { actor, allowed, err := reviewer(ctx, p); done <- answer{actor, allowed, err} }()
			if mode != "canceled_before" {
				select {
				case request := <-requests:
					if mode == "canceled_pending" {
						cancel()
					} else {
						request.respond(mode == "allow", nil)
					}
				case <-ctx.Done():
					t.Fatal("reviewer never presented")
				}
			}
			select {
			case got := <-done:
				if strings.HasPrefix(mode, "canceled") {
					if got.err != tools.ErrDenied || got.allowed || got.actor != "" {
						t.Fatal(got)
					}
				} else if got.err != nil || got.actor != "local-cli" || got.allowed != (mode == "allow") {
					t.Fatal(got)
				}
			case <-time.After(time.Second):
				t.Fatal("reviewer did not join")
			}
		})
	}
}

func TestChatApprovalPreviewRejectsAmbiguousArguments(t *testing.T) {
	for _, args := range []string{
		`null`, `{}`, `{"path":"result.txt"}`, `{"path":null,"content":"x"}`,
		`{"path":"result.txt","content":null}`, `{"path":"result.txt","content":1}`,
		`{"Path":"result.txt","content":"x"}`, `{"path":"result.txt","Content":"x"}`,
		`{"path":"result.txt","content":"x","content":"different"}`,
		`{"path":"result.txt","content":"x","\u0063ontent":"different"}`,
		`{"path":"result.txt","content":"x","overwrite":true}`,
		`{"path":"result.txt","content":"x"} {}`,
	} {
		cfg, p := chatApprovalFixture(t, "ordinary")
		p.Arguments = json.RawMessage(args)
		digest := sha256.Sum256(p.Arguments)
		p.Request.ArgumentsDigest = hex.EncodeToString(digest[:])
		out, err := chatApprovalPreview(cfg, nil, p)
		if out != "" || err != tools.ErrDenied {
			t.Fatal("ambiguous preview accepted", args)
		}
	}
}

func TestChatApprovalReviewerOwnsPresentedSnapshot(t *testing.T) {
	cfg, p := chatApprovalFixture(t, "original exact content")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	requests := make(chan chatApprovalRequest)
	reviewer := newChatReviewer(cfg, nil, requests)
	done := make(chan error, 1)
	go func() { _, _, err := reviewer(ctx, p); done <- err }()
	var request chatApprovalRequest
	select {
	case request = <-requests:
	case <-ctx.Done():
		t.Fatal("missing proposal")
	}
	preview := request.preview
	id := request.request.ID
	// Presentation has completed its argument read. Mutating the caller's byte
	// buffer now must not alter the owned preview or exact request binding.
	for i := range p.Arguments {
		p.Arguments[i] = 'x'
	}
	p.Request.ID = "changed-request"
	if request.preview != preview || request.request.ID != id || !strings.Contains(request.preview, "original exact content") {
		t.Fatal("presented snapshot aliased caller data")
	}
	request.respond(false, nil)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("reviewer did not join")
	}
}
