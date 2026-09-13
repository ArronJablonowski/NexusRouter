package releasepack

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestRenderTargetSBOMIsCanonicalAndComplete(t *testing.T) {
	toolchain, err := embeddedGoToolchainModule()
	if err != nil {
		t.Fatal(err)
	}
	modules := []noticeModule{
		toolchain,
		{Path: "example.com/dependency", Version: "v1.2.3", Files: []noticeFile{{Name: "LICENSE", Body: []byte("unclassified license\n")}}},
	}
	assets := []sbomSourceFile{
		{Name: "webui/assets/v1/index.html", SHA256: digestSBOMTest([]byte("index"))},
		{Name: "webui/assets/v1/app.js", SHA256: digestSBOMTest([]byte("script"))},
	}
	options := validSBOMTestOptions()
	first, err := renderTargetSBOM(options, modules, assets)
	if err != nil {
		t.Fatal(err)
	}
	second, err := renderTargetSBOM(options, []noticeModule{modules[1], modules[0]}, []sbomSourceFile{assets[1], assets[0]})
	if err != nil || !bytes.Equal(first, second) {
		t.Fatal("SBOM is not deterministic across input ordering", err)
	}
	if err = ValidateTargetSBOM(first); err != nil {
		t.Fatal("canonical SBOM rejected", err)
	}
	var document spdxDocument
	if err = json.Unmarshal(first, &document); err != nil {
		t.Fatal(err)
	}
	if document.SPDXVersion != "SPDX-2.3" || document.DataLicense != "CC0-1.0" || document.Comment != spdxScopeComment ||
		document.Packages[0].LicenseDeclared != "MIT" || document.Packages[0].LicenseConcluded != spdxNoAssertion || len(document.Packages[0].Checksums) != 0 {
		t.Fatal("root package identity, scope, or license semantics invalid")
	}
	licenses := map[string]string{}
	for _, item := range document.Packages[1:] {
		licenses[item.Name] = item.LicenseDeclared
	}
	if licenses[goToolchainModulePath] != "BSD-3-Clause" || licenses["example.com/dependency"] != spdxNoAssertion {
		t.Fatal("license conclusions do not distinguish reviewed and unknown licenses", licenses)
	}
	files := map[string]string{}
	for _, file := range document.Files {
		if file.LicenseConcluded != spdxNoAssertion {
			t.Fatal("unscanned file received a concluded license", file.FileName)
		}
		files[file.FileName] = file.Checksums[0].ChecksumValue
	}
	if files["./darwin"] != options.BinarySHA256 || files["./webui/assets/v1/app.js"] != assets[1].SHA256 || files["./webui/assets/v1/index.html"] != assets[0].SHA256 {
		t.Fatal("binary or exact frontend source hashes missing", files)
	}
	relations := map[string]bool{}
	for _, relation := range document.Relationships {
		relations[relation.ElementID+"\x00"+relation.Type+"\x00"+relation.RelatedID] = true
	}
	toolchainID := stableSPDXID("Package", goToolchainModulePath+"@"+toolchain.Version)
	if !relations["SPDXRef-Package-DarwinRouter\x00GENERATES\x00SPDXRef-File-darwin"] ||
		!relations[toolchainID+"\x00BUILD_TOOL_OF\x00SPDXRef-Package-DarwinRouter"] {
		t.Fatal("SPDX build relationships missing", document.Relationships)
	}
}

func TestRenderedTargetSBOMConformsToPinnedOfficialSPDX23Schema(t *testing.T) {
	schemaBody, err := os.ReadFile("testdata/spdx-2.3-schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if got := digestSBOMTest(schemaBody); got != "11cd94e7b41ae669d42dedc68448d1899cf27b862bce8a5c7f590de693270048" {
		t.Fatal("pinned SPDX schema digest changed", got)
	}
	var schemaDocument any
	if err = json.Unmarshal(schemaBody, &schemaDocument); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft7)
	const location = "https://spdx.org/rdf/terms/2.3"
	if err = compiler.AddResource(location, schemaDocument); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile(location)
	if err != nil {
		t.Fatal(err)
	}
	var instance any
	if err = json.Unmarshal(validSBOMTestBody(t), &instance); err != nil {
		t.Fatal(err)
	}
	if err = schema.Validate(instance); err != nil {
		t.Fatal("generated SBOM rejected by pinned official SPDX 2.3 schema", err)
	}
}

func TestValidateTargetSBOMRejectsNoncanonicalOrDriftedDocuments(t *testing.T) {
	body := validSBOMTestBody(t)
	for name, mutate := range map[string]func([]byte) []byte{
		"duplicate_field": func(value []byte) []byte {
			return bytes.Replace(value, []byte("  \"spdxVersion\": \"SPDX-2.3\","), []byte("  \"spdxVersion\": \"SPDX-2.3\",\n  \"spdxVersion\": \"SPDX-2.3\","), 1)
		},
		"unknown_field": func(value []byte) []byte {
			return bytes.Replace(value, []byte("{\n"), []byte("{\n  \"unexpected\": true,\n"), 1)
		},
		"noncanonical_indent": func(value []byte) []byte {
			var document any
			if json.Unmarshal(value, &document) != nil {
				t.Fatal("test fixture")
			}
			compact, _ := json.Marshal(document)
			return append(compact, '\n')
		},
		"digest": func(value []byte) []byte {
			return bytes.Replace(value, []byte(strings.Repeat("a", 64)), []byte(strings.Repeat("A", 64)), 1)
		},
		"namespace": func(value []byte) []byte {
			return bytes.Replace(value, []byte("linux-amd64/sbom"), []byte("linux-arm64/sbom"), 1)
		},
		"relationship": func(value []byte) []byte {
			return bytes.Replace(value, []byte("GENERATED_FROM"), []byte("CONTAINS"), 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			if ValidateTargetSBOM(mutate(append([]byte(nil), body...))) == nil {
				t.Fatal("invalid SBOM accepted")
			}
		})
	}
	if ValidateTargetSBOM(make([]byte, maxTargetSBOM+1)) == nil {
		t.Fatal("oversized SBOM accepted")
	}
}

