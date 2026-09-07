package releasepack

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func signingAuthorizationFixture() SigningAuthorization {
	return SigningAuthorization{
		SchemaVersion:         signingAuthorizationSchema,
		Project:               "DarwinRouter",
		Scope:                 signingAuthorizationScope,
		CandidateRecordSHA256: "sha256:" + strings.Repeat("1", 64),
		LicenseEvidenceSHA256: "sha256:" + strings.Repeat("5", 64),
		SHA256SUMSSHA256:      "sha256:" + strings.Repeat("2", 64),
		TrustRecordSHA256:     "sha256:" + strings.Repeat("3", 64),
		KeyID:                 "release-2026-01",
		KeyFingerprint:        "sha256:" + strings.Repeat("4", 64),
		Targets:               append([]SigningAuthorizationTarget(nil), authorizedTargets...),
		Gates:                 append([]SigningAuthorizationGate(nil), signingAuthorizationGates...),
		ApproverID:            "github:release-approver",
		ReleasePolicyURL:      "https://example.invalid/darwinrouter/release-policy/v1",
		ApprovedAt:            "2026-09-07T19:20:21Z",
	}
}

func signingAuthorizationBody(t *testing.T, record SigningAuthorization) []byte {
	t.Helper()
	body, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(body, '\n')
}

func signingAuthorizationExpected(record SigningAuthorization, body []byte) SigningAuthorizationExpectations {
	return SigningAuthorizationExpectations{
		RecordSHA256:          prefixedSigningAuthorizationDigest(body),
		CandidateRecordSHA256: record.CandidateRecordSHA256,
		LicenseEvidenceSHA256: record.LicenseEvidenceSHA256,
		SHA256SUMSSHA256:      record.SHA256SUMSSHA256,
		TrustRecordSHA256:     record.TrustRecordSHA256,
		KeyID:                 record.KeyID,
		KeyFingerprint:        record.KeyFingerprint,
	}
}

func TestParseAndReadSigningAuthorization(t *testing.T) {
	record := signingAuthorizationFixture()
	body := signingAuthorizationBody(t, record)
	parsed, err := ParseSigningAuthorization(body)
	if err != nil || parsed.ApproverID != record.ApproverID ||
		parsed.LicenseEvidenceSHA256 != record.LicenseEvidenceSHA256 {
		t.Fatal("canonical authorization rejected", err)
	}
	path := filepath.Join(t.TempDir(), "signing-authorization.json")
	if err = os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	parsed, err = ReadSigningAuthorization(path, signingAuthorizationExpected(record, body))
	if err != nil || parsed.SHA256SUMSSHA256 != record.SHA256SUMSSHA256 {
		t.Fatal("expected authorization rejected", err)
	}
}

func TestSigningAuthorizationContractFailsClosed(t *testing.T) {
	base := signingAuthorizationFixture()
	tests := []struct {
		name   string
		mutate func(*SigningAuthorization)
	}{
		{"schema", func(r *SigningAuthorization) { r.SchemaVersion++ }},
		{"project", func(r *SigningAuthorization) { r.Project = "Other" }},
		{"scope", func(r *SigningAuthorization) { r.Scope = "other" }},
		{"candidate_digest", func(r *SigningAuthorization) { r.CandidateRecordSHA256 = strings.Repeat("1", 64) }},
		{"license_evidence_digest", func(r *SigningAuthorization) { r.LicenseEvidenceSHA256 = "sha256:bad" }},
		{"license_evidence_missing", func(r *SigningAuthorization) { r.LicenseEvidenceSHA256 = "" }},
		{"sums_digest", func(r *SigningAuthorization) { r.SHA256SUMSSHA256 = "sha256:" + strings.Repeat("A", 64) }},
		{"trust_digest", func(r *SigningAuthorization) { r.TrustRecordSHA256 = "sha256:bad" }},
		{"key_id", func(r *SigningAuthorization) { r.KeyID = "Release Key" }},
		{"key_fingerprint", func(r *SigningAuthorization) { r.KeyFingerprint = "sha256:" + strings.Repeat("g", 64) }},
		{"target_missing", func(r *SigningAuthorization) { r.Targets = r.Targets[:3] }},
		{"target_reordered", func(r *SigningAuthorization) { r.Targets[0], r.Targets[1] = r.Targets[1], r.Targets[0] }},
		{"target_unapproved", func(r *SigningAuthorization) { r.Targets[0].Decision = "unapproved" }},
		{"gate_missing", func(r *SigningAuthorization) { r.Gates = r.Gates[:3] }},
		{"project_license_missing", func(r *SigningAuthorization) { r.Gates = r.Gates[1:] }},
		{"project_license_unapproved", func(r *SigningAuthorization) { r.Gates[0].Status = "unapproved" }},
		{"notice_unapproved", func(r *SigningAuthorization) { r.Gates[1].Status = "unapproved" }},
		{"signing_unapproved", func(r *SigningAuthorization) { r.Gates[2].Status = "unapproved" }},
		{"publication_approved", func(r *SigningAuthorization) { r.Gates[3].Status = "approved" }},
		{"approver", func(r *SigningAuthorization) { r.ApproverID = "Human Name" }},
		{"approver_too_short", func(r *SigningAuthorization) { r.ApproverID = "a" }},
		{"policy_http", func(r *SigningAuthorization) { r.ReleasePolicyURL = "http://example.invalid/policy" }},
		{"policy_credentials", func(r *SigningAuthorization) { r.ReleasePolicyURL = "https://user@example.invalid/policy" }},
		{"fractional_time", func(r *SigningAuthorization) { r.ApprovedAt = "2026-09-07T19:20:21.1Z" }},
		{"offset_time", func(r *SigningAuthorization) { r.ApprovedAt = "2026-09-07T13:20:21-06:00" }},
		{"pre_epoch_time", func(r *SigningAuthorization) { r.ApprovedAt = "1900-01-01T00:00:00Z" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := base
			record.Targets = append([]SigningAuthorizationTarget(nil), base.Targets...)
			record.Gates = append([]SigningAuthorizationGate(nil), base.Gates...)
			test.mutate(&record)
			if _, err := ParseSigningAuthorization(signingAuthorizationBody(t, record)); err == nil {
				t.Fatal("invalid signing authorization accepted")
			}
		})
	}
}

