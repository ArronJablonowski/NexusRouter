// Package releasepack builds local release artifacts and supports explicit
// offline signing and verification. It never publishes tags or uploads files.
package releasepack

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"
	"path"
	"sort"
	"strings"
	"time"
)

var ErrInvalid = errors.New("invalid release input")

const maxArtifact = 256 << 20

type Entry struct {
	Name string
	Data []byte
}

// Archive writes reproducible portable archives, rejecting paths that could
// escape extraction or depend on platform-specific path interpretation.
func Archive(w io.Writer, entries []Entry) error {
	if w == nil || len(entries) == 0 || len(entries) > 32 {
		return ErrInvalid
	}
	owned := append([]Entry(nil), entries...)
	total := 0
	seen := map[string]bool{}
	for _, e := range owned {
		if e.Name == "" || e.Name == "." || len(e.Name) > 128 || path.Clean(e.Name) != e.Name || strings.ContainsAny(e.Name, "\\:\x00") || strings.HasPrefix(e.Name, "/") || e.Name == ".." || strings.HasPrefix(e.Name, "../") || seen[e.Name] || len(e.Data) == 0 || len(e.Data) > maxArtifact {
			return ErrInvalid
		}
		for _, r := range e.Name {
			if r < 32 || r > 126 {
				return ErrInvalid
			}
		}
		seen[e.Name] = true
		total += len(e.Data)
		if total > maxArtifact {
			return ErrInvalid
		}
	}
	for name := range seen {
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if seen[parent] {
				return ErrInvalid
			}
		}
	}
	sort.Slice(owned, func(i, j int) bool { return owned[i].Name < owned[j].Name })
	gz := gzip.NewWriter(w)
	gz.Header.ModTime = time.Time{}
	gz.Header.OS = 255
	tw := tar.NewWriter(gz)
	for _, e := range owned {
		h := &tar.Header{Name: e.Name, Mode: 0755, Size: int64(len(e.Data)), ModTime: time.Unix(0, 0), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if _, err := tw.Write(e.Data); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}
