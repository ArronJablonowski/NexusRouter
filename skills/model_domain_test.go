package skills

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/providers"
)

func TestModelGeneratorDerivesDiscoverableDomain(t *testing.T) {
	full := make([]string, 4096)
	for i := range full {
		full[i] = "tag"
	}
	fullWithDomain := append([]string(nil), full...)
	fullWithDomain[0] = "coding"
	for _, tc := range []struct {
		name       string
		tags, want []string
		invalid    bool
	}{
		{"omitted", nil, []string{"coding"}, false},
		{"present", []string{"coding"}, []string{"coding"}, false},
		{"additional", []string{"go"}, []string{"go", "coding"}, false},
		{"full-missing-domain", full, nil, true},
		{"full-including-domain", fullWithDomain, fullWithDomain, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tags := tc.tags
			if tags == nil {
				tags = []string{}
			}
			encoded, err := json.Marshal(tags)
			if err != nil {
				t.Fatal(err)
			}
			body := strings.Replace(generatedSkillJSON, `"tags":[]`, `"tags":`+string(encoded), 1)
			g := modelGeneratorFixture(skillGenerationProvider(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
				return emit(providers.Chunk{Text: body, Done: true, FinishReason: "stop"})
			}))
			draft, err := g.Generate(context.Background(), sample().Key, learningExamples())
			if tc.invalid {
				if err != ErrValidation {
					t.Fatal("accepted oversized discovery metadata", err)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(draft.Tags, tc.want) {
				t.Fatal(draft.Tags, err)
			}
		})
	}
}
