package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ArronJablonowski/DarwinRouter/approvals"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/tools"
	"github.com/mattn/go-isatty"
)

type chatApprovalDecision struct {
	allowed bool
	err     error
}

// Only the trusted reviewer creates these requests; model text is never parsed
// as an approval. The preview is ephemeral and contains no known credentials.
type chatApprovalRequest struct {
	request approvals.Request
	preview string
	ctx     context.Context
	answer  chan chatApprovalDecision
}

func (r chatApprovalRequest) respond(allowed bool, err error) {
	select {
	case r.answer <- chatApprovalDecision{allowed: allowed, err: err}:
	default:
	}
}

func chatReviewTerminal(v any) bool {
	f, ok := v.(*os.File)
	if !ok {
		return false
	}
	raw, err := f.SyscallConn()
	if err != nil {
		return false
	}
	terminal := false
	if raw.Control(func(fd uintptr) { terminal = isatty.IsTerminal(fd) }) != nil {
		return false
	}
	return terminal
}

func newChatReviewer(settings config.Settings, secret func(string) string, requests chan<- chatApprovalRequest) tools.ApprovalReviewer {
	return func(ctx context.Context, p tools.ApprovalPrompt) (string, bool, error) {
		preview, err := chatApprovalPreview(settings, secret, p)
		if err != nil || ctx == nil || ctx.Err() != nil {
			return "", false, tools.ErrDenied
		}
		r := chatApprovalRequest{request: p.Request, preview: preview, ctx: ctx, answer: make(chan chatApprovalDecision, 1)}
		select {
		case requests <- r:
		case <-ctx.Done():
			return "", false, tools.ErrDenied
		}
		select {
		case decision := <-r.answer:
			if decision.err != nil || ctx.Err() != nil {
				return "", false, tools.ErrDenied
			}
			// The CLI authenticates through its caller-owned terminal, not through
			// a model-supplied actor or an HTTP request. No authority is delegated.
			return "local-cli", decision.allowed, nil
		case <-ctx.Done():
			return "", false, tools.ErrDenied
		}
	}
}

func chatApprovalPreview(settings config.Settings, secret func(string) string, p tools.ApprovalPrompt) (string, error) {
	if p.Request.Validate() != nil || p.Request.ToolName != "create_file" || len(p.Arguments) > 1<<20 || !utf8.Valid(p.Arguments) {
		return "", tools.ErrDenied
	}
	if p.Request.ToolBehavior != "" && p.Request.ToolBehavior != tools.BehaviorNonIdempotentWrite {
		return "", tools.ErrDenied
	}
	digest := sha256.Sum256(p.Arguments)
	if hex.EncodeToString(digest[:]) != p.Request.ArgumentsDigest {
		return "", tools.ErrDenied
	}
	// Independently enforce the builtin's exact fields as well as the registry
	// validation. In particular, null, duplicate and case-aliased keys must not
	// become an apparently reviewable action through permissive struct decoding.
	args, err := chatCreateArguments(p.Arguments)
	if err != nil {
		return "", tools.ErrDenied
	}
	// Reject instead of hiding bytes in a preview that purports to show the exact
	// action. Decoding first catches credentials encoded with JSON escapes.
	values := []string{args.Path, args.Content, settings.Tools.CreateRoot, p.Request.ID, p.Request.TaskID, p.Request.Scope}
	if secret != nil {
		envs := []string{"DARWIN_API_TOKEN"}
		for _, provider := range settings.Providers {
			if provider.APIKeyEnv != "" {
				envs = append(envs, provider.APIKeyEnv)
			}
		}
		for _, env := range envs {
			value := secret(env)
			if value == "" {
				continue
			}
			for _, text := range values {
				if strings.Contains(text, value) {
					return "", tools.ErrDenied
				}
			}
		}
	}
	contentDigest := sha256.Sum256([]byte(args.Content))
	var out strings.Builder
	fmt.Fprintf(&out, "[approval pending %s]\nCreate NEW file only; existing paths are never overwritten.\nConfigured root: %s\nRelative path: %s\nContent: %d UTF-8 bytes, SHA-256 %x\nExact content lines (ASCII-quoted, including newline escapes):\n", p.Request.ID, strconv.QuoteToASCII(settings.Tools.CreateRoot), strconv.QuoteToASCII(args.Path), len(args.Content), contentDigest)
	for _, line := range strings.SplitAfter(args.Content, "\n") {
		fmt.Fprintf(&out, "| %s\n", strconv.QuoteToASCII(line))
	}
	// The validated declaration is metadata, never permission to replay a write.
	if p.Request.ToolBehavior != "" {
		fmt.Fprintf(&out, "Declared behavior: %s (not retry authority)\n", p.Request.ToolBehavior)
	}
	fmt.Fprintf(&out, "Use /approve %s or /deny %s for this request only.\n", p.Request.ID, p.Request.ID)
	if out.Len() > 1<<20 {
		return "", tools.ErrDenied
	}
	return out.String(), nil
}

func chatCreateArguments(raw []byte) (args struct{ Path, Content string }, err error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	if token, e := d.Token(); e != nil || token != json.Delim('{') {
		return args, tools.ErrDenied
	}
	seen := map[string]bool{}
	for d.More() {
		key, e := d.Token()
		name, ok := key.(string)
		if e != nil || !ok || seen[name] || (name != "path" && name != "content") {
			return args, tools.ErrDenied
		}
		seen[name] = true
		value, e := d.Token()
		text, ok := value.(string)
		if e != nil || !ok {
			return args, tools.ErrDenied
		}
		if name == "path" {
			args.Path = text
		} else {
			args.Content = text
		}
	}
	if token, e := d.Token(); e != nil || token != json.Delim('}') {
		return args, tools.ErrDenied
	}
	if _, e := d.Token(); e != io.EOF || len(seen) != 2 || args.Path == "" || len(args.Path) > 4096 || len(args.Content) > 64<<10 {
		return args, tools.ErrDenied
	}
	return args, nil
}
