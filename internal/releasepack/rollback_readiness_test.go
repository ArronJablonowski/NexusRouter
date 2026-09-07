package releasepack

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type receiptVerifierFixture struct {
	identity PublicationReceiptIdentity
	err      error
	calls    int
	change   bool
}

func (v *receiptVerifierFixture) VerifyPublicationReceipt(_ context.Context, _ string, expected string) (PublicationReceiptIdentity, error) {
	v.calls++
	if v.err != nil || expected != v.identity.ReceiptSHA256 {
		return PublicationReceiptIdentity{}, ErrRollbackReadiness
	}
	result := v.identity
	if v.change && v.calls > 1 {
		result.ReleaseID++
	}
	return result, nil
}

func TestRollbackReadinessCanonicalModes(t *testing.T) {
	for _, mode := range []string{"first_release", "upgrade"} {
		t.Run(mode, func(t *testing.T) {
			record := rollbackFixture(mode)
			body := canonicalRollbackFixture(t, record)
			parsed, err := ParseRollbackReadiness(body)
			if err != nil || parsed.History.Mode != mode {
				t.Fatal("canonical record rejected", err)
			}
			for _, invalid := range [][]byte{body[:len(body)-1], append(append([]byte(nil), body...), '\n'), []byte("{}\n")} {
				if _, err = ParseRollbackReadiness(invalid); err == nil {
					t.Fatal("noncanonical record accepted")
				}
			}
		})
	}
}

func TestRollbackReadinessRejectsPolicyAndIdentityDrift(t *testing.T) {
	for name, mutate := range map[string]func(*RollbackReadiness){
		"schema":         func(r *RollbackReadiness) { r.SchemaVersion++ },
		"scope":          func(r *RollbackReadiness) { r.Scope = "other" },
		"receipt":        func(r *RollbackReadiness) { r.Current.PublicationReceiptSHA256 = "bad" },
		"repository":     func(r *RollbackReadiness) { r.Current.Repository = "../repo" },
		"tag":            func(r *RollbackReadiness) { r.Current.Tag = "latest" },
		"release_id":     func(r *RollbackReadiness) { r.Current.ReleaseID = 0 },
		"mode":           func(r *RollbackReadiness) { r.History.Mode = "automatic" },
		"invented_prior": func(r *RollbackReadiness) { r.History.PriorSupportedBinary = priorFixture() },
		"first_policy":   func(r *RollbackReadiness) { r.History.FirstReleaseDecision = "assumed" },
		"backup":         func(r *RollbackReadiness) { r.Backup = backupFixture() },
		"rehearsal":      func(r *RollbackReadiness) { r.Rehearsal.Status = "claimed" },
		"incident":       func(r *RollbackReadiness) { r.Incident.StatusURL = "http://example.invalid" },
		"approval":       func(r *RollbackReadiness) { r.Approval.ApprovedAt = "2026-09-07T12:00:00.1Z" },
	} {
		t.Run(name, func(t *testing.T) {
			record := rollbackFixture("first_release")
			mutate(&record)
			body, err := json.MarshalIndent(record, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = ParseRollbackReadiness(append(body, '\n')); err == nil {
				t.Fatal("invalid readiness record accepted")
			}
		})
	}
}

func TestVerifyFirstReleaseRollbackReadiness(t *testing.T) {
	record := rollbackFixture("first_release")
	options, verifier := writeRollbackFixtures(t, record)
	result, err := VerifyRollbackReadiness(t.Context(), options)
	if err != nil || result.RecordSHA256 != options.Expectations.RecordSHA256 || result.Mode != "first_release" || result.ReleaseID != record.Current.ReleaseID || verifier.calls != 2 {
		t.Fatal("first-release readiness rejected", result, err, verifier.calls)
	}
}

func TestVerifyUpgradeRollbackReadiness(t *testing.T) {
	record := rollbackFixture("upgrade")
	options, verifier := writeRollbackFixtures(t, record)
	result, err := VerifyRollbackReadiness(t.Context(), options)
	if err != nil || result.Mode != "upgrade" || verifier.calls != 2 {
		t.Fatal("upgrade readiness rejected", result, err, verifier.calls)
	}
	for name, mutate := range map[string]func(*RollbackReadinessOptions){
		"missing_backup": func(o *RollbackReadinessOptions) { o.BackupFile = "" },
		"backup_digest":  func(o *RollbackReadinessOptions) { o.Expectations.BackupSHA256 = fingerprint("0") },
		"backup_schema":  func(o *RollbackReadinessOptions) { o.Expectations.BackupStateSchema++ },
		"prior": func(o *RollbackReadinessOptions) {
			clone := *o.Expectations.ExpectedPrior
			clone.BinarySHA256 = fingerprint("0")
			o.Expectations.ExpectedPrior = &clone
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := options
			mutate(&changed)
			if _, err := VerifyRollbackReadiness(t.Context(), changed); err == nil {
				t.Fatal("mismatched upgrade evidence accepted")
			}
		})
	}
}

