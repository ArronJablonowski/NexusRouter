package releasepack

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublicationAuthorizationCanonicalAndIndependentBinding(t *testing.T) {
	record := publicationAuthorizationFixture()
	body := canonicalPublicationAuthorization(t, record)
	path := filepath.Join(t.TempDir(), "publication.json")
	if err := os.WriteFile(path, body, 0644); err != nil {
		t.Fatal(err)
	}
	expected := publicationExpectations(record, publicationDigest(body))
	parsed, err := ReadPublicationAuthorization(path, expected)
	if err != nil || parsed.Tag != record.Tag {
		t.Fatal("canonical authorization rejected", err)
	}
	for name, mutate := range map[string]func(*PublicationAuthorization){
		"schema":       func(r *PublicationAuthorization) { r.SchemaVersion++ },
		"host":         func(r *PublicationAuthorization) { r.GitHubHost = "example.com" },
		"repository":   func(r *PublicationAuthorization) { r.Repository = "bad" },
		"tag":          func(r *PublicationAuthorization) { r.Tag = "latest" },
		"title":        func(r *PublicationAuthorization) { r.ReleaseTitle = "other" },
		"prerelease":   func(r *PublicationAuthorization) { r.Prerelease = true },
		"notes":        func(r *PublicationAuthorization) { r.ReleaseNotesSHA256 = "bad" },
		"asset_name":   func(r *PublicationAuthorization) { r.Assets[0].Name = "other" },
		"asset_digest": func(r *PublicationAuthorization) { r.Assets[0].SHA256 = "bad" },
		"draft":        func(r *PublicationAuthorization) { r.Controls.DraftFirst = false },
		"create_only":  func(r *PublicationAuthorization) { r.Controls.CreateOnly = false },
		"immutable":    func(r *PublicationAuthorization) { r.Controls.ImmutableRequired = false },
		"gate":         func(r *PublicationAuthorization) { r.Gates[2].Status = "unapproved" },
		"time":         func(r *PublicationAuthorization) { r.ApprovedAt = "2026-09-07T00:00:00.1Z" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := clonePublicationAuthorization(record)
			mutate(&changed)
			if _, err := ParsePublicationAuthorization(canonicalPublicationAuthorization(t, changed)); err == nil {
				t.Fatal("invalid authorization accepted")
			}
		})
	}
	if _, err = ReadPublicationAuthorization(path, PublicationAuthorizationExpectations{}); err == nil {
		t.Fatal("missing independent expectations accepted")
	}
	wrong := expected
	wrong.Repository = "other/repository"
	if _, err = ReadPublicationAuthorization(path, wrong); err == nil {
		t.Fatal("wrong independent repository accepted")
	}
	duplicate := bytes.Replace(body, []byte("  \"schema_version\": 1,"), []byte("  \"schema_version\": 1,\n  \"schema_version\": 1,"), 1)
	reordered := bytes.Replace(body, []byte("  \"project\": \"DarwinRouter\",\n  \"scope\": \"darwinrouter-github-publication-authorization\","), []byte("  \"scope\": \"darwinrouter-github-publication-authorization\",\n  \"project\": \"DarwinRouter\","), 1)
	for _, invalid := range [][]byte{body[:len(body)-1], append(append([]byte(nil), body...), '\n'), append([]byte("{\"unknown\":true,"), body[1:]...), duplicate, reordered} {
		invalidPath := filepath.Join(t.TempDir(), "invalid.json")
		if err = os.WriteFile(invalidPath, invalid, 0644); err != nil {
			t.Fatal(err)
		}
		if _, err = ReadPublicationAuthorization(invalidPath, publicationExpectations(record, publicationDigest(invalid))); err == nil {
			t.Fatal("noncanonical authorization accepted")
		}
	}
	link := filepath.Join(t.TempDir(), "link.json")
	if err = os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err = ReadPublicationAuthorization(link, expected); err == nil {
		t.Fatal("symlink authorization accepted")
	}
}

func publicationAuthorizationFixture() PublicationAuthorization {
	digest := "sha256:" + strings.Repeat("1", 64)
	assets := make([]PublicationAsset, 0, 7)
	for _, name := range publicationAssetNames("1.0.0") {
		assets = append(assets, PublicationAsset{Name: name, Size: 100, SHA256: digest})
	}
	return PublicationAuthorization{
		SchemaVersion: publicationAuthorizationSchema, Project: "DarwinRouter", Scope: publicationAuthorizationScope,
		GitHubHost: "github.com", Repository: "ArronJablonowski/DarwinRouter", ReleaseVersion: "1.0.0",
		SourceCommit: strings.Repeat("a", 40), Tag: "v1.0.0", ReleaseTitle: "DarwinRouter v1.0.0",
		TagMessage: "DarwinRouter release v1.0.0",
		Tagger:     PublicationTagger{Name: "DarwinRouter Release", Email: "release@example.invalid", Date: "2026-09-07T00:01:00Z"},
		Prerelease: false, MakeLatest: true,
		ReleaseNotesSHA256: digest, CandidateRecordSHA256: digest, LicenseEvidenceSHA256: digest,
		SHA256SUMSSHA256: digest, SignatureFileSHA256: digest, TrustRecordSHA256: digest,
		SigningAuthorizationSHA256: digest, Assets: assets,
		Controls:   PublicationControls{DraftFirst: true, CreateOnly: true, ImmutableRequired: true},
		Gates:      append([]PublicationAuthorizationGate(nil), publicationAuthorizationGates...),
		ApproverID: "idp:publication-approver", PublicationPolicyURL: "https://example.invalid/publication-policy",
		ApprovedAt: "2026-09-07T00:02:00Z",
	}
}

func publicationExpectations(r PublicationAuthorization, recordDigest string) PublicationAuthorizationExpectations {
	return PublicationAuthorizationExpectations{
		RecordSHA256: recordDigest, Repository: r.Repository, ReleaseVersion: r.ReleaseVersion,
		SourceCommit: r.SourceCommit, ReleaseNotesSHA256: r.ReleaseNotesSHA256,
		CandidateRecordSHA256: r.CandidateRecordSHA256, LicenseEvidenceSHA256: r.LicenseEvidenceSHA256,
		SHA256SUMSSHA256: r.SHA256SUMSSHA256, SignatureFileSHA256: r.SignatureFileSHA256,
		TrustRecordSHA256: r.TrustRecordSHA256, SigningAuthorizationSHA256: r.SigningAuthorizationSHA256,
	}
}

func canonicalPublicationAuthorization(t *testing.T, record PublicationAuthorization) []byte {
	t.Helper()
	body, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(body, '\n')
}

func clonePublicationAuthorization(record PublicationAuthorization) PublicationAuthorization {
	body, _ := json.Marshal(record)
	var clone PublicationAuthorization
	_ = json.Unmarshal(body, &clone)
	return clone
}
