package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMemoryJSONUnicodeRejectsUnpairedSurrogates(t *testing.T) {
	for _, body := range []string{
		`{"id":"\ud800"}`, `{"id":"\udfff"}`, `{"\ud800":"value"}`,
		`{"id":"\ud800\ud800"}`, `{"id":"\udc00\ud800"}`, `{"id":"\ud800\u0041"}`,
		`{"id":"\ud800x"}`, `{"id":"\ud800\\udc00"}`, `{"id":"\ud800","other":"\udc00"}`,
		`{"nested":[{"content":"\uD800"}]}`, `{"id":"\ud83d\ude00\ud800"}`,
	} {
		if !json.Valid([]byte(body)) {
			t.Fatal("fixture not syntactically valid JSON")
		}
		if memoryJSONUnicodeValid([]byte(body)) {
			t.Fatalf("lossy Unicode admitted: %s", body)
		}
	}
}

func TestMemoryJSONUnicodeAllowsPairsAndLiteralEscapes(t *testing.T) {
	for _, body := range []string{
		`{"id":"\ud83d\ude00"}`, `{"id":"\uD800\uDC00"}`, `{"id":"\udbff\udfff"}`,
		`{"id":"\\ud800"}`, `{"id":"\\\"\\ud800"}`, `{"id":"\u005cud800"}`,
		`{"id":"世界😀�"}`, `{"id":"\ufffd"}`, `{"id":"\ud7ff\ue000"}`,
		`{"\ud83d\ude00":["\n\t\r",null,123,true]}`, `{"id":"\ud83d\ude00\ud83d\ude01"}`,
	} {
		if !memoryJSONUnicodeValid([]byte(body)) {
			t.Fatalf("lossless Unicode rejected: %s", body)
		}
	}
}

func TestMemoryJSONUnicodeBoundsAndMalformedInput(t *testing.T) {
	for _, body := range [][]byte{nil, []byte(`{"id":"\u"}`), []byte(`{"id":"\ud80z"}`), []byte(`{"id":"unterminated}`), []byte(`{} {}`), {'"', 255, '"'}} {
		if memoryJSONUnicodeValid(body) {
			t.Fatal("malformed encoding admitted")
		}
	}
	boundary := []byte(`"` + strings.Repeat("x", memoryBodyLimit-2) + `"`)
	if !memoryJSONUnicodeValid(boundary) {
		t.Fatal("exact bound rejected")
	}
	tooLarge := []byte(`"` + strings.Repeat("x", memoryBodyLimit-1) + `"`)
	if memoryJSONUnicodeValid(tooLarge) {
		t.Fatal("oversized encoding admitted")
	}
}
