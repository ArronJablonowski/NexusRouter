package config

import (
	"encoding/json"
	"testing"
)

func TestOllamaThinkingSettingPreservesExplicitFalse(t *testing.T) {
	s := Defaults()
	s.Providers = []Provider{{ID: "local", Kind: "ollama", Endpoint: "http://127.0.0.1:11435"}}
	v := false
	s.Providers[0].Kind = "ollama"
	s.Providers[0].OllamaThink = &v
	if e := s.Validate(); e != nil {
		t.Fatal(e)
	}
	b, e := json.Marshal(s)
	if e != nil {
		t.Fatal(e)
	}
	var decoded Settings
	if e = json.Unmarshal(b, &decoded); e != nil || decoded.Providers[0].OllamaThink == nil || *decoded.Providers[0].OllamaThink {
		t.Fatal("false lost", e)
	}
	s.Providers[0].Kind = "openai_compatible"
	if e = s.Validate(); e == nil {
		t.Fatal("wrong protocol accepted")
	}
}
