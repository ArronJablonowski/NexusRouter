package providers

import (
	"strings"
	"testing"
)

func TestSSEUsageRequiresUnambiguousCounts(t *testing.T) {
	for _, raw := range []string{
		`{}`, `{"prompt_tokens":1}`, `{"completion_tokens":2}`,
		`{"prompt_tokens":null,"completion_tokens":2}`, `{"prompt_tokens":1,"completion_tokens":null}`,
		`{"prompt_tokens":1,"prompt_tokens":2,"completion_tokens":2}`,
		`{"prompt_tokens":1,"completion_tokens":2,"completion_tokens":3}`,
		`{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3,"total_tokens":3}`,
		`{"prompt_tokens":1,"completion_tokens":2,"total_tokens":null}`,
		`{"prompt_tokens":1,"completion_tokens":2,"total_tokens":4}`,
		`{"prompt_tokens":1,"Prompt_tokens":0,"completion_tokens":2}`,
		`{"prompt_tokens":1,"completion_tokens":2,"Completion_Tokens":0}`,
		`{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3,"TOTAL_TOKENS":3}`,
		`{"prompt_tokens":-1,"completion_tokens":2}`, `{"prompt_tokens":1,"completion_tokens":-2}`,
		`{"prompt_tokens":"1","completion_tokens":2}`, `{"prompt_tokens":1.0,"completion_tokens":2}`,
		`{"prompt_tokens":9223372036854775808,"completion_tokens":0}`,
		`{"prompt_tokens":9223372036854775807,"completion_tokens":1}`,
		`[]`, `true`, `42`,
	} {
		t.Run(raw, func(t *testing.T) {
			body := "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
				"data: {\"choices\":[],\"usage\":" + raw + "}\n\ndata: [DONE]\n\n"
			err := readSSE(strings.NewReader(body), func(c Chunk) error {
				if c.Usage != nil || c.Done {
					t.Error("invalid usage released or completion acknowledged")
				}
				return nil
			})
			if err == nil {
				t.Fatal("invalid usage accepted")
			}
		})
	}
}

func TestSSEUsageOptionalAndExplicitZero(t *testing.T) {
	for _, test := range []struct {
		name, field string
		want        *Usage
	}{
		{"absent", "", nil},
		{"null", `,"usage":null`, nil},
		{"zero", `,"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}`, &Usage{}},
		{"counts", `,"usage":{"prompt_tokens":3,"completion_tokens":2}`, &Usage{3, 2}},
		{"details", `,"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5,"prompt_tokens_details":{"cached_tokens":1},"completion_tokens_details":{"reasoning_tokens":1}}`, &Usage{3, 2}},
		{"max", `,"usage":{"prompt_tokens":9223372036854775807,"completion_tokens":0,"total_tokens":9223372036854775807}`, &Usage{9223372036854775807, 0}},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\n" +
				"data: {\"choices\":[]" + test.field + "}\n\ndata: [DONE]\n\n"
			var actual *Usage
			done, text := false, ""
			err := readSSE(strings.NewReader(body), func(c Chunk) error {
				if c.Usage != nil {
					actual = c.Usage
				}
				done = done || c.Done
				text += c.Text
				return nil
			})
			if err != nil || !done || text != "answer" || (actual == nil) != (test.want == nil) || (actual != nil && *actual != *test.want) {
				t.Fatalf("usage=%+v want=%+v done=%v text=%q err=%v", actual, test.want, done, text, err)
			}
		})
	}
}

func TestOllamaUsagePresence(t *testing.T) {
	for _, test := range []struct {
		name, field string
		valid       bool
		want        *Usage
	}{
		{"absent", "", true, nil},
		{"zero", `,"prompt_eval_count":0,"eval_count":0`, true, &Usage{}},
		{"counts", `,"prompt_eval_count":3,"eval_count":2`, true, &Usage{3, 2}},
		{"partial", `,"prompt_eval_count":3`, false, nil},
		{"null", `,"prompt_eval_count":null,"eval_count":2`, false, nil},
		{"both_null", `,"prompt_eval_count":null,"eval_count":null`, false, nil},
		{"string", `,"prompt_eval_count":"3","eval_count":2`, false, nil},
		{"negative", `,"prompt_eval_count":3,"eval_count":-2`, false, nil},
		{"overflow", `,"prompt_eval_count":9223372036854775807,"eval_count":1`, false, nil},
		{"duplicate_input", `,"prompt_eval_count":1,"prompt_eval_count":0,"eval_count":0`, false, nil},
		{"duplicate_output", `,"prompt_eval_count":0,"eval_count":1,"eval_count":0`, false, nil},
		{"alias_input", `,"Prompt_Eval_Count":0,"eval_count":0`, false, nil},
		{"alias_output", `,"prompt_eval_count":0,"Eval_Count":0`, false, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := `{"message":{"content":"answer"},"done":true,"done_reason":"stop"` + test.field + "}\n"
			var actual *Usage
			done := false
			err := readOllama(strings.NewReader(body), func(c Chunk) error {
				if c.Usage != nil {
					actual = c.Usage
				}
				done = done || c.Done
				return nil
			})
			if (err == nil) != test.valid || done != test.valid || (actual == nil) != (test.want == nil) || (actual != nil && *actual != *test.want) {
				t.Fatalf("usage=%+v want=%+v done=%v err=%v", actual, test.want, done, err)
			}
		})
	}
}

func TestSSEUsageEnvelopeRejectsDuplicateAndAliasedKeys(t *testing.T) {
	for _, fields := range []string{
		`"usage":null,"usage":{"prompt_tokens":0,"completion_tokens":0}`,
		`"usage":{"prompt_tokens":0,"completion_tokens":0},"usage":null`,
		`"Usage":{"prompt_tokens":0,"completion_tokens":0}`,
		`"usage":null,"USAGE":{"prompt_tokens":0,"completion_tokens":0}`,
	} {
		t.Run(fields, func(t *testing.T) {
			body := "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
				"data: {\"choices\":[]," + fields + "}\n\ndata: [DONE]\n\n"
			err := readSSE(strings.NewReader(body), func(c Chunk) error {
				if c.Usage != nil || c.Done {
					t.Error("ambiguous envelope released usage or completion")
				}
				return nil
			})
			if err == nil {
				t.Fatal("ambiguous envelope accepted")
			}
		})
	}
}