func TestSigningAuthorizationProjectLicenseGateIsDigestBound(t *testing.T) {
	record := signingAuthorizationFixture()
	approved := signingAuthorizationBody(t, record)
	record.Gates = append([]SigningAuthorizationGate(nil), record.Gates...)
	record.Gates[0].Status = "unapproved"
	unapproved := signingAuthorizationBody(t, record)
	if prefixedSigningAuthorizationDigest(approved) == prefixedSigningAuthorizationDigest(unapproved) {
		t.Fatal("project-license decision did not change canonical record digest")
	}
	if _, err := ParseSigningAuthorization(unapproved); err == nil {
		t.Fatal("unapproved project license accepted")
	}
}

func TestSigningAuthorizationLicenseEvidenceIsDigestBound(t *testing.T) {
	record := signingAuthorizationFixture()
	original := signingAuthorizationBody(t, record)
	record.LicenseEvidenceSHA256 = "sha256:" + strings.Repeat("6", 64)
	changed := signingAuthorizationBody(t, record)
	if prefixedSigningAuthorizationDigest(original) == prefixedSigningAuthorizationDigest(changed) {
		t.Fatal("license-evidence identity did not change canonical record digest")
	}
	if _, err := ParseSigningAuthorization(changed); err != nil {
		t.Fatal("valid changed license-evidence identity rejected", err)
	}
}

func TestSigningAuthorizationRejectsNoncanonicalJSON(t *testing.T) {
	body := signingAuthorizationBody(t, signingAuthorizationFixture())
	unknown := append([]byte(nil), body...)
	unknown = append(unknown[:len(unknown)-2], []byte(",\n  \"extra\": true\n}\n")...)
	duplicate := append([]byte(nil), body...)
	duplicate = append(duplicate[:len(duplicate)-2], []byte(",\n  \"approved_at\": \"2026-09-07T19:20:21Z\"\n}\n")...)
	for _, invalid := range [][]byte{
		body[:len(body)-1],
		append(append([]byte(nil), body...), '\n'),
		unknown,
		duplicate,
		[]byte(`{"schema_version":1}` + "\n"),
	} {
		if _, err := ParseSigningAuthorization(invalid); err == nil {
			t.Fatal("noncanonical signing authorization accepted")
		}
	}
}

func TestSigningAuthorizationRequiresIndependentExactInputs(t *testing.T) {
	record := signingAuthorizationFixture()
	body := signingAuthorizationBody(t, record)
	path := filepath.Join(t.TempDir(), "authorization.json")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	base := signingAuthorizationExpected(record, body)
	tests := []struct {
		name   string
		mutate func(*SigningAuthorizationExpectations)
	}{
		{"record_digest", func(e *SigningAuthorizationExpectations) { e.RecordSHA256 = "sha256:" + strings.Repeat("0", 64) }},
		{"candidate_digest", func(e *SigningAuthorizationExpectations) {
			e.CandidateRecordSHA256 = "sha256:" + strings.Repeat("0", 64)
		}},
		{"license_evidence_digest", func(e *SigningAuthorizationExpectations) {
			e.LicenseEvidenceSHA256 = "sha256:" + strings.Repeat("0", 64)
		}},
		{"missing_license_evidence_digest", func(e *SigningAuthorizationExpectations) { e.LicenseEvidenceSHA256 = "" }},
		{"sums_digest", func(e *SigningAuthorizationExpectations) { e.SHA256SUMSSHA256 = "sha256:" + strings.Repeat("0", 64) }},
		{"trust_digest", func(e *SigningAuthorizationExpectations) { e.TrustRecordSHA256 = "sha256:" + strings.Repeat("0", 64) }},
		{"key_id", func(e *SigningAuthorizationExpectations) { e.KeyID = "other" }},
		{"key_fingerprint", func(e *SigningAuthorizationExpectations) { e.KeyFingerprint = "sha256:" + strings.Repeat("0", 64) }},
		{"missing_record_digest", func(e *SigningAuthorizationExpectations) { e.RecordSHA256 = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			expected := base
			test.mutate(&expected)
			if _, err := ReadSigningAuthorization(path, expected); err == nil {
				t.Fatal("mismatched independent input accepted")
			}
		})
	}
	link := filepath.Join(t.TempDir(), "authorization-link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSigningAuthorization(link, base); err == nil {
		t.Fatal("symlink authorization accepted")
	}
}
