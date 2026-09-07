package releasepack

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	candidateSchema = 1
	maxCandidate    = 64 << 10
)

// CandidateRecord is a canonical, reviewable statement of the proposed source
// identity and release contract. It deliberately records operator-controlled
// decisions as unapproved; a generated record is evidence of neither approval
// nor qualification.
type CandidateRecord struct {
	SchemaVersion         int                   `json:"schema_version"`
	ReleaseVersion        string                `json:"release_version"`
	SourceCommit          string                `json:"source_commit"`
	ReleaseManifestSchema int                   `json:"release_manifest_schema"`
	Targets               []CandidateTarget     `json:"targets"`
	ArchiveEntries        []CandidateEntry      `json:"archive_entries"`
	SourceCollateral      []CandidateCollateral `json:"source_collateral"`
	OperatorGates         []CandidateGate       `json:"operator_gates"`
}

type CandidateTarget struct {
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	Decision string `json:"decision"`
}

type CandidateEntry struct {
	Name     string `json:"name"`
	Mode     uint32 `json:"mode"`
	MaxBytes int64  `json:"max_bytes"`
	Shared   bool   `json:"shared_across_targets"`
}

type CandidateCollateral struct {
	Source string `json:"source"`
	Entry  string `json:"entry"`
	SHA256 string `json:"sha256"`
}

type CandidateGate struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

var candidateTargets = []CandidateTarget{
	{OS: "darwin", Arch: "amd64", Decision: "unapproved"},
	{OS: "darwin", Arch: "arm64", Decision: "unapproved"},
	{OS: "linux", Arch: "amd64", Decision: "unapproved"},
	{OS: "linux", Arch: "arm64", Decision: "unapproved"},
}

var candidateDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

var candidateGates = []CandidateGate{
	{Name: "release_version", Status: "unapproved"},
	{Name: "supported_targets", Status: "unapproved"},
	{Name: "third_party_notices", Status: "unapproved"},
	{Name: "production_signing", Status: "unapproved"},
	{Name: "publication", Status: "unapproved"},
}

