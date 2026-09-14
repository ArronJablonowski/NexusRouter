package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/releasepack"
)

func TestTrustRecordCLIEndToEnd(t *testing.T) {
	releaseDir, recordPath, keyID, keyFingerprint, recordDigest := signedCLITrustFixture(t)
	args := func() []string {
		return []string{
			"--dir", releaseDir,
			"--trust-record", recordPath,
			"--trust-record-sha256", recordDigest,
			"--key-id", keyID,
			"--key-fingerprint", keyFingerprint,
		}
	}
	var output bytes.Buffer
	if code := run(args(), &output); code != 0 || output.Len() != 0 {
		t.Fatalf("valid signed release rejected: code=%d output=%q", code, output.String())
	}

	recordBody, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	var record releasepack.TrustRecord
	if err = json.Unmarshal(recordBody, &record); err != nil {
		t.Fatal(err)
	}
	originalPolicyURL := record.ReleasePolicyURL
	record.ReleasePolicyURL = "https://example.invalid/darwinrouter/attacker-policy"
	tamperedRecordBody, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	tamperedRecordBody = append(tamperedRecordBody, '\n')
	if err = os.WriteFile(recordPath, tamperedRecordBody, 0644); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if code := run(args(), &output); code != 1 || output.String() != "release verification failed\n" {
		t.Fatalf("tampered trust-record result: code=%d output=%q", code, output.String())
	}

	record.ReleasePolicyURL = originalPolicyURL
	record.Status = "revoked"
	recordBody, err = json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	recordBody = append(recordBody, '\n')
	if err = os.WriteFile(recordPath, recordBody, 0644); err != nil {
		t.Fatal(err)
	}
	revokedDigest := prefixedSHA256(recordBody)
	revokedArgs := args()
	revokedArgs[5] = revokedDigest
	output.Reset()
	if code := run(revokedArgs, &output); code != 1 || output.String() != "release verification failed\n" {
		t.Fatalf("revoked record result: code=%d output=%q", code, output.String())
	}

	record.Status = "active"
	recordBody, err = json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	recordBody = append(recordBody, '\n')
	if err = os.WriteFile(recordPath, recordBody, 0644); err != nil {
		t.Fatal(err)
	}
	signaturePath := filepath.Join(releaseDir, "SHA256SUMS.sig")
	signature, err := os.ReadFile(signaturePath)
	if err != nil {
		t.Fatal(err)
	}
	if signature[0] == '0' {
		signature[0] = '1'
	} else {
		signature[0] = '0'
	}
	if err = os.WriteFile(signaturePath, signature, 0644); err != nil {
		t.Fatal(err)
	}
	tamperedArgs := args()
	tamperedArgs[5] = prefixedSHA256(recordBody)
	output.Reset()
	if code := run(tamperedArgs, &output); code != 1 || output.String() != "release verification failed\n" {
		t.Fatalf("tampered signature result: code=%d output=%q", code, output.String())
	}
}

func signedCLITrustFixture(t *testing.T) (string, string, string, string, string) {
	t.Helper()
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test source")
	}
	repository := filepath.Clean(filepath.Join(filepath.Dir(testFile), "../.."))
	source := filepath.Join(t.TempDir(), "source")
	for _, directory := range []string{"cmd/darwin", "docs", "examples", "webui/assets/v1"} {
		if err := os.MkdirAll(filepath.Join(source, directory), 0755); err != nil {
			t.Fatal(err)
		}
	}
	var err error
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"LICENSE", "go.mod", "go.sum", "docs/release-install.md", "docs/release-notes.md", "examples/local.yaml"} {
		body, err := os.ReadFile(filepath.Join(repository, name))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(source, name), body, 0644); err != nil {
			t.Fatal(err)
		}
	}
	assets, err := os.ReadDir(filepath.Join(repository, "webui", "assets", "v1"))
	if err != nil || len(assets) == 0 {
		t.Fatal("read WebUI release fixture", err)
	}
	for _, asset := range assets {
		info, infoErr := asset.Info()
		if infoErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			t.Fatal("unsafe WebUI release fixture", asset.Name(), infoErr)
		}
		body, readErr := os.ReadFile(filepath.Join(repository, "webui", "assets", "v1", asset.Name()))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if err = os.WriteFile(filepath.Join(source, "webui", "assets", "v1", asset.Name()), body, 0644); err != nil {
			t.Fatal(err)
		}
	}
	webUIBody := []byte("package webui\n\nimport \"embed\"\n\n//go:embed assets/v1/*\nvar assets embed.FS\n")
	if err = os.WriteFile(filepath.Join(source, "webui", "shell.go"), webUIBody, 0644); err != nil {
		t.Fatal(err)
	}
	mainBody := []byte("package main\n\nimport (\n\t\"fmt\"\n\t_ \"github.com/mattn/go-isatty\"\n)\n\nvar version = \"dev\"\n\nfunc main() { fmt.Println(version) }\n")
	if err := os.WriteFile(filepath.Join(source, "cmd/darwin/main.go"), mainBody, 0644); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		command := exec.Command("git", args...)
		command.Dir = source
		command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		body, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v: %s", args[0], err, body)
		}
		return strings.TrimSpace(string(body))
	}
	git("init", "-q")
	git("add", ".")
	git("-c", "user.name=Release Test", "-c", "user.email=release@example.invalid", "commit", "-qm", "fixture")
	commit := git("rev-parse", "HEAD")
	releaseDir := filepath.Join(t.TempDir(), "release")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := releasepack.Package(ctx, releasepack.Options{Version: "1.0.0-test.1", Commit: commit, Source: source, Out: releaseDir}); err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	seedPath := filepath.Join(t.TempDir(), "disposable-seed")
	if err = os.WriteFile(seedPath, []byte(hex.EncodeToString(private.Seed())+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = releasepack.Sign(releaseDir, seedPath); err != nil {
		t.Fatal(err)
	}
	keyDigest := sha256.Sum256(public)
	keyFingerprint := "sha256:" + hex.EncodeToString(keyDigest[:])
	keyID := "release-test-01"
	record := releasepack.TrustRecord{
		SchemaVersion:         1,
		Project:               "DarwinRouter",
		Scope:                 "darwinrouter-release-signing",
		KeyID:                 keyID,
		Algorithm:             "Ed25519",
		PublicKey:             hex.EncodeToString(public),
		PublicKeySHA256:       keyFingerprint,
		Status:                "active",
		PublishedAt:           "2026-09-07T12:34:56Z",
		ReleasePolicyURL:      "https://example.invalid/darwinrouter/release-policy",
		RotationRevocationURL: "https://example.invalid/darwinrouter/key-status",
	}
	recordBody, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	recordBody = append(recordBody, '\n')
	recordPath := filepath.Join(t.TempDir(), "trust-record.json")
	if err = os.WriteFile(recordPath, recordBody, 0644); err != nil {
		t.Fatal(err)
	}
	return releaseDir, recordPath, keyID, keyFingerprint, prefixedSHA256(recordBody)
}

func prefixedSHA256(body []byte) string {
	digest := sha256.Sum256(body)
	return fmt.Sprintf("sha256:%x", digest)
}
