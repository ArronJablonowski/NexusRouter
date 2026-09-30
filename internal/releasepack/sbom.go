package releasepack

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	spdxVersion       = "SPDX-2.3"
	spdxDataLicense   = "CC0-1.0"
	spdxNoAssertion   = "NOASSERTION"
	spdxScopeComment  = "Module-level release inventory. File entries are limited to the packaged executable and first-party embedded Web UI sources; this is not a complete source-file or build-provenance inventory."
	maxTargetSBOM     = 8 << 20
	maxSBOMSourceFile = 16 << 20
	maxSBOMSourceData = 64 << 20
)

var sbomAssetVersion = regexp.MustCompile(`^v[1-9][0-9]*$`)

// TargetSBOMOptions binds an SPDX document to one reproducible release target.
// Created must be an already-determined UTC RFC3339 timestamp; this package
// never samples the wall clock while producing release metadata.
type TargetSBOMOptions struct {
	Version      string
	Commit       string
	TargetOS     string
	TargetArch   string
	Created      string
	BinarySHA256 string
}

type spdxDocument struct {
	SPDXVersion       string           `json:"spdxVersion"`
	DataLicense       string           `json:"dataLicense"`
	SPDXID            string           `json:"SPDXID"`
	Name              string           `json:"name"`
	Comment           string           `json:"comment"`
	DocumentNamespace string           `json:"documentNamespace"`
	CreationInfo      spdxCreationInfo `json:"creationInfo"`
	DocumentDescribes []string         `json:"documentDescribes"`
	Packages          []spdxPackage    `json:"packages"`
	Files             []spdxFile       `json:"files"`
	Relationships     []spdxRelation   `json:"relationships"`
}

type spdxCreationInfo struct {
	Created  string   `json:"created"`
	Creators []string `json:"creators"`
}

type spdxPackage struct {
	Name             string         `json:"name"`
	SPDXID           string         `json:"SPDXID"`
	VersionInfo      string         `json:"versionInfo"`
	DownloadLocation string         `json:"downloadLocation"`
	FilesAnalyzed    bool           `json:"filesAnalyzed"`
	LicenseConcluded string         `json:"licenseConcluded"`
	LicenseDeclared  string         `json:"licenseDeclared"`
	CopyrightText    string         `json:"copyrightText"`
	Checksums        []spdxChecksum `json:"checksums,omitempty"`
}

type spdxFile struct {
	FileName         string         `json:"fileName"`
	SPDXID           string         `json:"SPDXID"`
	Checksums        []spdxChecksum `json:"checksums"`
	FileTypes        []string       `json:"fileTypes"`
	LicenseConcluded string         `json:"licenseConcluded"`
	CopyrightText    string         `json:"copyrightText"`
}

type spdxChecksum struct {
	Algorithm     string `json:"algorithm"`
	ChecksumValue string `json:"checksumValue"`
}

type spdxRelation struct {
	ElementID string `json:"spdxElementId"`
	Type      string `json:"relationshipType"`
	RelatedID string `json:"relatedSpdxElement"`
}

type sbomSourceFile struct {
	Name   string
	SHA256 string
}

// BuildTargetSBOM derives the exact cmd/nexus dependency closure and checked-
// in frontend source inventory before rendering canonical SPDX 2.3 JSON.
func BuildTargetSBOM(ctx context.Context, source string, env []string, options TargetSBOMOptions) ([]byte, error) {
	if ctx == nil || source == "" || len(env) == 0 {
		return nil, ErrInvalid
	}
	modules, err := targetNoticeModules(ctx, source, options.TargetOS, options.TargetArch, env)
	if err != nil {
		return nil, err
	}
	assets, err := discoverSBOMSourceFiles(ctx, source, env)
	if err != nil {
		return nil, err
	}
	return renderTargetSBOM(options, modules, assets)
}

