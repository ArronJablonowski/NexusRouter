package evaluation

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/providers"
)

func TestReviewerStructuredSchemaPinnedAndEstimated(t *testing.T) {
	var estimated []byte
	estimatorCalls, providerCalls := 0, 0
	input := ReviewRequest{Domain: "creative", Requirements: "Write", Candidate: "untrusted schema override", Evidence: []ReviewEvidence{{ID: "test_record", Content: "No execution"}}}
	p := reviewProvider(func(_ context.Context, request providers.Request, emit func(providers.Chunk) error) error {
		providerCalls++
		if !bytes.Equal(request.JSONSchema, estimated) {
			t.Fatal("schema absent from estimation or mutated by estimator")
		}
		var schema map[string]any
		if json.Unmarshal(request.JSONSchema, &schema) != nil || schema["type"] != "object" || schema["additionalProperties"] != false {
			t.Fatal("schema not closed")
		}
		properties := schema["properties"].(map[string]any)
		for key, value := range map[string]string{"evaluator_id": "separate-reviewer", "rubric_version": reviewRubric, "domain": "creative"} {
			if !reflect.DeepEqual(properties[key].(map[string]any)["enum"], []any{value}) {
				t.Fatal("trusted metadata not pinned", key)
			}
		}
		if !reflect.DeepEqual(schema["required"], []any{"version", "evaluator_id", "rubric_version", "domain", "verdict", "confidence", "findings"}) {
			t.Fatal("root fields not required")
		}
		findings := properties["findings"].(map[string]any)
		item := findings["items"].(map[string]any)
		if findings["maxItems"] != float64(64) || item["additionalProperties"] != false || !reflect.DeepEqual(item["required"], []any{"summary", "evidence_refs"}) {
			t.Fatal("findings unconstrained")
		}
		refs := item["properties"].(map[string]any)["evidence_refs"].(map[string]any)
		if refs["minItems"] != float64(1) || refs["maxItems"] != float64(64) || !reflect.DeepEqual(refs["items"].(map[string]any)["enum"], []any{"requirements", "candidate", "test_record"}) {
			t.Fatal("references not pinned")
		}
		if strings.Contains(string(request.JSONSchema), input.Candidate) || len(request.Tools) != 0 {
			t.Fatal("candidate acquired schema authority")
		}
		return emit(providers.Chunk{Text: reviewOutput("abstain"), Done: true, FinishReason: "stop"})
	})
	v := reviewer(p)
	v.StructuredOutput = true
	v.ContextEstimator = reviewContextEstimator(func(_ context.Context, request providers.Request) (int, error) {
		estimatorCalls++
		if !json.Valid(request.JSONSchema) {
			t.Fatal("estimator did not see schema")
		}
		estimated = bytes.Clone(request.JSONSchema)
		request.JSONSchema[0] = '!'
		return 1, nil
	})
	if _, err := v.Review(context.Background(), input); err != nil || estimatorCalls != 1 || providerCalls != 1 {
		t.Fatalf("review=%v estimator=%d provider=%d", err, estimatorCalls, providerCalls)
	}
}

func TestReviewerStructuredSchemaOwnedAndOptIn(t *testing.T) {
	trusted := AuditContext{EvaluatorID: "reviewer", RubricVersion: reviewRubric, Domain: "code", AllowedEvidenceRefs: []string{"candidate"}}
	schema := reviewOutputSchema(trusted)
	before := bytes.Clone(schema)
	trusted.AllowedEvidenceRefs[0] = "forged"
	if !bytes.Equal(before, schema) || strings.Contains(string(schema), "forged") {
		t.Fatal("schema aliases caller")
	}
	p := reviewProvider(func(_ context.Context, request providers.Request, emit func(providers.Chunk) error) error {
		if request.JSONSchema != nil {
			t.Fatal("default HTTP contract changed")
		}
		return emit(providers.Chunk{Text: reviewOutput("abstain"), Done: true, FinishReason: "stop"})
	})
	if _, err := reviewer(p).Review(context.Background(), ReviewRequest{Domain: "creative", Requirements: "Write"}); err != nil {
		t.Fatal(err)
	}
}

func TestReviewerStructuredSchemaDoesNotReplaceParser(t *testing.T) {
	for _, output := range []string{"not JSON", strings.ReplaceAll(reviewOutput("accept"), "candidate", "forged"), strings.ReplaceAll(reviewOutput("accept"), "separate-reviewer", "forged")} {
		calls := 0
		p := reviewProvider(func(_ context.Context, request providers.Request, emit func(providers.Chunk) error) error {
			calls++
			if len(request.JSONSchema) == 0 {
				t.Fatal("missing schema")
			}
			return emit(providers.Chunk{Text: output, Done: true, FinishReason: "stop"})
		})
		v := reviewer(p)
		v.StructuredOutput = true
		result, err := v.Review(context.Background(), ReviewRequest{Domain: "creative", Requirements: "Write"})
		if err == nil || result.Audit.Verdict != "" || calls != 1 {
			t.Fatal("malformed output accepted or retried")
		}
	}
}
