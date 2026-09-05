package config

import "testing"

func TestSolLocalSmokeProfile(t *testing.T) {
	s, err := Load(Options{ProjectFile: "../../examples/sol-local-smoke.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Mode != "hybrid" || s.Workers.Max != 2 || s.Hardware.Concurrent != "1" || s.Workers.DelegateModel != "local-worker" || s.Workers.DelegateMaxCalls != 1 || s.Workers.DelegateReadTools || s.Tools.Enabled || s.Memory.Enabled || s.Skills.Enabled || s.Skills.AutoDraft || s.Skills.AutoActivate || s.Skills.Learning.Enabled || s.Evaluation.Judge || s.Telemetry.OTEL {
		t.Fatal("smoke profile enabled unintended capabilities")
	}
	if len(s.Models) != 2 || s.Models[0].ID != "coordinator" || s.Models[0].Model != "gpt-5.6-sol" || s.Models[0].Locality != "cloud" || s.Models[1].ID != "local-worker" || s.Models[1].Locality != "local" || s.Models[1].RAMBytes == 0 {
		t.Fatal("smoke model identities or resource admission changed")
	}
	if len(s.Providers) != 2 || s.Providers[0].APIKeyEnv != "OPENAI_API_KEY" || s.Providers[1].APIKeyEnv != "" {
		t.Fatal("credential isolation changed")
	}
}
