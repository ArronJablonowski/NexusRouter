package branding

import "testing"

func TestCanonicalEnvironmentWinsIncludingEmpty(t *testing.T) {
	t.Setenv("DARWIN_API_TOKEN", "legacy")
	t.Setenv("NEXUS_API_TOKEN", "canonical")
	if got := Getenv("DARWIN_API_TOKEN"); got != "canonical" {
		t.Fatal(got)
	}
	t.Setenv("NEXUS_API_TOKEN", "")
	if got, ok := LookupEnv("DARWIN_API_TOKEN"); !ok || got != "" {
		t.Fatal("empty canonical value must not fall back")
	}
}
func TestLegacyEnvironmentRemainsSupported(t *testing.T) {
	t.Setenv("DARWIN_RENAME_TEST_ONLY", "legacy")
	if got := Getenv("DARWIN_RENAME_TEST_ONLY"); got != "legacy" {
		t.Fatal(got)
	}
}