// FreezeCandidate writes a new canonical candidate record from the immutable
// Git snapshot named by Commit. The source checkout must be clean and at that
// exact commit both before and after the record is constructed.
func FreezeCandidate(ctx context.Context, o Options) error {
	if ctx == nil || validate(o) != nil || o.Source == "" {
		return ErrInvalid
	}
	source, err := filepath.Abs(o.Source)
	if err != nil {
		return ErrInvalid
	}
	out, err := filepath.Abs(o.Out)
	if err != nil {
		return ErrInvalid
	}
	outputRoot, outputName, err := candidateOutputRoot(out, source)
	if err != nil {
		return err
	}
	defer outputRoot.Close()
	env := environment()
	root, err := command(ctx, source, env, "git", "rev-parse", "--show-toplevel")
	if err != nil || root != source || verifyCandidateCheckout(ctx, source, o.Commit, env) != nil {
		return ErrInvalid
	}
	snapshotDir, err := os.MkdirTemp(filepath.Dir(out), ".darwin-candidate-source-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(snapshotDir)
	if err = snapshot(ctx, source, o.Commit, snapshotDir, env); err != nil {
		return err
	}
	record, err := candidateRecord(o.Version, o.Commit, snapshotDir)
	if err != nil {
		return err
	}
	body, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if len(body) > maxCandidate || validateCandidate(body) != nil {
		return ErrInvalid
	}
	if err = verifyCandidateCheckout(ctx, source, o.Commit, env); err != nil {
		return err
	}
	file, err := outputRoot.OpenFile(outputName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(body)
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

// VerifyCandidate checks the canonical record and re-derives it from the exact
// commit in a clean checkout. It does not approve the recorded operator gates.
func VerifyCandidate(ctx context.Context, recordPath, source string) error {
	if ctx == nil || recordPath == "" || source == "" {
		return ErrInvalid
	}
	_, record, err := readCandidateRecord(recordPath)
	if err != nil {
		return err
	}
	return verifyCandidateRecord(ctx, record, source)
}

func readCandidateRecord(recordPath string) ([]byte, CandidateRecord, error) {
	info, err := os.Lstat(recordPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maxCandidate {
		return nil, CandidateRecord{}, ErrInvalid
	}
	file, err := os.Open(recordPath)
	if err != nil {
		return nil, CandidateRecord{}, ErrInvalid
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) || opened.Size() != info.Size() {
		return nil, CandidateRecord{}, ErrInvalid
	}
	body, err := io.ReadAll(io.LimitReader(file, maxCandidate+1))
	if err != nil || int64(len(body)) != opened.Size() {
		return nil, CandidateRecord{}, ErrInvalid
	}
	record, err := parseCandidate(body)
	if err != nil {
		return nil, CandidateRecord{}, err
	}
	return body, record, nil
}

func verifyCandidateRecord(ctx context.Context, record CandidateRecord, source string) error {
	root, err := filepath.Abs(source)
	if err != nil {
		return ErrInvalid
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return ErrInvalid
	}
	env := environment()
	top, err := command(ctx, root, env, "git", "rev-parse", "--show-toplevel")
	if err != nil || top != root || verifyCandidateCheckout(ctx, root, record.SourceCommit, env) != nil {
		return ErrInvalid
	}
	snapshotDir, err := os.MkdirTemp("", ".darwin-candidate-verify-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(snapshotDir)
	if err = snapshot(ctx, root, record.SourceCommit, snapshotDir, env); err != nil {
		return err
	}
	expected, err := candidateRecord(record.ReleaseVersion, record.SourceCommit, snapshotDir)
	if err != nil || expectedBody(record, expected) != nil {
		return ErrInvalid
	}
	return verifyCandidateCheckout(ctx, root, record.SourceCommit, env)
}

func candidateOutputRoot(out, source string) (*os.Root, string, error) {
	parent, name := filepath.Dir(out), filepath.Base(out)
	if name == "." || name == string(filepath.Separator) || name == "" {
		return nil, "", ErrInvalid
	}
	parentInfo, err := os.Lstat(parent)
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		return nil, "", ErrInvalid
	}
	parentReal, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return nil, "", ErrInvalid
	}
	sourceReal, err := filepath.EvalSymlinks(source)
	if err != nil {
		return nil, "", ErrInvalid
	}
	relative, err := filepath.Rel(sourceReal, parentReal)
	insideSource := relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
	if err != nil || insideSource {
		return nil, "", ErrInvalid
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		return nil, "", err
	}
	actual, err := root.Stat(".")
	if err != nil || !os.SameFile(parentInfo, actual) {
		root.Close()
		return nil, "", ErrInvalid
	}
	if _, err = root.Lstat(name); !os.IsNotExist(err) {
		root.Close()
		return nil, "", ErrInvalid
	}
	return root, name, nil
}

func verifyCandidateCheckout(ctx context.Context, source, commit string, env []string) error {
	head, err := command(ctx, source, env, "git", "rev-parse", "HEAD")
	if err != nil || head != commit {
		return ErrInvalid
	}
	status, err := command(ctx, source, env, "git", "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil || status != "" {
		return ErrInvalid
	}
	return nil
}

func candidateRecord(version, commit, source string) (CandidateRecord, error) {
	shared, err := loadCollateral(source)
	if err != nil {
		return CandidateRecord{}, err
	}
	entries := make([]CandidateEntry, len(archiveContract))
	for i, entry := range archiveContract {
		entries[i] = CandidateEntry{Name: entry.name, Mode: entry.mode, MaxBytes: entry.max, Shared: entry.name != noticeName && entry.name != "darwin"}
	}
	collateral := []struct {
		source, entry string
		body          []byte
	}{
		{"docs/release-install.md", installName, shared.install},
		{licenseName, licenseName, shared.license},
		{"docs/release-notes.md", releaseNotesName, shared.notes},
		{"examples/local.yaml", configName, shared.config},
	}
	sources := make([]CandidateCollateral, len(collateral))
	for i, item := range collateral {
		digest := sha256.Sum256(item.body)
		sources[i] = CandidateCollateral{Source: item.source, Entry: item.entry, SHA256: hex.EncodeToString(digest[:])}
	}
	return CandidateRecord{
		SchemaVersion:         candidateSchema,
		ReleaseVersion:        version,
		SourceCommit:          commit,
		ReleaseManifestSchema: 2,
		Targets:               append([]CandidateTarget(nil), candidateTargets...),
		ArchiveEntries:        entries,
		SourceCollateral:      sources,
		OperatorGates:         append([]CandidateGate(nil), candidateGates...),
	}, nil
}

func parseCandidate(body []byte) (CandidateRecord, error) {
	var record CandidateRecord
	if len(body) < 1 || len(body) > maxCandidate || json.Unmarshal(body, &record) != nil {
		return CandidateRecord{}, ErrInvalid
	}
	canonical, err := json.MarshalIndent(record, "", "  ")
	if err != nil || !bytes.Equal(body, append(canonical, '\n')) || validateCandidateRecord(record) != nil {
		return CandidateRecord{}, ErrInvalid
	}
	return record, nil
}

func validateCandidate(body []byte) error {
	_, err := parseCandidate(body)
	return err
}

func validateCandidateRecord(record CandidateRecord) error {
	if record.SchemaVersion != candidateSchema || record.ReleaseManifestSchema != 2 ||
		validate(Options{Version: record.ReleaseVersion, Commit: record.SourceCommit, Out: "release"}) != nil ||
		len(record.Targets) != len(candidateTargets) || len(record.ArchiveEntries) != len(archiveContract) ||
		len(record.SourceCollateral) != 4 || len(record.OperatorGates) != len(candidateGates) {
		return ErrInvalid
	}
	for i := range candidateTargets {
		if record.Targets[i] != candidateTargets[i] {
			return ErrInvalid
		}
	}
	for i, contract := range archiveContract {
		want := CandidateEntry{Name: contract.name, Mode: contract.mode, MaxBytes: contract.max, Shared: contract.name != noticeName && contract.name != "darwin"}
		if record.ArchiveEntries[i] != want {
			return ErrInvalid
		}
	}
	wantSources := [][2]string{{"docs/release-install.md", installName}, {licenseName, licenseName}, {"docs/release-notes.md", releaseNotesName}, {"examples/local.yaml", configName}}
	for i, want := range wantSources {
		got := record.SourceCollateral[i]
		if got.Source != want[0] || got.Entry != want[1] || !candidateDigestPattern.MatchString(got.SHA256) {
			return ErrInvalid
		}
	}
	for i := range candidateGates {
		if record.OperatorGates[i] != candidateGates[i] {
			return ErrInvalid
		}
	}
	return nil
}

func expectedBody(got, expected CandidateRecord) error {
	a, err := json.Marshal(got)
	if err != nil {
		return err
	}
	b, err := json.Marshal(expected)
	if err != nil || !bytes.Equal(a, b) {
		return ErrInvalid
	}
	return nil
}
