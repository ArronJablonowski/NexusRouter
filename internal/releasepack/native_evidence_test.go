package releasepack

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func nativeEvidenceFixture() NativeEvidence {
	return NativeEvidence{
		SchemaVersion: 1, Scope: "single_native_target_only", ReleaseVersion: "1.0.0-rc.3",
		SourceCommit: "0123456789abcdef0123456789abcdef01234567",
		Target:       NativeEvidenceTarget{OS: "darwin", Arch: "arm64"},
		Toolchain:    NativeEvidenceGo{GOHOSTOS: "darwin", GOHOSTARCH: "arm64", GOVERSION: "go1.27.1"},
		Gates:        append([]NativeEvidenceGate(nil), nativeEvidenceGates...),
	}
}

func nativeEvidenceBody(t *testing.T, record NativeEvidence) []byte {
	t.Helper()
	body, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(body, '\n')
}

func TestNativeEvidenceCanonicalSingleTargetContract(t *testing.T) {
	record := nativeEvidenceFixture()
	body := nativeEvidenceBody(t, record)
	if validateNativeEvidence(body) != nil {
		t.Fatal("canonical native evidence rejected")
	}
	path := filepath.Join(t.TempDir(), "native.json")
	if err := os.WriteFile(path, body, 0644); err != nil || VerifyNativeEvidence(path) != nil {
		t.Fatal("native evidence file rejected", err)
	}
	if record.Scope != "single_native_target_only" || record.Target.OS != record.Toolchain.GOHOSTOS || record.Target.Arch != record.Toolchain.GOHOSTARCH {
		t.Fatal("record can imply unavailable targets")
	}
}

func TestNativeEvidenceRejectsFalseOrCrossTargetClaims(t *testing.T) {
	base := nativeEvidenceFixture()
	tests := map[string]func(*NativeEvidence){
		"scope":       func(r *NativeEvidence) { r.Scope = "all_targets" },
		"version":     func(r *NativeEvidence) { r.ReleaseVersion = "v1.0.0" },
		"commit":      func(r *NativeEvidence) { r.SourceCommit = "bad" },
		"target_os":   func(r *NativeEvidence) { r.Target.OS = "windows" },
		"target_arch": func(r *NativeEvidence) { r.Target.Arch = "riscv64" },
		"cross_os":    func(r *NativeEvidence) { r.Toolchain.GOHOSTOS = "linux" },
		"cross_arch":  func(r *NativeEvidence) { r.Toolchain.GOHOSTARCH = "amd64" },
		"toolchain":   func(r *NativeEvidence) { r.Toolchain.GOVERSION = "devel" },
		"gate_failed": func(r *NativeEvidence) { r.Gates[1].Status = "failed" },
		"gate_missing": func(r *NativeEvidence) {
			r.Gates = r.Gates[:len(r.Gates)-1]
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			record := base
			record.Gates = append([]NativeEvidenceGate(nil), base.Gates...)
			mutate(&record)
			if validateNativeEvidence(nativeEvidenceBody(t, record)) == nil {
				t.Fatal("false native evidence accepted")
			}
		})
	}
}

