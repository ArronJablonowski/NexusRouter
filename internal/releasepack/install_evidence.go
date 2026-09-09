package releasepack

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"github.com/ArronJablonowski/DarwinRouter/internal/stateschema"
)

var ErrInstallRehearsalEvidence = errors.New("install rehearsal evidence verification failed")

const (
	installEvidenceSchema = 1
	installEvidenceScope  = "darwinrouter-native-install-migration-rehearsal"
	maxInstallEvidence    = 32 << 10
)

var installArtifactName = regexp.MustCompile(`^DarwinRouter_[0-9A-Za-z.-]+_(darwin|linux)_(amd64|arm64)\.tar\.gz$`)

// InstallRehearsalEvidence is a path-free record produced only after the native
// archive rehearsal has observed every listed result. It contains no operator
// configuration, database contents, credentials, or host paths.
type InstallRehearsalEvidence struct {
	SchemaVersion int                      `json:"schema_version"`
	Scope         string                   `json:"scope"`
	Release       InstallEvidenceRelease   `json:"release"`
	Target        NativeEvidenceTarget     `json:"native_target"`
	Artifact      InstallEvidenceArtifact  `json:"artifact"`
	Installation  InstallEvidenceInstall   `json:"installation"`
	Source        InstallEvidenceSource    `json:"source_state"`
	Backup        InstallEvidenceBackup    `json:"backup"`
	Migration     InstallEvidenceMigration `json:"migration"`
	Rollback      InstallEvidenceRollback  `json:"rollback"`
}

type InstallEvidenceRelease struct {
	Version string `json:"version"`
	Commit  string `json:"source_commit"`
}

type InstallEvidenceArtifact struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

type InstallEvidenceInstall struct {
	BinaryVersion      string `json:"binary_version"`
	PrivatePermissions string `json:"private_permissions"`
	Configuration      string `json:"configuration_validation"`
	DaemonStart        string `json:"daemon_start"`
	ExactWriterStop    string `json:"exact_writer_stop"`
}

type InstallEvidenceSource struct {
	Schema     int    `json:"schema"`
	QuickCheck string `json:"quick_check"`
	Quiescence string `json:"quiescence"`
}

type InstallEvidenceBackup struct {
	SHA256     string `json:"sha256"`
	Schema     int    `json:"schema"`
	QuickCheck string `json:"quick_check"`
}

type InstallEvidenceMigration struct {
	Schema                   int    `json:"schema"`
	QuickCheck               string `json:"quick_check"`
	PreservedRecordSHA256    string `json:"preserved_record_sha256"`
	TaskTimingPreserved      string `json:"task_timing_preserved"`
	LegacyUsageNotFabricated string `json:"legacy_usage_not_fabricated"`
}

type InstallEvidenceRollback struct {
	DatabaseSHA256 string `json:"database_sha256"`
	Schema         int    `json:"schema"`
	Pairing        string `json:"pairing"`
	BinaryVersion  string `json:"smoke_binary_version"`
	TargetOS       string `json:"smoke_target_os"`
	TargetArch     string `json:"smoke_target_arch"`
	Smoke          string `json:"smoke"`
}

// InstallRehearsalExpectations must be obtained independently of the retained
// record. Verification compares every release and artifact identity.
type InstallRehearsalExpectations struct {
	RecordSHA256   string
	Version        string
	Commit         string
	TargetOS       string
	TargetArch     string
	ArtifactName   string
	ArtifactSHA256 string
	SourceSchema   int
	CurrentSchema  int
	BackupSHA256   string
}

func (e InstallRehearsalExpectations) Validate() error {
	if !validInstallExpectations(e) {
		return ErrInstallRehearsalEvidence
	}
	return nil
}

type InstallRehearsalVerification struct {
	RecordSHA256   string `json:"record_sha256"`
	ReleaseVersion string `json:"release_version"`
	SourceCommit   string `json:"source_commit"`
	TargetOS       string `json:"target_os"`
	TargetArch     string `json:"target_arch"`
	ArtifactName   string `json:"artifact_name"`
	ArtifactSHA256 string `json:"artifact_sha256"`
	SourceSchema   int    `json:"source_schema"`
	CurrentSchema  int    `json:"current_schema"`
	BackupSHA256   string `json:"backup_sha256"`
	RollbackSchema int    `json:"rollback_schema"`
}

func ParseInstallRehearsalEvidence(body []byte) (InstallRehearsalEvidence, error) {
	var record InstallRehearsalEvidence
	if len(body) == 0 || len(body) > maxInstallEvidence || json.Unmarshal(body, &record) != nil || validateInstallEvidence(record) != nil {
		return InstallRehearsalEvidence{}, ErrInstallRehearsalEvidence
	}
	canonical, err := json.MarshalIndent(record, "", "  ")
	if err != nil || !bytes.Equal(body, append(canonical, '\n')) {
		return InstallRehearsalEvidence{}, ErrInstallRehearsalEvidence
	}
	return record, nil
}

func VerifyInstallRehearsalEvidence(path string, expected InstallRehearsalExpectations) (InstallRehearsalVerification, error) {
	var result InstallRehearsalVerification
	if !validInstallExpectations(expected) {
		return result, ErrInstallRehearsalEvidence
	}
	body, err := readInstallEvidence(path)
	if err != nil || installEvidenceDigest(body) != expected.RecordSHA256 {
		return result, ErrInstallRehearsalEvidence
	}
	record, err := ParseInstallRehearsalEvidence(body)
	if err != nil || !installEvidenceMatches(record, expected) {
		return result, ErrInstallRehearsalEvidence
	}
	return InstallRehearsalVerification{
		RecordSHA256: expected.RecordSHA256, ReleaseVersion: record.Release.Version,
		SourceCommit: record.Release.Commit, TargetOS: record.Target.OS, TargetArch: record.Target.Arch,
		ArtifactName: record.Artifact.Name, ArtifactSHA256: record.Artifact.SHA256,
		SourceSchema: record.Source.Schema, CurrentSchema: record.Migration.Schema,
		BackupSHA256: record.Backup.SHA256, RollbackSchema: record.Rollback.Schema,
	}, nil
}

