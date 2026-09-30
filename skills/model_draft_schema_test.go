package skills

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/providers"
)

func TestModelDraftSchemaClosedAndOwned(t *testing.T) {
	first := modelDraftOutputSchema()
	var schema struct {
		Type       string                     `json:"type"`
		Additional bool                       `json:"additionalProperties"`
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if json.Unmarshal(first, &schema) != nil || schema.Type != "object" || schema.Additional || len(schema.Properties) != 8 {
		t.Fatal(string(first))
	}
	want := []string{"version", "description", "tags", "steps", "required_tools", "configuration", "risks", "validation_cases"}
	if !reflect.DeepEqual(schema.Required, want) {
		t.Fatal(schema.Required)
	}
	for _, name := range want {
		if len(schema.Properties[name]) == 0 {
			t.Fatal("missing property", name)
		}
	}
	var version struct {
		Type string `json:"type"`
		Enum []int  `json:"enum"`
	}
	if json.Unmarshal(schema.Properties["version"], &version) != nil || version.Type != "integer" || !reflect.DeepEqual(version.Enum, []int{1}) {
		t.Fatal("version unconstrained")
	}
	for _, private := range []string{"key", "source_sessions", "source_evidence", "provenance", "tools"} {
		if _, ok := schema.Properties[private]; ok {
			t.Fatal("host authority in schema", private)
		}
	}
	second := modelDraftOutputSchema()
	first[0] = 'x'
	if !json.Valid(second) || !bytes.Equal(second, modelDraftOutputSchema()) {
		t.Fatal("schema bytes shared")
	}
}

func TestModelGeneratorStructuredSchemaEstimatedAndIsolated(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		var estimated json.RawMessage
		var retained json.RawMessage
		calls := 0
		g := modelGeneratorFixture(skillGenerationProvider(func(_ context.Context, r providers.Request, emit func(providers.Chunk) error) error {
			calls++
			if enabled && !bytes.Equal(r.JSONSchema, estimated) || len(r.Tools) != 0 {
				t.Fatal("estimated/schema request mismatch")
			}
			if enabled && !bytes.Equal(r.JSONSchema, modelDraftOutputSchema()) {
				t.Fatal("estimator mutated actual schema")
			}
			if !enabled && r.JSONSchema != nil {
				t.Fatal("legacy default changed")
			}
			retained = r.JSONSchema
			return emit(providers.Chunk{Text: generatedSkillJSON, Done: true, FinishReason: "stop"})
		}))
		g.StructuredOutput = enabled
		g.ContextEstimator = skillGenerationEstimator(func(_ context.Context, r providers.Request) (int, error) {
			estimated = append(json.RawMessage(nil), r.JSONSchema...)
			if enabled {
				if !json.Valid(r.JSONSchema) {
					t.Fatal("schema absent during estimation")
				}
				r.JSONSchema[0] = 'x'
			} else if len(r.JSONSchema) != 0 && string(r.JSONSchema) != "null" {
				t.Fatal("opt-out estimator got schema")
			}
			return 1, nil
		})
		if _, err := g.Generate(context.Background(), sample().Key, learningExamples()); err != nil {
			t.Fatal(err)
		}
		if enabled {
			retained[0] = 'x'
		}
		if _, err := g.Generate(context.Background(), sample().Key, learningExamples()); err != nil || calls != 2 {
			t.Fatal("prior request corrupted next schema", err, calls)
		}
	}
}

func TestModelGeneratorStructuredSchemaCannotBypassParser(t *testing.T) {
	for _, output := range []string{
		`{}`, strings.Replace(generatedSkillJSON, `"description":"Run relevant checks"`, `"description":"  "`, 1),
		strings.Replace(generatedSkillJSON, `"version":1`, `"version":1,"version":1`, 1),
		strings.Replace(generatedSkillJSON, `"version":1`, `"version":1,"source_sessions":["forged"]`, 1),
		strings.Repeat("x", (64<<10)+1),
	} {
		g := modelGeneratorFixture(skillGenerationProvider(func(_ context.Context, r providers.Request, emit func(providers.Chunk) error) error {
			if len(r.JSONSchema) == 0 {
				t.Fatal("schema not supplied")
			}
			return emit(providers.Chunk{Text: output, Done: true, FinishReason: "stop"})
		}))
		g.StructuredOutput = true
		result, err := g.GenerateDetailed(context.Background(), sample().Key, learningExamples())
		if err == nil || result.Draft.Key != (Key{}) || result.Usage != nil {
			t.Fatal("provider schema bypassed host validation")
		}
	}
}

func TestModelDraftSchemaAbstentionIsRepresentableButRejected(t *testing.T) {
	const abstention = `{"version":1,"description":"","tags":[],"steps":[],"required_tools":[],"configuration":"","risks":[],"validation_cases":[]}`
	var schema struct {
		Properties map[string]map[string]any `json:"properties"`
	}
	if json.Unmarshal(modelDraftOutputSchema(), &schema) != nil {
		t.Fatal("invalid schema")
	}
	for _, name := range []string{"description", "configuration", "tags", "steps", "required_tools", "risks", "validation_cases"} {
		property := schema.Properties[name]
		if property["minLength"] != nil || property["minItems"] != nil {
			t.Fatal("schema forbids abstention", name)
		}
		want := "array"
		if name == "description" || name == "configuration" {
			want = "string"
		}
		if property["type"] != want {
			t.Fatal("wrong abstention shape", name)
		}
	}
	for _, enabled := range []bool{false, true} {
		g := modelGeneratorFixture(skillGenerationProvider(func(_ context.Context, r providers.Request, emit func(providers.Chunk) error) error {
			if !strings.Contains(r.Messages[0].Content, abstention) || strings.Contains(r.Messages[0].Content, "abstain with an empty object") {
				t.Fatal("prompt abstention contradicts schema")
			}
			return emit(providers.Chunk{Text: abstention, Done: true, FinishReason: "stop"})
		}))
		g.StructuredOutput = enabled
		result, err := g.GenerateDetailed(context.Background(), sample().Key, learningExamples())
		if err == nil || result.Draft.Key != (Key{}) || result.Usage != nil {
			t.Fatal("abstention became a qualified workflow")
		}
	}
}
