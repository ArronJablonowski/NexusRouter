package releasepack

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	finalReleaseNotesSchema = 1
	releaseNotesTemplateH1  = "# DarwinRouter release notes — unreleased\n"
)

// CandidateFinalReleaseNotes binds the candidate identity to the exact notes
// bytes shipped in every archive and used as the GitHub Release body. The
// source collateral remains an immutable template; this derived digest avoids
// trying to commit a file containing the hash of its own source commit.
type CandidateFinalReleaseNotes struct {
	SchemaVersion int    `json:"schema_version"`
	SHA256        string `json:"sha256"`
}

func renderFinalReleaseNotes(template []byte, version, commit, created string) ([]byte, error) {
	if validate(Options{Version: version, Commit: commit, Out: "release"}) != nil ||
		!validSPDXCreated(created) || !canonicalText(template) ||
		!bytes.HasPrefix(template, []byte(releaseNotesTemplateH1)) {
		return nil, ErrInvalid
	}
	body := template[len(releaseNotesTemplateH1):]
	if len(bytes.TrimSpace(body)) == 0 || !validFinalReleaseNotesBody(body) {
		return nil, ErrInvalid
	}
	var notes bytes.Buffer
	fmt.Fprintf(&notes, "# DarwinRouter %s release notes\n\n", version)
	fmt.Fprintf(&notes, "- Release version: `%s`\n", version)
	fmt.Fprintf(&notes, "- Source commit: `%s`\n", commit)
	fmt.Fprintf(&notes, "- Release date (UTC): `%s`\n", created)
	notes.WriteString("- Supported artifact targets:\n")
	for _, target := range candidateTargets {
		fmt.Fprintf(&notes, "  - `%s/%s`\n", target.OS, target.Arch)
	}
	notes.WriteString("\n## Release contract and limitations\n\n")
	notes.WriteString("- No packaged target outside the four listed above is supported by this release contract.\n")
	notes.WriteString("- The bundled SPDX inventory is not vulnerability analysis, independent build provenance, or legal approval.\n")
	notes.WriteString("- These notes identify candidate bytes but do not grant target, legal, signing, or publication approval.\n\n")
	notes.Write(body)
	result := notes.Bytes()
	if len(result) > maxReleaseNotes || !canonicalText(result) {
		return nil, ErrInvalid
	}
	return append([]byte(nil), result...), nil
}

func validFinalReleaseNotesBody(body []byte) bool {
	for _, line := range strings.Split(string(body), "\n") {
		if (strings.HasPrefix(line, "# DarwinRouter ") && strings.HasSuffix(line, " release notes")) ||
			strings.HasPrefix(line, "- Release version:") || strings.HasPrefix(line, "- Source commit:") ||
			strings.HasPrefix(line, "- Release date (UTC):") || line == "- Supported artifact targets:" ||
			line == "## Release contract and limitations" {
			return false
		}
		for _, target := range candidateTargets {
			if line == fmt.Sprintf("  - `%s/%s`", target.OS, target.Arch) {
				return false
			}
		}
	}
	return true
}

func finalReleaseNotesRecord(template []byte, version, commit, created string) (CandidateFinalReleaseNotes, error) {
	notes, err := renderFinalReleaseNotes(template, version, commit, created)
	if err != nil {
		return CandidateFinalReleaseNotes{}, err
	}
	digest := sha256.Sum256(notes)
	return CandidateFinalReleaseNotes{SchemaVersion: finalReleaseNotesSchema, SHA256: hex.EncodeToString(digest[:])}, nil
}

// WriteFinalReleaseNotes exclusively materializes the exact candidate-bound
// notes body for review and publication. It grants no approval or publication
// authority, and the output must remain outside the source checkout.
func WriteFinalReleaseNotes(ctx context.Context, recordPath, source, out string) error {
	if ctx == nil || recordPath == "" || source == "" || out == "" || ctx.Err() != nil {
		return ErrInvalid
	}
	root, err := filepath.Abs(source)
	if err != nil {
		return ErrInvalid
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return ErrInvalid
	}
	destination, err := filepath.Abs(out)
	if err != nil {
		return ErrInvalid
	}
	outputRoot, outputName, err := candidateOutputRoot(destination, root)
	if err != nil {
		return err
	}
	defer outputRoot.Close()
	_, candidate, err := readCandidateRecord(recordPath)
	if err != nil || verifyCandidateRecord(ctx, candidate, root) != nil {
		return ErrInvalid
	}
	template, err := loadCollateral(root)
	if err != nil {
		return ErrInvalid
	}
	notes, err := renderFinalReleaseNotes(template.notes, candidate.ReleaseVersion, candidate.SourceCommit, candidate.ReleaseCreated)
	if err != nil {
		return ErrInvalid
	}
	digest := sha256.Sum256(notes)
	if hex.EncodeToString(digest[:]) != candidate.FinalReleaseNotes.SHA256 ||
		verifyCandidateCheckout(ctx, root, candidate.SourceCommit, environment()) != nil || ctx.Err() != nil {
		return ErrInvalid
	}
	file, err := outputRoot.OpenFile(outputName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(notes)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}
