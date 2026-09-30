package releasepack

import (
	"bytes"
	"encoding/json"
	"os"
	"regexp"
)

var signedToolchain = regexp.MustCompile(`^go(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(\.(0|[1-9][0-9]*))?$`)

// The release manifest is the canonical encoding emitted by Package. Requiring
// that encoding also rejects duplicate keys, aliases, unknown fields and nulls,
// avoiding differences between consumers of an authenticated JSON document.
func validateSignedManifest(root *os.Root, digests map[string]string) error {
	if len(digests) != 5 || digests["manifest.json"] == "" {
		return ErrSignature
	}
	body, err := readReleaseFile(root, "manifest.json", 64<<10)
	if err != nil {
		return ErrSignature
	}
	var manifest Manifest
	if json.Unmarshal(body, &manifest) != nil || manifest.SchemaVersion != releaseManifestSchema ||
		validate(Options{Version: manifest.Version, Commit: manifest.Commit, Out: "release"}) != nil ||
		!validSPDXCreated(manifest.Created) || len(manifest.Toolchain) > 64 || !signedToolchain.MatchString(manifest.Toolchain) || len(manifest.Artifacts) != 4 {
		return ErrSignature
	}
	canonical, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil || !bytes.Equal(body, append(canonical, '\n')) {
		return ErrSignature
	}
	targets := [4][2]string{{"darwin", "amd64"}, {"darwin", "arm64"}, {"linux", "amd64"}, {"linux", "arm64"}}
	sharedIndexes := [...]int{0, 1, 2, 5}
	var shared [len(sharedIndexes)]archiveEntryMetadata
	for i, target := range targets {
		artifact := manifest.Artifacts[i]
		name := "NexusRouter_" + manifest.Version + "_" + target[0] + "_" + target[1] + ".tar.gz"
		if artifact.OS != target[0] || artifact.Arch != target[1] || artifact.File != name || artifact.SHA256 == "" || digests[name] != artifact.SHA256 || len(artifact.Entries) != len(archiveContract) {
			return ErrSignature
		}
		if i == 0 {
			for j, index := range sharedIndexes {
				shared[j] = artifact.Entries[index]
			}
		} else {
			for j, index := range sharedIndexes {
				if artifact.Entries[index] != shared[j] {
					return ErrSignature
				}
			}
		}
		if validateReleaseArchive(root, manifest, artifact) != nil {
			return ErrSignature
		}
	}
	return nil
}