func TestNativeEvidenceRejectsNoncanonicalAndUnsafeFiles(t *testing.T) {
	body := nativeEvidenceBody(t, nativeEvidenceFixture())
	for _, invalid := range [][]byte{body[:len(body)-1], append(append([]byte(nil), body...), '\n'), []byte("{}\n")} {
		if validateNativeEvidence(invalid) == nil {
			t.Fatal("noncanonical native evidence accepted")
		}
	}
	dir := t.TempDir()
	valid := filepath.Join(dir, "valid.json")
	if err := os.WriteFile(valid, body, 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(valid, link); err != nil {
		t.Fatal(err)
	}
	if VerifyNativeEvidence(link) == nil || VerifyNativeEvidence(dir) == nil {
		t.Fatal("unsafe native evidence file accepted")
	}
}

func TestNativeGateTranscriptIsBounded(t *testing.T) {
	var destination bytes.Buffer
	log := &boundedNativeGateLog{destination: &destination}
	body := bytes.Repeat([]byte("x"), 1<<20)
	if n, err := log.Write(body); err != nil || n != len(body) {
		t.Fatal("bounded transcript rejected exact limit", n, err)
	}
	if n, err := log.Write([]byte("x")); err == nil || n != 0 || !log.overflow || destination.Len() != 1<<20 {
		t.Fatal("bounded transcript accepted overflow", n, err, log.overflow, destination.Len())
	}
}

type shortNativeLog struct{}

func (shortNativeLog) Write(body []byte) (int, error) { return len(body) - 1, nil }

func TestNativeTranscriptRejectsShortWrites(t *testing.T) {
	log := &strictNativeLog{destination: shortNativeLog{}}
	if n, err := log.Write([]byte("record")); err != io.ErrShortWrite || n != len("record")-1 {
		t.Fatal("short transcript write accepted", n, err)
	}
}

func TestQualifyNativeReleaseRunsBoundGatesBeforeWritingEvidence(t *testing.T) {
	source := t.TempDir()
	source, err := filepath.EvalSymlinks(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("fixture\n"), 0644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	env := environment()
	for _, step := range [][]string{
		{"init"},
		{"config", "user.email", "native-evidence@example.invalid"},
		{"config", "user.name", "Native Evidence Test"},
		{"add", "."},
		{"commit", "-m", "fixture"},
	} {
		if _, err := command(ctx, source, env, "git", step...); err != nil {
			t.Fatal(step, err)
		}
	}
	commit, err := command(ctx, source, env, "git", "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "make.log")
	script := "#!/bin/sh\nprintf '%s|%s|%s\\n' \"$*\" \"${DARWIN_RELEASE_VERSION-}\" \"${DARWIN_RELEASE_COMMIT-}\" >> \"" + logPath + "\"\n"
	if err = os.WriteFile(filepath.Join(bin, "make"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	out := filepath.Join(t.TempDir(), "native.json")
	var transcript bytes.Buffer
	if err = QualifyNativeRelease(ctx, Options{Version: "1.0.0-rc.3", Commit: commit, Source: source, Out: out}, &transcript); err != nil {
		t.Fatal(err)
	}
	logBody, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "check||\nqualify-release|1.0.0-rc.3|" + commit + "\n"
	if string(logBody) != want {
		t.Fatalf("gate invocation mismatch: %q", logBody)
	}
	for _, marker := range []string{"darwin-native-evidence version=1.0.0-rc.3 commit=" + commit, "== make check ==", "== make check passed ==", "== make qualify-release ==", "== make qualify-release passed =="} {
		if !bytes.Contains(transcript.Bytes(), []byte(marker)) {
			t.Fatal("complete transcript missing marker", marker)
		}
	}
	if VerifyNativeEvidence(out) != nil {
		t.Fatal("completed qualification did not write valid evidence")
	}
	if err = os.WriteFile(filepath.Join(source, "dirty"), []byte("dirty\n"), 0600); err != nil {
		t.Fatal(err)
	}
	second := filepath.Join(t.TempDir(), "native.json")
	if err = QualifyNativeRelease(ctx, Options{Version: "1.0.0-rc.3", Commit: commit, Source: source, Out: second}, io.Discard); err == nil {
		t.Fatal("dirty source produced evidence", err)
	}
	if _, err = os.Lstat(second); !os.IsNotExist(err) {
		t.Fatal("failed qualification left an evidence file", err)
	}
}

func TestQualifyNativeReleaseFailureBoundariesLeaveNoEvidence(t *testing.T) {
	for name, body := range map[string]string{
		"failed_check":           `if [ "$1" = check ]; then exit 7; fi`,
		"failed_qualification":   `if [ "$1" = qualify-release ]; then exit 7; fi`,
		"mutation_after_check":   `if [ "$1" = check ]; then touch "$NATIVE_TEST_SOURCE/dirty"; fi`,
		"mutation_after_qualify": `if [ "$1" = qualify-release ]; then touch "$NATIVE_TEST_SOURCE/dirty"; fi`,
	} {
		t.Run(name, func(t *testing.T) {
			source, commit := nativeEvidenceGitFixture(t)
			bin := t.TempDir()
			script := "#!/bin/sh\n" + strings.ReplaceAll(body, "$NATIVE_TEST_SOURCE", source) + "\n"
			if err := os.WriteFile(filepath.Join(bin, "make"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			out := filepath.Join(t.TempDir(), "native.json")
			var transcript bytes.Buffer
			err := QualifyNativeRelease(context.Background(), Options{Version: "1.0.0-rc.3", Commit: commit, Source: source, Out: out}, &transcript)
			if err == nil {
				t.Fatal("failed or dirty gate produced evidence")
			}
			if _, statErr := os.Lstat(out); !os.IsNotExist(statErr) {
				t.Fatal("failure left evidence file", statErr)
			}
		})
	}
}

func nativeEvidenceGitFixture(t *testing.T) (string, string) {
	t.Helper()
	source, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(source, "README.md"), []byte("fixture\n"), 0644); err != nil {
		t.Fatal(err)
	}
	ctx, env := context.Background(), environment()
	for _, step := range [][]string{{"init"}, {"config", "user.email", "native-evidence@example.invalid"}, {"config", "user.name", "Native Evidence Test"}, {"add", "."}, {"commit", "-m", "fixture"}} {
		if _, err = command(ctx, source, env, "git", step...); err != nil {
			t.Fatal(step, err)
		}
	}
	commit, err := command(ctx, source, env, "git", "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return source, commit
}
