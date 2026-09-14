package releasepack

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

var ErrApprovedInstallVerification = errors.New("approved native install verification failed")

const (
	approvedInstallVerificationSchema = 1
	approvedInstallVerificationScope  = "darwinrouter-approved-native-install-verification"
)

// ApprovedInstallVerificationOptions binds a native installation observation
// to the exact approval inputs checked by VerifyApproved. VerifierID, HostID,
// and PublicKeyChannel are operator assertions retained as public evidence.
type ApprovedInstallVerificationOptions struct {
	Verification     ApprovedVerificationOptions
	TargetOS         string
	TargetArch       string
	InstallRoot      string
	VerifierID       string
	HostID           string
	PublicKeyChannel string
	Now              func() time.Time
}

// ApprovedInstallVerificationReceipt is canonical point-in-time evidence that
// one approval-bound signed set was installed and its native version executed.
// It is neither remote attestation nor release or installation authorization.
type ApprovedInstallVerificationReceipt struct {
	SchemaVersion         int                        `json:"schema_version"`
	Scope                 string                     `json:"scope"`
	ReleaseVersion        string                     `json:"release_version"`
	SourceCommit          string                     `json:"source_commit"`
	TargetOS              string                     `json:"target_os"`
	TargetArch            string                     `json:"target_arch"`
	ManifestSHA256        string                     `json:"manifest_sha256"`
	ArtifactName          string                     `json:"artifact_name"`
	ArtifactSHA256        string                     `json:"artifact_sha256"`
	InstalledBinarySHA256 string                     `json:"installed_binary_sha256"`
	InstalledBinaryMode   string                     `json:"installed_binary_mode"`
	VersionOutput         string                     `json:"version_output"`
	ApprovalVerification  ApprovedVerificationResult `json:"approval_verification"`
	VerifierID            string                     `json:"verifier_id"`
	HostID                string                     `json:"host_id"`
	PublicKeyChannel      string                     `json:"public_key_channel"`
	VerifiedAt            string                     `json:"verified_at"`
	Result                string                     `json:"result"`
}

// VerifyApprovedInstall first performs the complete approval-bound verifier,
// then installs and executes only the exact host-matching signed archive. It
// re-runs VerifyApproved after execution; the installation helper also rereads
// the installed binary and complete signed set before returning.
func VerifyApprovedInstall(ctx context.Context, options ApprovedInstallVerificationOptions) (ApprovedInstallVerificationReceipt, error) {
	var empty ApprovedInstallVerificationReceipt
	if ctx == nil || ctx.Err() != nil || options.InstallRoot == "" || options.Now == nil ||
		options.TargetOS != runtime.GOOS || options.TargetArch != runtime.GOARCH ||
		!rollbackIdentity.MatchString(options.VerifierID) || !rollbackIdentity.MatchString(options.HostID) ||
		!trustHTTPSURL(options.PublicKeyChannel) ||
		!approvedInstallPathsDisjoint(options.InstallRoot, options.Verification.Source, options.Verification.Dir) {
		return empty, ErrApprovedInstallVerification
	}
	approval, err := verifyApproved(ctx, options.Verification, options.InstallRoot)
	if err != nil {
		return empty, ErrApprovedInstallVerification
	}
	candidateBody, candidate, err := readCandidateRecord(options.Verification.CandidateRecordFile)
	if err != nil || prefixedDigest(candidateBody) != approval.CandidateRecordSHA256 {
		return empty, ErrApprovedInstallVerification
	}
	root, sums, err := checkedRelease(options.Verification.Dir, true)
	if err != nil {
		return empty, ErrApprovedInstallVerification
	}
	manifest, manifestErr := approvedArtifactManifest(root, candidate)
	manifestBody, manifestBodyErr := readReleaseFile(root, "manifest.json", 64<<10)
	signatureBody, signatureErr := readReleaseFile(root, signatureName, 129)
	closeErr := root.Close()
	if manifestErr != nil || manifestBodyErr != nil || signatureErr != nil || closeErr != nil ||
		prefixedDigest(sums) != approval.SHA256SUMSSHA256 || prefixedDigest(signatureBody) != approval.SignatureFileSHA256 {
		return empty, ErrApprovedInstallVerification
	}
	var artifact Artifact
	for _, item := range manifest.Artifacts {
		if item.OS == options.TargetOS && item.Arch == options.TargetArch {
			artifact = item
			break
		}
	}
	if artifact.File == "" {
		return empty, ErrApprovedInstallVerification
	}
	observation := PostPublicationReceipt{
		ReleaseVersion:       candidate.ReleaseVersion,
		SourceCommit:         candidate.SourceCommit,
		ApprovalVerification: approval,
		Assets: []PostPublicationAsset{
			{Name: "manifest.json", ServerSHA256: prefixedDigest(manifestBody)},
			{Name: artifact.File, ServerSHA256: "sha256:" + artifact.SHA256},
		},
	}
	binarySHA256, versionOutput, err := executePublishedNativeArchive(ctx, options.Verification.Dir, options.InstallRoot, observation, options.TargetOS, options.TargetArch)
	if err != nil {
		return empty, ErrApprovedInstallVerification
	}
	closingApproval, err := verifyApproved(ctx, options.Verification, options.InstallRoot)
	if err != nil || closingApproval != approval || ctx.Err() != nil {
		return empty, ErrApprovedInstallVerification
	}
	verifiedAt := options.Now()
	if verifiedAt.IsZero() || verifiedAt.Location() != time.UTC || verifiedAt.Nanosecond() != 0 {
		return empty, ErrApprovedInstallVerification
	}
	receipt := ApprovedInstallVerificationReceipt{
		SchemaVersion: approvedInstallVerificationSchema, Scope: approvedInstallVerificationScope,
		ReleaseVersion: candidate.ReleaseVersion, SourceCommit: candidate.SourceCommit,
		TargetOS: options.TargetOS, TargetArch: options.TargetArch, ManifestSHA256: prefixedDigest(manifestBody),
		ArtifactName: artifact.File, ArtifactSHA256: "sha256:" + artifact.SHA256,
		InstalledBinarySHA256: binarySHA256, InstalledBinaryMode: "0755", VersionOutput: versionOutput,
		ApprovalVerification: approval, VerifierID: options.VerifierID, HostID: options.HostID,
		PublicKeyChannel: options.PublicKeyChannel, VerifiedAt: verifiedAt.Format("2006-01-02T15:04:05Z"), Result: "passed",
	}
	if _, err = MarshalApprovedInstallVerificationReceipt(receipt); err != nil {
		return empty, ErrApprovedInstallVerification
	}
	return receipt, nil
}

