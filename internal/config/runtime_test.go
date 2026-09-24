package config

import (
	"fmt"
	"testing"
)

func TestRuntimeMaxTurnsDefaultsAndBounds(t *testing.T) {
	s := Defaults()
	if s.Runtime.MaxTurns != 8 || s.Runtime.AutoApprovedCompaction || s.Validate() != nil {
		t.Fatal(s.Runtime)
	}
	for _, value := range []int{-1, 0, 1, 8, 32, 96, 97} {
		t.Run(fmt.Sprint(value), func(t *testing.T) {
			s := Defaults()
			s.Tools.Enabled = false
			s.Runtime.MaxTurns = value
			if err := s.Validate(); (err == nil) != (value >= 1 && value <= 96) {
				t.Fatalf("turns %d error %v", value, err)
			}
		})
	}
}

func TestRuntimeApprovedCompactionLayeringIsStrict(t *testing.T) {
	s, err := Load(Options{ProjectFile: file(t, "runtime:\n  auto_use_approved_summary: false\n"), Env: map[string]string{"runtime.auto_use_approved_summary": "true"}, Flags: map[string]string{"runtime.auto_use_approved_summary": "false"}})
	if err != nil || s.Runtime.AutoApprovedCompaction {
		t.Fatal(s.Runtime, err)
	}
	for _, value := range []string{"not-bool", "1", "'true'", "null"} {
		if _, err := Load(Options{ProjectFile: file(t, "runtime:\n  auto_use_approved_summary: "+value+"\n")}); err == nil {
			t.Fatalf("accepted %s", value)
		}
	}
}

func TestRuntimeMaxTurnsLayeredAndStrict(t *testing.T) {
	s, err := Load(Options{ProjectFile: file(t, "runtime:\n  max_turns: 2\n"), Env: map[string]string{"runtime.max_turns": "3"}, Flags: map[string]string{"runtime.max_turns": "1"}})
	if err != nil || s.Runtime.MaxTurns != 1 {
		t.Fatal(s.Runtime, err)
	}
	for _, value := range []string{"0", "97", "1.0", "'8'", "true", "null", "9999999999999999999999999"} {
		if _, err := Load(Options{ProjectFile: file(t, "runtime:\n  max_turns: "+value+"\n")}); err == nil {
			t.Fatalf("accepted %s", value)
		}
	}
	for _, value := range []string{"1.5", "9999999999999999999999999"} {
		if _, err := Load(Options{Env: map[string]string{"runtime.max_turns": value}, Flags: map[string]string{"runtime.max_turns": "8"}}); err == nil {
			t.Fatalf("invalid shadowed env %s", value)
		}
	}
}
