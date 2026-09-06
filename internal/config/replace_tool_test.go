package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReplaceToolConfigOptInAndRedaction(t *testing.T) {
	s := Defaults()
	body, _ := json.Marshal(s.Tools)
	if strings.Contains(string(body), "replace_") {
		t.Fatal("default fingerprint changed")
	}
	s.Tools.ReplaceEnabled = true
	if s.Validate() == nil {
		t.Fatal("replace accepted without tools/root")
	}
	s.Tools.Enabled = true
	s.Tools.ReadRoot = t.TempDir()
	s.Tools.ReplaceRoot = t.TempDir()
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	redacted, err := s.RedactedJSON()
	if err != nil || strings.Contains(string(redacted), s.Tools.ReplaceRoot) {
		t.Fatal("replace root exposed", err)
	}
	for _, root := range []string{"relative", strings.Repeat("x", 4097), "/bad\x00path", "/bad" + string([]byte{255})} {
		s.Tools.ReplaceRoot = root
		if s.Validate() == nil {
			t.Fatal("invalid replace root accepted")
		}
	}
}