func TestRollbackReadinessFailsClosedOnStaleOrSelfAuthoredEvidence(t *testing.T) {
	for name, change := range map[string]func(*RollbackReadinessOptions, *receiptVerifierFixture){
		"stale": func(o *RollbackReadinessOptions, _ *receiptVerifierFixture) {
			o.VerificationTime = time.Date(2026, 9, 9, 0, 0, 1, 0, time.UTC)
		},
		"record_digest": func(o *RollbackReadinessOptions, _ *receiptVerifierFixture) {
			o.Expectations.RecordSHA256 = fingerprint("0")
		},
		"rehearsal_digest": func(o *RollbackReadinessOptions, _ *receiptVerifierFixture) {
			o.Expectations.RehearsalSHA256 = fingerprint("0")
		},
		"receipt_error":   func(_ *RollbackReadinessOptions, v *receiptVerifierFixture) { v.err = errors.New("rejected") },
		"receipt_changed": func(_ *RollbackReadinessOptions, v *receiptVerifierFixture) { v.change = true },
		"self_authored_receipt": func(_ *RollbackReadinessOptions, v *receiptVerifierFixture) {
			v.identity.VerifierID = "idp:readiness-approver"
		},
	} {
		t.Run(name, func(t *testing.T) {
			options, verifier := writeRollbackFixtures(t, rollbackFixture("first_release"))
			change(&options, verifier)
			if _, err := VerifyRollbackReadiness(t.Context(), options); err == nil {
				t.Fatal("stale, mismatched or self-authored evidence accepted")
			}
		})
	}
}

func TestRollbackReadinessRejectsSymlinkEvidence(t *testing.T) {
	options, _ := writeRollbackFixtures(t, rollbackFixture("first_release"))
	link := filepath.Join(t.TempDir(), "rehearsal-link")
	if err := os.Symlink(options.RehearsalFile, link); err != nil {
		t.Fatal(err)
	}
	options.RehearsalFile = link
	if _, err := VerifyRollbackReadiness(t.Context(), options); err == nil {
		t.Fatal("symlink rehearsal evidence accepted")
	}
}

func TestCanonicalPostPublicationReceiptVerifierReturnsTypedIdentity(t *testing.T) {
	preflight, signedDir := publishedFixture(t)
	receipt, err := VerifyPublishedRelease(t.Context(), &fixtureReleaseReader{source: signedDir}, PublishedVerificationOptions{Preflight: preflight, DownloadDir: filepath.Join(t.TempDir(), "download")})
	if err != nil {
		t.Fatal(err)
	}
	body, err := MarshalPostPublicationReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "receipt.json")
	if err = os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	digest := rollbackDigest(body)
	identity, err := (CanonicalPostPublicationReceiptVerifier{VerifierID: "idp:publication-verifier"}).VerifyPublicationReceipt(t.Context(), path, digest)
	if err != nil || identity.ReceiptSHA256 != digest || identity.Repository != receipt.Repository || identity.ReleaseID != receipt.ReleaseID || identity.PublicationAuthorizationSHA256 != receipt.PublicationAuthorizationSHA256 {
		t.Fatal("typed receipt identity rejected", identity, err)
	}
	if _, err = (CanonicalPostPublicationReceiptVerifier{VerifierID: "idp:publication-verifier"}).VerifyPublicationReceipt(t.Context(), path, fingerprint("f")); err == nil {
		t.Fatal("wrong receipt digest accepted")
	}
}

func rollbackFixture(mode string) RollbackReadiness {
	record := RollbackReadiness{
		SchemaVersion: 1, Project: "DarwinRouter", Scope: rollbackReadinessScope,
		Current:   RollbackCurrentRelease{PublicationReceiptSHA256: fingerprint("1"), PublicationAuthorizationSHA256: fingerprint("2"), Repository: "ArronJablonowski/DarwinRouter", ReleaseVersion: "1.0.0", SourceCommit: strings.Repeat("a", 40), Tag: "v1.0.0", ReleaseID: 41, StateSchema: 29},
		History:   RollbackHistoryPolicy{Mode: "first_release", FirstReleaseDecision: "approved_no_previous_public_release"},
		Rehearsal: RollbackRehearsal{EvidenceSHA256: fingerprint("3"), Scenario: "first_release_install_recovery", FromSchema: 0, ToSchema: 29, Status: "passed", RehearsedAt: "2026-09-07T12:00:00Z", VerifierID: "idp:rehearsal-verifier"},
		Incident:  RollbackIncident{OwnerID: "team:release-incident", StatusURL: "https://status.example.invalid/darwinrouter"},
		Approval:  RollbackReadinessApproval{ApproverID: "idp:readiness-approver", PolicyURL: "https://policy.example.invalid/rollback", ApprovedAt: "2026-09-07T13:00:00Z", ValidUntil: "2026-09-09T00:00:00Z"},
	}
	if mode == "upgrade" {
		record.History = RollbackHistoryPolicy{Mode: "upgrade", FirstReleaseDecision: "not_applicable", PriorSupportedBinary: priorFixture()}
		record.Backup = backupFixture()
		record.Rehearsal = RollbackRehearsal{EvidenceSHA256: fingerprint("3"), Scenario: "published_release_upgrade_rollback", FromSchema: 28, ToSchema: 29, Status: "passed", RehearsedAt: "2026-09-07T12:00:00Z", VerifierID: "idp:rehearsal-verifier"}
	}
	return record
}

