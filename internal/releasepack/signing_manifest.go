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
	if json.Unmarshal(body, &manifest) != nil || manifest.SchemaVersion != 1 ||
		validate(Options{Version: manifest.Version, Commit: manifest.Commit, Out: "release"}) != nil ||
		len(manifest.Toolchain) > 64 || !signedToolchain.MatchString(manifest.Toolchain) || len(manifest.Artifacts) != 4 {
		return ErrSignature
	}
	canonical, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil || !bytes.Equal(body, append(canonical, '\n')) {
		return ErrSignature
	}
	targets := [4][2]string{{"darwin", "amd64"}, {"darwin", "arm64"}, {"linux", "amd64"}, {"linux", "arm64"}}
	for i, target := range targets {
		artifact := manifest.Artifacts[i]
		name := "DarwinRouter_" + manifest.Version + "_" + target[0] + "_" + target[1] + ".tar.gz"
		if artifact.OS != target[0] || artifact.Arch != target[1] || artifact.File != name || artifact.SHA256 == "" || digests[name] != artifact.SHA256 {
			return ErrSignature
		}
		if validateReleaseArchive(root, artifact) != nil {
			return ErrSignature
		}
	}
	return nil
}
