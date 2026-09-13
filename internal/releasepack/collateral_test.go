package releasepack

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

var collateralNames = []string{installName, licenseName, releaseNotesName, sbomName, noticeName, configName, "darwin"}

func TestCollateralLoadAndSevenMemberContract(t *testing.T) {
	source := collateralSourceFixture(t)
	shared, err := loadCollateral(source)
	if err != nil {
		t.Fatal(err)
	}
	notice := signingNoticeFixture("linux", "amd64")
	binary := signingBinaryFixture(t, "linux", "amd64")
	sbom := signingSBOMFixture(t, "1.0.0", strings.Repeat("a", 40), "linux", "amd64", binary)
	entries, metadata, err := releaseEntries(shared, notice, sbom, binary)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 7 || len(metadata) != 7 || len(archiveContract) != 7 {
		t.Fatalf("release member count: entries=%d metadata=%d contract=%d", len(entries), len(metadata), len(archiveContract))
	}
	for i, contract := range archiveContract {
		if entries[i].Name != collateralNames[i] || contract.name != collateralNames[i] ||
			metadata[i].Name != collateralNames[i] || metadata[i].Mode != contract.mode ||
			metadata[i].Size != int64(len(entries[i].Data)) || metadata[i].Size < 1 || metadata[i].Size > contract.max {
			t.Fatalf("member %d contract mismatch: %#v %#v", i, contract, metadata[i])
		}
		digest := sha256.Sum256(entries[i].Data)
		if metadata[i].SHA256 != hex.EncodeToString(digest[:]) || !validEntryMetadata(metadata[i], i, entries[i].Data) {
			t.Fatalf("member %d metadata digest mismatch", i)
		}
	}

	var encoded bytes.Buffer
	if err = Archive(&encoded, entries); err != nil {
		t.Fatal(err)
	}
	actual := collateralReadArchive(t, encoded.Bytes())
	if len(actual) != 7 {
		t.Fatalf("archive member count: %d", len(actual))
	}
	for i, member := range actual {
		contract := archiveContract[i]
		if member.header.Name != collateralNames[i] || !canonicalArchiveHeader(member.header, contract.name, int64(contract.mode), contract.max) {
			t.Fatalf("archive member %d order/mode/bounds mismatch: %#v", i, member.header)
		}
		if !bytes.Equal(member.body, entries[i].Data) {
			t.Fatalf("archive member %d body mismatch", i)
		}
	}
}

func TestRepositoryCollateralLoadsExactly(t *testing.T) {
	root, err := command(t.Context(), ".", environment(), "git", "rev-parse", "--show-toplevel")
	if err != nil {
		t.Fatal(err)
	}
	got, err := loadCollateral(root)
	if err != nil {
		t.Fatal(err)
	}
	for name, actual := range map[string][]byte{
		"docs/release-install.md": got.install,
		licenseName:               got.license,
		"docs/release-notes.md":   got.notes,
		"examples/local.yaml":     got.config,
	} {
		want, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || !bytes.Equal(actual, want) {
			t.Fatalf("collateral source mismatch %s: %v", name, err)
		}
	}
}

func TestReleaseEntriesEnforcesEveryMemberBound(t *testing.T) {
	base := signingCollateralFixture()
	notice := signingNoticeFixture("linux", "amd64")
	binary := signingBinaryFixture(t, "linux", "amd64")
	sbom := signingSBOMFixture(t, "1.0.0", strings.Repeat("a", 40), "linux", "amd64", binary)
	for name, mutate := range map[string]func(*collateral, *[]byte, *[]byte, *[]byte){
		"empty_install": func(c *collateral, _, _, _ *[]byte) { c.install = nil },
		"empty_license": func(c *collateral, _, _, _ *[]byte) { c.license = nil },
		"empty_notes":   func(c *collateral, _, _, _ *[]byte) { c.notes = nil },
		"empty_config":  func(c *collateral, _, _, _ *[]byte) { c.config = nil },
		"empty_notice":  func(_ *collateral, n, _, _ *[]byte) { *n = nil },
		"empty_sbom":    func(_ *collateral, _, s, _ *[]byte) { *s = nil },
		"empty_binary":  func(_ *collateral, _, _, b *[]byte) { *b = nil },
		"large_install": func(c *collateral, _, _, _ *[]byte) { c.install = bytes.Repeat([]byte("x"), maxInstall+1) },
		"large_license": func(c *collateral, _, _, _ *[]byte) { c.license = bytes.Repeat([]byte("x"), maxLicense+1) },
		"large_notes":   func(c *collateral, _, _, _ *[]byte) { c.notes = bytes.Repeat([]byte("x"), maxReleaseNotes+1) },
		"large_config":  func(c *collateral, _, _, _ *[]byte) { c.config = bytes.Repeat([]byte("x"), maxConfig+1) },
		"large_notice":  func(_ *collateral, n, _, _ *[]byte) { *n = bytes.Repeat([]byte("x"), maxNotice+1) },
		"large_sbom":    func(_ *collateral, _, s, _ *[]byte) { *s = bytes.Repeat([]byte("x"), maxTargetSBOM+1) },
	} {
		t.Run(name, func(t *testing.T) {
			shared := base
			n, s, b := append([]byte(nil), notice...), append([]byte(nil), sbom...), append([]byte(nil), binary...)
			mutate(&shared, &n, &s, &b)
			if _, _, err := releaseEntries(shared, n, s, b); err != ErrInvalid {
				t.Fatal("out-of-bound member accepted", err)
			}
		})
	}
	if archiveContract[6].name != "darwin" || archiveContract[6].max != maxArtifact {
		t.Fatal("binary bound is not maxArtifact")
	}
	invalid := archiveEntryMetadata{Name: "darwin", Mode: 0755, Size: maxArtifact + 1, SHA256: strings.Repeat("0", 64)}
	if validEntryMetadata(invalid, 6, binary) {
		t.Fatal("oversize binary metadata accepted")
	}
}

