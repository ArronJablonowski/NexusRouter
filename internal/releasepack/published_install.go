package releasepack

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/stateschema"
)

var ErrPublishedInstallEvidence = errors.New("published install evidence verification failed")

const (
	publishedInstallSchema = 1
	publishedInstallScope  = "darwinrouter-published-native-install-rehearsal"
)

// PublishedInstallEvidence binds an independently retained native install and
// migration rehearsal to the exact archive downloaded and authenticated by a
// post-publication receipt. It is evidence of the observed rehearsal only; it
// does not authorize publication, installation, migration, or rollback.
type PublishedInstallEvidence struct {
	SchemaVersion                  int    `json:"schema_version"`
	Scope                          string `json:"scope"`
	PublicationReceiptSHA256       string `json:"publication_receipt_sha256"`
	PublicationAuthorizationSHA256 string `json:"publication_authorization_sha256"`
	Repository                     string `json:"repository"`
	ReleaseVersion                 string `json:"release_version"`
	SourceCommit                   string `json:"source_commit"`
	Tag                            string `json:"tag"`
	ReleaseID                      int64  `json:"release_id"`
	PublicationObservedAt          string `json:"publication_observed_at"`
	InstallEvidenceSHA256          string `json:"install_evidence_sha256"`
	TargetOS                       string `json:"target_os"`
	TargetArch                     string `json:"target_arch"`
	ArtifactName                   string `json:"artifact_name"`
	ArtifactSHA256                 string `json:"artifact_sha256"`
	InstalledBinarySHA256          string `json:"installed_binary_sha256"`
	InstalledBinaryMode            string `json:"installed_binary_mode"`
	VersionOutput                  string `json:"version_output"`
	SourceSchema                   int    `json:"source_schema"`
	CurrentSchema                  int    `json:"current_schema"`
	BackupSHA256                   string `json:"backup_sha256"`
	RollbackSchema                 int    `json:"rollback_schema"`
	VerifierID                     string `json:"verifier_id"`
	VerifiedAt                     string `json:"verified_at"`
}

// PublishedInstallExpectations are supplied independently of both retained
// records. In particular, the install-record and backup digests must not be
// copied from the record being checked.
type PublishedInstallExpectations struct {
	PublicationReceiptSHA256 string
	InstallEvidenceSHA256    string
	TargetOS                 string
	TargetArch               string
	BackupSHA256             string
	VerifierID               string
	Now                      func() time.Time
	DownloadDir              string
	InstallRoot              string
}

