package textgateway

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/ArronJablonowski/NexusRouter/providers"
)

// gooseToolProjection changes only the child-visible tool namespace. Recheck
// the resulting stream and each call against canonical evidence before release.
// This never changes upstream context, schemas, arguments, IDs or journal data.
func gooseToolProjection(stream []byte, model string) ([]byte, error) {
	original, e := VerifyAgentCompletion(bytes.NewReader(stream), model)
	if e != nil {
		return nil, e
	}
	var output bytes.Buffer
	scan := bufio.NewScanner(bytes.NewReader(original.Stream))
	scan.Buffer(make([]byte, 4096), MaxRecordBytes)
	for scan.Scan() {
		line := scan.Bytes()
		if len(line) == 0 {
			continue
		}
		if bytes.Equal(line, []byte("data: [DONE]")) {
			output.WriteString("data: [DONE]\n\n")
			continue
		}
		var chunk map[string]json.RawMessage
		if !bytes.HasPrefix(line, []byte("data: ")) || json.Unmarshal(line[6:], &chunk) != nil {
			return nil, ErrProjection
		}
		var choices []map[string]json.RawMessage
		if json.Unmarshal(chunk["choices"], &choices) != nil {
			return nil, ErrProjection
		}
		for _, choice := range choices {
			var delta map[string]json.RawMessage
			if json.Unmarshal(choice["delta"], &delta) != nil {
				return nil, ErrProjection
			}
			if raw, ok := delta["tool_calls"]; ok {
				var calls []map[string]json.RawMessage
				if json.Unmarshal(raw, &calls) != nil {
					return nil, ErrProjection
				}
				for _, call := range calls {
					var fn map[string]json.RawMessage
					if json.Unmarshal(call["function"], &fn) != nil {
						return nil, ErrProjection
					}
					if raw, ok := fn["name"]; ok {
						var name string
						if json.Unmarshal(raw, &name) != nil || !agentToolName(name) {
							return nil, ErrProjection
						}
						fn["name"], _ = json.Marshal("nexus__" + name)
					}
					call["function"], _ = json.Marshal(fn)
				}
				delta["tool_calls"], _ = json.Marshal(calls)
			}
			choice["delta"], _ = json.Marshal(delta)
		}
		chunk["choices"], _ = json.Marshal(choices)
		raw, e := json.Marshal(chunk)
		if e != nil || len(raw)+6 >= MaxRecordBytes {
			return nil, ErrProjection
		}
		fmt.Fprintf(&output, "data: %s\n\n", raw)
		if output.Len() > maxStreamBytes {
			return nil, ErrProjection
		}
	}
	if scan.Err() != nil {
		return nil, ErrProjection
	}
	projected, e := VerifyAgentCompletion(bytes.NewReader(output.Bytes()), model)
	if e != nil || projected.Text != original.Text || !reflect.DeepEqual(projected.Usage, original.Usage) || len(projected.Calls) != len(original.Calls) {
		return nil, ErrProjection
	}
	want := make([]providers.ToolCall, len(original.Calls))
	copy(want, original.Calls)
	for i := range want {
		want[i].Name = "nexus__" + want[i].Name
	}
	if len(want) > 0 && !reflect.DeepEqual(want, projected.Calls) {
		return nil, ErrProjection
	}
	return output.Bytes(), nil
}