func renderTargetSBOM(options TargetSBOMOptions, modules []noticeModule, assets []sbomSourceFile) ([]byte, error) {
	if validateTargetSBOMOptions(options) != nil || len(modules) == 0 || len(modules) > 10_000 || len(assets) == 0 || len(assets) > 256 {
		return nil, ErrInvalid
	}
	if _, err := renderThirdPartyNotices(options.TargetOS, options.TargetArch, modules); err != nil {
		return nil, ErrInvalid
	}
	modules = append([]noticeModule(nil), modules...)
	sort.Slice(modules, func(i, j int) bool {
		if modules[i].Path == modules[j].Path {
			return modules[i].Version < modules[j].Version
		}
		return modules[i].Path < modules[j].Path
	})
	assets = append([]sbomSourceFile(nil), assets...)
	sort.Slice(assets, func(i, j int) bool { return assets[i].Name < assets[j].Name })

	rootID := "SPDXRef-Package-NexusRouter"
	document := spdxDocument{
		SPDXVersion: spdxVersion, DataLicense: spdxDataLicense, SPDXID: "SPDXRef-DOCUMENT",
		Name:              "NexusRouter-" + options.Version + "-" + options.TargetOS + "-" + options.TargetArch,
		Comment:           spdxScopeComment,
		DocumentNamespace: fmt.Sprintf("https://github.com/ArronJablonowski/NexusRouter/releases/%s/%s/%s-%s/sbom", options.Version, options.Commit, options.TargetOS, options.TargetArch),
		CreationInfo:      spdxCreationInfo{Created: options.Created, Creators: []string{"Tool: NexusRouter-releasepack"}},
		DocumentDescribes: []string{rootID},
	}
	document.Packages = append(document.Packages, spdxPackage{
		Name: "NexusRouter", SPDXID: rootID, VersionInfo: options.Version,
		DownloadLocation: spdxNoAssertion, FilesAnalyzed: false, LicenseConcluded: spdxNoAssertion, LicenseDeclared: "MIT",
		CopyrightText: spdxNoAssertion,
	})
	seenIDs := map[string]bool{document.SPDXID: true, rootID: true}
	for _, module := range modules {
		id := stableSPDXID("Package", module.Path+"@"+module.Version)
		if seenIDs[id] {
			return nil, ErrInvalid
		}
		seenIDs[id] = true
		license := spdxNoAssertion
		if module.Path == goToolchainModulePath {
			license = "BSD-3-Clause"
		}
		document.Packages = append(document.Packages, spdxPackage{
			Name: module.Path, SPDXID: id, VersionInfo: module.Version, DownloadLocation: spdxNoAssertion,
			FilesAnalyzed: false, LicenseConcluded: license, LicenseDeclared: license, CopyrightText: spdxNoAssertion,
		})
		relation := spdxRelation{ElementID: rootID, Type: "DEPENDS_ON", RelatedID: id}
		if module.Path == goToolchainModulePath {
			relation = spdxRelation{ElementID: id, Type: "BUILD_TOOL_OF", RelatedID: rootID}
		}
		document.Relationships = append(document.Relationships, relation)
	}
	binaryID := "SPDXRef-File-nexus"
	document.Files = append(document.Files, spdxFile{
		FileName: "./nexus", SPDXID: binaryID, Checksums: []spdxChecksum{{Algorithm: "SHA256", ChecksumValue: options.BinarySHA256}},
		FileTypes: []string{"BINARY"}, LicenseConcluded: spdxNoAssertion, CopyrightText: spdxNoAssertion,
	})
	seenIDs[binaryID] = true
	document.Relationships = append(document.Relationships, spdxRelation{ElementID: rootID, Type: "GENERATES", RelatedID: binaryID})
	previous := ""
	for _, asset := range assets {
		if !validSBOMAsset(asset) || asset.Name <= previous {
			return nil, ErrInvalid
		}
		previous = asset.Name
		id := stableSPDXID("File", asset.Name)
		if seenIDs[id] {
			return nil, ErrInvalid
		}
		seenIDs[id] = true
		document.Files = append(document.Files, spdxFile{
			FileName: "./" + asset.Name, SPDXID: id, Checksums: []spdxChecksum{{Algorithm: "SHA256", ChecksumValue: asset.SHA256}},
			FileTypes: []string{"SOURCE"}, LicenseConcluded: spdxNoAssertion, CopyrightText: spdxNoAssertion,
		})
		document.Relationships = append(document.Relationships, spdxRelation{ElementID: binaryID, Type: "GENERATED_FROM", RelatedID: id})
	}
	sort.Slice(document.Packages[1:], func(i, j int) bool { return document.Packages[1+i].SPDXID < document.Packages[1+j].SPDXID })
	sort.Slice(document.Files[1:], func(i, j int) bool { return document.Files[1+i].FileName < document.Files[1+j].FileName })
	sort.Slice(document.Relationships, func(i, j int) bool {
		a, b := document.Relationships[i], document.Relationships[j]
		return a.ElementID+"\x00"+a.Type+"\x00"+a.RelatedID < b.ElementID+"\x00"+b.Type+"\x00"+b.RelatedID
	})
	body, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, err
	}
	body = append(body, '\n')
	if len(body) > maxTargetSBOM || validateTargetSBOMDocument(document) != nil {
		return nil, ErrInvalid
	}
	return body, nil
}