// CreatePublishedInstallEvidence validates two canonical, immutable evidence
// files, installs and executes the authenticated native archive in a new
// private root, and returns evidence for that side-effecting rehearsal. Callers
// must reserve a durable output record before invoking this function.
func CreatePublishedInstallEvidence(ctx context.Context, receiptFile, installFile string, expected PublishedInstallExpectations) (PublishedInstallEvidence, error) {
	var empty PublishedInstallEvidence
	if ctx == nil || ctx.Err() != nil || receiptFile == "" || installFile == "" || expected.DownloadDir == "" || expected.InstallRoot == "" ||
		!trustFingerprint(expected.PublicationReceiptSHA256) || !trustFingerprint(expected.InstallEvidenceSHA256) ||
		!validInstallDigest(expected.BackupSHA256) || !rollbackIdentity.MatchString(expected.VerifierID) ||
		expected.Now == nil ||
		(expected.TargetOS != "darwin" && expected.TargetOS != "linux") ||
		(expected.TargetArch != "amd64" && expected.TargetArch != "arm64") {
		return empty, ErrPublishedInstallEvidence
	}
	if expected.TargetOS != runtime.GOOS || expected.TargetArch != runtime.GOARCH {
		return empty, ErrPublishedInstallEvidence
	}
	receiptBody, err := readRollbackFile(receiptFile, 64<<10)
	if err != nil || rollbackDigest(receiptBody) != expected.PublicationReceiptSHA256 {
		return empty, ErrPublishedInstallEvidence
	}
	receipt, err := ParsePostPublicationReceipt(receiptBody)
	if err != nil || ctx.Err() != nil {
		return empty, ErrPublishedInstallEvidence
	}
	artifactName := "DarwinRouter_" + receipt.ReleaseVersion + "_" + expected.TargetOS + "_" + expected.TargetArch + ".tar.gz"
	artifactSHA := receiptAssetSHA(receipt.Assets, artifactName)
	if !validInstallDigest(artifactSHA) {
		return empty, ErrPublishedInstallEvidence
	}
	installBody, err := readInstallEvidence(installFile)
	if err != nil || installEvidenceDigest(installBody) != expected.InstallEvidenceSHA256 {
		return empty, ErrPublishedInstallEvidence
	}
	install, err := ParseInstallRehearsalEvidence(installBody)
	if err != nil {
		return empty, ErrPublishedInstallEvidence
	}
	verified, err := VerifyInstallRehearsalEvidence(installFile, InstallRehearsalExpectations{
		RecordSHA256: expected.InstallEvidenceSHA256, Version: receipt.ReleaseVersion, Commit: receipt.SourceCommit,
		TargetOS: expected.TargetOS, TargetArch: expected.TargetArch, ArtifactName: artifactName, ArtifactSHA256: artifactSHA,
		SourceSchema: 29, CurrentSchema: stateschema.Current, BackupSHA256: expected.BackupSHA256,
	})
	observedAt, observedErr := time.Parse("2006-01-02T15:04:05Z", receipt.ObservedAt)
	if err != nil || observedErr != nil || ctx.Err() != nil {
		return empty, ErrPublishedInstallEvidence
	}
	binarySHA, versionOutput, err := executePublishedNativeArchive(ctx, expected.DownloadDir, expected.InstallRoot, receipt, expected.TargetOS, expected.TargetArch)
	if err != nil {
		return empty, ErrPublishedInstallEvidence
	}
	verifiedAt := expected.Now()
	if verifiedAt.IsZero() || verifiedAt.Location() != time.UTC || verifiedAt.Nanosecond() != 0 || verifiedAt.Before(observedAt) {
		return empty, ErrPublishedInstallEvidence
	}
	evidence := PublishedInstallEvidence{
		SchemaVersion: publishedInstallSchema, Scope: publishedInstallScope,
		PublicationReceiptSHA256: expected.PublicationReceiptSHA256, PublicationAuthorizationSHA256: receipt.PublicationAuthorizationSHA256,
		Repository: receipt.Repository, ReleaseVersion: receipt.ReleaseVersion, SourceCommit: receipt.SourceCommit,
		Tag: receipt.Tag, ReleaseID: receipt.ReleaseID, PublicationObservedAt: receipt.ObservedAt,
		InstallEvidenceSHA256: expected.InstallEvidenceSHA256, TargetOS: verified.TargetOS, TargetArch: verified.TargetArch,
		ArtifactName: verified.ArtifactName, ArtifactSHA256: verified.ArtifactSHA256,
		InstalledBinarySHA256: binarySHA, InstalledBinaryMode: "0755", VersionOutput: versionOutput, SourceSchema: verified.SourceSchema,
		CurrentSchema: verified.CurrentSchema, BackupSHA256: verified.BackupSHA256, RollbackSchema: verified.RollbackSchema,
		VerifierID: expected.VerifierID, VerifiedAt: verifiedAt.Format("2006-01-02T15:04:05Z"),
	}
	if install.Rollback.Smoke != "passed" {
		return empty, ErrPublishedInstallEvidence
	}
	if _, err = MarshalPublishedInstallEvidence(evidence); err != nil {
		return empty, ErrPublishedInstallEvidence
	}
	return evidence, nil
}

