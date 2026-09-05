package config

import (
	"encoding/json"
	"testing"
)

func TestPressurePolicyDefaults(t *testing.T) {
	s, err := Load(Options{})
	if err != nil || s.Hardware.LocalPressurePolicy != "reject" || s.Hardware.LocalQueueTimeout != "30s" {
		t.Fatal(s.Hardware, err)
	}
}

func TestPressurePolicyLayers(t *testing.T) {
	options := Options{UserFile: file(t, "hardware:\n  local_pressure_policy: wait\n  local_queue_timeout: 1s\n")}
	assert := func(policy, timeout string) {
		t.Helper()
		s, err := Load(options)
		if err != nil || s.Hardware.LocalPressurePolicy != policy || s.Hardware.LocalQueueTimeout != timeout {
			t.Fatal(s.Hardware, err)
		}
	}
	assert("wait", "1s")
	options.ProjectFile = file(t, "hardware:\n  local_pressure_policy: reject\n  local_queue_timeout: 2s\n")
	assert("reject", "2s")
	options.Env = Environment([]string{"DARWIN__HARDWARE__LOCAL_PRESSURE_POLICY=wait", "DARWIN__HARDWARE__LOCAL_QUEUE_TIMEOUT=3s"})
	assert("wait", "3s")
	options.Flags = map[string]string{"hardware.local_pressure_policy": "reject", "hardware.local_queue_timeout": "4s"}
	assert("reject", "4s")
}

func TestPressurePolicyValidation(t *testing.T) {
	for _, policy := range []string{"reject", "wait"} {
		for _, timeout := range []string{"100ms", "0.1s", "30s", "5m", "300s"} {
			s := Defaults()
			s.Hardware.LocalPressurePolicy, s.Hardware.LocalQueueTimeout = policy, timeout
			if err := s.Validate(); err != nil {
				t.Fatal(policy, timeout, err)
			}
		}
		for _, timeout := range []string{"", "0", "0s", "99ms", "5m1ns", "-1s", "300", "nan", "1d", "999999999999999999999d"} {
			s := Defaults()
			s.Hardware.LocalPressurePolicy, s.Hardware.LocalQueueTimeout = policy, timeout
			if s.Validate() == nil {
				t.Fatal("accepted invalid timeout", policy, timeout)
			}
		}
	}
	for _, policy := range []string{"", "Wait", "queue", "wait ", " reject", "true", "0"} {
		s := Defaults()
		s.Hardware.LocalPressurePolicy = policy
		if s.Validate() == nil {
			t.Fatal("accepted invalid policy", policy)
		}
	}
}

func TestPressurePolicyStrictLoading(t *testing.T) {
	for _, field := range []string{"local_pressure_policy", "local_queue_timeout"} {
		for _, value := range []string{"null", "true", "42", "1.5", "[]", "{}", "''"} {
			if _, err := Load(Options{ProjectFile: file(t, "hardware:\n  "+field+": "+value+"\n")}); err == nil {
				t.Fatal("accepted invalid field type/value", field, value)
			}
		}
	}
	for _, body := range []string{"hardware:\n  local_pressure_polciy: wait\n", "hardware:\n  local_queue_timeout: 1s\n  local_queue_timeout: 2s\n"} {
		if _, err := Load(Options{ProjectFile: file(t, body)}); err == nil {
			t.Fatal("accepted malformed layer", body)
		}
	}
}

func TestPressurePolicyRejectsShadowedNonStringLayers(t *testing.T) {
	for _, field := range []string{"local_pressure_policy", "local_queue_timeout"} {
		for _, value := range []string{"42", "1.5", "true", "false"} {
			bad := file(t, "hardware:\n  "+field+": "+value+"\n")
			good := file(t, "hardware:\n  local_pressure_policy: wait\n  local_queue_timeout: 1s\n")
			for _, options := range []Options{
				{UserFile: bad, ProjectFile: good},
				{ProjectFile: bad, Env: map[string]string{"hardware.local_pressure_policy": "wait", "hardware.local_queue_timeout": "1s"}},
				{ProjectFile: bad, Flags: map[string]string{"hardware.local_pressure_policy": "wait", "hardware.local_queue_timeout": "1s"}},
			} {
				if _, err := Load(options); err == nil {
					t.Fatal("accepted shadowed nonstring field", field, value)
				}
			}
		}
	}
	// Literal override text keeps the schema's string tag; valid durations and
	// policy names remain supported after strict per-file checks.
	s, err := Load(Options{
		ProjectFile: file(t, "hardware:\n  local_pressure_policy: 'reject'\n  local_queue_timeout: '30s'\n"),
		Env:         Environment([]string{"DARWIN__HARDWARE__LOCAL_PRESSURE_POLICY=wait", "DARWIN__HARDWARE__LOCAL_QUEUE_TIMEOUT=100ms"}),
		Flags:       map[string]string{"hardware.local_queue_timeout": "5m"},
	})
	if err != nil || s.Hardware.LocalPressurePolicy != "wait" || s.Hardware.LocalQueueTimeout != "5m" {
		t.Fatal(s.Hardware, err)
	}
}

func TestPressurePolicyRedactedDisplayPreservesSettings(t *testing.T) {
	s := Defaults()
	s.Hardware.LocalPressurePolicy, s.Hardware.LocalQueueTimeout = "wait", "2m"
	before := s.Hardware
	data, err := s.RedactedJSON()
	if err != nil {
		t.Fatal(err)
	}
	var got Settings
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Hardware != before || s.Hardware != before {
		t.Fatal("redaction removed or mutated nonsensitive policy", got.Hardware, s.Hardware)
	}
}
