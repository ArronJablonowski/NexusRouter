package providers

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"unicode/utf8"
)

const maxStreamBytes = 16 << 20

type callDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func readSSE(reader io.Reader, emit func(Chunk) error) error {
	scanner := bufio.NewScanner(io.LimitReader(reader, maxStreamBytes+1))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var data strings.Builder
	partial := false
	finished := false
	reason := ""
	total := 0
	usageSeen := false
	calls := map[int]*callDelta{}
	fail := func() error { return &Failure{Code: "incomplete_or_invalid_stream", Partial: partial} }
	consume := func() (bool, error) {
		if data.Len() == 0 {
			return false, nil
		}
		payload := strings.TrimSuffix(data.String(), "\n")
		data.Reset()
		if payload == "[DONE]" {
			if !finished {
				return false, fail()
			}
			indexes := []int{}
			for i := range calls {
				indexes = append(indexes, i)
			}
			sort.Ints(indexes)
			seen := map[string]bool{}
			for _, i := range indexes {
				c := calls[i]
				if c.ID == "" || seen[c.ID] || c.Function.Name == "" || !jsonObject([]byte(c.Function.Arguments)) {
					return false, fail()
				}
				seen[c.ID] = true
			}
			for _, i := range indexes {
				c := calls[i]
				if err := emit(Chunk{ToolCall: &ToolCall{c.ID, c.Function.Name, json.RawMessage(c.Function.Arguments)}}); err != nil {
					return false, err
				}
			}
			return true, emit(Chunk{Done: true, FinishReason: reason})
		}
		var chunk struct {
			Error   json.RawMessage `json:"error"`
			Choices []struct {
				Index int `json:"index"`
				Delta struct {
					Content   string      `json:"content"`
					Refusal   string      `json:"refusal"`
					ToolCalls []callDelta `json:"tool_calls"`
				} `json:"delta"`
				Finish *string `json:"finish_reason"`
			} `json:"choices"`
			Usage json.RawMessage `json:"usage"`
		}
		if !utf8.ValidString(payload) || !uniqueAccountingKeys([]byte(payload), "usage") || json.Unmarshal([]byte(payload), &chunk) != nil || (len(chunk.Error) > 0 && string(chunk.Error) != "null") || len(chunk.Choices) > 1 {
			return false, fail()
		}
		for _, choice := range chunk.Choices {
			if choice.Index != 0 || finished {
				return false, fail()
			}
			if choice.Delta.Refusal != "" {
				return false, &Failure{Code: "refusal", Partial: partial}
			}
			if choice.Delta.Content != "" {
				partial = true
				if err := emit(Chunk{Text: choice.Delta.Content}); err != nil {
					return false, err
				}
			}
			for _, d := range choice.Delta.ToolCalls {
				if d.Index < 0 || d.Index > 127 || (d.Type != "" && d.Type != "function") {
					return false, fail()
				}
				c := calls[d.Index]
				if c == nil {
					c = &callDelta{}
					calls[d.Index] = c
				}
				if d.ID != "" {
					if c.ID != "" && c.ID != d.ID {
						return false, fail()
					}
					c.ID = d.ID
				}
				c.Function.Name += d.Function.Name
				c.Function.Arguments += d.Function.Arguments
				partial = true
			}
			if choice.Finish != nil {
				finished = true
				reason = *choice.Finish
			}
		}
		if len(chunk.Usage) != 0 && !bytes.Equal(bytes.TrimSpace(chunk.Usage), []byte("null")) {
			usage, valid := parseSSEUsage(chunk.Usage)
			if usageSeen || !valid {
				return false, fail()
			}
			usageSeen = true
			if err := emit(Chunk{Usage: &usage}); err != nil {
				return false, err
			}
		}
		return false, nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		total += len(line) + 1
		if total > maxStreamBytes {
			return fail()
		}
		if line == "" {
			done, err := consume()
			if err != nil {
				return err
			}
			if done {
				return nil
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			data.WriteByte('\n')
		}
	}
	if scanner.Err() != nil {
		return fail()
	}
	done, err := consume()
	if err != nil {
		return err
	}
	if done {
		return nil
	}
	return fail()
}

// Missing counts are unknown, not zero. Decode token-by-token so duplicate
// accounting keys cannot silently overwrite one another. Detail objects are
// provider-specific and intentionally ignored rather than schema-restricted.
func parseSSEUsage(raw json.RawMessage) (Usage, bool) {
	if !uniqueAccountingKeys(raw, "prompt_tokens", "completion_tokens", "total_tokens") {
		return Usage{}, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return Usage{}, false
	}
	counts := map[string]int64{}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return Usage{}, false
		}
		name, ok := key.(string)
		if !ok {
			return Usage{}, false
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return Usage{}, false
		}
		if name != "prompt_tokens" && name != "completion_tokens" && name != "total_tokens" {
			continue
		}
		if _, duplicate := counts[name]; duplicate || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return Usage{}, false
		}
		var count int64
		if json.Unmarshal(value, &count) != nil || count < 0 {
			return Usage{}, false
		}
		counts[name] = count
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return Usage{}, false
	}
	if _, err = decoder.Token(); err != io.EOF {
		return Usage{}, false
	}
	input, hasInput := counts["prompt_tokens"]
	output, hasOutput := counts["completion_tokens"]
	if !hasInput || !hasOutput || input > math.MaxInt64-output {
		return Usage{}, false
	}
	if total, exists := counts["total_tokens"]; exists && total != input+output {
		return Usage{}, false
	}
	return Usage{InputTokens: input, OutputTokens: output}, true
}

