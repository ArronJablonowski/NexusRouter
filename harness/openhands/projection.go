// Package openhands binds the native SDK text result to a bounded bridge protocol.
// A projection alone is not execution evidence; the provider gateway must also
// verify normal completion and matching text before an outcome can be committed.
package openhands

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/harness/internal/wirejson"
)

const SupportedVersion = "1.50.1"
const MaxProjectionBytes = 8 << 20
const MaxTextBytes = 4 << 20

var ErrProjection = errors.New("invalid OpenHands SDK projection")

type Projection struct{ Model, ResponseID, Text string }

// ParseProjection requires one complete JSON envelope, native FINISHED status,
// exactly one assistant text event, a matching requested model and exit zero.
// SDK completion status does not prove the provider's stop reason or identity.
func ParseProjection(body []byte, exitCode int, model string) (Projection, error) {
	bad := func() (Projection, error) { return Projection{}, ErrProjection }
	if exitCode != 0 || len(body) == 0 || len(body) > MaxProjectionBytes || !utf8.Valid(body) || !wirejson.Unique(body) || model == "" {
		return bad()
	}
	var envelope struct {
		Version string `json:"sdk_version"`
		Model   string `json:"model"`
		Status  string `json:"status"`
		Events  []struct {
			Kind       string `json:"kind"`
			Source     string `json:"source"`
			ResponseID string `json:"llm_response_id"`
			Message    struct {
				Role    string `json:"role"`
				Content []struct {
					Type string  `json:"type"`
					Text *string `json:"text"`
				} `json:"content"`
				ToolCalls json.RawMessage `json:"tool_calls"`
				Reasoning json.RawMessage `json:"reasoning_content"`
			} `json:"llm_message"`
		} `json:"events"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if dec.Decode(&envelope) != nil {
		return bad()
	}
	var extra any
	if dec.Decode(&extra) != io.EOF || envelope.Version != SupportedVersion || envelope.Model != model || envelope.Status != "finished" || len(envelope.Events) != 1 {
		return bad()
	}
	e := envelope.Events[0]
	if e.Kind != "MessageEvent" || e.Source != "agent" || e.Message.Role != "assistant" || e.ResponseID == "" || len(e.ResponseID) > 256 || len(e.Message.Content) == 0 {
		return bad()
	}
	if string(e.Message.ToolCalls) != "null" || string(e.Message.Reasoning) != "null" {
		return bad()
	}
	var text strings.Builder
	for _, c := range e.Message.Content {
		if c.Type != "text" || c.Text == nil || text.Len()+len(*c.Text) > MaxTextBytes {
			return bad()
		}
		text.WriteString(*c.Text)
	}
	if strings.TrimSpace(text.String()) == "" {
		return bad()
	}
	return Projection{Model: model, ResponseID: e.ResponseID, Text: text.String()}, nil
}
