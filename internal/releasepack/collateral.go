package releasepack

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
)

const (
	installName      = "INSTALL.md"
	licenseName      = "LICENSE"
	releaseNotesName = "RELEASE_NOTES.md"
	configName       = "config.example.yaml"
	sbomName         = "SBOM.spdx.json"
	maxInstall       = 256 << 10
	maxLicense       = 64 << 10
	maxReleaseNotes  = 512 << 10
	maxConfig        = 256 << 10
)

type archiveEntryMetadata struct {
	Name   string `json:"name"`
	Mode   uint32 `json:"mode"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type collateral struct {
	install, license, notes, config []byte
}

var archiveContract = []struct {
	name string
	mode uint32
	max  int64
}{
	{installName, 0644, maxInstall},
	{licenseName, 0644, maxLicense},
	{releaseNotesName, 0644, maxReleaseNotes},
	{sbomName, 0644, maxTargetSBOM},
	{noticeName, 0644, maxNotice},
	{configName, 0644, maxConfig},
	{"nexus", 0755, maxArtifact},
}

func loadCollateral(source string) (collateral, error) {
	root, err := os.OpenRoot(source)
	if err != nil {
		return collateral{}, err
	}
	defer root.Close()
	read := func(name string, limit int64) ([]byte, error) {
		info, err := root.Lstat(name)
		if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > limit {
			return nil, ErrInvalid
		}
		body, err := root.ReadFile(name)
		if err != nil || int64(len(body)) != info.Size() || !canonicalText(body) {
			return nil, ErrInvalid
		}
		return body, nil
	}
	result := collateral{}
	if result.install, err = read("docs/release-install.md", maxInstall); err != nil {
		return collateral{}, err
	}
	if result.license, err = read(licenseName, maxLicense); err != nil {
		return collateral{}, err
	}
	if result.notes, err = read("docs/release-notes.md", maxReleaseNotes); err != nil {
		return collateral{}, err
	}
	if result.config, err = read("examples/local.yaml", maxConfig); err != nil {
		return collateral{}, err
	}
	settings, err := config.Load(config.Options{ProjectFile: filepath.Join(source, "examples/local.yaml")})
	if err != nil || settings.Mode != "local_only" || len(settings.Models) == 0 || len(settings.Providers) == 0 {
		return collateral{}, ErrInvalid
	}
	for _, provider := range settings.Providers {
		if provider.Kind != "ollama" || !loopbackReleaseEndpoint(provider.ResolvedEndpoint()) || provider.APIKeyEnv != "" {
			return collateral{}, ErrInvalid
		}
	}
	for _, model := range settings.Models {
		if model.Locality != "local" || model.ContextTokens != 0 || model.RAMBytes != 0 || model.EstimatedCost == nil || *model.EstimatedCost != 0 {
			return collateral{}, ErrInvalid
		}
	}
	if settings.Tools.Enabled || settings.Memory.Enabled || settings.Skills.Enabled || settings.Skills.AutoDraft || settings.Skills.AutoActivate || settings.Skills.Learning.Enabled || settings.Evaluation.Judge {
		return collateral{}, ErrInvalid
	}
	return result, nil
}

func loopbackReleaseEndpoint(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	host := u.Hostname()
	return (host == "127.0.0.1" || host == "::1" || host == "localhost") && u.Path == ""
}

func canonicalText(body []byte) bool {
	return len(body) > 0 && utf8.Valid(body) && bytes.IndexByte(body, 0) < 0 && bytes.IndexByte(body, '\r') < 0 && body[len(body)-1] == '\n'
}

func releaseEntries(c collateral, notice, sbom, binary []byte) ([]Entry, []archiveEntryMetadata, error) {
	entries := []Entry{{installName, c.install}, {licenseName, c.license}, {releaseNotesName, c.notes}, {sbomName, sbom}, {noticeName, notice}, {configName, c.config}, {"nexus", binary}}
	metadata := make([]archiveEntryMetadata, len(entries))
	for i, entry := range entries {
		contract := archiveContract[i]
		if entry.Name != contract.name || len(entry.Data) < 1 || int64(len(entry.Data)) > contract.max {
			return nil, nil, ErrInvalid
		}
		digest := sha256.Sum256(entry.Data)
		metadata[i] = archiveEntryMetadata{Name: entry.Name, Mode: contract.mode, Size: int64(len(entry.Data)), SHA256: hex.EncodeToString(digest[:])}
	}
	return entries, metadata, nil
}

func validEntryMetadata(got archiveEntryMetadata, index int, body []byte) bool {
	if index < 0 || index >= len(archiveContract) {
		return false
	}
	contract := archiveContract[index]
	digest := sha256.Sum256(body)
	return got.Name == contract.name && got.Mode == contract.mode && got.Size == int64(len(body)) && got.Size >= 1 && got.Size <= contract.max && got.SHA256 == hex.EncodeToString(digest[:])
}
