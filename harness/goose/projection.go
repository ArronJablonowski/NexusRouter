// Package goose validates the pinned native Goose text protocol. Projections are
// not execution evidence: actual completion must be verified by the host gateway.
package goose

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"github.com/ArronJablonowski/NexusRouter/harness/internal/wirejson"
	"math"
	"strings"
	"unicode/utf8"
)

const SupportedVersion = "1.52.0"
const MaxRecordBytes = 1 << 20
const MaxStreamBytes = 16 << 20
const MaxTextBytes = 4 << 20

var ErrProjection = errors.New("invalid Goose stream-json projection")

type Projection struct{ Provider, RequestedModel, Text string }

// ParseProjection accepts one text-only assistant message and one completion,
// ending at EOF after a successful reaped process. No normalized token counts
// are promoted to measured usage. Tools, notifications and errors fail closed.
func ParseProjection(body []byte, exitCode int, provider, model string) (Projection, error) {
	bad := func() (Projection, error) { return Projection{}, ErrProjection }
	if exitCode != 0 || !label(provider) || !label(model) || len(body) == 0 || len(body) > MaxStreamBytes || body[len(body)-1] != '\n' || !utf8.Valid(body) {
		return bad()
	}
	scan := bufio.NewScanner(bytes.NewReader(body))
	scan.Buffer(make([]byte, 4096), MaxRecordBytes)
	messageSeen, finished := false, false
	result := Projection{Provider: provider, RequestedModel: model}
	for scan.Scan() {
		line := scan.Bytes()
		if finished || !wirejson.Unique(line) {
			return bad()
		}
		var event struct {
			Type    string
			Message *struct {
				ID, Role string
				Content  []struct {
					Type string
					Text *string
				}
				Metadata struct {
					Inference *struct {
						Provider, RequestedModel string
						ResolvedModel            *string
					}
				}
			}
		}
		if json.Unmarshal(line, &event) != nil {
			return bad()
		}
		switch event.Type {
		case "message":
			m := event.Message
			if messageSeen || m == nil || m.Role != "assistant" || !label(m.ID) || m.Metadata.Inference == nil || m.Metadata.Inference.Provider != provider || m.Metadata.Inference.RequestedModel != model || len(m.Content) == 0 {
				return bad()
			}
			if r := m.Metadata.Inference.ResolvedModel; r != nil && *r != model {
				return bad()
			}
			var text strings.Builder
			for _, block := range m.Content {
				if block.Type != "text" || block.Text == nil || text.Len()+len(*block.Text) > MaxTextBytes {
					return bad()
				}
				text.WriteString(*block.Text)
			}
			if strings.TrimSpace(text.String()) == "" {
				return bad()
			}
			result.Text = text.String()
			messageSeen = true
		case "complete":
			if !messageSeen || event.Message != nil {
				return bad()
			}
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(line, &fields)
			if _, ok := fields["total_tokens"]; !ok {
				return bad()
			}
			for key, raw := range fields {
				if key == "type" {
					continue
				}
				switch key {
				case "total_tokens", "input_tokens", "output_tokens", "cache_read_input_tokens", "cache_write_input_tokens", "cost_usd":
				default:
					return bad()
				}
				if string(raw) == "null" {
					continue
				}
				var n float64
				if json.Unmarshal(raw, &n) != nil || n < 0 || math.IsInf(n, 0) || math.IsNaN(n) || (key != "cost_usd" && (math.Trunc(n) != n || n > math.MaxInt32)) {
					return bad()
				}
			}
			finished = true
		default:
			return bad()
		}
	}
	if scan.Err() != nil || !finished {
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