func TestCollateralSchemaThreeMetadataAndSharedEquality(t *testing.T) {
	dir, _, _ := signingFixture(t)
	body, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err = json.Unmarshal(body, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != releaseManifestSchema || len(manifest.Artifacts) != 4 {
		t.Fatalf("manifest schema/artifacts: %d/%d", manifest.SchemaVersion, len(manifest.Artifacts))
	}
	sharedIndexes := []int{0, 1, 2, 5}
	for artifactIndex, artifact := range manifest.Artifacts {
		members := collateralReadArchiveFile(t, filepath.Join(dir, artifact.File))
		if len(artifact.Entries) != 7 || len(members) != 7 {
			t.Fatalf("artifact %d member metadata count", artifactIndex)
		}
		for i, member := range members {
			if !validEntryMetadata(artifact.Entries[i], i, member.body) || artifact.Entries[i].Name != member.header.Name || artifact.Entries[i].Mode != uint32(member.header.Mode) {
				t.Fatalf("artifact %d member %d metadata mismatch", artifactIndex, i)
			}
		}
		if artifactIndex > 0 {
			for _, index := range sharedIndexes {
				if artifact.Entries[index] != manifest.Artifacts[0].Entries[index] || !bytes.Equal(members[index].body, collateralReadArchiveFile(t, filepath.Join(dir, manifest.Artifacts[0].File))[index].body) {
					t.Fatalf("shared collateral differs: artifact=%d member=%d", artifactIndex, index)
				}
			}
		}
	}
}

func TestLoadCollateralRejectsUnsafeOrInvalidSources(t *testing.T) {
	tests := []struct {
		name   string
		file   string
		mutate func(*testing.T, string)
	}{
		{"missing_install", "docs/release-install.md", func(t *testing.T, path string) { collateralRemove(t, path) }},
		{"missing_license", licenseName, func(t *testing.T, path string) { collateralRemove(t, path) }},
		{"missing_notes", "docs/release-notes.md", func(t *testing.T, path string) { collateralRemove(t, path) }},
		{"missing_config", "examples/local.yaml", func(t *testing.T, path string) { collateralRemove(t, path) }},
		{"invalid_utf8", licenseName, func(t *testing.T, path string) { collateralWrite(t, path, []byte{0xff, '\n'}) }},
		{"nul", "docs/release-install.md", func(t *testing.T, path string) { collateralWrite(t, path, []byte("bad\x00\n")) }},
		{"carriage_return", "docs/release-notes.md", func(t *testing.T, path string) { collateralWrite(t, path, []byte("bad\r\n")) }},
		{"no_final_lf", licenseName, func(t *testing.T, path string) { collateralWrite(t, path, []byte("no newline")) }},
		{"symlink", licenseName, collateralSymlink},
		{"oversize_install", "docs/release-install.md", func(t *testing.T, path string) { collateralWrite(t, path, bytes.Repeat([]byte("x"), maxInstall+1)) }},
		{"oversize_license", licenseName, func(t *testing.T, path string) { collateralWrite(t, path, bytes.Repeat([]byte("x"), maxLicense+1)) }},
		{"oversize_notes", "docs/release-notes.md", func(t *testing.T, path string) {
			collateralWrite(t, path, bytes.Repeat([]byte("x"), maxReleaseNotes+1))
		}},
		{"oversize_config", "examples/local.yaml", func(t *testing.T, path string) { collateralWrite(t, path, bytes.Repeat([]byte("x"), maxConfig+1)) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := collateralSourceFixture(t)
			test.mutate(t, filepath.Join(source, test.file))
			if _, err := loadCollateral(source); err == nil {
				t.Fatal("invalid collateral accepted")
			}
		})
	}
}

func TestLoadCollateralRequiresFailClosedLocalSample(t *testing.T) {
	tests := map[string]string{
		"cloud_mode":           strings.Replace(collateralConfigFixture, "mode: local_only", "mode: cloud_only", 1),
		"cloud_provider":       strings.Replace(collateralConfigFixture, "kind: ollama", "kind: openai", 1),
		"remote_endpoint":      strings.Replace(collateralConfigFixture, "http://127.0.0.1:11434", "https://ollama.example", 1),
		"credential_reference": strings.Replace(collateralConfigFixture, "    endpoint: http://127.0.0.1:11434", "    endpoint: http://127.0.0.1:11434\n    api_key_env: OLLAMA_TOKEN", 1),
		"cloud_model":          strings.Replace(collateralConfigFixture, "locality: local", "locality: cloud", 1),
		"known_context":        strings.Replace(collateralConfigFixture, "context_tokens: 0", "context_tokens: 4096", 1),
		"known_ram":            strings.Replace(collateralConfigFixture, "ram_bytes: 0", "ram_bytes: 1024", 1),
		"nonzero_cost":         strings.Replace(collateralConfigFixture, "estimated_cost: 0", "estimated_cost: 0.01", 1),
		"missing_models":       strings.Replace(collateralConfigFixture, "models:", "absent_models:", 1),
		"missing_providers":    strings.Replace(collateralConfigFixture, "providers:", "absent_providers:", 1),
		"tools_enabled":        strings.Replace(collateralConfigFixture, "tools:\n  enabled: false", "tools:\n  enabled: true", 1),
		"memory_enabled":       strings.Replace(collateralConfigFixture, "memory:\n  enabled: false", "memory:\n  enabled: true", 1),
		"skills_enabled":       strings.Replace(collateralConfigFixture, "skills:\n  enabled: false", "skills:\n  enabled: true", 1),
		"skill_auto_draft":     strings.Replace(collateralConfigFixture, "auto_draft: false", "auto_draft: true", 1),
		"skill_auto_activate":  strings.Replace(collateralConfigFixture, "auto_activate_after_validation: false", "auto_activate_after_validation: true", 1),
		"skill_learning":       strings.Replace(collateralConfigFixture, "learning:\n    enabled: false", "learning:\n    enabled: true", 1),
		"llm_judge":            strings.Replace(collateralConfigFixture, "llm_judge_enabled: false", "llm_judge_enabled: true", 1),
	}
	for name, config := range tests {
		t.Run(name, func(t *testing.T) {
			source := collateralSourceFixture(t)
			collateralWrite(t, filepath.Join(source, "examples/local.yaml"), []byte(config))
			if _, err := loadCollateral(source); err == nil {
				t.Fatal("non-fail-closed sample accepted")
			}
		})
	}
}

func TestSigningRejectsCollateralMemberTamperMissingExtraAndReorder(t *testing.T) {
	for _, scenario := range []string{"tamper", "missing", "extra", "reordered"} {
		t.Run(scenario, func(t *testing.T) {
			dir, seed, public := signingFixture(t)
			path := filepath.Join(dir, "DarwinRouter_1.0.0_darwin_amd64.tar.gz")
			members := collateralReadArchiveFile(t, path)
			entries := make([]Entry, len(members))
			for i, member := range members {
				entries[i] = Entry{Name: member.header.Name, Data: member.body}
			}
			var archive []byte
			switch scenario {
			case "tamper":
				entries[0].Data = []byte("tampered install\n")
				archive = collateralArchive(t, entries)
			case "missing":
				archive = collateralArchive(t, entries[1:])
			case "extra":
				entries = append(entries, Entry{Name: "unexpected.txt", Data: []byte("unexpected\n")})
				archive = collateralArchive(t, entries)
			case "reordered":
				entries[0], entries[1] = entries[1], entries[0]
				archive = collateralRawArchive(t, entries)
			}
			collateralWrite(t, path, archive)
			refreshSigningFixture(t, dir)
			if err := signUncheckedForTest(dir, seed); err != ErrSignature {
				t.Fatal("invalid collateral signed", err)
			}
			authenticateSigningFixture(t, dir, seed)
			if err := Verify(dir, public); err != ErrSignature {
				t.Fatal("authenticated invalid collateral verified", err)
			}
		})
	}
}

func TestSigningRejectsCrossTargetSharedCollateralMismatch(t *testing.T) {
	dir, seed, _ := signingFixture(t)
	manifestPath := filepath.Join(dir, "manifest.json")
	body, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err = json.Unmarshal(body, &manifest); err != nil {
		t.Fatal(err)
	}
	artifact := &manifest.Artifacts[1]
	archivePath := filepath.Join(dir, artifact.File)
	members := collateralReadArchiveFile(t, archivePath)
	members[0].body = []byte("different but internally valid install\n")
	entries := make([]Entry, len(members))
	for i, member := range members {
		entries[i] = Entry{Name: member.header.Name, Data: member.body}
		digest := sha256.Sum256(member.body)
		artifact.Entries[i] = archiveEntryMetadata{Name: member.header.Name, Mode: uint32(member.header.Mode), Size: int64(len(member.body)), SHA256: hex.EncodeToString(digest[:])}
	}
	archive := collateralArchive(t, entries)
	collateralWrite(t, archivePath, archive)
	digest := sha256.Sum256(archive)
	artifact.SHA256 = hex.EncodeToString(digest[:])
	body, err = json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	collateralWrite(t, manifestPath, append(body, '\n'))
	collateralRewriteSums(t, dir, manifest)
	if err := signUncheckedForTest(dir, seed); err != ErrSignature {
		t.Fatal("cross-target collateral mismatch signed", err)
	}
}

const collateralConfigFixture = `version: 1
mode: local_only
providers:
  - id: local
    kind: ollama
    endpoint: http://127.0.0.1:11434
models:
  - id: local-model
    provider: local
    model: fixture
    locality: local
    capabilities: [chat]
    context_tokens: 0
    estimated_cost: 0
    ram_bytes: 0
tools:
  enabled: false
memory:
  enabled: false
skills:
  enabled: false
  auto_draft: false
  auto_activate_after_validation: false
  learning:
    enabled: false
evaluation:
  llm_judge_enabled: false
`

func collateralSourceFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"docs", "examples"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string][]byte{
		"docs/release-install.md": []byte("# Install\n"),
		licenseName:               []byte("MIT License\n"),
		"docs/release-notes.md":   []byte("# Release notes\n"),
		"examples/local.yaml":     []byte(collateralConfigFixture),
	} {
		collateralWrite(t, filepath.Join(root, name), body)
	}
	return root
}

