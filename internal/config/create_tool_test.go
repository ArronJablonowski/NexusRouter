package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCreateToolConfigOptIn(t *testing.T) {
	s := Defaults()
	body, _ := json.Marshal(s.Tools)
	if strings.Contains(string(body), "create_") {
		t.Fatal("default fingerprint changed")
	}
	s.Tools.CreateEnabled = true
	if s.Validate() == nil {
		t.Fatal("accepted missing roots")
	}
	s.Tools.Enabled = true
	s.Tools.ReadRoot = t.TempDir()
	s.Tools.CreateRoot = t.TempDir()
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	s.Tools.CreateRoot = "relative"
	if s.Validate() == nil {
		t.Fatal("accepted relative root")
	}
}
