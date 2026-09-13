package releasepack

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestArchiveDeterministic(t *testing.T) {
	entries := []Entry{{"z", []byte("last")}, {"darwin", []byte("binary")}}
	var a, b bytes.Buffer
	if err := Archive(&a, entries); err != nil {
		t.Fatal(err)
	}
	if err := Archive(&b, []Entry{entries[1], entries[0]}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) || entries[0].Name != "z" {
		t.Fatal("unstable archive or mutated input")
	}
	gz, err := gzip.NewReader(bytes.NewReader(a.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var names []string
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		names = append(names, h.Name)
		wantMode := int64(0644)
		if h.Name == "darwin" {
			wantMode = 0755
		}
		if h.Uid != 0 || h.Gid != 0 || h.Uname != "" || h.Gname != "" || h.ModTime.Unix() != 0 || h.Mode != wantMode {
			t.Fatal(h)
		}
	}
	if !reflect.DeepEqual(names, []string{"darwin", "z"}) {
		t.Fatal(names)
	}
}

func TestArchiveRejectsPathsAndBounds(t *testing.T) {
	for _, name := range []string{"", "/absolute", "../escape", "a/../../escape", "a//b", "a/./b", "C:drive", "a\\b", "a\nname", "a\x00b", strings.Repeat("x", 129)} {
		if err := Archive(io.Discard, []Entry{{name, []byte("x")}}); err == nil {
			t.Fatal(name)
		}
	}
	if Archive(io.Discard, []Entry{{"a", []byte("x")}, {"a", []byte("y")}}) == nil || Archive(io.Discard, []Entry{{"a", nil}}) == nil || Archive(io.Discard, make([]Entry, 33)) == nil {
		t.Fatal("bounds accepted")
	}
}

func TestReleaseTarStreamLimitIncludesWorstCasePadding(t *testing.T) {
	// One-byte bodies force 511 bytes of padding for each contract entry.
	entries := make([]Entry, len(archiveContract))
	for i, contract := range archiveContract {
		entries[i] = Entry{Name: contract.name, Data: []byte("x")}
	}
	var archive bytes.Buffer
	if err := Archive(&archive, entries); err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	stream, err := io.ReadAll(gz)
	closeErr := gz.Close()
	if err != nil || closeErr != nil {
		t.Fatal("read archive", err, closeErr)
	}
	// Seven headers + seven padded data blocks + two EOF blocks.
	if len(stream) != 16*tarBlockSize {
		t.Fatalf("unexpected canonical tar size: %d", len(stream))
	}
	if maxTarStream != maxReleaseData()+16*tarBlockSize {
		t.Fatal("release tar limit does not reserve every header, padding block, and EOF block")
	}
}

func TestMetadataValidation(t *testing.T) {
	base := Options{Version: "1.0.0-rc.1", Commit: strings.Repeat("a", 40), Out: "out"}
	if validate(base) != nil {
		t.Fatal("valid metadata rejected")
	}
	for _, v := range []string{"", "v1.0.0", "01.0.0", "1.0", "1.0.0-01", "1.0.0-", "1.0.0;bad", "1.0.0\n"} {
		o := base
		o.Version = v
		if validate(o) == nil {
			t.Fatal(v)
		}
	}
	for _, c := range []string{"", strings.Repeat("A", 40), strings.Repeat("a", 39), strings.Repeat("a", 41)} {
		o := base
		o.Commit = c
		if validate(o) == nil {
			t.Fatal(c)
		}
	}
}

func TestPackageRefusesExistingAndCanceled(t *testing.T) {
	out := t.TempDir()
	o := Options{Version: "1.0.0", Commit: strings.Repeat("a", 40), Out: out, Source: out}
	if Package(context.Background(), o) == nil {
		t.Fatal("existing output accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	o.Out = filepath.Join(out, "new")
	if Package(ctx, o) == nil {
		t.Fatal("cancellation ignored")
	}
	if _, err := os.Stat(o.Out); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestPublishNeverOverwrites(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "stage")
	target := filepath.Join(root, "out")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := publish(source, target); err == nil {
		t.Fatal("existing directory overwritten")
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatal("stage lost", err)
	}
}
