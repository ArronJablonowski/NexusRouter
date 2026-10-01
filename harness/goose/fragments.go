package goose

import (
	"bufio"
	"bytes"
	"encoding/json"
	"reflect"

	"github.com/ArronJablonowski/NexusRouter/harness/internal/wirejson"
)

// Goose emits adjacent assistant deltas with the same message ID. Join only
// matching envelopes; the projection validators still bind all content to the
// host transcript and reject duplicated, omitted or reordered output.
func joinAssistantFragments(body []byte) ([]byte, error) {
	if len(body) == 0 || len(body) > MaxStreamBytes || body[len(body)-1] != '\n' {
		return nil, ErrProjection
	}
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 4096), MaxRecordBytes)
	var out bytes.Buffer
	var pending map[string]any
	flush := func() error {
		if pending == nil {
			return nil
		}
		b, e := json.Marshal(pending)
		if e != nil || len(b) > MaxRecordBytes {
			return ErrProjection
		}
		out.Write(b)
		out.WriteByte('\n')
		pending = nil
		return nil
	}
	for scanner.Scan() {
		line := scanner.Bytes()
		if !wirejson.Unique(line) {
			return nil, ErrProjection
		}
		var event map[string]any
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.UseNumber()
		if dec.Decode(&event) != nil || event == nil {
			return nil, ErrProjection
		}
		message, ok := event["message"].(map[string]any)
		eligible := ok && event["type"] == "message" && message["role"] == "assistant"
		if eligible && pending != nil {
			old := pending["message"].(map[string]any)
			id, valid := message["id"].(string)
			if valid && id != "" && id == old["id"] {
				a, aok := old["content"].([]any)
				b, bok := message["content"].([]any)
				if !aok || !bok {
					return nil, ErrProjection
				}
				delete(old, "content")
				delete(message, "content")
				// Native timestamps may tick between deltas; they are not provenance.
				oldTime, newTime := old["created"], message["created"]
				delete(old, "created")
				delete(message, "created")
				same := reflect.DeepEqual(pending, event)
				old["created"], message["created"] = oldTime, newTime
				old["content"], message["content"] = a, b
				if !same {
					return nil, ErrProjection
				}
				old["content"] = append(a, b...)
				continue
			}
		}
		if flush() != nil {
			return nil, ErrProjection
		}
		if eligible {
			pending = event
		} else {
			out.Write(line)
			out.WriteByte('\n')
		}
	}
	if scanner.Err() != nil || flush() != nil || out.Len() > MaxStreamBytes {
		return nil, ErrProjection
	}
	return out.Bytes(), nil
}