func MarshalPublishedInstallEvidence(evidence PublishedInstallEvidence) ([]byte, error) {
	observedAt, observedErr := time.Parse("2006-01-02T15:04:05Z", evidence.PublicationObservedAt)
	verifiedAt, verifiedErr := time.Parse("2006-01-02T15:04:05Z", evidence.VerifiedAt)
	expectedArtifact := "DarwinRouter_" + evidence.ReleaseVersion + "_" + evidence.TargetOS + "_" + evidence.TargetArch + ".tar.gz"
	if evidence.SchemaVersion != publishedInstallSchema || evidence.Scope != publishedInstallScope ||
		!trustFingerprint(evidence.PublicationReceiptSHA256) || !trustFingerprint(evidence.PublicationAuthorizationSHA256) ||
		!githubRepository.MatchString(evidence.Repository) || validate(Options{Version: evidence.ReleaseVersion, Commit: evidence.SourceCommit, Out: "evidence"}) != nil ||
		evidence.Tag != "v"+evidence.ReleaseVersion || evidence.ReleaseID < 1 || observedErr != nil || verifiedErr != nil ||
		!wholeSecondUTC(evidence.PublicationObservedAt) || !wholeSecondUTC(evidence.VerifiedAt) || verifiedAt.Before(observedAt) ||
		!trustFingerprint(evidence.InstallEvidenceSHA256) || (evidence.TargetOS != "darwin" && evidence.TargetOS != "linux") ||
		(evidence.TargetArch != "amd64" && evidence.TargetArch != "arm64") || evidence.ArtifactName != expectedArtifact ||
		!validInstallDigest(evidence.ArtifactSHA256) || !validInstallDigest(evidence.InstalledBinarySHA256) ||
		evidence.InstalledBinaryMode != "0755" || evidence.VersionOutput != "darwin "+evidence.ReleaseVersion ||
		evidence.SourceSchema != 29 || evidence.CurrentSchema != stateschema.Current ||
		!validInstallDigest(evidence.BackupSHA256) || evidence.RollbackSchema != 29 || !rollbackIdentity.MatchString(evidence.VerifierID) {
		return nil, ErrPublishedInstallEvidence
	}
	body, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return nil, ErrPublishedInstallEvidence
	}
	return append(body, '\n'), nil
}

