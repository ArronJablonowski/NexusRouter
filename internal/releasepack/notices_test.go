package releasepack

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRenderThirdPartyNoticesDeterministic(t *testing.T) {
	modules := []noticeModule{
		{Path: "example.com/b", Version: "v2.0.0", Files: []noticeFile{{Name: "LICENSE", Body: []byte("license without newline")}, {Name: "NOTICE", Body: []byte("notice without newline")}}},
		{Path: "example.com/a", Version: "v1.0.0", Files: []noticeFile{{Name: "LICENSE", Body: []byte("license\n")}}},
	}
	first, err := renderThirdPartyNotices("linux", "amd64", modules)
	if err != nil {
		t.Fatal(err)
	}
	second, err := renderThirdPartyNotices("linux", "amd64", modules)
	if err != nil || !bytes.Equal(first, second) {
		t.Fatal("notice rendering is not deterministic", err)
	}
	if bytes.Index(first, []byte("Module: example.com/a")) > bytes.Index(first, []byte("Module: example.com/b")) {
		t.Fatal("modules are not sorted")
	}
	if validateNotice(first, "linux", "amd64") != nil || validateNotice(first, "darwin", "amd64") == nil {
		t.Fatal("notice target validation failed")
	}
}

func TestValidateNoticeRejectsMalformedOrTamperedContent(t *testing.T) {
	valid, err := renderThirdPartyNotices("linux", "amd64", []noticeModule{{
		Path: "example.com/dependency", Version: "v1.0.0",
		Files: []noticeFile{{Name: "LICENSE", Body: []byte("license text\n")}},
	}})
	if err != nil || validateNotice(valid, "linux", "amd64") != nil {
		t.Fatal("valid notice rejected", err)
	}
	for name, mutate := range map[string]func([]byte) []byte{
		"extra_bytes": func(body []byte) []byte { return append(body, 'x') },
		"wrong_target": func(body []byte) []byte {
			return bytes.Replace(body, []byte("Target: linux/amd64"), []byte("Target: darwin/amd64"), 1)
		},
		"unsafe_module": func(body []byte) []byte {
			return bytes.Replace(body, []byte("example.com/dependency"), []byte("../unsafe/dependency"), 1)
		},
		"digest_tamper": func(body []byte) []byte {
			return bytes.Replace(body, []byte("license text"), []byte("license tExt"), 1)
		},
		"no_license": func(body []byte) []byte {
			return bytes.Replace(body, []byte("Source-File: LICENSE"), []byte("Source-File: NOTICE"), 1)
		},
		"bad_count": func(body []byte) []byte {
			return bytes.Replace(body, []byte("Module-Count: 2"), []byte("Module-Count: 3"), 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			body := mutate(append([]byte(nil), valid...))
			if validateNotice(body, "linux", "amd64") == nil {
				t.Fatal("malformed notice accepted")
			}
		})
	}
}

func TestNoticeFilesIncludesCompleteLegalSet(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"LICENSE":              "primary",
		"LICENSE-3RD-PARTY.md": "nested attributions",
		"NOTICE":               "notice",
		"PATENTS":              "patents",
		"README.md":            "not legal payload",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	files, err := noticeFiles(dir)
	if err != nil || len(files) != 4 {
		t.Fatalf("legal file collection: %d: %v", len(files), err)
	}
	if files[0].Name != "LICENSE" || files[1].Name != "LICENSE-3RD-PARTY.md" || files[2].Name != "NOTICE" || files[3].Name != "PATENTS" {
		t.Fatal("unexpected legal file ordering", files)
	}
	empty := t.TempDir()
	if _, err = noticeFiles(empty); err != ErrInvalid {
		t.Fatal("module without legal files accepted", err)
	}
	symlinkOnly := t.TempDir()
	if err = os.Symlink(filepath.Join(dir, "LICENSE"), filepath.Join(symlinkOnly, "LICENSE")); err != nil {
		t.Fatal(err)
	}
	if _, err = noticeFiles(symlinkOnly); err != ErrInvalid {
		t.Fatal("symlink legal file accepted", err)
	}
}

