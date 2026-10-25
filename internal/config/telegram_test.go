package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func telegramFixture() Settings {
	s := Defaults()
	zero := 0.0
	s.Providers = []Provider{{ID: "local", Kind: "ollama", Endpoint: "http://127.0.0.1:11434"}}
	s.Models = []Model{{ID: "chat", Model: "chat", Provider: "local", Locality: "local", Capabilities: []string{"chat"}, ContextTokens: 8192, RAMBytes: 1024, EstimatedCost: &zero}}
	s.Telegram = Telegram{Enabled: true, TokenEnv: "NEXUS_TELEGRAM_BOT_TOKEN", AllowedUsers: []int64{123456789}, ModelID: "chat", StateDirectory: "/private/telegram"}
	return s
}
func TestTelegramConfigDisabledIdentityAndValidation(t *testing.T) {
	body, _ := json.Marshal(Defaults())
	if strings.Contains(string(body), `"telegram"`) {
		t.Fatal("default config identity changed")
	}
	s := telegramFixture()
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	body, err := s.RedactedJSON()
	if err != nil || strings.Contains(string(body), "123456789") || strings.Contains(string(body), "/private/telegram") {
		t.Fatal("private config leaked", err)
	}
	for _, change := range []func(*Settings){
		func(s *Settings) { s.Telegram.AllowedUsers = nil },
		func(s *Settings) { s.Telegram.AllowedUsers = []int64{-1} },
		func(s *Settings) { s.Telegram.AllowedUsers = []int64{1, 1} },
		func(s *Settings) { s.Telegram.StateDirectory = "relative" },
		func(s *Settings) { s.Telegram.StateDirectory = "/a/../b" },
		func(s *Settings) { s.Telegram.TokenEnv = "123:secret" },
		func(s *Settings) { s.Telegram.ModelID = "absent" },
		func(s *Settings) { s.Telemetry.Database = "/private/telegram/tasks.db" },
		func(s *Settings) { s.Models[0].Locality = "cloud" },
		func(s *Settings) { s.Models[0].Capabilities = []string{"vision"} },
		func(s *Settings) { cost := 1.0; s.Models[0].EstimatedCost = &cost },
	} {
		s := telegramFixture()
		change(&s)
		if s.Validate() == nil {
			t.Fatal("unsafe config accepted")
		}
	}
}
