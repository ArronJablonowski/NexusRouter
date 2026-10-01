// Package hermes implements the pinned Hermes stream-json boundary. Its init
// model is configured intent, not actual provider identity. A projection alone
// is never sufficient to accept an execution or to assign quality feedback.
package hermes

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/harness/internal/wirejson"
)

const SupportedVersion = "0.21.5+4983.g6633626"
const SupportedRevision = "663362680b6ffa4fbffeb58f6682564239a1953b"
const MaxRecordBytes = 1 << 20
const MaxStreamBytes = 16 << 20
const MaxTextBytes = 4 << 20

var ErrProjection = errors.New("invalid Hermes stream-json projection")

// Projection describes native-reported text only. No provider attribution or
// measured token counts are synthesized from Hermes's normalized envelope.
type Projection struct{ ConfiguredModel, SessionID, Text string }

// ParseProjection requires a reaped process's exit code, one init, optional text
// deltas and exactly one terminal result followed by EOF. No tools are supported
// by this first protocol boundary. A future runner must independently bind the
// policy-gateway response and verify admission, isolation and process cleanup.
func ParseProjection(body []byte, exitCode int, model string) (Projection, error) {
	bad := func() (Projection, error) { return Projection{}, ErrProjection }
	if exitCode != 0 || !label(model) || len(body) == 0 || len(body) > MaxStreamBytes || body[len(body)-1] != '\n' || !utf8.Valid(body) {
		return bad()
	}
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 4096), MaxRecordBytes)
	initialized, finished := false, false
	hasDelta := false
	initSession := ""
	var text strings.Builder
	var result Projection
	records := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		records++
		if records > 10000 || finished || !wirejson.Unique(line) {
			return bad()
		}
		var event struct {
			Type, Subtype, Model string
			Text                 *string
			SessionID            string `json:"session_id"`
			ExitCode             *int   `json:"exit_code"`
			DurationMS           *int64 `json:"duration_ms"`
			Timestamp            *int64
			Error                json.RawMessage
			Tokens               *struct {
				Input, Output, Total *int64
				CacheRead            *int64 `json:"cache_read"`
				CacheWrite           *int64 `json:"cache_write"`
			}
		}
		if json.Unmarshal(line, &event) != nil || event.Timestamp == nil || *event.Timestamp < 0 || len(event.Error) != 0 {
			return bad()
		}
		switch event.Type {
		case "system":
			if initialized || event.Subtype != "init" || event.Model != model || (event.SessionID != "" && !label(event.SessionID)) {
				return bad()
			}
			initialized = true
			initSession = event.SessionID
		case "text":
			if !initialized || event.Text == nil || text.Len()+len(*event.Text) > MaxTextBytes {
				return bad()
			}
			hasDelta = true
			text.WriteString(*event.Text)
		case "result":
			if !initialized || event.Text == nil || strings.TrimSpace(*event.Text) == "" || len(*event.Text) > MaxTextBytes || !label(event.SessionID) || (initSession != "" && event.SessionID != initSession) || event.ExitCode == nil || *event.ExitCode != 0 || event.DurationMS == nil || *event.DurationMS < 0 || event.Tokens == nil {
				return bad()
			}
			for _, n := range []*int64{event.Tokens.Input, event.Tokens.Output, event.Tokens.Total, event.Tokens.CacheRead, event.Tokens.CacheWrite} {
				if n == nil || *n < 0 || *n > 1<<40 {
					return bad()
				}
			}
			// Some runtimes return final text without streaming. If deltas exist,
			// they must agree exactly rather than silently hiding another answer.
			if hasDelta && text.String() != *event.Text {
				return bad()
			}
			finished = true
			result = Projection{ConfiguredModel: model, SessionID: event.SessionID, Text: *event.Text}
		default:
			return bad()
		}
	}
	if scanner.Err() != nil || !finished {
		return bad()
	}
	return result, nil
}

func label(s string) bool {
	if s == "" || len(s) > 256 || !utf8.ValidString(s) || strings.TrimSpace(s) != s {
		return false
	}
	for _, r := range s {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}