func readOllama(reader io.Reader, emit func(Chunk) error) error {
	prefix := fmt.Sprintf("call_%x", rand.Text())
	calls := []ToolCall{}
	scanner := bufio.NewScanner(io.LimitReader(reader, maxStreamBytes+1))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	partial := false
	total := 0
	index := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		total += len(line) + 1
		var c struct {
			Error   string          `json:"error"`
			Done    bool            `json:"done"`
			Reason  string          `json:"done_reason"`
			Input   json.RawMessage `json:"prompt_eval_count"`
			Output  json.RawMessage `json:"eval_count"`
			Message struct {
				Content string `json:"content"`
				Calls   []struct {
					Function struct {
						Name      string          `json:"name"`
						Arguments json.RawMessage `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		}
		detail := ""
		switch {
		case total > maxStreamBytes:
			detail = "byte_limit"
		case !utf8.Valid(line):
			detail = "invalid_utf8"
		case !json.Valid(line):
			detail = "invalid_json"
		case !uniqueAccountingKeys(line, "prompt_eval_count", "eval_count"):
			detail = "invalid_accounting_keys"
		case json.Unmarshal(line, &c) != nil:
			detail = "invalid_json"
		case c.Error != "":
			detail = "upstream_error"
		}
		if detail != "" {
			return &Failure{Code: "invalid_stream", Partial: partial, StreamDetail: detail}
		}

		if c.Message.Content != "" {
			partial = true
			if err := emit(Chunk{Text: c.Message.Content}); err != nil {
				return err
			}
		}
		for _, call := range c.Message.Calls {
			if len(calls) >= 128 || call.Function.Name == "" || !jsonObject(call.Function.Arguments) {
				return &Failure{Code: "invalid_tool_call", Partial: partial, StreamDetail: "invalid_tool_arguments"}
			}
			index++
			partial = true
			calls = append(calls, ToolCall{fmt.Sprintf("%s_%d", prefix, index), call.Function.Name, call.Function.Arguments})
		}
		if c.Done {
			var usage *Usage
			if len(c.Input) != 0 || len(c.Output) != 0 {
				input, inputOK := parseUsageCount(c.Input)
				output, outputOK := parseUsageCount(c.Output)
				if !inputOK || !outputOK || input > math.MaxInt64-output {
					return &Failure{Code: "invalid_usage", Partial: partial, StreamDetail: "invalid_usage_counts"}
				}
				usage = &Usage{InputTokens: input, OutputTokens: output}
			}
			for _, call := range calls {
				if err := emit(Chunk{ToolCall: &call}); err != nil {
					return err
				}
			}
			if usage != nil {
				if err := emit(Chunk{Usage: usage}); err != nil {
					return err
				}
			}
			return emit(Chunk{Done: true, FinishReason: c.Reason})
		}
	}
	detail := "missing_done"
	if scanner.Err() != nil {
		detail = "stream_read_error"
	}
	return &Failure{Code: "incomplete_stream", Partial: partial, StreamDetail: detail}
}

func parseUsageCount(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return 0, false
	}
	var value int64
	if json.Unmarshal(raw, &value) != nil || value < 0 {
		return 0, false
	}
	return value, true
}

// encoding/json accepts case-insensitive struct field names and last-wins
// duplicate keys. Accounting fields need exact spelling and one occurrence,
// while unrelated provider extension fields remain forward-compatible.
func uniqueAccountingKeys(raw []byte, names ...string) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return false
	}
	seen := make(map[string]bool, len(names))
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return false
		}
		name, ok := key.(string)
		if !ok {
			return false
		}
		for _, canonical := range names {
			if strings.EqualFold(name, canonical) {
				if name != canonical || seen[canonical] {
					return false
				}
				seen[canonical] = true
			}
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return false
		}
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return false
	}
	_, err = decoder.Token()
	return err == io.EOF
}

func jsonObject(raw []byte) bool {
	var object map[string]json.RawMessage
	return json.Unmarshal(raw, &object) == nil && object != nil
}