func priorFixture() *RollbackPriorSupportedBinary {
	return &RollbackPriorSupportedBinary{Repository: "ArronJablonowski/DarwinRouter", ReleaseVersion: "0.9.0", SourceCommit: strings.Repeat("b", 40), Tag: "v0.9.0", TargetOS: "darwin", TargetArch: "arm64", ArtifactName: "DarwinRouter_0.9.0_darwin_arm64.tar.gz", ArtifactSHA256: fingerprint("4"), BinarySHA256: fingerprint("5"), PublicationReceiptSHA256: fingerprint("6"), VerificationReceiptSHA256: fingerprint("7"), StateSchema: 28}
}

func backupFixture() *RollbackBackup {
	return &RollbackBackup{SHA256: fingerprint("8"), StateSchema: 28, CapturedAt: "2026-09-07T11:00:00Z", VerifierID: "idp:backup-verifier"}
}

func writeRollbackFixtures(t *testing.T, record RollbackReadiness) (RollbackReadinessOptions, *receiptVerifierFixture) {
	t.Helper()
	directory := t.TempDir()
	rehearsal := []byte("independently retained rehearsal transcript\n")
	record.Rehearsal.EvidenceSHA256 = rollbackDigest(rehearsal)
	write := func(name string, body []byte) string {
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	recordBody := canonicalRollbackFixture(t, record)
	receiptFile := write("publication-receipt.json", []byte("verified receipt fixture\n"))
	rehearsalFile := write("rehearsal.log", rehearsal)
	options := RollbackReadinessOptions{
		RecordFile: write("rollback-readiness.json", recordBody), ReceiptFile: receiptFile, RehearsalFile: rehearsalFile,
		Expectations:     RollbackReadinessExpectations{RecordSHA256: rollbackDigest(recordBody), PublicationReceiptSHA256: record.Current.PublicationReceiptSHA256, PublicationAuthorizationSHA256: record.Current.PublicationAuthorizationSHA256, Repository: record.Current.Repository, ReleaseVersion: record.Current.ReleaseVersion, SourceCommit: record.Current.SourceCommit, Tag: record.Current.Tag, ReleaseID: record.Current.ReleaseID, CurrentStateSchema: record.Current.StateSchema, Mode: record.History.Mode, RehearsalSHA256: record.Rehearsal.EvidenceSHA256, ReceiptVerifierID: "idp:publication-verifier", RehearsalVerifierID: record.Rehearsal.VerifierID, IncidentOwnerID: record.Incident.OwnerID, StatusURL: record.Incident.StatusURL, ApproverID: record.Approval.ApproverID, PolicyURL: record.Approval.PolicyURL},
		VerificationTime: time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC),
	}
	if record.Backup != nil {
		backup := []byte("immutable pre-upgrade database backup\n")
		record.Backup.SHA256 = rollbackDigest(backup)
		recordBody = canonicalRollbackFixture(t, record)
		options.RecordFile = write("rollback-readiness-upgrade.json", recordBody)
		options.Expectations.RecordSHA256 = rollbackDigest(recordBody)
		options.BackupFile = write("pre-upgrade.db", backup)
		options.Expectations.ExpectedPrior = record.History.PriorSupportedBinary
		options.Expectations.BackupSHA256 = record.Backup.SHA256
		options.Expectations.BackupStateSchema = record.Backup.StateSchema
		options.Expectations.BackupVerifierID = record.Backup.VerifierID
	}
	verifier := &receiptVerifierFixture{identity: PublicationReceiptIdentity{ReceiptSHA256: record.Current.PublicationReceiptSHA256, Repository: record.Current.Repository, ReleaseVersion: record.Current.ReleaseVersion, Tag: record.Current.Tag, SourceCommit: record.Current.SourceCommit, PublicationAuthorizationSHA256: record.Current.PublicationAuthorizationSHA256, ReleaseID: record.Current.ReleaseID, VerifiedAt: "2026-09-07T12:30:00Z", VerifierID: "idp:publication-verifier"}}
	options.ReceiptVerifier = verifier
	return options, verifier
}

func canonicalRollbackFixture(t *testing.T, record RollbackReadiness) []byte {
	t.Helper()
	body, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(body, '\n')
}

func fingerprint(character string) string { return "sha256:" + strings.Repeat(character, 64) }
