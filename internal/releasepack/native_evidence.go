package releasepack

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/stateschema"
)

const (
	nativeEvidenceSchema = 1
	maxNativeEvidence    = 32 << 10
)

var ErrNativeEvidenceVerification = errors.New("native evidence verification failed")

// NativeEvidence is one host's canonical qualification result. It deliberately
// has no field capable of claiming that another target was executed.
type NativeEvidence struct {
	SchemaVersion    int                           `json:"schema_version"`
	Scope            string                        `json:"scope"`
	ReleaseVersion   string                        `json:"release_version"`
	SourceCommit     string                        `json:"source_commit"`
	Target           NativeEvidenceTarget          `json:"target"`
	Toolchain        NativeEvidenceGo              `json:"toolchain"`
	Gates            []NativeEvidenceGate          `json:"gates"`
	InstallRehearsal *NativeInstallEvidenceBinding `json:"install_rehearsal,omitempty"`
}

type NativeInstallEvidenceBinding struct {
	RecordSHA256   string `json:"record_sha256"`
	ArtifactName   string `json:"artifact_name"`
	ArtifactSHA256 string `json:"artifact_sha256"`
	BackupSHA256   string `json:"backup_sha256"`
	SourceSchema   int    `json:"source_schema"`
	CurrentSchema  int    `json:"current_schema"`
}

type NativeEvidenceTarget struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

type NativeEvidenceGo struct {
	GOHOSTOS   string `json:"gohostos"`
	GOHOSTARCH string `json:"gohostarch"`
	GOVERSION  string `json:"goversion"`
}

type NativeEvidenceGate struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// NativeEvidenceExpectations must come from a channel independent of the
// retained native evidence bundle. Every identity and digest needed to bind a
// schema-2 native record to its install-rehearsal companion is explicit.
type NativeEvidenceExpectations struct {
	RecordSHA256                 string
	InstallRehearsalRecordSHA256 string
	Version                      string
	Commit                       string
	TargetOS                     string
	TargetArch                   string
	GoVersion                    string
	ArtifactName                 string
	ArtifactSHA256               string
	SourceSchema                 int
	CurrentSchema                int
	BackupSHA256                 string
}

// Validate rejects incomplete or internally inconsistent external
// expectations before either retained record is read.
func (e NativeEvidenceExpectations) Validate() error {
	if !validNativeEvidenceExpectations(e) {
		return ErrNativeEvidenceVerification
	}
	return nil
}

// NativeEvidenceVerification is the path-free result of verifying both exact
// canonical records in a native evidence bundle.
type NativeEvidenceVerification struct {
	RecordSHA256                 string `json:"record_sha256"`
	InstallRehearsalRecordSHA256 string `json:"install_rehearsal_record_sha256"`
	ReleaseVersion               string `json:"release_version"`
	SourceCommit                 string `json:"source_commit"`
	TargetOS                     string `json:"target_os"`
	TargetArch                   string `json:"target_arch"`
	GOVersion                    string `json:"go_version"`
	ArtifactName                 string `json:"artifact_name"`
	ArtifactSHA256               string `json:"artifact_sha256"`
	SourceSchema                 int    `json:"source_schema"`
	CurrentSchema                int    `json:"current_schema"`
	BackupSHA256                 string `json:"backup_sha256"`
	RollbackSchema               int    `json:"rollback_schema"`
}

var nativeEvidenceGates = []NativeEvidenceGate{
	{Name: "source_bound_clean_before", Status: "passed"},
	{Name: "make_check", Status: "passed"},
	{Name: "source_unchanged_after_check", Status: "passed"},
	{Name: "make_qualify_release", Status: "passed"},
	{Name: "source_unchanged_after_qualification", Status: "passed"},
}

