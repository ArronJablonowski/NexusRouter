package releasepack

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func signingFixture(t *testing.T) (dir, seedFile, publicFile string) {
	t.Helper()
	dir = t.TempDir()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keys := t.TempDir()
	seedFile, publicFile = filepath.Join(keys, "seed"), filepath.Join(keys, "public")
	writeSigningFixture(t, seedFile, []byte(hex.EncodeToString(private.Seed())+"\n"), 0600)
	writeSigningFixture(t, publicFile, []byte(hex.EncodeToString(public)+"\n"), 0644)
	var sums strings.Builder
	manifest := Manifest{SchemaVersion: 1, Version: "1.0.0", Commit: strings.Repeat("a", 40), Toolchain: "go1.27.1"}
	for _, target := range [][2]string{{"darwin", "amd64"}, {"darwin", "arm64"}, {"linux", "amd64"}, {"linux", "arm64"}} {
		name := "DarwinRouter_1.0.0_" + target[0] + "_" + target[1] + ".tar.gz"
		body := []byte("fixture " + name)
		writeSigningFixture(t, filepath.Join(dir, name), body, 0644)
		fmt.Fprintf(&sums, "%x  %s\n", sha256.Sum256(body), name)
		manifest.Artifacts = append(manifest.Artifacts, Artifact{OS: target[0], Arch: target[1], File: name, SHA256: fmt.Sprintf("%x", sha256.Sum256(body))})
	}
	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	body = append(body, '\n')
	writeSigningFixture(t, filepath.Join(dir, "manifest.json"), body, 0644)
	fmt.Fprintf(&sums, "%x  manifest.json\n", sha256.Sum256(body))
	writeSigningFixture(t, filepath.Join(dir, "SHA256SUMS"), []byte(sums.String()), 0644)
	return
}