func executePublishedNativeArchive(ctx context.Context, downloadDir, installRoot string, receipt PostPublicationReceipt, targetOS, targetArch string) (string, string, error) {
	if ctx.Err() != nil || targetOS != runtime.GOOS || targetArch != runtime.GOARCH {
		return "", "", ErrPublishedInstallEvidence
	}
	download, err := filepath.Abs(downloadDir)
	if err != nil {
		return "", "", ErrPublishedInstallEvidence
	}
	download, err = filepath.EvalSymlinks(download)
	if err != nil {
		return "", "", ErrPublishedInstallEvidence
	}
	root, sums, err := checkedRelease(download, true)
	if err != nil {
		return "", "", ErrPublishedInstallEvidence
	}
	defer root.Close()
	if prefixedDigest(sums) != receipt.ApprovalVerification.SHA256SUMSSHA256 {
		return "", "", ErrPublishedInstallEvidence
	}
	signature, err := readReleaseFile(root, signatureName, 129)
	if err != nil || prefixedDigest(signature) != receipt.ApprovalVerification.SignatureFileSHA256 {
		return "", "", ErrPublishedInstallEvidence
	}
	manifestBody, err := readReleaseFile(root, "manifest.json", 64<<10)
	if err != nil || prefixedDigest(manifestBody) != receiptAssetSHA(receipt.Assets, "manifest.json") {
		return "", "", ErrPublishedInstallEvidence
	}
	var manifest Manifest
	if json.Unmarshal(manifestBody, &manifest) != nil || manifest.Version != receipt.ReleaseVersion || manifest.Commit != receipt.SourceCommit {
		return "", "", ErrPublishedInstallEvidence
	}
	var artifact Artifact
	for _, candidate := range manifest.Artifacts {
		if candidate.OS == targetOS && candidate.Arch == targetArch {
			artifact = candidate
			break
		}
	}
	if artifact.File == "" || len(artifact.Entries) != len(archiveContract) || "sha256:"+artifact.SHA256 != receiptAssetSHA(receipt.Assets, artifact.File) || validateReleaseArchive(root, manifest, artifact) != nil {
		return "", "", ErrPublishedInstallEvidence
	}
	archiveBody, err := readReleaseFile(root, artifact.File, maxArchive)
	if err != nil {
		return "", "", ErrPublishedInstallEvidence
	}
	archiveSum := sha256.Sum256(archiveBody)
	if hex.EncodeToString(archiveSum[:]) != artifact.SHA256 || "sha256:"+hex.EncodeToString(archiveSum[:]) != receiptAssetSHA(receipt.Assets, artifact.File) {
		return "", "", ErrPublishedInstallEvidence
	}
	binary, err := extractAuthenticatedBinary(archiveBody, artifact)
	if err != nil {
		return "", "", ErrPublishedInstallEvidence
	}
	install, err := filepath.Abs(installRoot)
	if err != nil {
		return "", "", ErrPublishedInstallEvidence
	}
	parent, installName := filepath.Dir(install), filepath.Base(install)
	parentInfo, err := os.Lstat(parent)
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 || parentInfo.Mode().Perm()&0022 != 0 ||
		installName == "." || installName == string(filepath.Separator) {
		return "", "", ErrPublishedInstallEvidence
	}
	realParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", "", ErrPublishedInstallEvidence
	}
	install = filepath.Join(realParent, installName)
	if rel, relErr := filepath.Rel(download, install); relErr != nil || rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", ErrPublishedInstallEvidence
	}
	parentRoot, err := os.OpenRoot(realParent)
	if err != nil {
		return "", "", ErrPublishedInstallEvidence
	}
	defer parentRoot.Close()
	actualParent, err := parentRoot.Stat(".")
	if err != nil || !os.SameFile(parentInfo, actualParent) || actualParent.Mode().Perm()&0022 != 0 {
		return "", "", ErrPublishedInstallEvidence
	}
	if err = parentRoot.Mkdir(installName, 0700); err != nil {
		return "", "", ErrPublishedInstallEvidence
	}
	installedRoot, err := parentRoot.OpenRoot(installName)
	if err != nil {
		return "", "", ErrPublishedInstallEvidence
	}
	defer installedRoot.Close()
	if err = installedRoot.Chmod(".", 0700); err != nil {
		return "", "", ErrPublishedInstallEvidence
	}
	installedInfo, err := installedRoot.Stat(".")
	parentInstalledInfo, parentInstallErr := parentRoot.Stat(installName)
	if err != nil || parentInstallErr != nil || !installedInfo.IsDir() || installedInfo.Mode().Perm() != 0700 || !os.SameFile(installedInfo, parentInstalledInfo) {
		return "", "", ErrPublishedInstallEvidence
	}
	if err = installedRoot.Mkdir("bin", 0700); err != nil {
		return "", "", ErrPublishedInstallEvidence
	}
	binRoot, err := installedRoot.OpenRoot("bin")
	if err != nil {
		return "", "", ErrPublishedInstallEvidence
	}
	defer binRoot.Close()
	if err = binRoot.Chmod(".", 0700); err != nil {
		return "", "", ErrPublishedInstallEvidence
	}
	binInfo, err := binRoot.Stat(".")
	installedBinInfo, installedBinErr := installedRoot.Stat("bin")
	if err != nil || installedBinErr != nil || !binInfo.IsDir() || binInfo.Mode().Perm() != 0700 || !os.SameFile(binInfo, installedBinInfo) {
		return "", "", ErrPublishedInstallEvidence
	}
	file, err := binRoot.OpenFile("darwin", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0755)
	if err != nil {
		return "", "", ErrPublishedInstallEvidence
	}
	written, writeErr := file.Write(binary)
	chmodErr, syncErr := file.Chmod(0755), file.Sync()
	info, statErr := file.Stat()
	closeErr := file.Close()
	if writeErr != nil || written != len(binary) || syncErr != nil || chmodErr != nil || statErr != nil || closeErr != nil ||
		!info.Mode().IsRegular() || info.Mode().Perm() != 0755 {
		return "", "", ErrPublishedInstallEvidence
	}
	if err = syncPublishedInstallRoot(binRoot); err != nil || syncPublishedInstallRoot(installedRoot) != nil || syncPublishedInstallRoot(parentRoot) != nil {
		return "", "", ErrPublishedInstallEvidence
	}
	binaryPath := filepath.Join(install, "bin", "darwin")
	if !publishedInstallChainMatches(parentRoot, installedRoot, binRoot, parentInfo, installedInfo, binInfo, info) ||
		!publishedInstallPathMatches(realParent, install, binaryPath, parentInfo, installedInfo, binInfo, info) {
		return "", "", ErrPublishedInstallEvidence
	}
	var output boundedExecutionOutput
	executionContext, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	command := exec.CommandContext(executionContext, binaryPath, "version")
	command.WaitDelay = 2 * time.Second
	command.Dir = install
	command.Env = []string{"HOME=" + install, "TMPDIR=" + install, "PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C", "TZ=UTC"}
	command.Stdout, command.Stderr = &output, &output
	if err = command.Run(); err != nil || output.overflow || strings.TrimSpace(output.String()) != "darwin "+receipt.ReleaseVersion {
		return "", "", ErrPublishedInstallEvidence
	}
	if !publishedInstallChainMatches(parentRoot, installedRoot, binRoot, parentInfo, installedInfo, binInfo, info) ||
		!publishedInstallPathMatches(realParent, install, binaryPath, parentInfo, installedInfo, binInfo, info) {
		return "", "", ErrPublishedInstallEvidence
	}
	installed, err := binRoot.Open("darwin")
	if err != nil {
		return "", "", ErrPublishedInstallEvidence
	}
	installedBody, readErr := io.ReadAll(io.LimitReader(installed, maxArtifact+1))
	actual, statErr := installed.Stat()
	closeErr = installed.Close()
	if readErr != nil || statErr != nil || closeErr != nil || len(installedBody) != len(binary) || !actual.Mode().IsRegular() || actual.Mode().Perm() != 0755 || !os.SameFile(info, actual) || !bytes.Equal(installedBody, binary) {
		return "", "", ErrPublishedInstallEvidence
	}
	closingSums, err := readReleaseFile(root, "SHA256SUMS", 64<<10)
	if err != nil || !bytes.Equal(closingSums, sums) {
		return "", "", ErrPublishedInstallEvidence
	}
	closingSignature, err := readReleaseFile(root, signatureName, 129)
	if err != nil || !bytes.Equal(closingSignature, signature) {
		return "", "", ErrPublishedInstallEvidence
	}
	closingManifest, err := readReleaseFile(root, "manifest.json", 64<<10)
	if err != nil || !bytes.Equal(closingManifest, manifestBody) {
		return "", "", ErrPublishedInstallEvidence
	}
	closingArchive, err := readReleaseFile(root, artifact.File, maxArchive)
	if err != nil || !bytes.Equal(closingArchive, archiveBody) || checkReleaseFiles(root, sums, true) != nil {
		return "", "", ErrPublishedInstallEvidence
	}
	digest := sha256.Sum256(binary)
	return "sha256:" + hex.EncodeToString(digest[:]), strings.TrimSpace(output.String()), nil
}

