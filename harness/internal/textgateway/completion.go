package textgateway

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/providers"
)

const MaxRecordBytes = 1 << 20
const maxStreamBytes = 16 << 20

type Completion struct {
	// Usage is present only when the completed upstream stream reports both counts.
	Usage  *providers.Usage
	Text   string
	Stream []byte
}

// verifyCompletion buffers until a normal terminal, [DONE], and EOF. An upstream
// HTTP status or a harness envelope alone cannot establish completion. Only the
// canonicalized verified stream may be released to the native process.
func verifyCompletion(reader io.Reader, model string) (Completion, error) {
	bad := func() (Completion, error) { return Completion{}, ErrProjection }
	if !identifier(model) {
		return bad()
	}
	limited := &io.LimitedReader{R: reader, N: maxStreamBytes + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), MaxRecordBytes)
	var event, text, wire bytes.Buffer
	finished, done, usageSeen := false, false, false
	streamID := ""
	var measured *providers.Usage
	records := 0
	dispatch := func() bool {
		if event.Len() == 0 {
			return true
		}
		data := bytes.TrimSuffix(event.Bytes(), []byte("\n"))
		defer event.Reset()
		if done {
			return false
		}
		if string(data) == "[DONE]" {
			if !finished {
				return false
			}
			done = true
			wire.WriteString("data: [DONE]\n\n")
			return true
		}
		records++
		if records > 10000 || !utf8.Valid(data) || !uniqueJSON(data) {
			return false
		}
		var chunk struct {
			ID, Object, Model string
			Error             json.RawMessage
			Choices           []struct {
				Index        *int
				Delta        map[string]json.RawMessage
				FinishReason *string `json:"finish_reason"`
			}
			Usage json.RawMessage
		}
		if json.Unmarshal(data, &chunk) != nil || chunk.Model != model || chunk.Object != "chat.completion.chunk" || !identifier(chunk.ID) || len(chunk.Error) != 0 {
			return false
		}
		if streamID == "" {
			streamID = chunk.ID
		} else if streamID != chunk.ID {
			return false
		}
		hasUsage := len(chunk.Usage) > 0 && string(chunk.Usage) != "null"
		if hasUsage {
			if usageSeen || !validUsage(chunk.Usage) {
				return false
			}
			usageSeen = true
			measured = measuredUsage(chunk.Usage)
		}
		if len(chunk.Choices) == 0 {
			if !finished || !hasUsage {
				return false
			}
		} else {
			if len(chunk.Choices) != 1 || finished {
				return false
			}
			choice := chunk.Choices[0]
			if choice.Index == nil || *choice.Index != 0 || choice.Delta == nil {
				return false
			}
			for name, raw := range choice.Delta {
				switch name {
				case "role":
					var role string
					if json.Unmarshal(raw, &role) != nil || role != "assistant" {
						return false
					}
				case "content":
					if string(raw) == "null" {
						continue
					}
					var part string
					if json.Unmarshal(raw, &part) != nil || text.Len()+len(part) > MaxTextBytes {
						return false
					}
					text.WriteString(part)
				default:
					return false // tools, refusal, reasoning and other modalities
				}
			}
			if choice.FinishReason != nil {
				if *choice.FinishReason != "stop" {
					return false
				}
				finished = true
			}
		}
		// Canonical JSON prevents different downstream parsing of framing or keys.
		var fields map[string]json.RawMessage
		if json.Unmarshal(data, &fields) != nil {
			return false
		}
		canonical, err := json.Marshal(fields)
		if err != nil {
			return false
		}
		fmt.Fprintf(&wire, "data: %s\n\n", canonical)
		return wire.Len() <= maxStreamBytes
	}
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			if !dispatch() {
				return bad()
			}
			continue
		}
		if done {
			return bad()
		}
		if line[0] == ':' {
			continue
		}
		if !bytes.HasPrefix(line, []byte("data:")) {
			return bad()
		}
		data := bytes.TrimPrefix(line, []byte("data:"))
		data = bytes.TrimPrefix(data, []byte(" "))
		if event.Len()+len(data)+1 > MaxRecordBytes {
			return bad()
		}
		event.Write(data)
		event.WriteByte('\n')
	}
	if scanner.Err() != nil || limited.N <= 0 || event.Len() != 0 || !done || !finished || strings.TrimSpace(text.String()) == "" {
		return bad()
	}
	return Completion{Text: text.String(), Stream: wire.Bytes(), Usage: measured}, nil
}

func validUsage(body []byte) bool { return measuredUsage(body) != nil }

func measuredUsage(body []byte) *providers.Usage {
	var usage struct {
		Input  *int64 `json:"prompt_tokens"`
		Output *int64 `json:"completion_tokens"`
		Total  *int64 `json:"total_tokens"`
	}
	if json.Unmarshal(body, &usage) != nil || usage.Input == nil || usage.Output == nil || usage.Total == nil {
		return nil
	}
	for _, n := range []*int64{usage.Input, usage.Output, usage.Total} {
		if *n < 0 || *n > 1<<40 {
			return nil
		}
	}
	if *usage.Input+*usage.Output != *usage.Total {
		return nil
	}
	return &providers.Usage{InputTokens: *usage.Input, OutputTokens: *usage.Output}
}
