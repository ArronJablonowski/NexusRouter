package releasepack

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/stateschema"
)

var ErrRollbackReadiness = errors.New("rollback readiness verification failed")

const (
	rollbackReadinessSchema = 1
	rollbackReadinessScope  = "darwinrouter-rollback-readiness"
	maxRollbackReadiness    = 32 << 10
	maxRehearsalEvidence    = 16 << 20
	maxBackupEvidence       = int64(16 << 30)
)

var rollbackIdentity = regexp.MustCompile(`^[a-z0-9][a-z0-9._:@/-]{1,126}[a-z0-9]$`)
var rollbackRepository = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}/[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`)

// PublicationReceiptIdentity is the narrow, cycle-free seam supplied by the
// independent post-publication verifier. An opaque file digest is insufficient.
type PublicationReceiptIdentity struct {
	ReceiptSHA256                  string
	Repository                     string
	ReleaseVersion                 string
	Tag                            string
	SourceCommit                   string
	PublicationAuthorizationSHA256 string
	ReleaseID                      int64
	VerifiedAt                     string
	VerifierID                     string
}

type PublicationReceiptVerifier interface {
	VerifyPublicationReceipt(context.Context, string, string) (PublicationReceiptIdentity, error)
}

type RehearsalEvidenceIdentity struct {
	EvidenceSHA256                 string
	Scenario                       string
	FromSchema                     int
	ToSchema                       int
	Status                         string
	RehearsedAt                    string
	VerifierID                     string
	Repository                     string
	ReleaseVersion                 string
	SourceCommit                   string
	Tag                            string
	ReleaseID                      int64
	PublicationReceiptSHA256       string
	PublicationAuthorizationSHA256 string
	InstallEvidenceSHA256          string
	TargetOS                       string
	TargetArch                     string
	ArtifactName                   string
	ArtifactSHA256                 string
	InstalledBinarySHA256          string
	BackupSHA256                   string
	RollbackSchema                 int
}

type RehearsalEvidenceVerifier interface {
	VerifyRehearsalEvidence(context.Context, string, string) (RehearsalEvidenceIdentity, error)
}

type RollbackReadiness struct {
	SchemaVersion int                       `json:"schema_version"`
	Project       string                    `json:"project"`
	Scope         string                    `json:"scope"`
	Current       RollbackCurrentRelease    `json:"current_release"`
	History       RollbackHistoryPolicy     `json:"release_history"`
	Backup        *RollbackBackup           `json:"pre_upgrade_backup"`
	Rehearsal     RollbackRehearsal         `json:"rehearsal"`
	Incident      RollbackIncident          `json:"incident_response"`
	Approval      RollbackReadinessApproval `json:"approval"`
}

type RollbackCurrentRelease struct {
	PublicationReceiptSHA256       string `json:"publication_receipt_sha256"`
	PublicationAuthorizationSHA256 string `json:"publication_authorization_sha256"`
	Repository                     string `json:"repository"`
	ReleaseVersion                 string `json:"release_version"`
	SourceCommit                   string `json:"source_commit"`
	Tag                            string `json:"tag"`
	ReleaseID                      int64  `json:"release_id"`
	StateSchema                    int    `json:"state_schema"`
}

type RollbackHistoryPolicy struct {
	Mode                 string                        `json:"mode"`
	FirstReleaseDecision string                        `json:"first_release_decision"`
	PriorSupportedBinary *RollbackPriorSupportedBinary `json:"prior_supported_binary"`
	FirstReleaseRollback *FirstReleaseRollbackPolicy   `json:"first_release_rollback"`
}

type FirstReleaseRollbackPolicy struct {
	DaemonAction string `json:"daemon_action"`
	BinaryAction string `json:"binary_action"`
	DataAction   string `json:"data_action"`
}

type RollbackPriorSupportedBinary struct {
	Repository                string `json:"repository"`
	ReleaseVersion            string `json:"release_version"`
	SourceCommit              string `json:"source_commit"`
	Tag                       string `json:"tag"`
	TargetOS                  string `json:"target_os"`
	TargetArch                string `json:"target_arch"`
	ArtifactName              string `json:"artifact_name"`
	ArtifactSHA256            string `json:"artifact_sha256"`
	BinarySHA256              string `json:"binary_sha256"`
	PublicationReceiptSHA256  string `json:"publication_receipt_sha256"`
	VerificationReceiptSHA256 string `json:"verification_receipt_sha256"`
	StateSchema               int    `json:"state_schema"`
}

type RollbackBackup struct {
	SHA256      string `json:"sha256"`
	StateSchema int    `json:"state_schema"`
	CapturedAt  string `json:"captured_at"`
	VerifierID  string `json:"verifier_id"`
}

type RollbackRehearsal struct {
	EvidenceSHA256 string `json:"evidence_sha256"`
	Scenario       string `json:"scenario"`
	FromSchema     int    `json:"from_schema"`
	ToSchema       int    `json:"to_schema"`
	Status         string `json:"status"`
	RehearsedAt    string `json:"rehearsed_at"`
	VerifierID     string `json:"verifier_id"`
}

type RollbackIncident struct {
	OwnerID   string `json:"owner_id"`
	StatusURL string `json:"status_url"`
}

type RollbackReadinessApproval struct {
	ApproverID string `json:"approver_id"`
	PolicyURL  string `json:"policy_url"`
	ApprovedAt string `json:"approved_at"`
	ValidUntil string `json:"valid_until"`
}

// RollbackReadinessExpectations are independently obtained, not copied from
// the readiness record. ExpectedPrior must be nil only for first_release.
type RollbackReadinessExpectations struct {
	RecordSHA256                   string
	PublicationReceiptSHA256       string
	PublicationAuthorizationSHA256 string
	Repository                     string
	ReleaseVersion                 string
	SourceCommit                   string
	Tag                            string
	ReleaseID                      int64
	CurrentStateSchema             int
	Mode                           string
	ExpectedPrior                  *RollbackPriorSupportedBinary
	BackupSHA256                   string
	BackupStateSchema              int
	RehearsalSHA256                string
	ReceiptVerifierID              string
	RehearsalVerifierID            string
	BackupVerifierID               string
	IncidentOwnerID                string
	StatusURL                      string
	ApproverID                     string
	PolicyURL                      string
	FirstReleaseDaemonAction       string
	FirstReleaseBinaryAction       string
	FirstReleaseDataAction         string
}

type RollbackReadinessOptions struct {
	RecordFile          string
	ReceiptFile         string
	RehearsalFile       string
	BackupFile          string
	Expectations        RollbackReadinessExpectations
	ReceiptVerifier     PublicationReceiptVerifier
	RehearsalVerifier   RehearsalEvidenceVerifier
	ReadinessVerifierID string
	Now                 func() time.Time
}

type RollbackReadinessResult struct {
	SchemaVersion            int    `json:"schema_version"`
	Scope                    string `json:"scope"`
	RecordSHA256             string `json:"record_sha256"`
	PublicationReceiptSHA256 string `json:"publication_receipt_sha256"`
	Repository               string `json:"repository"`
	ReleaseVersion           string `json:"release_version"`
	SourceCommit             string `json:"source_commit"`
	Tag                      string `json:"tag"`
	ReleaseID                int64  `json:"release_id"`
	Mode                     string `json:"mode"`
	StateSchema              int    `json:"state_schema"`
	ValidUntil               string `json:"valid_until"`
	RehearsalSHA256          string `json:"rehearsal_sha256"`
	ReadinessVerifierID      string `json:"readiness_verifier_id"`
	VerifiedAt               string `json:"verified_at"`
}

func ParseRollbackReadiness(body []byte) (RollbackReadiness, error) {
	var record RollbackReadiness
	if len(body) == 0 || len(body) > maxRollbackReadiness || json.Unmarshal(body, &record) != nil || validateRollbackReadiness(record) != nil {
		return RollbackReadiness{}, ErrRollbackReadiness
	}
	canonical, err := json.MarshalIndent(record, "", "  ")
	if err != nil || !bytes.Equal(body, append(canonical, '\n')) {
		return RollbackReadiness{}, ErrRollbackReadiness
	}
	return record, nil
}

func MarshalRollbackReadiness(record RollbackReadiness) ([]byte, error) {
	if validateRollbackReadiness(record) != nil {
		return nil, ErrRollbackReadiness
	}
	body, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return nil, ErrRollbackReadiness
	}
	return append(body, '\n'), nil
}

func VerifyRollbackReadiness(ctx context.Context, options RollbackReadinessOptions) (RollbackReadinessResult, error) {
	var result RollbackReadinessResult
	if ctx == nil || options.ReceiptVerifier == nil || options.RehearsalVerifier == nil || options.RecordFile == "" || options.ReceiptFile == "" || options.RehearsalFile == "" || options.Now == nil || !rollbackIdentity.MatchString(options.ReadinessVerifierID) || !validRollbackExpectations(options.Expectations) || ctx.Err() != nil {
		return result, ErrRollbackReadiness
	}
	body, err := readRollbackFile(options.RecordFile, maxRollbackReadiness)
	if err != nil || rollbackDigest(body) != options.Expectations.RecordSHA256 {
		return result, ErrRollbackReadiness
	}
	record, err := ParseRollbackReadiness(body)
	if err != nil || !rollbackMatchesExpectations(record, options.Expectations) {
		return result, ErrRollbackReadiness
	}
	receipt, err := options.ReceiptVerifier.VerifyPublicationReceipt(ctx, options.ReceiptFile, options.Expectations.PublicationReceiptSHA256)
	if err != nil || !receiptMatches(record, receipt) || ctx.Err() != nil {
		return result, ErrRollbackReadiness
	}
	rehearsal, err := options.RehearsalVerifier.VerifyRehearsalEvidence(ctx, options.RehearsalFile, options.Expectations.RehearsalSHA256)
	if err != nil || !rehearsalMatches(record, rehearsal) || rehearsal.VerifierID != options.Expectations.RehearsalVerifierID {
		return result, ErrRollbackReadiness
	}
	if record.History.Mode == "upgrade" {
		if options.BackupFile == "" {
			return result, ErrRollbackReadiness
		}
		if digest, err := digestRollbackFile(options.BackupFile, maxBackupEvidence); err != nil || digest != record.Backup.SHA256 || digest != options.Expectations.BackupSHA256 {
			return result, ErrRollbackReadiness
		}
	} else if options.BackupFile != "" {
		return result, ErrRollbackReadiness
	}
	approvedAt, _ := strictRollbackTime(record.Approval.ApprovedAt)
	validUntil, _ := strictRollbackTime(record.Approval.ValidUntil)
	receiptAt, _ := strictRollbackTime(receipt.VerifiedAt)
	rehearsedAt, _ := strictRollbackTime(record.Rehearsal.RehearsedAt)
	if receipt.VerifierID != options.Expectations.ReceiptVerifierID || approvedAt.Before(receiptAt) || approvedAt.Before(rehearsedAt) || !validUntil.After(approvedAt) || record.Approval.ApproverID == receipt.VerifierID || record.Approval.ApproverID == record.Rehearsal.VerifierID || record.Approval.ApproverID == options.ReadinessVerifierID || options.ReadinessVerifierID == receipt.VerifierID || options.ReadinessVerifierID == rehearsal.VerifierID {
		return result, ErrRollbackReadiness
	}
	if record.Backup != nil {
		capturedAt, _ := strictRollbackTime(record.Backup.CapturedAt)
		if approvedAt.Before(capturedAt) || record.Approval.ApproverID == record.Backup.VerifierID || options.ReadinessVerifierID == record.Backup.VerifierID {
			return result, ErrRollbackReadiness
		}
	}
	// Recheck independently verified receipt identity after every local evidence
	// file has been observed. A changed or stale receipt fails the operation.
	finalReceipt, err := options.ReceiptVerifier.VerifyPublicationReceipt(ctx, options.ReceiptFile, options.Expectations.PublicationReceiptSHA256)
	if err != nil || finalReceipt != receipt || ctx.Err() != nil {
		return result, ErrRollbackReadiness
	}
	finalRehearsal, err := options.RehearsalVerifier.VerifyRehearsalEvidence(ctx, options.RehearsalFile, options.Expectations.RehearsalSHA256)
	finalBody, bodyErr := readRollbackFile(options.RecordFile, maxRollbackReadiness)
	if err != nil || finalRehearsal != rehearsal || bodyErr != nil || !bytes.Equal(finalBody, body) {
		return result, ErrRollbackReadiness
	}
	if record.History.Mode == "upgrade" {
		if digest, digestErr := digestRollbackFile(options.BackupFile, maxBackupEvidence); digestErr != nil || digest != options.Expectations.BackupSHA256 {
			return result, ErrRollbackReadiness
		}
	}
	verifiedAt := options.Now()
	if verifiedAt.IsZero() || verifiedAt.Location() != time.UTC || verifiedAt.Nanosecond() != 0 || verifiedAt.Before(approvedAt) || verifiedAt.After(validUntil) || ctx.Err() != nil {
		return result, ErrRollbackReadiness
	}
	result = RollbackReadinessResult{SchemaVersion: 1, Scope: "darwinrouter-rollback-readiness-verification", RecordSHA256: options.Expectations.RecordSHA256, PublicationReceiptSHA256: receipt.ReceiptSHA256, Repository: receipt.Repository, ReleaseVersion: receipt.ReleaseVersion, SourceCommit: receipt.SourceCommit, Tag: receipt.Tag, ReleaseID: receipt.ReleaseID, Mode: record.History.Mode, StateSchema: record.Current.StateSchema, ValidUntil: record.Approval.ValidUntil, RehearsalSHA256: rehearsal.EvidenceSHA256, ReadinessVerifierID: options.ReadinessVerifierID, VerifiedAt: verifiedAt.Format("2006-01-02T15:04:05Z")}
	if _, err = MarshalRollbackReadinessResult(result); err != nil {
		return RollbackReadinessResult{}, err
	}
	return result, nil
}

func MarshalRollbackReadinessResult(result RollbackReadinessResult) ([]byte, error) {
	if result.SchemaVersion != 1 || result.Scope != "darwinrouter-rollback-readiness-verification" || !trustFingerprint(result.RecordSHA256) || !trustFingerprint(result.PublicationReceiptSHA256) || !trustFingerprint(result.RehearsalSHA256) || !rollbackRepository.MatchString(result.Repository) || validate(Options{Version: result.ReleaseVersion, Commit: result.SourceCommit, Out: "release"}) != nil || result.Tag != "v"+result.ReleaseVersion || result.ReleaseID < 1 || (result.Mode != "first_release" && result.Mode != "upgrade") || result.StateSchema != stateschema.Current || !rollbackIdentity.MatchString(result.ReadinessVerifierID) {
		return nil, ErrRollbackReadiness
	}
	verified, e1 := strictRollbackTime(result.VerifiedAt)
	valid, e2 := strictRollbackTime(result.ValidUntil)
	if e1 != nil || e2 != nil || verified.After(valid) {
		return nil, ErrRollbackReadiness
	}
	body, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return nil, ErrRollbackReadiness
	}
	return append(body, '\n'), nil
}

func ParseRollbackReadinessResult(body []byte) (RollbackReadinessResult, error) {
	var result RollbackReadinessResult
	if len(body) == 0 || len(body) > maxRollbackReadiness || json.Unmarshal(body, &result) != nil {
		return RollbackReadinessResult{}, ErrRollbackReadiness
	}
	canonical, err := MarshalRollbackReadinessResult(result)
	if err != nil || !bytes.Equal(body, canonical) {
		return RollbackReadinessResult{}, ErrRollbackReadiness
	}
	return result, nil
}

func validateRollbackReadiness(record RollbackReadiness) error {
	if record.SchemaVersion != rollbackReadinessSchema || record.Project != "DarwinRouter" || record.Scope != rollbackReadinessScope ||
		!validCurrentRollback(record.Current) || !rollbackIdentity.MatchString(record.Incident.OwnerID) || !trustHTTPSURL(record.Incident.StatusURL) ||
		!rollbackIdentity.MatchString(record.Approval.ApproverID) || !trustHTTPSURL(record.Approval.PolicyURL) ||
		!trustFingerprint(record.Rehearsal.EvidenceSHA256) || record.Rehearsal.Status != "passed" || !rollbackIdentity.MatchString(record.Rehearsal.VerifierID) {
		return ErrRollbackReadiness
	}
	if _, err := strictRollbackTime(record.Rehearsal.RehearsedAt); err != nil {
		return err
	}
	if _, err := strictRollbackTime(record.Approval.ApprovedAt); err != nil {
		return err
	}
	if _, err := strictRollbackTime(record.Approval.ValidUntil); err != nil {
		return err
	}
	switch record.History.Mode {
	case "first_release":
		if record.History.FirstReleaseDecision != "approved_no_previous_public_release" || record.History.PriorSupportedBinary != nil || record.Backup != nil || !validFirstReleasePolicy(record.History.FirstReleaseRollback) || record.Rehearsal.Scenario != "published_native_install_rollback" || record.Rehearsal.FromSchema >= record.Rehearsal.ToSchema || record.Rehearsal.ToSchema != record.Current.StateSchema {
			return ErrRollbackReadiness
		}
	case "upgrade":
		if record.History.FirstReleaseDecision != "not_applicable" || record.History.FirstReleaseRollback != nil || record.History.PriorSupportedBinary == nil || record.Backup == nil || !validPriorBinary(*record.History.PriorSupportedBinary, record.Current.Repository) || !validBackup(*record.Backup) || record.Rehearsal.Scenario != "published_native_install_rollback" || record.Rehearsal.FromSchema != record.Backup.StateSchema || record.Rehearsal.FromSchema != record.History.PriorSupportedBinary.StateSchema || record.Rehearsal.ToSchema != record.Current.StateSchema || record.Rehearsal.FromSchema >= record.Rehearsal.ToSchema {
			return ErrRollbackReadiness
		}
		if record.History.PriorSupportedBinary.ReleaseVersion == record.Current.ReleaseVersion || record.History.PriorSupportedBinary.SourceCommit == record.Current.SourceCommit {
			return ErrRollbackReadiness
		}
	default:
		return ErrRollbackReadiness
	}
	return nil
}

func validCurrentRollback(current RollbackCurrentRelease) bool {
	return trustFingerprint(current.PublicationReceiptSHA256) && trustFingerprint(current.PublicationAuthorizationSHA256) && rollbackRepository.MatchString(current.Repository) && validate(Options{Version: current.ReleaseVersion, Commit: current.SourceCommit, Out: "release"}) == nil && current.Tag == "v"+current.ReleaseVersion && current.ReleaseID > 0 && current.StateSchema == stateschema.Current
}

func validFirstReleasePolicy(policy *FirstReleaseRollbackPolicy) bool {
	return policy != nil && policy.DaemonAction == "stop" && policy.BinaryAction == "uninstall" && policy.DataAction == "preserve_current_schema_no_restore"
}

func validPriorBinary(prior RollbackPriorSupportedBinary, repository string) bool {
	return prior.Repository == repository && validate(Options{Version: prior.ReleaseVersion, Commit: prior.SourceCommit, Out: "release"}) == nil && prior.Tag == "v"+prior.ReleaseVersion && (prior.TargetOS == "darwin" || prior.TargetOS == "linux") && (prior.TargetArch == "amd64" || prior.TargetArch == "arm64") && prior.ArtifactName == "DarwinRouter_"+prior.ReleaseVersion+"_"+prior.TargetOS+"_"+prior.TargetArch+".tar.gz" && trustFingerprint(prior.ArtifactSHA256) && trustFingerprint(prior.BinarySHA256) && trustFingerprint(prior.PublicationReceiptSHA256) && trustFingerprint(prior.VerificationReceiptSHA256) && prior.StateSchema > 0 && prior.StateSchema <= 1_000_000
}

func validBackup(backup RollbackBackup) bool {
	if !trustFingerprint(backup.SHA256) || backup.StateSchema < 1 || backup.StateSchema > 1_000_000 || !rollbackIdentity.MatchString(backup.VerifierID) {
		return false
	}
	_, err := strictRollbackTime(backup.CapturedAt)
	return err == nil
}

func validRollbackExpectations(expected RollbackReadinessExpectations) bool {
	if !trustFingerprint(expected.RecordSHA256) || !trustFingerprint(expected.PublicationReceiptSHA256) || !trustFingerprint(expected.PublicationAuthorizationSHA256) || !rollbackRepository.MatchString(expected.Repository) || validate(Options{Version: expected.ReleaseVersion, Commit: expected.SourceCommit, Out: "release"}) != nil || expected.Tag != "v"+expected.ReleaseVersion || expected.ReleaseID < 1 || expected.CurrentStateSchema != stateschema.Current || !trustFingerprint(expected.RehearsalSHA256) || !rollbackIdentity.MatchString(expected.ReceiptVerifierID) || !rollbackIdentity.MatchString(expected.RehearsalVerifierID) || !rollbackIdentity.MatchString(expected.IncidentOwnerID) || !trustHTTPSURL(expected.StatusURL) || !rollbackIdentity.MatchString(expected.ApproverID) || !trustHTTPSURL(expected.PolicyURL) {
		return false
	}
	if expected.Mode == "first_release" {
		return expected.ExpectedPrior == nil && expected.BackupSHA256 == "" && expected.BackupStateSchema == 0 && expected.BackupVerifierID == "" && expected.FirstReleaseDaemonAction == "stop" && expected.FirstReleaseBinaryAction == "uninstall" && expected.FirstReleaseDataAction == "preserve_current_schema_no_restore"
	}
	return expected.Mode == "upgrade" && expected.ExpectedPrior != nil && validPriorBinary(*expected.ExpectedPrior, expected.Repository) && trustFingerprint(expected.BackupSHA256) && expected.BackupStateSchema > 0 && rollbackIdentity.MatchString(expected.BackupVerifierID)
}

func rollbackMatchesExpectations(record RollbackReadiness, expected RollbackReadinessExpectations) bool {
	if record.Current.PublicationReceiptSHA256 != expected.PublicationReceiptSHA256 || record.Current.PublicationAuthorizationSHA256 != expected.PublicationAuthorizationSHA256 || record.Current.Repository != expected.Repository || record.Current.ReleaseVersion != expected.ReleaseVersion || record.Current.SourceCommit != expected.SourceCommit || record.Current.Tag != expected.Tag || record.Current.ReleaseID != expected.ReleaseID || record.Current.StateSchema != expected.CurrentStateSchema || record.History.Mode != expected.Mode || record.Rehearsal.EvidenceSHA256 != expected.RehearsalSHA256 || record.Rehearsal.VerifierID != expected.RehearsalVerifierID || record.Incident.OwnerID != expected.IncidentOwnerID || record.Incident.StatusURL != expected.StatusURL || record.Approval.ApproverID != expected.ApproverID || record.Approval.PolicyURL != expected.PolicyURL {
		return false
	}
	if expected.Mode == "first_release" {
		return record.History.PriorSupportedBinary == nil && record.Backup == nil && record.History.FirstReleaseRollback != nil && record.History.FirstReleaseRollback.DaemonAction == expected.FirstReleaseDaemonAction && record.History.FirstReleaseRollback.BinaryAction == expected.FirstReleaseBinaryAction && record.History.FirstReleaseRollback.DataAction == expected.FirstReleaseDataAction
	}
	return record.History.PriorSupportedBinary != nil && *record.History.PriorSupportedBinary == *expected.ExpectedPrior && record.Backup != nil && record.Backup.SHA256 == expected.BackupSHA256 && record.Backup.StateSchema == expected.BackupStateSchema && record.Backup.VerifierID == expected.BackupVerifierID
}

func rehearsalMatches(record RollbackReadiness, evidence RehearsalEvidenceIdentity) bool {
	base := trustFingerprint(evidence.EvidenceSHA256) && evidence.EvidenceSHA256 == record.Rehearsal.EvidenceSHA256 && evidence.Scenario == record.Rehearsal.Scenario && evidence.FromSchema == record.Rehearsal.FromSchema && evidence.ToSchema == record.Rehearsal.ToSchema && evidence.Status == record.Rehearsal.Status && evidence.RehearsedAt == record.Rehearsal.RehearsedAt && evidence.VerifierID == record.Rehearsal.VerifierID && evidence.Repository == record.Current.Repository && evidence.ReleaseVersion == record.Current.ReleaseVersion && evidence.SourceCommit == record.Current.SourceCommit && evidence.Tag == record.Current.Tag && evidence.ReleaseID == record.Current.ReleaseID && evidence.PublicationReceiptSHA256 == record.Current.PublicationReceiptSHA256 && evidence.PublicationAuthorizationSHA256 == record.Current.PublicationAuthorizationSHA256 && trustFingerprint(evidence.InstallEvidenceSHA256) && trustFingerprint(evidence.ArtifactSHA256) && trustFingerprint(evidence.InstalledBinarySHA256) && evidence.ToSchema == stateschema.Current && evidence.RollbackSchema == evidence.FromSchema
	if !base {
		return false
	}
	return record.History.Mode == "first_release" || record.Backup != nil && evidence.BackupSHA256 == record.Backup.SHA256
}

func receiptMatches(record RollbackReadiness, receipt PublicationReceiptIdentity) bool {
	if !trustFingerprint(receipt.ReceiptSHA256) || !rollbackRepository.MatchString(receipt.Repository) || validate(Options{Version: receipt.ReleaseVersion, Commit: receipt.SourceCommit, Out: "release"}) != nil || receipt.Tag != "v"+receipt.ReleaseVersion || !trustFingerprint(receipt.PublicationAuthorizationSHA256) || receipt.ReleaseID < 1 || !rollbackIdentity.MatchString(receipt.VerifierID) {
		return false
	}
	if _, err := strictRollbackTime(receipt.VerifiedAt); err != nil {
		return false
	}
	return receipt.ReceiptSHA256 == record.Current.PublicationReceiptSHA256 && receipt.Repository == record.Current.Repository && receipt.ReleaseVersion == record.Current.ReleaseVersion && receipt.SourceCommit == record.Current.SourceCommit && receipt.Tag == record.Current.Tag && receipt.ReleaseID == record.Current.ReleaseID && receipt.PublicationAuthorizationSHA256 == record.Current.PublicationAuthorizationSHA256
}

func strictRollbackTime(value string) (time.Time, error) {
	parsed, err := time.Parse("2006-01-02T15:04:05Z", value)
	if err != nil || parsed.Before(time.Unix(0, 0)) || parsed.Format("2006-01-02T15:04:05Z") != value {
		return time.Time{}, ErrRollbackReadiness
	}
	return parsed, nil
}

func readRollbackFile(path string, max int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > max {
		return nil, ErrRollbackReadiness
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, ErrRollbackReadiness
	}
	defer file.Close()
	actual, err := file.Stat()
	if err != nil || !actual.Mode().IsRegular() || !os.SameFile(info, actual) || actual.Size() != info.Size() {
		return nil, ErrRollbackReadiness
	}
	body, err := io.ReadAll(io.LimitReader(file, max+1))
	if err != nil || int64(len(body)) != actual.Size() {
		return nil, ErrRollbackReadiness
	}
	return body, nil
}

func digestRollbackFile(path string, max int64) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > max {
		return "", ErrRollbackReadiness
	}
	file, err := os.Open(path)
	if err != nil {
		return "", ErrRollbackReadiness
	}
	hash := sha256.New()
	n, copyErr := io.Copy(hash, io.LimitReader(file, max+1))
	actual, statErr := file.Stat()
	closeErr := file.Close()
	if copyErr != nil || statErr != nil || closeErr != nil || n != info.Size() || n > max || !actual.Mode().IsRegular() || !os.SameFile(info, actual) || actual.Size() != info.Size() {
		return "", ErrRollbackReadiness
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func rollbackDigest(body []byte) string {
	digest := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(digest[:])
}