func syncPublishedInstallRoot(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	syncErr, closeErr := directory.Sync(), directory.Close()
	return errors.Join(syncErr, closeErr)
}

func publishedInstallChainMatches(parentRoot, installRoot, binRoot *os.Root, parentInfo, installInfo, binInfo, binaryInfo os.FileInfo) bool {
	actualParent, parentErr := parentRoot.Stat(".")
	parentInstall, parentInstallErr := parentRoot.Stat(filepath.Base(installRoot.Name()))
	actualInstall, installErr := installRoot.Stat(".")
	installBin, installBinErr := installRoot.Stat("bin")
	actualBin, binErr := binRoot.Stat(".")
	actualBinary, binaryErr := binRoot.Stat("darwin")
	return parentErr == nil && parentInstallErr == nil && installErr == nil && installBinErr == nil && binErr == nil && binaryErr == nil &&
		os.SameFile(parentInfo, actualParent) && os.SameFile(installInfo, parentInstall) && os.SameFile(installInfo, actualInstall) &&
		os.SameFile(binInfo, installBin) && os.SameFile(binInfo, actualBin) && os.SameFile(binaryInfo, actualBinary) &&
		actualParent.Mode().Perm()&0022 == 0 && actualInstall.Mode().Perm() == 0700 && actualBin.Mode().Perm() == 0700 &&
		actualBinary.Mode().IsRegular() && actualBinary.Mode().Perm() == 0755
}