func TestReadSBOMSourceFilesHashesExactRegularAssets(t *testing.T) {
	source := t.TempDir()
	directory := filepath.Join(source, "webui", "assets", "v1")
	if err := os.MkdirAll(directory, 0755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{"index.html": []byte("<html>\n"), "app.js": []byte("script\n")} {
		if err := os.WriteFile(filepath.Join(directory, name), body, 0644); err != nil {
			t.Fatal(err)
		}
	}
	files, err := readSBOMSourceFiles(source)
	if err != nil || len(files) != 2 {
		t.Fatal("source inventory failed", err, files)
	}
	if files[0].Name != "webui/assets/v1/app.js" || files[0].SHA256 != digestSBOMTest([]byte("script\n")) ||
		files[1].Name != "webui/assets/v1/index.html" || files[1].SHA256 != digestSBOMTest([]byte("<html>\n")) {
		t.Fatal("source inventory did not bind exact names and bytes", files)
	}
	if err = os.Symlink(filepath.Join(directory, "app.js"), filepath.Join(directory, "linked.js")); err != nil {
		t.Fatal(err)
	}
	if _, err = readSBOMSourceFiles(source); err != ErrInvalid {
		t.Fatal("symlink asset accepted", err)
	}
}

func TestDiscoverSBOMSourceFilesUsesCandidateEmbedSelection(t *testing.T) {
	source := t.TempDir()
	for _, directory := range []string{"webui/assets/v1", "webui/assets/v2"} {
		if err := os.MkdirAll(filepath.Join(source, directory), 0755); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string][]byte{
		"go.mod":                     []byte("module example.com/candidate\n\ngo 1.25\n"),
		"webui/shell.go":             []byte("package webui\n\nimport \"embed\"\n\n//go:embed assets/v2/*\nvar assets embed.FS\n"),
		"webui/assets/v1/stale.js":   []byte("stale\n"),
		"webui/assets/v2/current.js": []byte("current\n"),
	} {
		if err := os.WriteFile(filepath.Join(source, name), body, 0644); err != nil {
			t.Fatal(err)
		}
	}
	files, err := discoverSBOMSourceFiles(t.Context(), source, environment())
	if err != nil || len(files) != 1 || files[0].Name != "webui/assets/v2/current.js" || files[0].SHA256 != digestSBOMTest([]byte("current\n")) {
		t.Fatal("candidate embed inventory was not authoritative", files, err)
	}
}

func TestRenderTargetSBOMRejectsInvalidBindings(t *testing.T) {
	toolchain, err := embeddedGoToolchainModule()
	if err != nil {
		t.Fatal(err)
	}
	asset := sbomSourceFile{Name: "webui/assets/v1/app.js", SHA256: digestSBOMTest([]byte("script"))}
	for name, mutate := range map[string]func(*TargetSBOMOptions){
		"version": func(options *TargetSBOMOptions) { options.Version = "01.2.3" },
		"commit":  func(options *TargetSBOMOptions) { options.Commit = strings.Repeat("A", 40) },
		"target":  func(options *TargetSBOMOptions) { options.TargetArch = "386" },
		"created": func(options *TargetSBOMOptions) { options.Created = "2026-09-13T12:00:00-06:00" },
		"digest":  func(options *TargetSBOMOptions) { options.BinarySHA256 = "sha256:" + strings.Repeat("a", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			options := validSBOMTestOptions()
			mutate(&options)
			if _, err := renderTargetSBOM(options, []noticeModule{toolchain}, []sbomSourceFile{asset}); err != ErrInvalid {
				t.Fatal("invalid binding accepted", err)
			}
		})
	}
}

func validSBOMTestBody(t *testing.T) []byte {
	t.Helper()
	toolchain, err := embeddedGoToolchainModule()
	if err != nil {
		t.Fatal(err)
	}
	body, err := renderTargetSBOM(validSBOMTestOptions(), []noticeModule{toolchain}, []sbomSourceFile{{Name: "webui/assets/v1/app.js", SHA256: digestSBOMTest([]byte("script"))}})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func validSBOMTestOptions() TargetSBOMOptions {
	return TargetSBOMOptions{
		Version: "1.2.3", Commit: strings.Repeat("b", 40), TargetOS: "linux", TargetArch: "amd64",
		Created: "2026-09-13T18:00:00Z", BinarySHA256: strings.Repeat("a", 64),
	}
}

func digestSBOMTest(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

func signingSBOMFixture(t *testing.T, version, commit, targetOS, targetArch string, binary []byte) []byte {
	t.Helper()
	toolchain, err := embeddedGoToolchainModule()
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(binary)
	body, err := renderTargetSBOM(TargetSBOMOptions{
		Version: version, Commit: commit, TargetOS: targetOS, TargetArch: targetArch,
		Created: "2026-09-13T18:00:00Z", BinarySHA256: hex.EncodeToString(digest[:]),
	}, []noticeModule{
		{Path: "example.com/dependency", Version: "v1.0.0", Files: []noticeFile{{Name: "LICENSE", Body: []byte("fixture license\n")}}},
		toolchain,
	}, []sbomSourceFile{{Name: "webui/assets/v1/app.js", SHA256: digestSBOMTest([]byte("fixture frontend source"))}})
	if err != nil {
		t.Fatal(err)
	}
	return body
}