func TestThirdPartyNoticesMatchesCurrentTargetClosure(t *testing.T) {
	ctx := context.Background()
	root, err := command(ctx, ".", environment(), "git", "rev-parse", "--show-toplevel")
	if err != nil {
		t.Fatal(err)
	}
	notices := map[string][]byte{}
	for _, target := range [][2]string{{"darwin", "amd64"}, {"darwin", "arm64"}, {"linux", "amd64"}, {"linux", "arm64"}} {
		body, noticeErr := thirdPartyNotices(ctx, root, target[0], target[1], environment())
		if noticeErr != nil || validateNotice(body, target[0], target[1]) != nil {
			t.Fatal(target, noticeErr)
		}
		for _, required := range []string{"Module: go.dev/toolchain", "Version: v" + strings.TrimPrefix(runtime.Version(), "go"), "Source-File: LICENSE", "Source-File: PATENTS", "Copyright 2009 The Go Authors."} {
			if !strings.Contains(string(body), required) {
				t.Fatal("Go toolchain attribution missing", target, required)
			}
		}
		notices[target[0]+"/"+target[1]] = body
	}
	darwin, linux := notices["darwin/amd64"], notices["linux/amd64"]
	if !bytes.Contains(darwin, []byte("Module: github.com/ncruces/go-strftime")) || bytes.Contains(linux, []byte("Module: github.com/ncruces/go-strftime")) {
		t.Fatal("target-specific module closures were not preserved")
	}
	for _, required := range []string{"Module: modernc.org/libc", "Source-File: LICENSE-3RD-PARTY.md", "Module: golang.org/x/sys", "Source-File: PATENTS"} {
		if !strings.Contains(string(darwin), required) || !strings.Contains(string(linux), required) {
			t.Fatal("required dependency notice missing", required)
		}
	}
}

func TestGoToolchainLegalFilesFailClosed(t *testing.T) {
	makeRoot := func(t *testing.T) string {
		t.Helper()
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "LICENSE"), []byte(goLicenseText), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "PATENTS"), []byte(goPatentsText), 0644); err != nil {
			t.Fatal(err)
		}
		return root
	}
	t.Run("exact", func(t *testing.T) {
		files, err := validatedGoLegalFiles(makeRoot(t))
		if err != nil || len(files) != 2 || files[0].Name != "LICENSE" || files[1].Name != "PATENTS" {
			t.Fatal("exact legal files rejected", err)
		}
	})
	t.Run("homebrew_split", func(t *testing.T) {
		parent := t.TempDir()
		root := filepath.Join(parent, "libexec")
		if err := os.Mkdir(root, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(parent, "LICENSE"), []byte(goLicenseText), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "PATENTS"), []byte(goPatentsText), 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := validatedGoLegalFiles(root); err != nil {
			t.Fatal("supported split toolchain rejected", err)
		}
	})
	for _, scenario := range []string{"missing", "mismatch", "symlink"} {
		t.Run(scenario, func(t *testing.T) {
			root := makeRoot(t)
			path := filepath.Join(root, "LICENSE")
			switch scenario {
			case "missing":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "mismatch":
				if err := os.WriteFile(path, []byte("different\n"), 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(root, "PATENTS"), path); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := validatedGoLegalFiles(root); err == nil {
				t.Fatal("invalid toolchain legal files accepted")
			}
		})
	}
}

func TestReleaseExecutableUsesExplicitAbsolutePATH(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "go")
	if err := os.WriteFile(path, []byte("fixture\n"), 0700); err != nil {
		t.Fatal(err)
	}
	resolved, err := releaseExecutable([]string{"PATH=" + directory}, "go")
	expected, evalErr := filepath.EvalSymlinks(path)
	if err != nil || evalErr != nil || resolved != expected {
		t.Fatal("explicit executable rejected", resolved, err)
	}
	for _, env := range [][]string{nil, {"PATH=relative"}, {"PATH=" + directory, "PATH=" + directory}} {
		if _, err := releaseExecutable(env, "go"); err == nil {
			t.Fatal("unsafe PATH accepted", env)
		}
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := releaseExecutable([]string{"PATH=" + directory}, "go"); err == nil {
		t.Fatal("nonexecutable tool accepted")
	}
}

func TestNoticeArchiveOrderModesAndTarget(t *testing.T) {
	notice, err := renderThirdPartyNotices("linux", "arm64", []noticeModule{{
		Path: "example.com/dependency", Version: "v1.0.0",
		Files: []noticeFile{{Name: "LICENSE", Body: []byte("license\n")}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err = Archive(&archive, []Entry{{Name: "darwin", Data: []byte("binary")}, {Name: noticeName, Data: notice}}); err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	header, err := tr.Next()
	if err != nil || !canonicalArchiveHeader(header, noticeName, 0644, maxNotice) {
		t.Fatal("notice entry is not first and canonical", err)
	}
	body, err := io.ReadAll(tr)
	if err != nil || validateNotice(body, "linux", "arm64") != nil || validateNotice(body, "linux", "amd64") == nil {
		t.Fatal("notice body is not target-bound", err)
	}
	header, err = tr.Next()
	if err != nil || !canonicalArchiveHeader(header, "darwin", 0755, maxArtifact) {
		t.Fatal("binary entry is not second and executable", err)
	}
	if body, err = io.ReadAll(tr); err != nil || string(body) != "binary" {
		t.Fatal("binary payload mismatch", err)
	}
	if _, err = tr.Next(); err != io.EOF {
		t.Fatal("unexpected archive entry", err)
	}
}