func publishedInstallPathMatches(parentPath, installPath, binaryPath string, parentInfo, installInfo, binInfo, binaryInfo os.FileInfo) bool {
	actualParent, parentErr := os.Lstat(parentPath)
	actualInstall, installErr := os.Lstat(installPath)
	actualBin, binErr := os.Lstat(filepath.Dir(binaryPath))
	actualBinary, binaryErr := os.Lstat(binaryPath)
	return parentErr == nil && installErr == nil && binErr == nil && binaryErr == nil &&
		os.SameFile(parentInfo, actualParent) && os.SameFile(installInfo, actualInstall) && os.SameFile(binInfo, actualBin) && os.SameFile(binaryInfo, actualBinary)
}

func extractAuthenticatedBinary(archive []byte, artifact Artifact) ([]byte, error) {
	if len(artifact.Entries) != len(archiveContract) {
		return nil, ErrPublishedInstallEvidence
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, ErrPublishedInstallEvidence
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	var binary []byte
	for i, contract := range archiveContract {
		header, nextErr := reader.Next()
		if nextErr != nil || !canonicalArchiveHeader(header, contract.name, int64(contract.mode), contract.max) {
			return nil, ErrPublishedInstallEvidence
		}
		body, readErr := io.ReadAll(io.LimitReader(reader, contract.max+1))
		if readErr != nil || int64(len(body)) != header.Size || !validEntryMetadata(artifact.Entries[i], i, body) {
			return nil, ErrPublishedInstallEvidence
		}
		if contract.name == "darwin" {
			binary = body
		}
	}
	if _, err = reader.Next(); err != io.EOF || len(binary) == 0 {
		return nil, ErrPublishedInstallEvidence
	}
	return binary, nil
}

type boundedExecutionOutput struct {
	bytes.Buffer
	overflow bool
}

func (b *boundedExecutionOutput) Write(body []byte) (int, error) {
	if b.Len()+len(body) > 4096 {
		b.overflow = true
		return len(body), nil
	}
	return b.Buffer.Write(body)
}

func ParsePublishedInstallEvidence(body []byte) (PublishedInstallEvidence, error) {
	var evidence PublishedInstallEvidence
	if len(body) == 0 || len(body) > 32<<10 || json.Unmarshal(body, &evidence) != nil {
		return PublishedInstallEvidence{}, ErrPublishedInstallEvidence
	}
	canonical, err := MarshalPublishedInstallEvidence(evidence)
	if err != nil || !bytes.Equal(body, canonical) {
		return PublishedInstallEvidence{}, ErrPublishedInstallEvidence
	}
	return evidence, nil
}
