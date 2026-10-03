package api

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/providers"
)

// serveChatCompletions is a bounded text-only compatibility adapter. Authentication,
// origin checks and the execution deadline are supplied by ServeHTTP. Run returns
// only after durable completion; streaming uses the redacted text callback and
// never exposes raw journal deltas. Unknown metadata must not be invented.
func (h *Handler) serveChatCompletions(w http.ResponseWriter, r *http.Request) {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		chatFailure(w, 415, "invalid_request_error", "json_required")
		return
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		w.Header().Set("Retry-After", "1")
		chatFailure(w, 503, "server_error", "capacity")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		var limit *http.MaxBytesError
		status := 400
		if errors.As(err, &limit) {
			status = 413
		}
		chatFailure(w, status, "invalid_request_error", "invalid_request")
		return
	}
	req, stream, err := decodeChatRequest(body)
	if err != nil {
		chatFailure(w, 400, "invalid_request_error", "unsupported_or_invalid_request")
		return
	}
	if r.Context().Err() != nil {
		return
	}
	if stream.Enabled {
		h.serveChatStream(w, r, req, stream.IncludeUsage)
		return
	}
	result, err := h.services.Run(r.Context(), req)
	if r.Context().Err() == context.Canceled {
		return
	}
	if err != nil || r.Context().Err() != nil {
		status, code, kind := 500, "task_failed", "server_error"
		if errors.Is(err, app.ErrAdmission) || errors.Is(err, app.ErrHarnessUnsupported) {
			status, code, kind = 422, "admission_denied", "invalid_request_error"
		}
		if errors.Is(err, context.DeadlineExceeded) || r.Context().Err() == context.DeadlineExceeded {
			status, code, kind = 504, "deadline_exceeded", "server_error"
		}
		chatFailure(w, status, kind, code)
		return
	}
	if len(result.Text) > 1<<20 || !utf8.ValidString(result.Text) {
		chatFailure(w, 502, "server_error", "invalid_upstream_response")
		return
	}
	id := "chatcmpl-" + rand.Text()
	created := time.Now().Unix()
	var finish any
	switch result.FinishReason {
	case "stop", "length", "content_filter", "tool_calls", "function_call":
		finish = result.FinishReason
	}
	base := func(object string, choice map[string]any) map[string]any {
		return map[string]any{"id": id, "object": object, "created": created, "model": req.ModelID, "choices": []any{choice}}
	}
	response := base("chat.completion", map[string]any{"index": 0, "message": map[string]string{"role": "assistant", "content": result.Text}, "finish_reason": finish})
	if u := result.Usage; u != nil && u.InputTokens >= 0 && u.OutputTokens >= 0 && u.InputTokens <= (1<<63-1)-u.OutputTokens {
		response["usage"] = map[string]int64{"prompt_tokens": u.InputTokens, "completion_tokens": u.OutputTokens, "total_tokens": u.InputTokens + u.OutputTokens}
	}
	if result.HarnessEvidenceStatus != "" {
		response["nexus_harness_evidence_status"] = result.HarnessEvidenceStatus
	}
	writeJSON(w, 200, response)
}

func chatFailure(w http.ResponseWriter, status int, kind, code string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": code, "type": kind, "param": nil, "code": code}})
}

type chatStreamOptions struct {
	Enabled, IncludeUsage bool
}

func decodeChatRequest(body []byte) (app.Request, chatStreamOptions, error) {
	bad := errors.New("unsupported or invalid chat request")
	req := app.Request{}
	stream := chatStreamOptions{}
	fields, err := chatObject(body, "model", "messages", "stream", "stream_options", "harness_id", "routing")
	if err != nil || chatString(fields["model"], &req.ModelID) != nil || strings.TrimSpace(req.ModelID) == "" || len(req.ModelID) > 256 {
		return req, stream, bad
	}
	if raw, ok := fields["harness_id"]; ok {
		if chatString(raw, &req.HarnessID) != nil || req.HarnessID == "" || len(req.HarnessID) > 128 || strings.TrimSpace(req.HarnessID) != req.HarnessID || strings.ContainsFunc(req.HarnessID, unicode.IsControl) {
			return req, stream, bad
		}
	}
	if raw, ok := fields["routing"]; ok {
		if decodeChatRouting(raw, &req) != nil {
			return req, stream, bad
		}
	}
	if req.HarnessID == "auto" && (req.ModelID != "auto" || req.Domain == "" || req.Profile == "" || req.ContextTokens < 8192) {
		return req, stream, bad
	}
	if raw, ok := fields["stream"]; ok {
		if string(raw) != "true" && string(raw) != "false" {
			return req, stream, bad
		}
		stream.Enabled = string(raw) == "true"
	}
	if raw, ok := fields["stream_options"]; ok {
		if !stream.Enabled {
			return req, stream, bad
		}
		if string(raw) != "null" {
			options, err := chatObject(raw, "include_usage")
			if err != nil {
				return req, stream, bad
			}
			if include, ok := options["include_usage"]; ok {
				if string(include) != "true" && string(include) != "false" {
					return req, stream, bad
				}
				stream.IncludeUsage = string(include) == "true"
			}
		}
	}
	var messages []json.RawMessage
	if json.Unmarshal(fields["messages"], &messages) != nil || len(messages) == 0 || len(messages) > 256 {
		return req, stream, bad
	}
	for _, raw := range messages {
		fields, err := chatObject(raw, "role", "content")
		m := providers.Message{}
		if err != nil || chatString(fields["role"], &m.Role) != nil || chatString(fields["content"], &m.Content) != nil {
			return req, stream, bad
		}
		if m.Role != "system" && m.Role != "user" && m.Role != "assistant" {
			return req, stream, bad
		}
		req.Messages = append(req.Messages, m)
	}
	return req, stream, nil
}

func chatString(raw json.RawMessage, target *string) error {
	if len(raw) == 0 || raw[0] != '"' {
		return errors.New("string required")
	}
	return json.Unmarshal(raw, target)
}

// chatObject rejects duplicate, unknown and case-mismatched fields. Decoding
// into a Go struct alone would silently accept duplicates and case aliases.
func chatObject(body []byte, allowed ...string) (map[string]json.RawMessage, error) {
	bad := errors.New("invalid object")
	if !utf8.Valid(body) {
		return nil, bad
	}
	d := json.NewDecoder(strings.NewReader(string(body)))
	first, err := d.Token()
	if err != nil || first != json.Delim('{') {
		return nil, bad
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return nil, bad
		}
		valid := false
		for _, name := range allowed {
			valid = valid || name == key
		}
		if !valid || fields[key] != nil {
			return nil, bad
		}
		var raw json.RawMessage
		if d.Decode(&raw) != nil {
			return nil, bad
		}
		fields[key] = raw
	}
	if end, err := d.Token(); err != nil || end != json.Delim('}') {
		return nil, bad
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, bad
	}
	return fields, nil
}
