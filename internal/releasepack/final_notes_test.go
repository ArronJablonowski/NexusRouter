package releasepack

import (
	"bytes"
	"strings"
	"testing"
)

func TestFinalReleaseNotesBindCandidateIdentityAndTargets(t *testing.T) {
	template := []byte(releaseNotesTemplateH1 + "\nCandidate summary.\n")
	commit := strings.Repeat("a", 40)
	created := "2026-09-14T18:00:00Z"
	notes, err := renderFinalReleaseNotes(template, "1.0.0", commit, created)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range [][]byte{
		[]byte("# NexusRouter 1.0.0 release notes\n"),
		[]byte("- Release version: `1.0.0`\n"),
		[]byte("- Source commit: `" + commit + "`\n"),
		[]byte("- Release date (UTC): `" + created + "`\n"),
		[]byte("  - `darwin/amd64`\n"), []byte("  - `darwin/arm64`\n"),
		[]byte("  - `linux/amd64`\n"), []byte("  - `linux/arm64`\n"),
		[]byte("## Release contract and limitations\n"),
		[]byte("Candidate summary.\n"),
	} {
		if bytes.Count(notes, required) != 1 {
			t.Fatalf("expected one canonical field %q", required)
		}
	}
	base, err := finalReleaseNotesRecord(template, "1.0.0", commit, created)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func() ([]byte, string, string, string){
		"version": func() ([]byte, string, string, string) { return template, "1.0.1", commit, created },
		"commit":  func() ([]byte, string, string, string) { return template, "1.0.0", strings.Repeat("b", 40), created },
		"date":    func() ([]byte, string, string, string) { return template, "1.0.0", commit, "2026-09-15T18:00:00Z" },
		"body": func() ([]byte, string, string, string) {
			return append(append([]byte(nil), template...), []byte("Drift.\n")...), "1.0.0", commit, created
		},
	} {
		t.Run(name, func(t *testing.T) {
			changedTemplate, version, changedCommit, changedCreated := mutate()
			changed, changedErr := finalReleaseNotesRecord(changedTemplate, version, changedCommit, changedCreated)
			if changedErr != nil {
				t.Fatal(changedErr)
			}
			if changed == base {
				t.Fatal("candidate drift did not change final notes identity")
			}
		})
	}
}

func TestFinalReleaseNotesRejectInvalidTemplateOrIdentity(t *testing.T) {
	valid := []byte(releaseNotesTemplateH1 + "\nSummary.\n")
	commit := strings.Repeat("a", 40)
	for name, input := range map[string]struct {
		template                 []byte
		version, commit, created string
	}{
		"heading": {[]byte("# Other\n\nSummary.\n"), "1.0.0", commit, "2026-09-14T18:00:00Z"},
		"empty":   {[]byte(releaseNotesTemplateH1), "1.0.0", commit, "2026-09-14T18:00:00Z"},
		"version": {valid, "v1.0.0", commit, "2026-09-14T18:00:00Z"},
		"commit":  {valid, "1.0.0", "bad", "2026-09-14T18:00:00Z"},
		"date":    {valid, "1.0.0", commit, "2026-09-14T12:00:00-06:00"},
	} {
		t.Run(name, func(t *testing.T) {
			if notes, err := renderFinalReleaseNotes(input.template, input.version, input.commit, input.created); err == nil || notes != nil {
				t.Fatal("invalid final notes input accepted")
			}
		})
	}
}

func TestFinalReleaseNotesRejectDuplicateGeneratedIdentitySyntax(t *testing.T) {
	commit := strings.Repeat("a", 40)
	for _, duplicate := range []string{
		"# NexusRouter 1.0.0 release notes",
		"- Release version: `1.0.0`",
		"- Source commit: `" + commit + "`",
		"- Release date (UTC): `2026-09-14T18:00:00Z`",
		"- Supported artifact targets:",
		"  - `darwin/amd64`", "  - `darwin/arm64`",
		"  - `linux/amd64`", "  - `linux/arm64`",
		"## Release contract and limitations",
	} {
		t.Run(strings.NewReplacer(" ", "_", "`", "", "/", "_").Replace(duplicate), func(t *testing.T) {
			template := []byte(releaseNotesTemplateH1 + "\nSummary.\n\n" + duplicate + "\n")
			if notes, err := renderFinalReleaseNotes(template, "1.0.0", commit, "2026-09-14T18:00:00Z"); err == nil || notes != nil {
				t.Fatal("duplicate generated identity syntax accepted")
			}
		})
	}
}