// ValidateTargetSBOM accepts only the canonical JSON emitted by this package.
func ValidateTargetSBOM(body []byte) error {
	if len(body) == 0 || len(body) > maxTargetSBOM || body[len(body)-1] != '\n' {
		return ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var document spdxDocument
	if decoder.Decode(&document) != nil || decoder.Decode(&struct{}{}) != io.EOF || validateTargetSBOMDocument(document) != nil {
		return ErrInvalid
	}
	canonical, err := json.MarshalIndent(document, "", "  ")
	if err != nil || !bytes.Equal(body, append(canonical, '\n')) {
		return ErrInvalid
	}
	return nil
}

func validateTargetSBOMDocument(document spdxDocument) error {
	if document.SPDXVersion != spdxVersion || document.DataLicense != spdxDataLicense || document.SPDXID != "SPDXRef-DOCUMENT" ||
		document.Name == "" || len(document.Name) > 256 || document.Comment != spdxScopeComment || len(document.DocumentDescribes) != 1 || document.DocumentDescribes[0] != "SPDXRef-Package-NexusRouter" ||
		len(document.CreationInfo.Creators) != 1 || document.CreationInfo.Creators[0] != "Tool: NexusRouter-releasepack" || !validSPDXCreated(document.CreationInfo.Created) {
		return ErrInvalid
	}
	parsed, err := url.Parse(document.DocumentNamespace)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "github.com" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return ErrInvalid
	}
	if len(document.Packages) < 2 || len(document.Packages) > 10_001 || len(document.Files) < 2 || len(document.Files) > 257 {
		return ErrInvalid
	}
	ids := map[string]bool{document.SPDXID: true}
	toolchains := 0
	for i, item := range document.Packages {
		if !validSPDXElementID(item.SPDXID) || ids[item.SPDXID] || item.Name == "" || item.VersionInfo == "" || item.DownloadLocation != spdxNoAssertion || item.FilesAnalyzed || item.CopyrightText != spdxNoAssertion {
			return ErrInvalid
		}
		ids[item.SPDXID] = true
		if i == 0 {
			if item.Name != "NexusRouter" || item.SPDXID != "SPDXRef-Package-NexusRouter" || !semver.MatchString(item.VersionInfo) || item.LicenseConcluded != spdxNoAssertion || item.LicenseDeclared != "MIT" || len(item.Checksums) != 0 {
				return ErrInvalid
			}
			continue
		}
		if i > 1 && document.Packages[i-1].SPDXID >= item.SPDXID {
			return ErrInvalid
		}
		if !safeNoticeModule(item.Name, item.VersionInfo) || item.SPDXID != stableSPDXID("Package", item.Name+"@"+item.VersionInfo) {
			return ErrInvalid
		}
		if item.Name == goToolchainModulePath {
			toolchains++
			if item.LicenseConcluded != "BSD-3-Clause" || item.LicenseDeclared != "BSD-3-Clause" || !noticeModuleVersion.MatchString(item.VersionInfo) {
				return ErrInvalid
			}
		} else if item.LicenseConcluded != spdxNoAssertion || item.LicenseDeclared != spdxNoAssertion {
			return ErrInvalid
		}
		if len(item.Checksums) != 0 {
			return ErrInvalid
		}
	}
	if toolchains != 1 {
		return ErrInvalid
	}
	for i, file := range document.Files {
		if !validSPDXElementID(file.SPDXID) || ids[file.SPDXID] || len(file.Checksums) != 1 || !validSHA256Checksum(file.Checksums[0]) || file.CopyrightText != spdxNoAssertion || len(file.FileTypes) != 1 {
			return ErrInvalid
		}
		ids[file.SPDXID] = true
		if i == 0 {
			if file.FileName != "./nexus" || file.SPDXID != "SPDXRef-File-nexus" || file.FileTypes[0] != "BINARY" || file.LicenseConcluded != spdxNoAssertion {
				return ErrInvalid
			}
		} else {
			asset := sbomSourceFile{Name: strings.TrimPrefix(file.FileName, "./"), SHA256: file.Checksums[0].ChecksumValue}
			if file.FileName != "./"+asset.Name || !validSBOMAsset(asset) || file.SPDXID != stableSPDXID("File", asset.Name) || file.FileTypes[0] != "SOURCE" || file.LicenseConcluded != spdxNoAssertion || (i > 1 && document.Files[i-1].FileName >= file.FileName) {
				return ErrInvalid
			}
		}
	}
	nameSuffix := "-" + targetFromNamespace(parsed.Path)
	if nameSuffix == "-" || !strings.HasSuffix(document.Name, nameSuffix) || strings.TrimSuffix(document.Name, nameSuffix) != "NexusRouter-"+document.Packages[0].VersionInfo ||
		document.DocumentNamespace != expectedSBOMNamespace(document.Packages[0].VersionInfo, parsed.Path) {
		return ErrInvalid
	}
	previous := ""
	seenRelations := map[string]bool{}
	for _, relation := range document.Relationships {
		key := relation.ElementID + "\x00" + relation.Type + "\x00" + relation.RelatedID
		if key <= previous || seenRelations[key] || !ids[relation.ElementID] || !ids[relation.RelatedID] || relation.ElementID == relation.RelatedID || (relation.Type != "DEPENDS_ON" && relation.Type != "BUILD_TOOL_OF" && relation.Type != "GENERATES" && relation.Type != "GENERATED_FROM") {
			return ErrInvalid
		}
		previous, seenRelations[key] = key, true
	}
	if len(document.Relationships) != len(document.Packages)-1+len(document.Files) {
		return ErrInvalid
	}
	for _, item := range document.Packages[1:] {
		key := "SPDXRef-Package-NexusRouter\x00DEPENDS_ON\x00" + item.SPDXID
		if item.Name == goToolchainModulePath {
			key = item.SPDXID + "\x00BUILD_TOOL_OF\x00SPDXRef-Package-NexusRouter"
		}
		if !seenRelations[key] {
			return ErrInvalid
		}
	}
	if !seenRelations["SPDXRef-Package-NexusRouter\x00GENERATES\x00SPDXRef-File-nexus"] {
		return ErrInvalid
	}
	for _, file := range document.Files[1:] {
		if !seenRelations["SPDXRef-File-nexus\x00GENERATED_FROM\x00"+file.SPDXID] {
			return ErrInvalid
		}
	}
	return nil
}

