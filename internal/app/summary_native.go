package app

import (
	"bytes"
	"encoding/json"
	"io"
	"unicode/utf8"

	"github.com/ArronJablonowski/DarwinRouter/providers"
)

// Summary input must not silently collapse conflicting argument keys. Validate
// the original JSON before the shared decoded-string redaction path, retaining
// the unchanged source journal as the only provenance authority.
func nativeSummaryMessages(messages []providers.Message, secrets []string) ([]providers.Message, error) {
	for _, message := range messages {
		for _, call := range message.ToolCalls {
			if len(call.Arguments) > 1<<20 || !utf8.Valid(call.Arguments) {
				return nil, ErrAdmission
			}
			decoder := json.NewDecoder(bytes.NewReader(call.Arguments))
			decoder.UseNumber()
			value, err := codexHistoryJSON(decoder, 0)
			if _, object := value.(map[string]any); err != nil || !object {
				return nil, ErrAdmission
			}
			if _, err := decoder.Token(); err != io.EOF {
				return nil, ErrAdmission
			}
		}
	}
	return redactCodexHistoryMessages(messages, secrets)
}
