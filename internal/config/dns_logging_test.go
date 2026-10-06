package config

import "testing"

func TestDNSLoggingLevels(t *testing.T) {
	for _, level := range []string{"", "managed", "full"} {
		s := Defaults()
		s.Telemetry.DNSLogging = level
		if s.Validate() != nil {
			t.Fatalf("level %q rejected", level)
		}
		if (s.DNSAudit().Path == "") != (level == "") {
			t.Fatal("audit enable mismatch")
		}
	}
	s := Defaults()
	s.Telemetry.DNSLogging = "invalid"
	if s.Validate() == nil {
		t.Fatal("unknown level accepted")
	}
}

func TestDNSLoggingSettingsRoundTrip(t *testing.T) {
	path := file(t, "version: 1\nskills:\n  enabled: false\n")
	for _, level := range []string{"managed", "full", ""} {
		next, digest, err := ReadProjectToolAccess(path)
		if err != nil {
			t.Fatal(err)
		}
		next.DNSLogging = level
		saved, _, err := UpdateProjectToolAccess(path, digest, next)
		if err != nil || saved.DNSLogging != level {
			t.Fatal(saved, err)
		}
		settings, err := Load(Options{ProjectFile: path})
		if err != nil || settings.Telemetry.DNSLogging != level {
			t.Fatal(err)
		}
	}
}