func targetFromNamespace(namespacePath string) string {
	parts := strings.Split(strings.TrimPrefix(namespacePath, "/"), "/")
	if len(parts) != 7 || parts[0] != "ArronJablonowski" || parts[1] != "NexusRouter" || parts[2] != "releases" || parts[6] != "sbom" ||
		!semver.MatchString(parts[3]) || !commitPattern.MatchString(parts[4]) {
		return ""
	}
	target := parts[5]
	if target != "darwin-amd64" && target != "darwin-arm64" && target != "linux-amd64" && target != "linux-arm64" {
		return ""
	}
	return target
}

func expectedSBOMNamespace(version, namespacePath string) string {
	parts := strings.Split(strings.TrimPrefix(namespacePath, "/"), "/")
	if len(parts) != 7 || parts[3] != version || targetFromNamespace(namespacePath) == "" {
		return ""
	}
	return "https://github.com/" + strings.Join(parts, "/")
}

func validateTargetSBOMOptions(options TargetSBOMOptions) error {
	if validate(Options{Version: options.Version, Commit: options.Commit, Out: "sbom"}) != nil ||
		(options.TargetOS != "darwin" && options.TargetOS != "linux") || (options.TargetArch != "amd64" && options.TargetArch != "arm64") ||
		!validSPDXCreated(options.Created) || !rawSHA256(options.BinarySHA256) {
		return ErrInvalid
	}
	return nil
}

func validSPDXCreated(value string) bool {
	parsed, err := time.Parse("2006-01-02T15:04:05Z", value)
	return err == nil && !parsed.Before(time.Unix(0, 0)) && parsed.Format("2006-01-02T15:04:05Z") == value
}

func rawSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == hex.EncodeToString(decoded)
}

func validSHA256Checksum(checksum spdxChecksum) bool {
	return checksum.Algorithm == "SHA256" && rawSHA256(checksum.ChecksumValue)
}

func stableSPDXID(kind, value string) string {
	digest := sha256.Sum256([]byte(kind + "\x00" + value))
	return "SPDXRef-" + kind + "-" + hex.EncodeToString(digest[:16])
}

