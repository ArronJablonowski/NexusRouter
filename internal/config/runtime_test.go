package config

import (
	"fmt"
	"testing"
)

func TestRuntimeMaxTurnsDefaultsAndBounds(t *testing.T) {
	s := Defaults()
	if s.Runtime.MaxTurns != 8 || s.Validate() != nil {
		t.Fatal(s.Runtime)
	}
	for _, value := range []int{-1, 0, 1, 8, 32, 33} {
		t.Run(fmt.Sprint(value), func(t *testing.T) {
			s := Defaults()
			s.Tools.Enabled = false
			s.Runtime.MaxTurns = value
			if err := s.Validate(); (err == nil) != (value >= 1 && value <= 32) {
				t.Fatalf("turns %d error %v", value, err)
			}
		})
	}
}

func TestRuntimeMaxTurnsLayeredAndStrict(t *testing.T) {
	s, err := Load(Options{ProjectFile: file(t, "runtime:\n  max_turns: 2\n"), Env: map[string]string{"runtime.max_turns": "3"}, Flags: map[string]string{"runtime.max_turns": "1"}})
	if err != nil || s.Runtime.MaxTurns != 1 {
		t.Fatal(s.Runtime, err)
	}
	for _, value := range []string{"0", "33", "1.0", "'8'", "true", "null", "9999999999999999999999999"} {
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
