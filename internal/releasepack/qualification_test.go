package releasepack

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"debug/elf"
	"debug/macho"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// Opt-in because this builds the entire clean, committed project eight times.
// It uses generated throwaway keys, never an operator's signing identity.
func TestReleaseQualification(t *testing.T) {
	if os.Getenv("DARWIN_RELEASE_QUALIFY") != "1" {
		t.Skip("set DARWIN_RELEASE_QUALIFY=1 on a clean committed checkout")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()
	source, err := command(ctx, ".", environment(), "git", "rev-parse", "--show-toplevel")
	if err != nil {
		t.Fatal(err)
	}
	commit, err := command(ctx, source, environment(), "git", "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("qualifying source commit %s on %s/%s", commit, runtime.GOOS, runtime.GOARCH)
	parent := t.TempDir()
	first, second := filepath.Join(parent, "first"), filepath.Join(parent, "second")
	const version = "0.0.0-qualification"
	if err = Package(ctx, Options{Version: version, Commit: commit, Out: first, Source: source}); err != nil {
		t.Fatal(err)
	}
	if _, err = command(ctx, source, environment(), "go", "run", "./cmd/package-release", "--version", version, "--commit", commit, "--out", second, "--source", source); err != nil {
		t.Fatal("package CLI", err)
	}
	entries, err := os.ReadDir(first)
	if err != nil || len(entries) != 6 {
		t.Fatalf("expected exactly four archives, manifest and sums: %v", err)
	}
	secondEntries, err := os.ReadDir(second)
	if err != nil || len(secondEntries) != len(entries) {
		t.Fatalf("second build has a different payload count: %v", err)
	}
	for _, entry := range entries {
		a, e := os.ReadFile(filepath.Join(first, entry.Name()))
		if e != nil {
			t.Fatal(e)
		}
		b, e := os.ReadFile(filepath.Join(second, entry.Name()))
		if e != nil || !bytes.Equal(a, b) {
			t.Fatalf("non-reproducible artifact %s: %v", entry.Name(), e)
		}
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(private)
	seedFile, publicFile := filepath.Join(parent, "test-seed"), filepath.Join(parent, "test-public")
	if err = os.WriteFile(seedFile, []byte(hex.EncodeToString(private.Seed())+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(publicFile, []byte(hex.EncodeToString(public)+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err = Sign(first, seedFile); err != nil {
		t.Fatal(err)
	}
	if err = Verify(first, publicFile); err != nil {
		t.Fatal(err)
	}
	if _, err = command(ctx, source, environment(), "go", "run", "./cmd/sign-release", "--dir", second, "--key", seedFile); err != nil {
		t.Fatal("signing CLI", err)
	}
	if _, err = command(ctx, source, environment(), "go", "run", "./cmd/verify-release", "--dir", second, "--public-key", publicFile); err != nil {
		t.Fatal("verification CLI", err)
	}
	firstSignature, err := os.ReadFile(filepath.Join(first, signatureName))
	if err != nil {
		t.Fatal(err)
	}
	secondSignature, err := os.ReadFile(filepath.Join(second, signatureName))
	if err != nil || !bytes.Equal(firstSignature, secondSignature) {
		t.Fatal("library/CLI signature mismatch", err)
	}
	body, err := os.ReadFile(filepath.Join(first, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err = json.Unmarshal(body, &manifest); err != nil || manifest.Version != version || manifest.Commit != commit {
		t.Fatalf("manifest identity: %v", err)
	}
	nativeRan := false
	for _, artifact := range manifest.Artifacts {
		binary := qualificationBinary(t, filepath.Join(first, artifact.File))
		if artifact.OS == "darwin" {
			f, e := macho.NewFile(bytes.NewReader(binary))
			want := macho.CpuAmd64
			if artifact.Arch == "arm64" {
				want = macho.CpuArm64
			}
			if e != nil || f.Cpu != want || f.Type != macho.TypeExec {
				t.Fatalf("Mach-O target mismatch: %s: %v", artifact.File, e)
			}
		} else {
			f, e := elf.NewFile(bytes.NewReader(binary))
			want := elf.EM_X86_64
			if artifact.Arch == "arm64" {
				want = elf.EM_AARCH64
			}
			if e != nil || f.Machine != want || f.Type != elf.ET_EXEC {
				t.Fatalf("ELF target mismatch: %s: %v", artifact.File, e)
			}
			for _, program := range f.Progs {
				if program.Type == elf.PT_INTERP {
					t.Fatalf("unexpected dynamic interpreter: %s", artifact.File)
				}
			}
		}
		if artifact.OS == runtime.GOOS && artifact.Arch == runtime.GOARCH {
			path := filepath.Join(parent, "darwin")
			if err = os.WriteFile(path, binary, 0700); err != nil {
				t.Fatal(err)
			}
			got, e := command(ctx, parent, environment(), path, "version")
			if e != nil || got != "darwin "+version {
				t.Fatalf("native version mismatch: %q: %v", got, e)
			}
			nativeRan = true
		}
	}
	if !nativeRan {
		t.Fatal("no native release target executed")
	}
	if err = os.WriteFile(filepath.Join(first, manifest.Artifacts[0].File), []byte("tampered"), 0644); err != nil {
		t.Fatal(err)
	}
	if Verify(first, publicFile) == nil {
		t.Fatal("tampered release verified")
	}
	if _, err = command(ctx, source, environment(), "go", "run", "./cmd/verify-release", "--dir", first, "--public-key", publicFile); err == nil {
		t.Fatal("verification CLI accepted tampering")
	}
	t.Log("eight builds: every unsigned byte matched; four executable formats checked; package/sign/verify CLIs exercised; ephemeral signatures matched; native version ran; tampering rejected")
}

func qualificationBinary(t *testing.T, path string) []byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	h, err := tr.Next()
	if err != nil || h.Name != "darwin" || h.Typeflag != tar.TypeReg || h.Mode != 0755 || h.Size > maxArtifact {
		t.Fatalf("unexpected archive header: %v", err)
	}
	body, err := io.ReadAll(io.LimitReader(tr, maxArtifact+1))
	if err != nil || int64(len(body)) != h.Size {
		t.Fatal("archive size mismatch", err)
	}
	if _, err = tr.Next(); err != io.EOF {
		t.Fatal("unexpected trailing archive entry", err)
	}
	return body
}
