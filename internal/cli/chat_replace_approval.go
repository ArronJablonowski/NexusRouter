package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/tools"
)

func chatReplaceApprovalPreview(settings config.Settings, secret func(string) string, p tools.ApprovalPrompt) (string, error) {
	if !settings.Tools.Enabled || !settings.Tools.ReplaceEnabled || !filepath.IsAbs(settings.Tools.ReplaceRoot) || p.Request.Validate() != nil || p.Request.ToolName != "replace_file" || p.Request.ToolBehavior != tools.BehaviorNonIdempotentWrite || len(p.Arguments) > 1<<20 || !utf8.Valid(p.Arguments) {
		return "", tools.ErrDenied
	}
	digest := sha256.Sum256(p.Arguments)
	if hex.EncodeToString(digest[:]) != p.Request.ArgumentsDigest {
		return "", tools.ErrDenied
	}
	args, err := chatReplaceArguments(p.Arguments)
	if err != nil {
		return "", tools.ErrDenied
	}
	requestBody, err := json.Marshal(p.Request)
	if err != nil {
		return "", tools.ErrDenied
	}
	values := []string{args.Path, args.Expected, args.Content, settings.Tools.ReplaceRoot, string(requestBody), p.Description, p.Request.ID, p.Request.TaskID, p.Request.TurnID, p.Request.ToolCallID, p.Request.Scope}
	if secret != nil {
		envs := []string{"NEXUS_API_TOKEN", "DARWIN_API_TOKEN"}
		for _, provider := range settings.Providers {
			if provider.APIKeyEnv != "" {
				envs = append(envs, provider.APIKeyEnv)
			}
		}
		for _, env := range envs {
			value := secret(env)
			if value != "" {
				for _, text := range values {
					if strings.Contains(text, value) {
						return "", tools.ErrDenied
					}
				}
			}
		}
	}
	oldDigest, newDigest := sha256.Sum256([]byte(args.Expected)), sha256.Sum256([]byte(args.Content))
	var out strings.Builder
	fmt.Fprintf(&out, "[approval pending %s]\nReplace existing file only if its content exactly matches OLD.\nConfigured root: %s\nRelative path: %s\n", p.Request.ID, strconv.QuoteToASCII(settings.Tools.ReplaceRoot), strconv.QuoteToASCII(args.Path))
	fmt.Fprintf(&out, "OLD: %d UTF-8 bytes, SHA-256 %x\nExact OLD content (ASCII-quoted):\n| %s\nNEW: %d UTF-8 bytes, SHA-256 %x\nExact NEW content (ASCII-quoted):\n| %s\n", len(args.Expected), oldDigest, strconv.QuoteToASCII(args.Expected), len(args.Content), newDigest, strconv.QuoteToASCII(args.Content))
	out.WriteString("Warning: atomic content replacement; permission bits retained, extended metadata not preserved; private recovery copy retained; external writers not fenced.\n")
	fmt.Fprintf(&out, "Declared behavior: %s (not retry authority)\nUse /approve %s or /deny %s for this request only.\n", p.Request.ToolBehavior, p.Request.ID, p.Request.ID)
	if out.Len() > 1<<20 {
		return "", tools.ErrDenied
	}
	return out.String(), nil
}

func chatReplaceArguments(raw []byte) (args struct{ Path, Expected, Content string }, err error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	if token, e := d.Token(); e != nil || token != json.Delim('{') {
		return args, tools.ErrDenied
	}
	seen := map[string]bool{}
	for d.More() {
		key, e := d.Token()
		name, ok := key.(string)
		if e != nil || !ok || seen[name] || (name != "path" && name != "expected_content" && name != "content") {
			return args, tools.ErrDenied
		}
		seen[name] = true
		value, e := d.Token()
		text, ok := value.(string)
		if e != nil || !ok || !utf8.ValidString(text) {
			return args, tools.ErrDenied
		}
		switch name {
		case "path":
			args.Path = text
		case "expected_content":
			args.Expected = text
		case "content":
			args.Content = text
		}
	}
	if token, e := d.Token(); e != nil || token != json.Delim('}') {
		return args, tools.ErrDenied
	}
	if _, e := d.Token(); e != io.EOF || len(seen) != 3 || !chatReplacePath(args.Path) || args.Expected == args.Content || len(args.Expected) > 64<<10 || len(args.Content) > 64<<10 {
		return args, tools.ErrDenied
	}
	return args, nil
}

func chatReplacePath(path string) bool {
	if !filepath.IsLocal(path) || len(path) > 4096 || strings.ContainsAny(path, "\x00\\") || !utf8.ValidString(path) {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	for _, r := range path {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}
