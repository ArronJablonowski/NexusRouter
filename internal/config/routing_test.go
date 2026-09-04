package config

import (
	"math"
	"testing"
)

func TestRoutingMetadataValidation(t *testing.T) {
	for _, kind := range []string{"negative_context", "negative_cost", "nan_cost", "failure_domain", "duplicate_route", "exploration", "concurrency"} {
		t.Run(kind, func(t *testing.T) {
			s := Defaults()
			zero := 0.0
			s.Providers = []Provider{{ID: "local", Kind: "ollama", Endpoint: "http://127.0.0.1:11434"}}
			s.Models = []Model{{ID: "chat", Model: "fixture", Provider: "local", Locality: "local", Capabilities: []string{"chat"}, ContextTokens: 8192, EstimatedCost: &zero}}
			if err := s.Validate(); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "negative_context":
				s.Models[0].ContextTokens = -1
			case "negative_cost":
				zero = -1
			case "nan_cost":
				zero = math.NaN()
			case "failure_domain":
				s.Models[0].FailureDomain = "bad/domain"
			case "duplicate_route":
				m := s.Models[0]
				m.ID = "alias"
				s.Models = append(s.Models, m)
			case "exploration":
				s.Routing.Exploration = .26
			case "concurrency":
				s.Hardware.Concurrent = "65"
			}
			if s.Validate() == nil {
				t.Fatal("unsafe routing metadata accepted")
			}
		})
	}
}
