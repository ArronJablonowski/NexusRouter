package releasepack

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"unicode/utf8"
)

// packageNoticeSources uses module-relative package identity, never an ambient
// package directory supplied by go list. Only target-selected Go files qualify.
func packageNoticeSources(pkg listedPackage) ([]string, error) {
	if pkg.Module == nil || (pkg.ImportPath != pkg.Module.Path && !strings.HasPrefix(pkg.ImportPath, pkg.Module.Path+"/")) {
		return nil, ErrInvalid
	}
	prefix := strings.TrimPrefix(strings.TrimPrefix(pkg.ImportPath, pkg.Module.Path), "/")
	var result []string
	for _, name := range pkg.GoFiles {
		if path.Base(name) != name || strings.ContainsAny(name, "\\\x00\r\n") || !strings.HasSuffix(name, ".go") || name == ".go" {
			return nil, ErrInvalid
		}
		result = append(result, path.Join(prefix, name))
	}
	return result, nil
}

// Source notices supplement root legal files. This is conservative textual
// attribution, not an assertion of license completeness or linker reachability.
func appendSourceNotices(root *os.Root, files []noticeFile, sources []string) ([]noticeFile, error) {
	sources = append([]string(nil), sources...)
	sort.Strings(sources)
	total := 0
	names := map[string]bool{}
	for _, f := range files {
		total += len(f.Body)
		names[f.Name] = true
	}
	previous := ""
	var sourceBytes int64
	var combined bytes.Buffer
	for _, source := range sources {
		if source == previous {
			continue
		}
		previous = source
		if source == "" || path.Clean(source) != source || strings.HasPrefix(source, "/") || source == ".." || strings.HasPrefix(source, "../") || strings.ContainsAny(source, "\\\x00\r\n") {
			return nil, ErrInvalid
		}
		body, err := readNoticeSource(root, source)
		if err != nil {
			return nil, err
		}
		sourceBytes += int64(len(body))
		if sourceBytes > 256<<20 {
			return nil, ErrInvalid
		}
		set := token.NewFileSet()
		parsed, err := parser.ParseFile(set, source, body, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return nil, ErrInvalid
		}
		var comments bytes.Buffer
		seenComments := map[string]bool{}
		for _, group := range parsed.Comments {
			start, end := set.Position(group.Pos()).Offset, set.Position(group.End()).Offset
			raw := body[start:end]
			lower := strings.ToLower(string(raw))
			if !strings.Contains(lower, "copyright") && !strings.Contains(lower, "redistribution") && !strings.Contains(lower, "permission is hereby") && !strings.Contains(lower, "public domain") {
				continue
			}
			if seenComments[string(raw)] {
				continue
			}
			seenComments[string(raw)] = true
			comments.Write(raw)
			comments.WriteByte('\n')
			comments.WriteByte('\n')
		}
		if comments.Len() == 0 {
			continue
		}
		sourceHash := sha256.Sum256(body)
		extracted := []byte("Attribution comments from target-selected Go source: " + source + "\nSource-SHA-256: " + hex.EncodeToString(sourceHash[:]) + "\n\n" + comments.String())
		total += len(extracted)
		if total > maxNotice {
			return nil, ErrInvalid
		}
		combined.Write(extracted)
	}
	if combined.Len() > 0 {
		const name = "NOTICE-SOURCE-ATTRIBUTIONS.txt"
		if names[name] || len(files) >= 1000 {
			return nil, ErrInvalid
		}
		files = append(files, noticeFile{Name: name, Body: combined.Bytes()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return files, nil
}

func readNoticeSource(root *os.Root, name string) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > 64<<20 {
		return nil, ErrInvalid
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, ErrInvalid
	}
	opened, statErr := f.Stat()
	body, readErr := io.ReadAll(io.LimitReader(f, info.Size()+1))
	final, finalErr := f.Stat()
	closeErr := f.Close()
	named, namedErr := root.Lstat(name)
	if statErr != nil || readErr != nil || finalErr != nil || closeErr != nil || namedErr != nil || !named.Mode().IsRegular() || !os.SameFile(info, opened) || !os.SameFile(opened, final) || !os.SameFile(final, named) || int64(len(body)) != info.Size() || opened.Size() != info.Size() || final.Size() != info.Size() || named.Size() != info.Size() || !utf8.Valid(body) || bytes.IndexByte(body, 0) >= 0 {
		return nil, ErrInvalid
	}
	return body, nil
}