type nativeGoEnvironment struct {
	GOOS       string `json:"GOOS"`
	GOARCH     string `json:"GOARCH"`
	GOHOSTOS   string `json:"GOHOSTOS"`
	GOHOSTARCH string `json:"GOHOSTARCH"`
	GOVERSION  string `json:"GOVERSION"`
}

// QualifyNativeRelease runs both release gates itself, writes a bounded complete
// transcript to log, and creates evidence only after the exact source remains
// clean. The evidence file must be outside the checkout and is created exclusively.
func QualifyNativeRelease(ctx context.Context, o Options, log io.Writer) error {
	if ctx == nil || validate(o) != nil || o.Source == "" || log == nil {
		return ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	transcript := &strictNativeLog{destination: log}
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
	var installOut string
	if o.InstallEvidenceOut != "" {
		installOut, err = filepath.Abs(o.InstallEvidenceOut)
		if err != nil || installOut == out {
			return ErrInvalid
		}
		installRoot, _, preflightErr := candidateOutputRoot(installOut, source)
		if preflightErr != nil {
			return preflightErr
		}
		if closeErr := installRoot.Close(); closeErr != nil {
			return closeErr
		}
	}
	env := environment()
	top, err := command(ctx, source, env, "git", "rev-parse", "--show-toplevel")
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	if err != nil || top != source || verifyCandidateCheckout(ctx, source, o.Commit, env) != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		return ErrInvalid
	}
	hostJSON, err := command(ctx, source, env, "go", "env", "-json", "GOOS", "GOARCH", "GOHOSTOS", "GOHOSTARCH", "GOVERSION")
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	if err != nil {
		return err
	}
	var observed nativeGoEnvironment
	if json.Unmarshal([]byte(hostJSON), &observed) != nil || validateNativeGo(observed) != nil {
		return ErrInvalid
	}
	if _, err = fmt.Fprintf(transcript, "darwin-native-evidence version=%s commit=%s target=%s/%s gohost=%s/%s go=%s\n", o.Version, o.Commit, observed.GOOS, observed.GOARCH, observed.GOHOSTOS, observed.GOHOSTARCH, observed.GOVERSION); err != nil {
		return err
	}
	if err = nativeGateCommand(ctx, source, env, "check", transcript); err != nil {
		return err
	}
	if err = verifyCandidateCheckout(ctx, source, o.Commit, env); err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		return err
	}
	qualificationEnv := append(append([]string(nil), env...), "DARWIN_RELEASE_VERSION="+o.Version, "DARWIN_RELEASE_COMMIT="+o.Commit)
	if installOut != "" {
		qualificationEnv = append(qualificationEnv, "DARWIN_INSTALL_REHEARSAL_EVIDENCE_OUT="+installOut)
	}
	if err = nativeGateCommand(ctx, source, qualificationEnv, "qualify-mvp", transcript); err != nil {
		return err
	}
	if err = verifyCandidateCheckout(ctx, source, o.Commit, env); err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		return err
	}
	if err = nativeGateCommand(ctx, source, qualificationEnv, "qualify-release-test", transcript); err != nil {
		return err
	}
	if err = verifyCandidateCheckout(ctx, source, o.Commit, env); err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	record := NativeEvidence{
		SchemaVersion: nativeEvidenceSchema, Scope: "single_native_target_only",
		ReleaseVersion: o.Version, SourceCommit: o.Commit,
		Target:    NativeEvidenceTarget{OS: observed.GOOS, Arch: observed.GOARCH},
		Toolchain: NativeEvidenceGo{GOHOSTOS: observed.GOHOSTOS, GOHOSTARCH: observed.GOHOSTARCH, GOVERSION: observed.GOVERSION},
		Gates:     append([]NativeEvidenceGate(nil), nativeEvidenceGates...),
	}
	if installOut != "" {
		installBody, readErr := readInstallEvidence(installOut)
		if readErr != nil {
			return ErrInvalid
		}
		installRecord, parseErr := ParseInstallRehearsalEvidence(installBody)
		if parseErr != nil || installRecord.Release.Version != o.Version || installRecord.Release.Commit != o.Commit ||
			installRecord.Target.OS != observed.GOOS || installRecord.Target.Arch != observed.GOARCH {
			return ErrInvalid
		}
		record.SchemaVersion = 2
		record.InstallRehearsal = &NativeInstallEvidenceBinding{
			RecordSHA256: installEvidenceDigest(installBody), ArtifactName: installRecord.Artifact.Name,
			ArtifactSHA256: installRecord.Artifact.SHA256, BackupSHA256: installRecord.Backup.SHA256,
			SourceSchema: installRecord.Source.Schema, CurrentSchema: installRecord.Migration.Schema,
		}
		if _, err = fmt.Fprintf(transcript, "darwin-install-rehearsal-evidence record_sha256=%s artifact_name=%s artifact_sha256=%s backup_sha256=%s source_schema=%d current_schema=%d\n",
			record.InstallRehearsal.RecordSHA256, record.InstallRehearsal.ArtifactName, record.InstallRehearsal.ArtifactSHA256,
			record.InstallRehearsal.BackupSHA256, record.InstallRehearsal.SourceSchema, record.InstallRehearsal.CurrentSchema); err != nil {
			return err
		}
	}
	body, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if validateNativeEvidence(body) != nil {
		return ErrInvalid
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	f, err := outputRoot.OpenFile(outputName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = f.Close()
			_ = outputRoot.Remove(outputName)
			_ = syncNativeEvidenceRoot(outputRoot)
		}
	}()
	_, writeErr := f.Write(body)
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err = syncNativeEvidenceRoot(outputRoot); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	committed = true
	return nil
}