func approvedInstallPathsDisjoint(install string, protected ...string) bool {
	installPath, err := canonicalPublishedInstallProtectedPath(install)
	if err != nil {
		return false
	}
	for _, path := range protected {
		protectedPath, pathErr := canonicalPublishedInstallProtectedPath(path)
		if pathErr != nil || pathsOverlap(installPath, protectedPath) {
			return false
		}
	}
	return true
}

func pathsOverlap(left, right string) bool {
	leftToRight, leftErr := filepath.Rel(left, right)
	rightToLeft, rightErr := filepath.Rel(right, left)
	return leftErr != nil || rightErr != nil || pathWithin(leftToRight) || pathWithin(rightToLeft)
}

func pathWithin(relative string) bool {
	return relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// MarshalApprovedInstallVerificationReceipt returns the canonical schema-1
// encoding: two-space indentation and one final LF.
func MarshalApprovedInstallVerificationReceipt(receipt ApprovedInstallVerificationReceipt) ([]byte, error) {
	expectedArtifact := "DarwinRouter_" + receipt.ReleaseVersion + "_" + receipt.TargetOS + "_" + receipt.TargetArch + ".tar.gz"
	if receipt.SchemaVersion != approvedInstallVerificationSchema || receipt.Scope != approvedInstallVerificationScope ||
		validate(Options{Version: receipt.ReleaseVersion, Commit: receipt.SourceCommit, Out: "receipt"}) != nil ||
		(receipt.TargetOS != "darwin" && receipt.TargetOS != "linux") ||
		(receipt.TargetArch != "amd64" && receipt.TargetArch != "arm64") || receipt.ArtifactName != expectedArtifact ||
		!trustFingerprint(receipt.ManifestSHA256) || !trustFingerprint(receipt.ArtifactSHA256) ||
		!trustFingerprint(receipt.InstalledBinarySHA256) || receipt.InstalledBinaryMode != "0755" ||
		receipt.VersionOutput != "darwin "+receipt.ReleaseVersion || !validReceiptVerification(receipt.ApprovalVerification) ||
		!rollbackIdentity.MatchString(receipt.VerifierID) || !rollbackIdentity.MatchString(receipt.HostID) ||
		!trustHTTPSURL(receipt.PublicKeyChannel) || !wholeSecondUTC(receipt.VerifiedAt) || receipt.Result != "passed" {
		return nil, ErrApprovedInstallVerification
	}
	body, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return nil, ErrApprovedInstallVerification
	}
	return append(body, '\n'), nil
}

// ParseApprovedInstallVerificationReceipt accepts only the canonical encoding.
func ParseApprovedInstallVerificationReceipt(body []byte) (ApprovedInstallVerificationReceipt, error) {
	var receipt ApprovedInstallVerificationReceipt
	if len(body) == 0 || len(body) > 32<<10 || json.Unmarshal(body, &receipt) != nil {
		return ApprovedInstallVerificationReceipt{}, ErrApprovedInstallVerification
	}
	canonical, err := MarshalApprovedInstallVerificationReceipt(receipt)
	if err != nil || string(body) != string(canonical) {
		return ApprovedInstallVerificationReceipt{}, ErrApprovedInstallVerification
	}
	return receipt, nil
}