func validSPDXElementID(value string) bool {
	if !strings.HasPrefix(value, "SPDXRef-") || len(value) > 128 {
		return false
	}
	for _, character := range value[len("SPDXRef-"):] {
		if (character < 'A' || character > 'Z') && (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '.' && character != '-' {
			return false
		}
	}
	return len(value) > len("SPDXRef-")
}

func validSBOMAsset(asset sbomSourceFile) bool {
	parts := strings.Split(asset.Name, "/")
	return len(parts) == 4 && parts[0] == "webui" && parts[1] == "assets" && sbomAssetVersion.MatchString(parts[2]) &&
		parts[3] != "" && parts[3] != "." && parts[3] != ".." && filepath.Base(parts[3]) == parts[3] &&
		filepath.ToSlash(filepath.Clean(asset.Name)) == asset.Name && rawSHA256(asset.SHA256)
}

// discoverSBOMSourceFiles asks the candidate source package which files its
// embed directive actually selects. It must not use constants compiled into
// the releasepack caller, which may be older than the candidate commit.
func discoverSBOMSourceFiles(ctx context.Context, source string, env []string) ([]sbomSourceFile, error) {
	if ctx == nil || source == "" || len(env) == 0 {
		return nil, ErrInvalid
	}
	out, err := command(ctx, source, env, "go", "list", "-mod=readonly", "-json", "./webui")
	if err != nil {
		return nil, err
	}
	var listed struct {
		EmbedFiles []string
	}
	if json.Unmarshal([]byte(out), &listed) != nil || len(listed.EmbedFiles) == 0 || len(listed.EmbedFiles) > 256 {
		return nil, ErrInvalid
	}
	names := append([]string(nil), listed.EmbedFiles...)
	sort.Strings(names)
	previous := ""
	for _, name := range names {
		asset := sbomSourceFile{Name: "webui/" + filepath.ToSlash(name), SHA256: strings.Repeat("0", 64)}
		if name <= previous || !strings.HasPrefix(name, "assets/") || !validSBOMAsset(asset) {
			return nil, ErrInvalid
		}
		previous = name
	}
	files, err := readNamedSBOMSourceFiles(filepath.Join(source, "webui"), names)
	if err != nil {
		return nil, err
	}
	for i := range files {
		files[i].Name = "webui/" + files[i].Name
	}
	return files, nil
}

func readSBOMSourceFiles(source string) ([]sbomSourceFile, error) {
	directory := filepath.Join(source, "webui", "assets", "v1")
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) == 0 || len(entries) > 256 {
		return nil, ErrInvalid
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.Type().IsRegular() || entry.Name() == "" || filepath.Base(entry.Name()) != entry.Name() {
			return nil, ErrInvalid
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	files, err := readNamedSBOMSourceFiles(directory, names)
	if err != nil {
		return nil, err
	}
	for i := range files {
		files[i].Name = "webui/assets/v1/" + files[i].Name
	}
	return files, nil
}

func readNamedSBOMSourceFiles(directory string, names []string) ([]sbomSourceFile, error) {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	result := make([]sbomSourceFile, 0, len(names))
	total := int64(0)
	for _, name := range names {
		if name == "" || filepath.IsAbs(name) || filepath.ToSlash(filepath.Clean(name)) != filepath.ToSlash(name) || strings.HasPrefix(name, "../") {
			return nil, ErrInvalid
		}
		info, err := os.Lstat(filepath.Join(directory, name))
		if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maxSBOMSourceFile {
			return nil, ErrInvalid
		}
		file, err := root.Open(name)
		if err != nil {
			return nil, ErrInvalid
		}
		actual, statErr := file.Stat()
		digest := sha256.New()
		read, readErr := io.Copy(digest, io.LimitReader(file, info.Size()+1))
		final, finalErr := file.Stat()
		closeErr := file.Close()
		if statErr != nil || finalErr != nil || closeErr != nil || !actual.Mode().IsRegular() || !os.SameFile(info, actual) || !os.SameFile(actual, final) || actual.Size() != info.Size() || final.Size() != actual.Size() || readErr != nil || read != info.Size() {
			return nil, ErrInvalid
		}
		total += read
		if total > maxSBOMSourceData {
			return nil, ErrInvalid
		}
		result = append(result, sbomSourceFile{Name: filepath.ToSlash(name), SHA256: hex.EncodeToString(digest.Sum(nil))})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}