func syncNativeEvidenceRoot(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

// VerifyNativeEvidence validates exact canonical bytes. The unsigned record is
// retained evidence, not an independent attestation of the commands it names.
func VerifyNativeEvidence(path string) error {
	body, err := readNativeEvidence(path)
	if err != nil {
		return ErrInvalid
	}
	return validateNativeEvidence(body)
}

// VerifyNativeEvidenceAgainst verifies a complete schema-2 native evidence
// bundle without consulting the source checkout, a build tool, or the network.
// The primary and companion record digests and all release, target, toolchain,
// artifact, and schema identities are compared with independent expectations.
func VerifyNativeEvidenceAgainst(nativePath, installRehearsalPath string, expected NativeEvidenceExpectations) (NativeEvidenceVerification, error) {
	var result NativeEvidenceVerification
	if !validNativeEvidenceExpectations(expected) {
		return result, ErrNativeEvidenceVerification
	}
	body, err := readNativeEvidence(nativePath)
	if err != nil || nativeEvidenceDigest(body) != expected.RecordSHA256 {
		return result, ErrNativeEvidenceVerification
	}
	var record NativeEvidence
	if validateNativeEvidence(body) != nil || json.Unmarshal(body, &record) != nil || record.SchemaVersion != 2 || record.InstallRehearsal == nil {
		return result, ErrNativeEvidenceVerification
	}
	binding := record.InstallRehearsal
	if record.ReleaseVersion != expected.Version || record.SourceCommit != expected.Commit ||
		record.Target.OS != expected.TargetOS || record.Target.Arch != expected.TargetArch || record.Toolchain.GOVERSION != expected.GoVersion ||
		binding.RecordSHA256 != expected.InstallRehearsalRecordSHA256 || binding.ArtifactName != expected.ArtifactName ||
		binding.ArtifactSHA256 != expected.ArtifactSHA256 || binding.SourceSchema != expected.SourceSchema ||
		binding.CurrentSchema != expected.CurrentSchema || binding.BackupSHA256 != expected.BackupSHA256 {
		return result, ErrNativeEvidenceVerification
	}
	companion, err := VerifyInstallRehearsalEvidence(installRehearsalPath, InstallRehearsalExpectations{
		RecordSHA256: expected.InstallRehearsalRecordSHA256,
		Version:      expected.Version, Commit: expected.Commit,
		TargetOS: expected.TargetOS, TargetArch: expected.TargetArch,
		ArtifactName: expected.ArtifactName, ArtifactSHA256: expected.ArtifactSHA256,
		SourceSchema: expected.SourceSchema, CurrentSchema: expected.CurrentSchema,
		BackupSHA256: expected.BackupSHA256,
	})
	if err != nil {
		return result, ErrNativeEvidenceVerification
	}
	return NativeEvidenceVerification{
		RecordSHA256: expected.RecordSHA256, InstallRehearsalRecordSHA256: companion.RecordSHA256,
		ReleaseVersion: record.ReleaseVersion, SourceCommit: record.SourceCommit,
		TargetOS: record.Target.OS, TargetArch: record.Target.Arch, GOVersion: record.Toolchain.GOVERSION,
		ArtifactName: companion.ArtifactName, ArtifactSHA256: companion.ArtifactSHA256,
		SourceSchema: companion.SourceSchema, CurrentSchema: companion.CurrentSchema, BackupSHA256: companion.BackupSHA256,
		RollbackSchema: companion.RollbackSchema,
	}, nil
}

func validNativeEvidenceExpectations(e NativeEvidenceExpectations) bool {
	expectedArtifactName := "DarwinRouter_" + e.Version + "_" + e.TargetOS + "_" + e.TargetArch + ".tar.gz"
	if !validInstallDigest(e.RecordSHA256) || !validInstallDigest(e.InstallRehearsalRecordSHA256) ||
		validate(Options{Version: e.Version, Commit: e.Commit, Out: "evidence"}) != nil ||
		validateNativeGo(nativeGoEnvironment{GOOS: e.TargetOS, GOARCH: e.TargetArch, GOHOSTOS: e.TargetOS, GOHOSTARCH: e.TargetArch, GOVERSION: e.GoVersion}) != nil ||
		e.ArtifactName != expectedArtifactName {
		return false
	}
	return validInstallExpectations(InstallRehearsalExpectations{
		RecordSHA256: e.InstallRehearsalRecordSHA256,
		Version:      e.Version, Commit: e.Commit, TargetOS: e.TargetOS, TargetArch: e.TargetArch,
		ArtifactName: e.ArtifactName, ArtifactSHA256: e.ArtifactSHA256,
		SourceSchema: e.SourceSchema, CurrentSchema: e.CurrentSchema, BackupSHA256: e.BackupSHA256,
	})
}

func readNativeEvidence(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maxNativeEvidence {
		return nil, ErrInvalid
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, ErrInvalid
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil || !actual.Mode().IsRegular() || !os.SameFile(info, actual) || actual.Size() != info.Size() {
		return nil, ErrInvalid
	}
	body, err := io.ReadAll(io.LimitReader(f, maxNativeEvidence+1))
	if err != nil || int64(len(body)) != actual.Size() {
		return nil, ErrInvalid
	}
	return body, nil
}

func nativeEvidenceDigest(body []byte) string {
	digest := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func validateNativeEvidence(body []byte) error {
	var record NativeEvidence
	if len(body) < 1 || len(body) > maxNativeEvidence || json.Unmarshal(body, &record) != nil || validateNativeEvidenceRecord(record) != nil {
		return ErrInvalid
	}
	canonical, err := json.MarshalIndent(record, "", "  ")
	if err != nil || !bytes.Equal(body, append(canonical, '\n')) {
		return ErrInvalid
	}
	return nil
}

func validateNativeEvidenceRecord(record NativeEvidence) error {
	if (record.SchemaVersion != nativeEvidenceSchema && record.SchemaVersion != 2) || record.Scope != "single_native_target_only" ||
		validate(Options{Version: record.ReleaseVersion, Commit: record.SourceCommit, Out: "evidence"}) != nil ||
		validateNativeGo(nativeGoEnvironment{GOOS: record.Target.OS, GOARCH: record.Target.Arch, GOHOSTOS: record.Toolchain.GOHOSTOS, GOHOSTARCH: record.Toolchain.GOHOSTARCH, GOVERSION: record.Toolchain.GOVERSION}) != nil ||
		len(record.Gates) != len(nativeEvidenceGates) {
		return ErrInvalid
	}
	if record.SchemaVersion == nativeEvidenceSchema && record.InstallRehearsal != nil ||
		record.SchemaVersion == 2 && !validNativeInstallBinding(record) {
		return ErrInvalid
	}
	for i := range nativeEvidenceGates {
		if record.Gates[i] != nativeEvidenceGates[i] {
			return ErrInvalid
		}
	}
	return nil
}

func validNativeInstallBinding(record NativeEvidence) bool {
	binding := record.InstallRehearsal
	if binding == nil {
		return false
	}
	expectedName := "DarwinRouter_" + record.ReleaseVersion + "_" + record.Target.OS + "_" + record.Target.Arch + ".tar.gz"
	return validInstallDigest(binding.RecordSHA256) && binding.ArtifactName == expectedName &&
		validInstallDigest(binding.ArtifactSHA256) && validInstallDigest(binding.BackupSHA256) &&
		binding.SourceSchema == 29 && binding.CurrentSchema == stateschema.Current
}

func validateNativeGo(observed nativeGoEnvironment) error {
	supported := (observed.GOOS == "darwin" || observed.GOOS == "linux") && (observed.GOARCH == "amd64" || observed.GOARCH == "arm64")
	if !supported || observed.GOOS != observed.GOHOSTOS || observed.GOARCH != observed.GOHOSTARCH || !signedToolchain.MatchString(observed.GOVERSION) {
		return ErrInvalid
	}
	return nil
}

func nativeGateCommand(ctx context.Context, source string, env []string, target string, log io.Writer) error {
	// The release qualification test itself has a 45-minute ceiling. Preserve
	// that meaningful gate instead of inheriting the short metadata-command
	// timeout, while still bounding hung build processes and captured output.
	gateCtx, cancel := context.WithTimeout(ctx, nativeGateTimeout)
	defer cancel()
	cmd := exec.CommandContext(gateCtx, "make", target)
	configureNativeProcessTree(cmd)
	cmd.Cancel = func() error { return terminateNativeProcessTree(cmd) }
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = source
	cmd.Env = env
	if _, err := fmt.Fprintf(log, "== make %s ==\n", target); err != nil {
		return err
	}
	output := &boundedNativeGateLog{destination: log}
	cmd.Stdout, cmd.Stderr = output, output
	if err := cmd.Run(); err != nil || output.overflow {
		if contextErr := gateCtx.Err(); contextErr != nil {
			return contextErr
		}
		return fmt.Errorf("native release gate failed: %s", target)
	}
	if err := gateCtx.Err(); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(log, "\n== make %s passed ==\n", target); err != nil {
		return err
	}
	if err := gateCtx.Err(); err != nil {
		return err
	}
	return nil
}

const nativeGateTimeout = 60 * time.Minute

type boundedNativeGateLog struct {
	destination io.Writer
	written     int
	overflow    bool
}

type strictNativeLog struct{ destination io.Writer }

func (s *strictNativeLog) Write(body []byte) (int, error) {
	n, err := s.destination.Write(body)
	if err == nil && n != len(body) {
		return n, io.ErrShortWrite
	}
	return n, err
}

func (b *boundedNativeGateLog) Write(body []byte) (int, error) {
	if b.written+len(body) > 1<<20 {
		b.overflow = true
		return 0, ErrInvalid
	}
	n, err := b.destination.Write(body)
	b.written += n
	if err == nil && n != len(body) {
		return n, io.ErrShortWrite
	}
	return n, err
}
