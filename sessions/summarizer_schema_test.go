package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/providers"
)

const emptySummaryProposal = `{"version":1,"summary":{"decisions":[],"requirements":[],"pending_work":[],"failures":[],"artifacts":[],"activity":[]}}`

func TestSummarySchemaClosedOwnedAndAllowsAbstention(t *testing.T) {
	type object struct {
		Type       string                     `json:"type"`
		Additional *bool                      `json:"additionalProperties"`
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	first := summaryOutputSchema()
	var outer, inner object
	if json.Unmarshal(first, &outer) != nil || outer.Type != "object" || outer.Additional == nil || *outer.Additional || len(outer.Properties) != 2 || !reflect.DeepEqual(outer.Required, []string{"version", "summary"}) {
		t.Fatal("outer schema is not closed", string(first))
	}
	var version struct {
		Type string `json:"type"`
		Enum []int  `json:"enum"`
	}
	if json.Unmarshal(outer.Properties["version"], &version) != nil || version.Type != "integer" || !reflect.DeepEqual(version.Enum, []int{1}) {
		t.Fatal("unconstrained version")
	}
	want := []string{"decisions", "requirements", "pending_work", "failures", "artifacts", "activity"}
	if json.Unmarshal(outer.Properties["summary"], &inner) != nil || inner.Type != "object" || inner.Additional == nil || *inner.Additional || len(inner.Properties) != 6 || !reflect.DeepEqual(inner.Required, want) {
		t.Fatal("summary schema is not closed")
	}
	for _, field := range want {
		var property map[string]any
		if json.Unmarshal(inner.Properties[field], &property) != nil || property["type"] != "array" || property["minItems"] != nil || property["maxItems"] != float64(128) {
			t.Fatal("abstention forbidden or array unbounded", field)
		}
		items, ok := property["items"].(map[string]any)
		if !ok || items["type"] != "string" {
			t.Fatal("non-string items")
		}
	}
	second := summaryOutputSchema()
	first[0] = 'x'
	if !json.Valid(second) || !bytes.Equal(second, summaryOutputSchema()) {
		t.Fatal("schema bytes shared")
	}
}

func TestSummarizerStructuredSchemaEstimatedAndIsolated(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		var estimated, retained json.RawMessage
		calls := 0
		s := summarySettings(summaryProvider(func(_ context.Context, r providers.Request, emit func(providers.Chunk) error) error {
			calls++
			if len(r.Tools) != 0 || enabled && (!bytes.Equal(r.JSONSchema, estimated) || !bytes.Equal(r.JSONSchema, summaryOutputSchema())) {
				t.Fatal("schema estimate mismatch or mutation")
			}
			if !enabled && r.JSONSchema != nil {
				t.Fatal("default changed")
			}
			retained = r.JSONSchema
			// Optional categories remain accepted by the authoritative host parser.
			return emit(providers.Chunk{Text: summaryResponse, Done: true, FinishReason: "stop"})
		}))
		s.StructuredOutput = enabled
		s.ContextEstimator = summaryEstimator(func(_ context.Context, r providers.Request) (int, error) {
			estimated = append(json.RawMessage(nil), r.JSONSchema...)
			if enabled {
				if !json.Valid(r.JSONSchema) {
					t.Fatal("schema absent at estimation")
				}
				r.JSONSchema[0] = 'x'
			} else if len(r.JSONSchema) != 0 && string(r.JSONSchema) != "null" {
				t.Fatal("default estimator schema changed")
			}
			return 1, nil
		})
		if _, err := s.Draft(context.Background(), summarySource(), 2); err != nil {
			t.Fatal(err)
		}
		if enabled {
			retained[0] = 'x'
		}
		if _, err := s.Draft(context.Background(), summarySource(), 2); err != nil || calls != 2 {
			t.Fatal("prior request changed next schema", err)
		}
	}
}

func TestSummarizerStructuredSchemaCannotBypassHostParser(t *testing.T) {
	for _, output := range []string{
		emptySummaryProposal,
		`{"version":1,"summary":{"decisions":[" "]}}`,
		strings.Replace(summaryResponse, `"version":1`, `"version":1,"version":1`, 1),
		strings.Replace(summaryResponse, `"version":1`, `"version":1,"source_digest":"forged"`, 1),
		`{"version":1,"summary":{"decisions":["ok"],"tools":[]}}`,
		strings.Repeat("x", (64<<10)+1),
	} {
		for _, enabled := range []bool{false, true} {
			s := summarySettings(summaryProvider(func(_ context.Context, r providers.Request, emit func(providers.Chunk) error) error {
				if !strings.Contains(r.Messages[0].Content, emptySummaryProposal) {
					t.Fatal("prompt/schema abstention mismatch")
				}
				return emit(providers.Chunk{Text: output, Done: true, FinishReason: "stop"})
			}))
			s.StructuredOutput = enabled
			draft, err := s.Draft(context.Background(), summarySource(), 2)
			if err == nil || draft.Checkpoint != nil || draft.SourceDigest != "" {
				t.Fatal("provider schema bypassed host validation")
			}
		}
	}
}