func collateralWrite(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.WriteFile(path, body, 0644); err != nil {
		t.Fatal(err)
	}
}

func collateralRemove(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

func collateralSymlink(t *testing.T, path string) {
	t.Helper()
	target := filepath.Join(t.TempDir(), "target")
	collateralWrite(t, target, []byte("MIT License\n"))
	collateralRemove(t, path)
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}

type collateralArchiveMember struct {
	header *tar.Header
	body   []byte
}

func collateralReadArchiveFile(t *testing.T, path string) []collateralArchiveMember {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return collateralReadArchive(t, body)
}

func collateralReadArchive(t *testing.T, body []byte) []collateralArchiveMember {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var members []collateralArchiveMember
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		entry, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		copyHeader := *header
		members = append(members, collateralArchiveMember{header: &copyHeader, body: entry})
	}
	return members
}

func collateralArchive(t *testing.T, entries []Entry) []byte {
	t.Helper()
	var archive bytes.Buffer
	if err := Archive(&archive, entries); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

func collateralRawArchive(t *testing.T, entries []Entry) []byte {
	t.Helper()
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	gz.Header.ModTime = time.Time{}
	gz.Header.OS = 255
	tw := tar.NewWriter(gz)
	for _, entry := range entries {
		mode := int64(0644)
		if entry.Name == "darwin" {
			mode = 0755
		}
		header := &tar.Header{Name: entry.Name, Mode: mode, Size: int64(len(entry.Data)), ModTime: time.Unix(0, 0), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(entry.Data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

func collateralRewriteSums(t *testing.T, dir string, manifest Manifest) {
	t.Helper()
	var sums strings.Builder
	for _, artifact := range manifest.Artifacts {
		body, err := os.ReadFile(filepath.Join(dir, artifact.File))
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(body)
		sums.WriteString(hex.EncodeToString(digest[:]) + "  " + artifact.File + "\n")
	}
	body, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	sums.WriteString(hex.EncodeToString(digest[:]) + "  manifest.json\n")
	collateralWrite(t, filepath.Join(dir, "SHA256SUMS"), []byte(sums.String()))
}

func TestCollateralContractNamesAreCanonicalAndSorted(t *testing.T) {
	if !reflect.DeepEqual(collateralNames, []string{"INSTALL.md", "LICENSE", "RELEASE_NOTES.md", "SBOM.spdx.json", "THIRD_PARTY_NOTICES.txt", "config.example.yaml", "darwin"}) {
		t.Fatal("unexpected collateral contract names", collateralNames)
	}
	for i := 1; i < len(collateralNames); i++ {
		if collateralNames[i] <= collateralNames[i-1] {
			t.Fatal("collateral names are not in canonical archive order", collateralNames)
		}
	}
}
