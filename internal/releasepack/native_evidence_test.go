package releasepack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/stateschema"
)

type cancelOnNativeQualification struct {
	destination io.Writer
	cancel      context.CancelFunc
}

func (w cancelOnNativeQualification) Write(body []byte) (int, error) {
	n, err := w.destination.Write(body)
	if bytes.Contains(body, []byte("== make qualify-release-test passed ==")) {
		w.cancel()
	}
	return n, err
}

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

func TestNativeEvidenceSchemaTwoBindsInstallRehearsal(t *testing.T) {
	record := nativeEvidenceFixture()
	record.SchemaVersion = 2
	record.InstallRehearsal = &NativeInstallEvidenceBinding{
		RecordSHA256: testInstallDigest("1"),
		ArtifactName: "DarwinRouter_1.0.0-rc.3_darwin_arm64.tar.gz", ArtifactSHA256: testInstallDigest("2"),
		BackupSHA256: testInstallDigest("3"), SourceSchema: 29, CurrentSchema: stateschema.Current,
	}
	if validateNativeEvidence(nativeEvidenceBody(t, record)) != nil {
		t.Fatal("schema-2 native evidence rejected")
	}
	for name, mutate := range map[string]func(*NativeEvidence){
		"missing":         func(r *NativeEvidence) { r.InstallRehearsal = nil },
		"record":          func(r *NativeEvidence) { r.InstallRehearsal.RecordSHA256 = "bad" },
		"artifact_name":   func(r *NativeEvidence) { r.InstallRehearsal.ArtifactName = "other.tar.gz" },
		"artifact_digest": func(r *NativeEvidence) { r.InstallRehearsal.ArtifactSHA256 = "bad" },
		"backup":          func(r *NativeEvidence) { r.InstallRehearsal.BackupSHA256 = "bad" },
		"source_schema":   func(r *NativeEvidence) { r.InstallRehearsal.SourceSchema = 28 },
		"current_schema":  func(r *NativeEvidence) { r.InstallRehearsal.CurrentSchema = stateschema.Current - 1 },
	} {
		t.Run(name, func(t *testing.T) {
			changed := record
			binding := *record.InstallRehearsal
			changed.InstallRehearsal = &binding
			mutate(&changed)
			if validateNativeEvidence(nativeEvidenceBody(t, changed)) == nil {
				t.Fatal("invalid schema-2 binding accepted")
			}
		})
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
	installFixture := filepath.Join(t.TempDir(), "install-fixture.json")
	writeNativeInstallFixture(t, installFixture, "1.0.0-rc.3", commit, runtime.GOOS, runtime.GOARCH, nil)
	script := "#!/bin/sh\nprintf '%s|%s|%s|%s\\n' \"$*\" \"${DARWIN_RELEASE_VERSION-}\" \"${DARWIN_RELEASE_COMMIT-}\" \"${DARWIN_INSTALL_REHEARSAL_EVIDENCE_OUT-}\" >> \"" + logPath + "\"\nif [ \"$1\" = qualify-release-test ]; then cp \"" + installFixture + "\" \"$DARWIN_INSTALL_REHEARSAL_EVIDENCE_OUT\"; fi\n"
	if err = os.WriteFile(filepath.Join(bin, "make"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	out := filepath.Join(t.TempDir(), "native.json")
	installOut := filepath.Join(t.TempDir(), "install.json")
	var transcript bytes.Buffer
	if err = QualifyNativeRelease(ctx, Options{Version: "1.0.0-rc.3", Commit: commit, Source: source, Out: out, InstallEvidenceOut: installOut}, &transcript); err != nil {
		t.Fatal(err)
	}
	logBody, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "check|||\nqualify-mvp|1.0.0-rc.3|" + commit + "|" + installOut + "\nqualify-release-test|1.0.0-rc.3|" + commit + "|" + installOut + "\n"
	if string(logBody) != want {
		t.Fatalf("gate invocation mismatch: %q", logBody)
	}
	for _, marker := range []string{"darwin-native-evidence version=1.0.0-rc.3 commit=" + commit, "== make check ==", "== make check passed ==", "== make qualify-mvp ==", "== make qualify-mvp passed ==", "== make qualify-release-test ==", "== make qualify-release-test passed =="} {
		if !bytes.Contains(transcript.Bytes(), []byte(marker)) {
			t.Fatal("complete transcript missing marker", marker)
		}
	}
	if VerifyNativeEvidence(out) != nil {
		t.Fatal("completed qualification did not write valid evidence")
	}
	primaryBody, err := os.ReadFile(out)
	var primary NativeEvidence
	if err != nil || json.Unmarshal(primaryBody, &primary) != nil || primary.SchemaVersion != 2 || primary.InstallRehearsal == nil {
		t.Fatal("primary evidence omitted install binding", err)
	}
	installBody, err := os.ReadFile(installOut)
	if err != nil || primary.InstallRehearsal.RecordSHA256 != installEvidenceDigest(installBody) || primary.InstallRehearsal.ArtifactName != "DarwinRouter_1.0.0-rc.3_"+runtime.GOOS+"_"+runtime.GOARCH+".tar.gz" {
		t.Fatal("primary evidence install binding mismatch", err)
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

func TestQualifyNativeReleaseRequiresBoundInstallEvidenceBeforePrimary(t *testing.T) {
	for name, setup := range map[string]func(*testing.T, string, string, string, string) string{
		"missing":      func(_ *testing.T, _, _, _, _ string) string { return ":" },
		"gate_failure": func(_ *testing.T, _, _, _, _ string) string { return "exit 7" },
		"malformed":    func(_ *testing.T, _, _, _, out string) string { return "printf bad > \"" + out + "\"" },
		"commit_mismatch": func(t *testing.T, _, goos, goarch, out string) string {
			fixture := filepath.Join(t.TempDir(), "fixture.json")
			writeNativeInstallFixture(t, fixture, "1.0.0", strings.Repeat("f", 40), goos, goarch, nil)
			return "cp \"" + fixture + "\" \"" + out + "\""
		},
		"version_mismatch": func(t *testing.T, commit, goos, goarch, out string) string {
			fixture := filepath.Join(t.TempDir(), "fixture.json")
			writeNativeInstallFixture(t, fixture, "1.0.1", commit, goos, goarch, nil)
			return "cp \"" + fixture + "\" \"" + out + "\""
		},
		"target_mismatch": func(t *testing.T, commit, goos, goarch, out string) string {
			fixture := filepath.Join(t.TempDir(), "fixture.json")
			writeNativeInstallFixture(t, fixture, "1.0.0", commit, goos, goarch, func(r *InstallRehearsalEvidence) {
				if r.Target.OS == "darwin" {
					r.Target.OS = "linux"
				} else {
					r.Target.OS = "darwin"
				}
				r.Artifact.Name = "DarwinRouter_1.0.0_" + r.Target.OS + "_" + r.Target.Arch + ".tar.gz"
				r.Rollback.TargetOS = r.Target.OS
			})
			return "cp \"" + fixture + "\" \"" + out + "\""
		},
	} {
		t.Run(name, func(t *testing.T) {
			source, commit := nativeEvidenceGitFixture(t)
			out, installOut := filepath.Join(t.TempDir(), "native.json"), filepath.Join(t.TempDir(), "install.json")
			bin := t.TempDir()
			operation := setup(t, commit, runtime.GOOS, runtime.GOARCH, installOut)
			script := "#!/bin/sh\nif [ \"$1\" = qualify-release-test ]; then " + operation + "; fi\n"
			if err := os.WriteFile(filepath.Join(bin, "make"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			err := QualifyNativeRelease(context.Background(), Options{Version: "1.0.0", Commit: commit, Source: source, Out: out, InstallEvidenceOut: installOut}, io.Discard)
			if err == nil {
				t.Fatal("unbound install evidence accepted")
			}
			if _, statErr := os.Lstat(out); !os.IsNotExist(statErr) {
				t.Fatal("failure left primary evidence", statErr)
			}
		})
	}
}

func TestQualifyNativeReleasePreflightsBothEvidenceDestinations(t *testing.T) {
	for name, paths := range map[string]func(*testing.T, string) (string, string){
		"existing_install": func(t *testing.T, _ string) (string, string) {
			out, install := filepath.Join(t.TempDir(), "native.json"), filepath.Join(t.TempDir(), "install.json")
			if err := os.WriteFile(install, []byte("existing"), 0600); err != nil {
				t.Fatal(err)
			}
			return out, install
		},
		"symlink_install": func(t *testing.T, _ string) (string, string) {
			out, root := filepath.Join(t.TempDir(), "native.json"), t.TempDir()
			target, install := filepath.Join(root, "target"), filepath.Join(root, "install.json")
			if err := os.WriteFile(target, []byte("existing"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, install); err != nil {
				t.Fatal(err)
			}
			return out, install
		},
		"inside_source": func(t *testing.T, source string) (string, string) {
			return filepath.Join(t.TempDir(), "native.json"), filepath.Join(source, "install.json")
		},
		"same_output": func(t *testing.T, _ string) (string, string) {
			out := filepath.Join(t.TempDir(), "same.json")
			return out, out
		},
		"existing_primary": func(t *testing.T, _ string) (string, string) {
			out, install := filepath.Join(t.TempDir(), "native.json"), filepath.Join(t.TempDir(), "install.json")
			if err := os.WriteFile(out, []byte("existing"), 0600); err != nil {
				t.Fatal(err)
			}
			return out, install
		},
		"symlink_primary": func(t *testing.T, _ string) (string, string) {
			root, install := t.TempDir(), filepath.Join(t.TempDir(), "install.json")
			target, out := filepath.Join(root, "target"), filepath.Join(root, "native.json")
			if err := os.WriteFile(target, []byte("existing"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, out); err != nil {
				t.Fatal(err)
			}
			return out, install
		},
		"inside_source_primary": func(t *testing.T, source string) (string, string) {
			return filepath.Join(source, "native.json"), filepath.Join(t.TempDir(), "install.json")
		},
	} {
		t.Run(name, func(t *testing.T) {
			source, commit := nativeEvidenceGitFixture(t)
			out, install := paths(t, source)
			bin, marker := t.TempDir(), filepath.Join(t.TempDir(), "make-ran")
			if err := os.WriteFile(filepath.Join(bin, "make"), []byte("#!/bin/sh\ntouch \""+marker+"\"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			if err := QualifyNativeRelease(context.Background(), Options{Version: "1.0.0", Commit: commit, Source: source, Out: out, InstallEvidenceOut: install}, io.Discard); err == nil {
				t.Fatal("unsafe destination accepted")
			}
			if _, err := os.Lstat(marker); !os.IsNotExist(err) {
				t.Fatal("gate ran before output preflight")
			}
			if name != "existing_primary" && name != "symlink_primary" {
				if _, err := os.Lstat(out); !os.IsNotExist(err) {
					t.Fatal("failure left primary evidence", err)
				}
			} else if VerifyNativeEvidence(out) == nil {
				t.Fatal("unsafe preexisting primary path became valid evidence")
			}
		})
	}
}

func TestQualifyNativeReleaseFailureBoundariesLeaveNoEvidence(t *testing.T) {
	for name, body := range map[string]string{
		"failed_check":           `if [ "$1" = check ]; then exit 7; fi`,
		"failed_mvp":             `if [ "$1" = qualify-mvp ]; then exit 7; fi`,
		"failed_release_test":    `if [ "$1" = qualify-release-test ]; then exit 7; fi`,
		"mutation_after_check":   `if [ "$1" = check ]; then touch "$NATIVE_TEST_SOURCE/dirty"; fi`,
		"mutation_after_mvp":     `if [ "$1" = qualify-mvp ]; then touch "$NATIVE_TEST_SOURCE/dirty"; fi`,
		"mutation_after_qualify": `if [ "$1" = qualify-release-test ]; then touch "$NATIVE_TEST_SOURCE/dirty"; fi`,
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

func TestQualifyNativeReleaseLateCancellationLeavesNoEvidence(t *testing.T) {
	source, commit := nativeEvidenceGitFixture(t)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "make"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	out := filepath.Join(t.TempDir(), "native.json")
	ctx, cancel := context.WithCancel(context.Background())
	log := cancelOnNativeQualification{destination: io.Discard, cancel: cancel}
	err := QualifyNativeRelease(ctx, Options{Version: "1.0.0", Commit: commit, Source: source, Out: out}, log)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("late cancellation returned %v", err)
	}
	if _, statErr := os.Lstat(out); !os.IsNotExist(statErr) {
		t.Fatal("late cancellation wrote primary evidence", statErr)
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

func writeNativeInstallFixture(t *testing.T, path, version, commit, goos, goarch string, mutate func(*InstallRehearsalEvidence)) {
	t.Helper()
	record := validInstallEvidenceFixture()
	record.Release.Version, record.Release.Commit = version, commit
	record.Target = NativeEvidenceTarget{OS: goos, Arch: goarch}
	record.Artifact.Name = "DarwinRouter_" + version + "_" + goos + "_" + goarch + ".tar.gz"
	record.Installation.BinaryVersion = version
	record.Rollback.BinaryVersion, record.Rollback.TargetOS, record.Rollback.TargetArch = version, goos, goarch
	if mutate != nil {
		mutate(&record)
	}
	if err := os.WriteFile(path, canonicalInstallEvidence(t, record), 0600); err != nil {
		t.Fatal(err)
	}
}
