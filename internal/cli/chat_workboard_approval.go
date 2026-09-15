package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/tools"
	"github.com/ArronJablonowski/DarwinRouter/webui"
)

var chatWorkboardActions = map[string]webui.BoardAction{
	"workboard_create_board":      webui.BoardCreate,
	"workboard_revise_board":      webui.BoardRevise,
	"workboard_archive_board":     webui.BoardArchive,
	"workboard_create_card":       webui.CardCreate,
	"workboard_update_card":       webui.CardRevise,
	"workboard_transition_card":   webui.CardMove,
	"workboard_reorder_card":      webui.CardReorder,
	"workboard_add_dependency":    webui.DependencyAdd,
	"workboard_remove_dependency": webui.DependencyRemove,
	"workboard_request_pause":     webui.CardPauseRequest,
	"workboard_request_resume":    webui.CardResumeRequest,
	"workboard_request_cancel":    webui.CardCancelRequest,
}

func chatWorkboardApprovalPreview(settings config.Settings, secret func(string) string, p tools.ApprovalPrompt) (string, error) {
	action, ok := chatWorkboardActions[p.Request.ToolName]
	if !ok || !settings.Tools.WorkboardReadEnabled || !settings.Tools.WorkboardWriteEnabled ||
		p.Request.Validate() != nil || p.Request.ToolBehavior != tools.BehaviorIdempotentWrite ||
		len(p.Arguments) == 0 || len(p.Arguments) > 1<<20 || !utf8.Valid(p.Arguments) {
		return "", tools.ErrDenied
	}
	digest := sha256.Sum256(p.Arguments)
	if hex.EncodeToString(digest[:]) != p.Request.ArgumentsDigest {
		return "", tools.ErrDenied
	}
	request, values, err := decodeChatWorkboardRequest(p.Arguments, action)
	if err != nil {
		return "", tools.ErrDenied
	}
	wantScope := "workboards"
	if action != webui.BoardCreate {
		wantScope = "workboard:" + request.BoardID
	}
	values = append(values, string(p.Arguments), hex.EncodeToString(digest[:]), wantScope, p.Description, p.Request.ID, p.Request.TaskID, p.Request.TurnID, p.Request.ToolCallID, p.Request.Scope)
	if p.Request.Scope != wantScope || chatApprovalContainsSecret(settings, secret, values) {
		return "", tools.ErrDenied
	}
	var out strings.Builder
	fmt.Fprintf(&out, "[approval pending %s]\nDarwinRouter Kanban mutation proposed by the local root model.\nTool: %s\nAction: %s\nExact resource scope: %s\n", p.Request.ID, p.Request.ToolName, action, strconv.QuoteToASCII(wantScope))
	fmt.Fprintf(&out, "Exact model arguments: %d UTF-8 bytes, SHA-256 %x\n| %s\n", len(p.Arguments), digest, strconv.QuoteToASCII(string(p.Arguments)))
	out.WriteString("Warning: approval authorizes this exact one-use request only. A committed or uncertain effect is never automatically replayed.\n")
	fmt.Fprintf(&out, "Declared behavior: %s (not retry authority)\nUse /approve %s or /deny %s for this request only.\n", p.Request.ToolBehavior, p.Request.ID, p.Request.ID)
	if out.Len() > 1<<20 {
		return "", tools.ErrDenied
	}
	return out.String(), nil
}

func decodeChatWorkboardRequest(raw []byte, action webui.BoardAction) (webui.BoardRequest, []string, error) {
	var request webui.BoardRequest
	if webui.RejectDuplicateJSONFields(raw) != nil {
		return request, nil, tools.ErrDenied
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var fields map[string]any
	if decoder.Decode(&fields) != nil || fields == nil || decoder.Decode(new(any)) != io.EOF {
		return request, nil, tools.ErrDenied
	}
	if _, exists := fields["version"]; exists {
		return request, nil, tools.ErrDenied
	}
	if _, exists := fields["action"]; exists {
		return request, nil, tools.ErrDenied
	}
	fields["version"], fields["action"] = webui.ContractVersion, action
	canonical, err := json.Marshal(fields)
	if err != nil {
		return request, nil, tools.ErrDenied
	}
	strict := json.NewDecoder(bytes.NewReader(canonical))
	strict.DisallowUnknownFields()
	if strict.Decode(&request) != nil || strict.Decode(new(any)) != io.EOF || request.Validate() != nil {
		return webui.BoardRequest{}, nil, tools.ErrDenied
	}
	return request, chatWorkboardStrings(fields), nil
}

func chatWorkboardStrings(value any) []string {
	var result []string
	var walk func(any)
	walk = func(current any) {
		switch typed := current.(type) {
		case string:
			result = append(result, typed)
		case []any:
			for _, item := range typed {
				walk(item)
			}
		case map[string]any:
			for key, item := range typed {
				result = append(result, key)
				walk(item)
			}
		}
	}
	walk(value)
	return result
}

func chatApprovalContainsSecret(settings config.Settings, secret func(string) string, values []string) bool {
	if secret == nil {
		return false
	}
	envs := []string{"DARWIN_API_TOKEN"}
	for _, provider := range settings.Providers {
		if provider.APIKeyEnv != "" {
			envs = append(envs, provider.APIKeyEnv)
		}
	}
	for _, env := range envs {
		credential := secret(env)
		if credential == "" {
			continue
		}
		for _, value := range values {
			if strings.Contains(value, credential) {
				return true
			}
		}
	}
	return false
}