func TestSigningManifestContract(t *testing.T) {
	for _, scenario := range []string{"schema", "version", "commit", "toolchain", "target", "hash", "filename", "count", "malformed", "duplicate_key", "missing_manifest", "extra_payload"} {
		t.Run(scenario, func(t *testing.T) {
			dir, seedFile, public := signingFixture(t)
			path := filepath.Join(dir, "manifest.json")
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var manifest Manifest
			if err = json.Unmarshal(body, &manifest); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "schema":
				manifest.SchemaVersion = 2
			case "version":
				manifest.Version = "01.0.0"
			case "commit":
				manifest.Commit = strings.Repeat("A", 40)
			case "toolchain":
				manifest.Toolchain = "go01.27.1"
			case "target":
				manifest.Artifacts[0].OS = "windows"
			case "hash":
				manifest.Artifacts[0].SHA256 = strings.Repeat("0", 64)
			case "filename":
				manifest.Artifacts[0].File = "different.tar.gz"
			case "count":
				manifest.Artifacts = manifest.Artifacts[:3]
			}
			body, err = json.MarshalIndent(manifest, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			body = append(body, '\n')
			if scenario == "malformed" {
				body = []byte("{invalid}")
			}
			if scenario == "duplicate_key" {
				body = []byte(strings.Replace(string(body), `"schema_version": 1,`, `"schema_version": 1, "schema_version": 1,`, 1))
			}
			writeSigningFixture(t, path, body, 0644)
			if scenario == "missing_manifest" {
				if err = os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "extra_payload" {
				writeSigningFixture(t, filepath.Join(dir, "extra.txt"), []byte("extra"), 0644)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, entry := range entries {
				if entry.Name() != "SHA256SUMS" {
					names = append(names, entry.Name())
				}
			}
			sort.Strings(names)
			var sums strings.Builder
			for _, name := range names {
				body, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil {
					t.Fatal(err)
				}
				fmt.Fprintf(&sums, "%x  %s\n", sha256.Sum256(body), name)
			}
			writeSigningFixture(t, filepath.Join(dir, "SHA256SUMS"), []byte(sums.String()), 0644)
			if err := Sign(dir, seedFile); err != ErrSignature {
				t.Fatal("invalid contract signed", err)
			}
			// Even an authentic signature cannot turn malformed release metadata
			// into a conforming DarwinRouter package.
			seed, err := signingKeyFile(seedFile, true)
			if err != nil {
				t.Fatal(err)
			}
			sig := ed25519.Sign(ed25519.NewKeyFromSeed(seed), []byte(sums.String()))
			writeSigningFixture(t, filepath.Join(dir, signatureName), []byte(hex.EncodeToString(sig)+"\n"), 0644)
			if err := Verify(dir, public); err != ErrSignature {
				t.Fatal("invalid authenticated contract accepted", err)
			}
		})
	}
}

func writeSigningFixture(t *testing.T, path string, body []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, body, mode); err != nil {
		t.Fatal(err)
	}
}

func TestSigningOfflineRoundTrip(t *testing.T) {
	dir, seed, public := signingFixture(t)
	if err := Sign(dir, seed); err != nil {
		t.Fatal(err)
	}
	if err := Verify(dir, public); err != nil {
		t.Fatal(err)
	}
	if err := Sign(dir, seed); err != ErrSignature {
		t.Fatal("existing signature overwritten", err)
	}
	_, _, wrong := signingFixture(t)
	if err := Verify(dir, wrong); err != ErrSignature {
		t.Fatal("untrusted key accepted", err)
	}
}

func TestSigningTampering(t *testing.T) {
	for _, name := range []string{"DarwinRouter_1.0.0_darwin_amd64.tar.gz", "manifest.json", "SHA256SUMS", "SHA256SUMS.sig"} {
		t.Run(name, func(t *testing.T) {
			dir, seed, public := signingFixture(t)
			if err := Sign(dir, seed); err != nil {
				t.Fatal(err)
			}
			writeSigningFixture(t, filepath.Join(dir, name), []byte("tampered"), 0644)
			if err := Verify(dir, public); err != ErrSignature {
				t.Fatal("tampering accepted", err)
			}
		})
	}
}

func TestSigningRejectsUnsafeFilesAndIncompleteCoverage(t *testing.T) {
	for _, scenario := range []string{"symlink_archive", "symlink_sums", "symlink_key", "directory_archive", "unlisted", "bad_permissions", "traversal", "duplicate", "unsorted", "no_newline"} {
		t.Run(scenario, func(t *testing.T) {
			dir, seed, _ := signingFixture(t)
			sumsPath := filepath.Join(dir, "SHA256SUMS")
			sums, err := os.ReadFile(sumsPath)
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "symlink_archive", "symlink_sums", "symlink_key":
				path := filepath.Join(dir, "DarwinRouter_1.0.0_darwin_amd64.tar.gz")
				if scenario == "symlink_sums" {
					path = sumsPath
				} else if scenario == "symlink_key" {
					path = seed
				}
				target := filepath.Join(t.TempDir(), "target")
				if err := os.Rename(path, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "directory_archive":
				path := filepath.Join(dir, "DarwinRouter_1.0.0_darwin_amd64.tar.gz")
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "unlisted":
				writeSigningFixture(t, filepath.Join(dir, "extra"), []byte("uncovered"), 0600)
			case "bad_permissions":
				if err := os.Chmod(seed, 0644); err != nil {
					t.Fatal(err)
				}
			case "traversal":
				sums = []byte(strings.ReplaceAll(string(sums), "DarwinRouter_1.0.0_darwin_amd64.tar.gz", "../DarwinRouter_1.0.0_darwin_amd64.tar.gz"))
			case "duplicate":
				sums = append(sums, sums...)
			case "unsorted":
				lines := strings.Split(strings.TrimSuffix(string(sums), "\n"), "\n")
				sums = []byte(lines[1] + "\n" + lines[0] + "\n")
			case "no_newline":
				sums = sums[:len(sums)-1]
			}
			if scenario == "traversal" || scenario == "duplicate" || scenario == "unsorted" || scenario == "no_newline" {
				writeSigningFixture(t, sumsPath, sums, 0644)
			}
			if err := Sign(dir, seed); err != ErrSignature {
				t.Fatal("unsafe release accepted", err)
			}
			if _, err := os.Lstat(filepath.Join(dir, signatureName)); !os.IsNotExist(err) {
				t.Fatal("invalid release created signature")
			}
		})
	}
}

func TestSigningVerificationSymlinksAndCoverage(t *testing.T) {
	for _, name := range []string{"DarwinRouter_1.0.0_darwin_amd64.tar.gz", "manifest.json", "SHA256SUMS", "SHA256SUMS.sig", "public", "extra"} {
		t.Run(name, func(t *testing.T) {
			dir, seed, public := signingFixture(t)
			if err := Sign(dir, seed); err != nil {
				t.Fatal(err)
			}
			if name == "extra" {
				writeSigningFixture(t, filepath.Join(dir, name), []byte("unlisted"), 0600)
			} else {
				path := filepath.Join(dir, name)
				if name == "public" {
					path = public
				}
				target := filepath.Join(t.TempDir(), "target")
				if err := os.Rename(path, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			if err := Verify(dir, public); err != ErrSignature {
				t.Fatal("unsafe signed release accepted", err)
			}
		})
	}
}
