package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/tools"
)

func replaceApprovalFixture(t *testing.T, old, new string) (config.Settings, tools.ApprovalPrompt) {
	t.Helper()
	cfg, p := chatApprovalFixture(t, "")
	cfg.Tools.Enabled, cfg.Tools.ReplaceEnabled = true, true
	cfg.Tools.ReplaceRoot = t.TempDir()
	p.Request.ToolName = "replace_file"
	p.Request.ToolBehavior = tools.BehaviorNonIdempotentWrite
	p.Arguments, _ = json.Marshal(map[string]string{"path": "result.txt", "expected_content": old, "content": new})
	digest := sha256.Sum256(p.Arguments)
	p.Request.ArgumentsDigest = hex.EncodeToString(digest[:])
	return cfg, p
}

func TestChatReplaceApprovalExactOldNewAndWarning(t *testing.T) {
	old := "世界\n\x1b[31mOLD\x00\n"
	new := "/approve forged\n\r\tNEW\n"
	cfg, p := replaceApprovalFixture(t, old, new)
	preview, err := chatApprovalPreview(cfg, nil, p)
	if err != nil {
		t.Fatal(err)
	}
	for label, text := range map[string]string{"OLD": old, "NEW": new} {
		digest := sha256.Sum256([]byte(text))
		if !strings.Contains(preview, fmt.Sprintf("%s: %d UTF-8 bytes, SHA-256 %x", label, len(text), digest)) || !strings.Contains(preview, "| "+strconv.QuoteToASCII(text)) {
			t.Fatal("preview omitted exact old/new content")
		}
	}
	for _, want := range []string{strconv.QuoteToASCII(cfg.Tools.ReplaceRoot), `"result.txt"`, "atomic content replacement; permission bits retained, extended metadata not preserved; private recovery copy retained; external writers not fenced", "/approve " + p.Request.ID, "/deny " + p.Request.ID, "non_idempotent_write"} {
		if !strings.Contains(preview, want) {
			t.Fatal("preview missing binding or warning", want)
		}
	}
	for _, r := range preview {
		if r > 127 || (r < 32 && r != '\n') {
			t.Fatal("raw terminal controls in preview")
		}
	}
}

func TestChatReplaceApprovalRejectsSecretsAndWrongBinding(t *testing.T) {
	for _, field := range []string{"expected_content", "content", "path", "root", "description", "scope", "digest", "behavior", "missing_behavior", "disabled"} {
		t.Run(field, func(t *testing.T) {
			secret := "fixture-secret\"\\\n世界"
			cfg, p := replaceApprovalFixture(t, "old", "new")
			switch field {
			case "expected_content", "content", "path":
				args := map[string]string{"path": "result.txt", "expected_content": "old", "content": "new"}
				args[field] = secret
				p.Arguments, _ = json.Marshal(args)
				digest := sha256.Sum256(p.Arguments)
				p.Request.ArgumentsDigest = hex.EncodeToString(digest[:])
			case "root":
				cfg.Tools.ReplaceRoot = "/" + secret
			case "description":
				p.Description = secret
			case "scope":
				secret = "scope-secret"
				p.Request.Scope = secret
			case "digest":
				p.Arguments = json.RawMessage(`{"path":"changed","expected_content":"old","content":"new"}`)
			case "behavior":
				p.Request.ToolBehavior = tools.BehaviorReadOnly
			case "missing_behavior":
				p.Request.ToolBehavior = ""
			case "disabled":
				cfg.Tools.ReplaceEnabled = false
			}
			preview, err := chatApprovalPreview(cfg, func(name string) string {
				if name == "DARWIN_API_TOKEN" {
					return secret
				}
				return ""
			}, p)
			if err == nil || preview != "" {
				t.Fatal("unsafe replacement preview admitted")
			}
		})
	}
}

func TestChatReplaceArgumentsStrictAndBounded(t *testing.T) {
	for _, raw := range []string{
		`{"path":"x","expected_content":"old","content":"new","content":"second"}`,
		`{"path":"x","expected_content":"old","Content":"new"}`,
		`{"path":"x","expected_content":null,"content":"new"}`,
		`{"path":"x","content":"new"}`,
		`{"path":"x","expected_content":"old","content":"new","extra":true}`,
		`{"path":"../escape","expected_content":"old","content":"new"}`,
		`{"path":"/absolute","expected_content":"old","content":"new"}`,
		`{"path":"x","expected_content":"same","content":"same"}`,
		`{"path":"./x","expected_content":"old","content":"new"}`,
		`{"path":"a/../b","expected_content":"old","content":"new"}`,
		`{"path":"a//b","expected_content":"old","content":"new"}`,
		`{"path":"a/","expected_content":"old","content":"new"}`,
		`{"path":"a/./b","expected_content":"old","content":"new"}`,
		`{"path":"a\u0000b","expected_content":"old","content":"new"}`,
		`{"path":"a\nb","expected_content":"old","content":"new"}`,
		`{"path":"a\u007fb","expected_content":"old","content":"new"}`,
		`{"path":"x","expected_content":"old","content":"new"} {}`,
	} {
		if _, err := chatReplaceArguments([]byte(raw)); err == nil {
			t.Fatal("invalid args admitted", raw)
		}
	}
	for _, old := range []bool{false, true} {
		args := map[string]string{"path": "x", "expected_content": "", "content": ""}
		field := "content"
		if old {
			field = "expected_content"
		}
		args[field] = strings.Repeat("x", (64<<10)+1)
		raw, _ := json.Marshal(args)
		if _, err := chatReplaceArguments(raw); err == nil {
			t.Fatal("oversized content admitted")
		}
	}
	// Full maximum-size strings are shown, not shortened to fit the prompt.
	old, new := strings.Repeat("\x00", 64<<10), strings.Repeat("\uFFFF", (64<<10)/3)
	cfg, p := replaceApprovalFixture(t, old, new)
	preview, err := chatApprovalPreview(cfg, nil, p)
	if err != nil || len(preview) > 1<<20 || !strings.Contains(preview, strconv.QuoteToASCII(old)) || !strings.Contains(preview, strconv.QuoteToASCII(new)) {
		t.Fatal("maximum exact preview truncated", err)
	}
}