// retainInstallRehearsalEvidence is intentionally package-private. The native
// rehearsal calls it with observations captured inline; external callers can
// verify records but cannot use the package as a claim-signing API.
func retainInstallRehearsalEvidence(source, out string, record InstallRehearsalEvidence) error {
	if validateInstallEvidence(record) != nil || source == "" || out == "" {
		return ErrInstallRehearsalEvidence
	}
	source, err := filepath.Abs(source)
	if err != nil {
		return ErrInstallRehearsalEvidence
	}
	out, err = filepath.Abs(out)
	if err != nil {
		return ErrInstallRehearsalEvidence
	}
	root, name, err := candidateOutputRoot(out, source)
	if err != nil {
		return err
	}
	defer root.Close()
	body, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if _, err = ParseInstallRehearsalEvidence(body); err != nil {
		return err
	}
	f, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(body)
	syncErr, closeErr := f.Sync(), f.Close()
	if writeErr != nil {
		return writeErr
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	syncErr, closeErr = directory.Sync(), directory.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func validateInstallEvidence(r InstallRehearsalEvidence) error {
	passed := func(value string) bool { return value == "passed" }
	expectedName := "DarwinRouter_" + r.Release.Version + "_" + r.Target.OS + "_" + r.Target.Arch + ".tar.gz"
	if r.SchemaVersion != installEvidenceSchema || r.Scope != installEvidenceScope ||
		validate(Options{Version: r.Release.Version, Commit: r.Release.Commit, Out: "evidence"}) != nil ||
		(r.Target.OS != "darwin" && r.Target.OS != "linux") || (r.Target.Arch != "amd64" && r.Target.Arch != "arm64") ||
		!installArtifactName.MatchString(r.Artifact.Name) || r.Artifact.Name != expectedName || !validInstallDigest(r.Artifact.SHA256) ||
		r.Installation.BinaryVersion != r.Release.Version || !passed(r.Installation.PrivatePermissions) || !passed(r.Installation.Configuration) || !passed(r.Installation.DaemonStart) || !passed(r.Installation.ExactWriterStop) ||
		r.Source.Schema != 29 || r.Source.QuickCheck != "ok" || !passed(r.Source.Quiescence) ||
		r.Backup.Schema != r.Source.Schema || r.Backup.QuickCheck != "ok" || !validInstallDigest(r.Backup.SHA256) ||
		r.Migration.Schema != stateschema.Current || r.Migration.QuickCheck != "ok" || !validInstallDigest(r.Migration.PreservedRecordSHA256) || !passed(r.Migration.TaskTimingPreserved) || !passed(r.Migration.LegacyUsageNotFabricated) ||
		r.Rollback.DatabaseSHA256 != r.Backup.SHA256 || r.Rollback.Schema != r.Source.Schema || r.Rollback.Pairing != "current_binary_read_only_schema_fixture" || r.Rollback.BinaryVersion != r.Release.Version || r.Rollback.TargetOS != r.Target.OS || r.Rollback.TargetArch != r.Target.Arch || !passed(r.Rollback.Smoke) {
		return ErrInstallRehearsalEvidence
	}
	return nil
}

func validInstallExpectations(e InstallRehearsalExpectations) bool {
	return validInstallDigest(e.RecordSHA256) && validate(Options{Version: e.Version, Commit: e.Commit, Out: "evidence"}) == nil &&
		(e.TargetOS == "darwin" || e.TargetOS == "linux") && (e.TargetArch == "amd64" || e.TargetArch == "arm64") &&
		installArtifactName.MatchString(e.ArtifactName) && validInstallDigest(e.ArtifactSHA256) && e.SourceSchema == 29 && e.CurrentSchema == stateschema.Current && validInstallDigest(e.BackupSHA256)
}

func installEvidenceMatches(r InstallRehearsalEvidence, e InstallRehearsalExpectations) bool {
	return r.Release.Version == e.Version && r.Release.Commit == e.Commit && r.Target.OS == e.TargetOS && r.Target.Arch == e.TargetArch &&
		r.Artifact.Name == e.ArtifactName && r.Artifact.SHA256 == e.ArtifactSHA256 && r.Source.Schema == e.SourceSchema &&
		r.Migration.Schema == e.CurrentSchema && r.Backup.SHA256 == e.BackupSHA256
}

func readInstallEvidence(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maxInstallEvidence {
		return nil, ErrInstallRehearsalEvidence
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, ErrInstallRehearsalEvidence
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil || !actual.Mode().IsRegular() || !os.SameFile(info, actual) || actual.Size() != info.Size() {
		return nil, ErrInstallRehearsalEvidence
	}
	body, err := io.ReadAll(io.LimitReader(f, maxInstallEvidence+1))
	if err != nil || int64(len(body)) != actual.Size() {
		return nil, ErrInstallRehearsalEvidence
	}
	return body, nil
}

func installEvidenceDigest(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func validInstallDigest(value string) bool {
	return len(value) == 71 && value[:7] == "sha256:" && trustFingerprint(value)
}
