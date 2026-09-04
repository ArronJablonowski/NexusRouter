package providers

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
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
			Usage *struct {
				Input  int64 `json:"prompt_tokens"`
				Output int64 `json:"completion_tokens"`
			} `json:"usage"`
		}
		if !utf8.ValidString(payload) || json.Unmarshal([]byte(payload), &chunk) != nil || (len(chunk.Error) > 0 && string(chunk.Error) != "null") || len(chunk.Choices) > 1 {
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
		if chunk.Usage != nil {
			if usageSeen || chunk.Usage.Input < 0 || chunk.Usage.Output < 0 {
				return false, fail()
			}
			usageSeen = true
			if err := emit(Chunk{Usage: &Usage{chunk.Usage.Input, chunk.Usage.Output}}); err != nil {
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
			Error   string `json:"error"`
			Done    bool   `json:"done"`
			Reason  string `json:"done_reason"`
			Input   int64  `json:"prompt_eval_count"`
			Output  int64  `json:"eval_count"`
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
		if total > maxStreamBytes || !utf8.Valid(line) || json.Unmarshal(line, &c) != nil || c.Error != "" {
			return &Failure{Code: "invalid_stream", Partial: partial}
		}
		if c.Message.Content != "" {
			partial = true
			if err := emit(Chunk{Text: c.Message.Content}); err != nil {
				return err
			}
		}
		for _, call := range c.Message.Calls {
			if len(calls) >= 128 || call.Function.Name == "" || !jsonObject(call.Function.Arguments) {
				return &Failure{Code: "invalid_tool_call", Partial: partial}
			}
			index++
			partial = true
			calls = append(calls, ToolCall{fmt.Sprintf("%s_%d", prefix, index), call.Function.Name, call.Function.Arguments})
		}
		if c.Done {
			if c.Input < 0 || c.Output < 0 {
				return &Failure{Code: "invalid_usage", Partial: partial}
			}
			for _, call := range calls {
				if err := emit(Chunk{ToolCall: &call}); err != nil {
					return err
				}
			}
			if err := emit(Chunk{Usage: &Usage{c.Input, c.Output}}); err != nil {
				return err
			}
			return emit(Chunk{Done: true, FinishReason: c.Reason})
		}
	}
	return &Failure{Code: "incomplete_stream", Partial: partial}
}

func jsonObject(raw []byte) bool {
	var object map[string]json.RawMessage
	return json.Unmarshal(raw, &object) == nil && object != nil
}
