package releasepack

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSourceNoticesPreserveNestedRedistribution(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	notice := "/*-\n * Copyright (c) 2003 Poul-Henning Kamp\n * Redistribution in binary form must reproduce this notice.\n */"
	source := "package nested\n\n" + notice + "\n\n" + notice + "\nfunc Used() {}\n// ordinary implementation detail\n"
	for name, body := range map[string]string{"LICENSE": "Root license\n", "nested/selected.go": source, "nested/other.go": "package nested\n// Copyright Unselected Target\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	pkg := listedPackage{ImportPath: "example.org/lib/nested", Module: &listedModule{Path: "example.org/lib"}, GoFiles: []string{"selected.go"}}
	paths, err := packageNoticeSources(pkg)
	if err != nil {
		t.Fatal(err)
	}
	first, err := noticeFiles(dir, paths...)
	if err != nil {
		t.Fatal(err)
	}
	second, err := noticeFiles(dir, append(paths, paths...)...)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatal("nondeterministic or duplicate notices", err)
	}
	if len(first) != 2 || !strings.HasPrefix(first[1].Name, "NOTICE-SOURCE-") {
		t.Fatal(first)
	}
	body := first[1].Body
	if bytes.Count(body, []byte(notice)) != 1 || !bytes.Contains(body, []byte("nested/selected.go")) || !bytes.Contains(body, []byte("Source-SHA-256:")) || bytes.Contains(body, []byte("Unselected Target")) || bytes.Contains(body, []byte("ordinary implementation detail")) {
		t.Fatal(string(body))
	}
	if err := os.WriteFile(filepath.Join(dir, "nested/selected.go"), []byte(strings.ReplaceAll(source, "2003", "2004")), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := noticeFiles(dir, paths...)
	if err != nil || bytes.Equal(changed[1].Body, body) {
		t.Fatal("source change not bound", err)
	}
}

func TestSourceNoticesRejectUnsafeSources(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "LICENSE"), []byte("license"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "valid.go"), []byte("package example\n// Copyright example\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("valid.go", filepath.Join(dir, "linked.go")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../outside.go", "/outside.go", "linked.go", "missing.go", "a/../valid.go"} {
		t.Run(name, func(t *testing.T) {
			if _, err := noticeFiles(dir, name); err == nil {
				t.Fatal("unsafe source accepted")
			}
		})
	}
	for _, name := range []string{"../valid.go", "/valid.go", "a\\valid.go", ".go", "file.c"} {
		if _, err := packageNoticeSources(listedPackage{ImportPath: "example.org/lib", Module: &listedModule{Path: "example.org/lib"}, GoFiles: []string{name}}); err == nil {
			t.Fatal("unsafe go list file", name)
		}
	}
	if _, err := packageNoticeSources(listedPackage{ImportPath: "example.org/lib-evil", Module: &listedModule{Path: "example.org/lib"}, GoFiles: []string{"valid.go"}}); err == nil {
		t.Fatal("module escape accepted")
	}
}
