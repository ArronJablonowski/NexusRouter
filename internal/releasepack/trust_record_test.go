package releasepack

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func trustRecordFixture(t *testing.T, publicFile string) (string, TrustRecord) {
	t.Helper()
	body, err := os.ReadFile(publicFile)
	if err != nil {
		t.Fatal(err)
	}
	key, err := decodeSigningHex(body, ed25519.PublicKeySize)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(key)
	record := TrustRecord{
		SchemaVersion:         1,
		Project:               "DarwinRouter",
		Scope:                 trustScope,
		KeyID:                 "release-2026-01",
		Algorithm:             "Ed25519",
		PublicKey:             hex.EncodeToString(key),
		PublicKeySHA256:       "sha256:" + hex.EncodeToString(digest[:]),
		Status:                "active",
		PublishedAt:           "2026-09-07T12:34:56Z",
		ReleasePolicyURL:      "https://github.com/ArronJablonowski/DarwinRouter/blob/main/docs/release-policy.md",
		RotationRevocationURL: "https://github.com/ArronJablonowski/DarwinRouter/security/advisories",
	}
	encoded, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "trust-record.json")
	writeSigningFixture(t, path, append(encoded, '\n'), 0644)
	return path, record
}

func trustRecordDigest(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func TestTrustRecordVerification(t *testing.T) {
	dir, seed, public := signingFixture(t)
	if err := Sign(dir, seed); err != nil {
		t.Fatal(err)
	}
	recordFile, record := trustRecordFixture(t, public)
	recordDigest := trustRecordDigest(t, recordFile)
	if err := VerifyTrustRecord(dir, recordFile, "release-2026-01", record.PublicKeySHA256, recordDigest); err != nil {
		t.Fatal(err)
	}
	if err := VerifyTrustRecord(dir, recordFile, "release-2026-02", record.PublicKeySHA256, recordDigest); err != ErrTrustRecord {
		t.Fatal("unexpected key identity accepted", err)
	}
	if err := VerifyTrustRecord(dir, recordFile, "../release-2026-01", record.PublicKeySHA256, recordDigest); err != ErrTrustRecord {
		t.Fatal("unsafe expected key identity accepted", err)
	}
	if err := VerifyTrustRecord(dir, recordFile, record.KeyID, "sha256:"+strings.Repeat("0", 64), recordDigest); err != ErrTrustRecord {
		t.Fatal("unexpected fingerprint accepted", err)
	}
	if err := VerifyTrustRecord(dir, recordFile, record.KeyID, record.PublicKeySHA256, "sha256:"+strings.Repeat("0", 64)); err != ErrTrustRecord {
		t.Fatal("unexpected trust-record digest accepted", err)
	}

	_, _, otherPublic := signingFixture(t)
	wrongRecord, wrongIdentity := trustRecordFixture(t, otherPublic)
	wrongDigest := trustRecordDigest(t, wrongRecord)
	if err := VerifyTrustRecord(dir, wrongRecord, "release-2026-01", record.PublicKeySHA256, wrongDigest); err != ErrTrustRecord {
		t.Fatal("unexpected trust-record fingerprint accepted", err)
	}
	if err := VerifyTrustRecord(dir, wrongRecord, wrongIdentity.KeyID, wrongIdentity.PublicKeySHA256, wrongDigest); err != ErrSignature {
		t.Fatal("wrong signing key accepted", err)
	}
}

func TestTrustRecordStatusAndSchemaFailClosed(t *testing.T) {
	dir, seed, public := signingFixture(t)
	if err := Sign(dir, seed); err != nil {
		t.Fatal(err)
	}
	recordFile, base := trustRecordFixture(t, public)
	for _, scenario := range []string{
		"schema", "project", "scope", "key_id", "algorithm", "public_key", "fingerprint",
		"status", "revoked", "published_at", "policy_url", "revocation_url", "unknown", "duplicate",
		"reordered", "noncanonical", "missing_lf", "nul", "oversize",
	} {
		t.Run(scenario, func(t *testing.T) {
			record := base
			switch scenario {
			case "schema":
				record.SchemaVersion = 2
			case "project":
				record.Project = "Other"
			case "scope":
				record.Scope = "git-authentication"
			case "key_id":
				record.KeyID = "Release Key"
			case "algorithm":
				record.Algorithm = "ssh-ed25519"
			case "public_key":
				record.PublicKey = strings.ToUpper(record.PublicKey)
			case "fingerprint":
				record.PublicKeySHA256 = "sha256:" + strings.Repeat("0", 64)
			case "status":
				record.Status = "retired"
			case "revoked":
				record.Status = "revoked"
			case "published_at":
				record.PublishedAt = "2026-09-07T12:34:56+00:00"
			case "policy_url":
				record.ReleasePolicyURL = "http://example.com/policy"
			case "revocation_url":
				record.RotationRevocationURL = "https://user@example.com/revocations"
			}
			encoded, err := json.MarshalIndent(record, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			encoded = append(encoded, '\n')
			switch scenario {
			case "unknown":
				encoded = []byte(strings.Replace(string(encoded), "\n}", ",\n  \"extra\": true\n}", 1))
			case "duplicate":
				encoded = []byte(strings.Replace(string(encoded), `"schema_version": 1,`, `"schema_version": 1, "schema_version": 1,`, 1))
			case "reordered":
				encoded = []byte(strings.Replace(string(encoded), "  \"project\": \"DarwinRouter\",\n  \"scope\": \"darwinrouter-release-signing\",", "  \"scope\": \"darwinrouter-release-signing\",\n  \"project\": \"DarwinRouter\",", 1))
			case "noncanonical":
				encoded, err = json.Marshal(record)
				if err != nil {
					t.Fatal(err)
				}
			case "missing_lf":
				encoded = encoded[:len(encoded)-1]
			case "nul":
				encoded = append(encoded, 0)
			case "oversize":
				encoded = make([]byte, trustRecordMax+1)
			}
			path := filepath.Join(t.TempDir(), "record.json")
			writeSigningFixture(t, path, encoded, 0644)
			_, parseErr := ParseTrustRecord(encoded)
			if scenario == "revoked" {
				if parseErr != nil {
					t.Fatal("canonical revoked archival record rejected", parseErr)
				}
			} else if parseErr != ErrTrustRecord {
				t.Fatal("invalid record parsed", parseErr)
			}
			if err := VerifyTrustRecord(dir, path, base.KeyID, base.PublicKeySHA256, trustRecordDigest(t, path)); err != ErrTrustRecord {
				t.Fatal("invalid record verified", err)
			}
		})
	}
	if err := VerifyTrustRecord(dir, recordFile, base.KeyID, base.PublicKeySHA256, trustRecordDigest(t, recordFile)); err != nil {
		t.Fatal("base record no longer valid", err)
	}
}

func TestTrustRecordRejectsTamperedSignature(t *testing.T) {
	dir, seed, public := signingFixture(t)
	if err := Sign(dir, seed); err != nil {
		t.Fatal(err)
	}
	recordFile, record := trustRecordFixture(t, public)
	signaturePath := filepath.Join(dir, signatureName)
	signature, err := os.ReadFile(signaturePath)
	if err != nil {
		t.Fatal(err)
	}
	if signature[0] == '0' {
		signature[0] = '1'
	} else {
		signature[0] = '0'
	}
	writeSigningFixture(t, signaturePath, signature, 0644)
	if err := VerifyTrustRecord(dir, recordFile, record.KeyID, record.PublicKeySHA256, trustRecordDigest(t, recordFile)); err != ErrSignature {
		t.Fatal("tampered signature accepted", err)
	}
}

func TestTrustRecordURLAndIdentityGrammar(t *testing.T) {
	for _, raw := range []string{
		"", "http://example.com/policy", "HTTPS://example.com/policy", "https:/policy",
		"https://user@example.com/policy", "https://example.com/policy#current",
		"https:opaque", "//example.com/policy", "https://example.com/" + strings.Repeat("x", 2048),
	} {
		if trustHTTPSURL(raw) {
			t.Fatalf("unsafe trust URL accepted: %q", raw)
		}
	}
	for _, raw := range []string{"https://example.com/policy", "https://example.com/policy?v=1"} {
		if !trustHTTPSURL(raw) {
			t.Fatalf("valid trust URL rejected: %q", raw)
		}
	}
	for _, id := range []string{"", "A", "-release", "release-", "release key", "release/key", strings.Repeat("a", 65)} {
		if trustKeyID.MatchString(id) {
			t.Fatalf("unsafe key ID accepted: %q", id)
		}
	}
	for _, id := range []string{"a", "release-2026-01", "release_2026.01", strings.Repeat("a", 64)} {
		if !trustKeyID.MatchString(id) {
			t.Fatalf("valid key ID rejected: %q", id)
		}
	}
	for _, value := range []string{"", "SHA256:" + strings.Repeat("0", 64), "sha256:" + strings.Repeat("0", 63), "sha256:" + strings.Repeat("g", 64)} {
		if trustFingerprint(value) {
			t.Fatalf("unsafe fingerprint accepted: %q", value)
		}
	}
	if !trustFingerprint("sha256:" + strings.Repeat("0", 64)) {
		t.Fatal("valid fingerprint rejected")
	}
}

func TestTrustRecordFileBoundary(t *testing.T) {
	_, _, public := signingFixture(t)
	recordFile, _ := trustRecordFixture(t, public)
	symlink := filepath.Join(t.TempDir(), "record-link.json")
	if err := os.Symlink(recordFile, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := readTrustRecordFile(symlink); err != ErrTrustRecord {
		t.Fatal("symlink trust record accepted", err)
	}
	directory := t.TempDir()
	if _, err := readTrustRecordFile(directory); err != ErrTrustRecord {
		t.Fatal("directory trust record accepted", err)
	}
}

func TestTrustRecordConcurrentReplacementFailsClosed(t *testing.T) {
	dir, seed, public := signingFixture(t)
	if err := Sign(dir, seed); err != nil {
		t.Fatal(err)
	}
	valid, record := trustRecordFixture(t, public)
	_, _, wrongPublic := signingFixture(t)
	wrong, _ := trustRecordFixture(t, wrongPublic)
	target := filepath.Join(t.TempDir(), "record.json")
	validBody, err := os.ReadFile(valid)
	if err != nil {
		t.Fatal(err)
	}
	wrongBody, err := os.ReadFile(wrong)
	if err != nil {
		t.Fatal(err)
	}
	writeSigningFixture(t, target, validBody, 0644)
	done := make(chan error, 1)
	go func() {
		for i := 0; i < 250; i++ {
			body := wrongBody
			if i%2 == 0 {
				body = validBody
			}
			temporary := filepath.Join(filepath.Dir(target), "next.json")
			if err := os.WriteFile(temporary, body, 0644); err != nil {
				done <- err
				return
			}
			if err := os.Rename(temporary, target); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	for i := 0; i < 250; i++ {
		err := VerifyTrustRecord(dir, target, record.KeyID, record.PublicKeySHA256, trustRecordDigest(t, target))
		if err != nil && err != ErrSignature && err != ErrTrustRecord {
			t.Fatalf("unexpected replacement result: %v", err)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
