package providers

import (
	"errors"
	"strings"
	"testing"
)

func TestOllamaStreamDiagnosticsAreBoundedAndNeverReleaseFailedCalls(t *testing.T) {
	for _, tc := range []struct{ body, detail string }{
		{`{"error":"private secret body"}`, "upstream_error"},
		{`{"done":true,"eval_count":1,"eval_count":2}`, "invalid_accounting_keys"},
		{`{"private secret`, "invalid_json"},
		{`{"done":false}`, "missing_done"},
		{`{"message":{"tool_calls":[{"function":{"name":"save","arguments":"private secret"}}]}}`, "invalid_tool_arguments"},
		{`{"done":true,"prompt_eval_count":1,"eval_count":-1}`, "invalid_usage_counts"},
	} {
		t.Run(tc.detail, func(t *testing.T) {
			err := readOllama(strings.NewReader(tc.body), func(c Chunk) error {
				if c.Done || c.ToolCall != nil {
					t.Fatal("released malformed stream")
				}
				return nil
			})
			var f *Failure
			if !errors.As(err, &f) || f.SafeStreamDetail() != tc.detail || strings.Contains(err.Error(), "private secret") {
				t.Fatal(err)
			}
		})
	}
	if strings.Contains((&Failure{Code: "invalid_stream", StreamDetail: "private secret"}).Error(), "private secret") {
		t.Fatal("untrusted diagnostic leaked")
	}
}
