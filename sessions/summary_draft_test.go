package sessions

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParseSummaryDraftAcceptsExactVersionedShape(t *testing.T) {
	raw := []byte(`{"version":1,"summary":{"decisions":["Use Go"],"pending_work":["Run tests"],"failures":["A fixture failed"],"artifacts":["main.go"],"requirements":["Preserve privacy"],"activity":["Read source"]}}`)
	got, err := ParseSummaryDraft(raw)
	want := Summary{Decisions: []string{"Use Go"}, PendingWork: []string{"Run tests"}, Failures: []string{"A fixture failed"}, Artifacts: []string{"main.go"}, Requirements: []string{"Preserve privacy"}, Activity: []string{"Read source"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal(got, err)
	}
	for _, field := range []string{"decisions", "pending_work", "failures", "artifacts", "requirements", "activity"} {
		if _, err := ParseSummaryDraft([]byte(`{"summary":{"` + field + `":["entry"]},"version":1}`)); err != nil {
			t.Fatal(field, err)
		}
	}
	if _, err := ParseSummaryDraft([]byte(" \n" + `{"version":1,"summary":{"decisions":[],"activity":["Unicode 日本語"]}}` + "\n")); err != nil {
		t.Fatal(err)
	}
	// Parsed strings and slices are detached from the input buffer.
	for i := range raw {
		raw[i] = 'x'
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("input buffer aliases parsed result")
	}
}

func TestParseSummaryDraftRejectsInvalidShapes(t *testing.T) {
	for _, raw := range []string{
		``, `null`, `[]`, `{}`, `{"version":1}`, `{"summary":{"activity":["entry"]}}`,
		`{"version":1,"summary":null}`, `{"version":1,"summary":[]}`, `{"version":1,"summary":{}}`,
		`{"version":1,"summary":{"decisions":[]}}`,
		`{"version":1,"summary":{"decisions":[""]}}`,
		`{"version":1,"summary":{"requirements":[" \t\n"]}}`,
		`{"version":1,"summary":{"activity":["\u2003"]}}`,
		`{"version":1,"summary":{"decisions":null}}`,
		`{"version":1,"summary":{"decisions":[null]}}`,
		`{"version":1,"summary":{"decisions":[1]}}`,
		`{"version":1,"summary":{"decisions":[true]}}`,
		`{"version":1,"summary":{"decisions":[{}]}}`,
		`{"version":1,"summary":{"decisions":[[]]}}`,
		`{"version":1,"summary":{"decisions":"entry"}}`,
		`{"version":1,"summary":{"decisions":{"0":"entry"}}}`,
		`{"version":1,"summary":{"decisions":["entry"]},"extra":true}`,
		`{"version":1,"summary":{"decisions":["entry"],"extra":true}}`,
		`{"Version":1,"summary":{"decisions":["entry"]}}`,
		`{"version":1,"Summary":{"decisions":["entry"]}}`,
		`{"version":1,"summary":{"Decisions":["entry"]}}`,
		`{"version":1,"summary":{"pendingWork":["entry"]}}`,
		`{"version":1,"version":1,"summary":{"decisions":["entry"]}}`,
		`{"version":1,"summary":{"decisions":["entry"]},"summary":{"decisions":["other"]}}`,
		`{"version":1,"summary":{"decisions":["entry"],"decisions":["other"]}}`,
		`{"version":1,"summary":{"decisions":["entry"],"\u0064ecisions":["other"]}}`,
		`{"version":1,"summary":{"decisions":["entry"]}} {}`,
		`{"version":1,"summary":{"decisions":["entry"]}} garbage`,
		"```json\n" + `{"version":1,"summary":{"decisions":["entry"]}}` + "\n```",
		`{"version":1,"summary":{"decisions":["entry",]}}`,
	} {
		if got, err := ParseSummaryDraft([]byte(raw)); !errors.Is(err, ErrHistory) || !reflect.DeepEqual(got, Summary{}) {
			t.Fatalf("accepted %s: %+v %v", raw, got, err)
		}
	}
	for _, version := range []string{"0", "2", "1.0", "1e0", `"1"`, "false", "true", "null", "[]", "{}"} {
		if _, err := ParseSummaryDraft([]byte(`{"version":` + version + `,"summary":{"decisions":["entry"]}}`)); !errors.Is(err, ErrHistory) {
			t.Fatal(version, err)
		}
	}
	if _, err := ParseSummaryDraft([]byte("{\"version\":1,\"summary\":{\"decisions\":[\"\xff\"]}}")); !errors.Is(err, ErrHistory) {
		t.Fatal("invalid UTF8 accepted", err)
	}
}

func TestParseSummaryDraftLimitsEveryCategoryAndRawBytes(t *testing.T) {
	// Decoded strings may expand under canonical JSON encoding; the shared
	// summary budget still applies even when raw generated text fits its limit.
	expanding := `{"version":1,"summary":{"activity":["` + strings.Repeat("<", 12000) + `"]}}`
	if _, err := ParseSummaryDraft([]byte(expanding)); !errors.Is(err, ErrHistory) {
		t.Fatal("serialized summary limit not enforced", err)
	}
	for _, field := range []string{"decisions", "pending_work", "failures", "artifacts", "requirements", "activity"} {
		for _, count := range []int{128, 129} {
			entries := make([]string, count)
			for i := range entries {
				entries[i] = "entry"
			}
			raw, _ := json.Marshal(map[string]any{"version": 1, "summary": map[string]any{field: entries}})
			_, err := ParseSummaryDraft(raw)
			if count == 128 && err != nil {
				t.Fatal(field, count, err)
			}
			if count == 129 && !errors.Is(err, ErrHistory) {
				t.Fatal(field, count, err)
			}
		}
	}
	raw := `{"version":1,"summary":{"decisions":["entry"]}}`
	for _, extra := range []int{0, 1} {
		padded := raw + strings.Repeat(" ", (64<<10)-len(raw)+extra)
		_, err := ParseSummaryDraft([]byte(padded))
		if extra == 0 && err != nil {
			t.Fatal("exact raw limit rejected", err)
		}
		if extra == 1 && !errors.Is(err, ErrHistory) {
			t.Fatal("raw limit not enforced", err)
		}
	}
}
