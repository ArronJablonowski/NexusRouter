package releasepack

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const noticeName = "THIRD_PARTY_NOTICES.txt"
const maxNotice = 2 << 20

var noticeModulePath = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._~+/\-]{0,511}$`)
var noticeModuleVersion = regexp.MustCompile(`^v[0-9][0-9A-Za-z.+\-]{0,127}$`)

type listedPackage struct {
	Module *listedModule `json:"Module"`
}

type listedModule struct {
	Path    string        `json:"Path"`
	Version string        `json:"Version"`
	Dir     string        `json:"Dir"`
	Main    bool          `json:"Main"`
	Replace *listedModule `json:"Replace"`
}

type noticeModule struct {
	Path, Version string
	Files         []noticeFile
}

type noticeFile struct {
	Name string
	Body []byte
}

// thirdPartyNotices derives the attribution bundle from the exact target's
// cmd/darwin build closure. It intentionally excludes the main module: the
// project license is a separate release-approval decision.
func thirdPartyNotices(ctx context.Context, source, targetOS, targetArch string, env []string) ([]byte, error) {
	listEnv := append(append([]string(nil), env...), "GOOS="+targetOS, "GOARCH="+targetArch)
	toolchain, goExecutable, err := goToolchainAttribution(ctx, source, env)
	if err != nil {
		return nil, err
	}
	out, err := command(ctx, source, listEnv, goExecutable, "list", "-mod=readonly", "-deps", "-json", "./cmd/darwin")
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(strings.NewReader(out))
	modules := map[string]listedModule{}
	for {
		var pkg listedPackage
		decodeErr := decoder.Decode(&pkg)
		if decodeErr == io.EOF {
			break
		}
		if decodeErr != nil {
			return nil, ErrInvalid
		}
		if pkg.Module == nil || pkg.Module.Main {
			continue
		}
		module := *pkg.Module
		// Local or alternate replacements make provenance ambiguous. A release
		// must resolve them explicitly rather than silently attributing one path
		// while shipping another module's code.
		if module.Replace != nil || module.Path == "" || module.Version == "" || module.Dir == "" {
			return nil, ErrInvalid
		}
		key := module.Path + "@" + module.Version
		if previous, ok := modules[key]; ok && previous.Dir != module.Dir {
			return nil, ErrInvalid
		}
		modules[key] = module
	}
	if len(modules) == 0 {
		return nil, ErrInvalid
	}
	items := make([]noticeModule, 0, len(modules)+1)
	for _, module := range modules {
		files, err := noticeFiles(module.Dir)
		if err != nil {
			return nil, err
		}
		items = append(items, noticeModule{Path: module.Path, Version: module.Version, Files: files})
	}
	items = append(items, toolchain)
	sort.Slice(items, func(i, j int) bool {
		if items[i].Path == items[j].Path {
			return items[i].Version < items[j].Version
		}
		return items[i].Path < items[j].Path
	})
	return renderThirdPartyNotices(targetOS, targetArch, items)
}

func noticeFiles(dir string) ([]noticeFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		upper := strings.ToUpper(entry.Name())
		if entry.Type().IsRegular() && (strings.HasPrefix(upper, "LICENSE") || strings.HasPrefix(upper, "COPYING") || strings.HasPrefix(upper, "NOTICE") || strings.HasPrefix(upper, "PATENTS")) {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 || len(names) > 1_000 {
		return nil, ErrInvalid
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	files := make([]noticeFile, 0, len(names))
	total := 0
	for _, name := range names {
		info, err := root.Stat(name)
		if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maxNotice {
			return nil, ErrInvalid
		}
		body, err := root.ReadFile(name)
		if err != nil || !utf8.Valid(body) || bytes.IndexByte(body, 0) >= 0 {
			return nil, ErrInvalid
		}
		total += len(body)
		if total > maxNotice {
			return nil, ErrInvalid
		}
		files = append(files, noticeFile{Name: name, Body: body})
	}
	return files, nil
}

func renderThirdPartyNotices(targetOS, targetArch string, modules []noticeModule) ([]byte, error) {
	if (targetOS != "darwin" && targetOS != "linux") || (targetArch != "amd64" && targetArch != "arm64") || len(modules) == 0 || len(modules) > 10_000 {
		return nil, ErrInvalid
	}
	ordered := append([]noticeModule(nil), modules...)
	toolchainPresent := false
	for _, module := range ordered {
		toolchainPresent = toolchainPresent || module.Path == goToolchainModulePath
	}
	if !toolchainPresent {
		toolchain, err := embeddedGoToolchainModule()
		if err != nil {
			return nil, err
		}
		ordered = append(ordered, toolchain)
	}
	for i := range ordered {
		ordered[i].Files = append([]noticeFile(nil), ordered[i].Files...)
		sort.Slice(ordered[i].Files, func(a, b int) bool { return ordered[i].Files[a].Name < ordered[i].Files[b].Name })
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Path == ordered[j].Path {
			return ordered[i].Version < ordered[j].Version
		}
		return ordered[i].Path < ordered[j].Path
	})
	var body bytes.Buffer
	fmt.Fprintf(&body, "DarwinRouter third-party notices\nTarget: %s/%s\nGenerated from the cmd/darwin dependency closure and exact Go build toolchain.\nModule-Count: %d\n", targetOS, targetArch, len(ordered))
	previousModule := ""
	toolchainCount := 0
	for _, module := range ordered {
		moduleKey := module.Path + "@" + module.Version
		if !safeNoticeModule(module.Path, module.Version) || moduleKey <= previousModule || len(module.Files) == 0 {
			return nil, ErrInvalid
		}
		previousModule = moduleKey
		if module.Path == goToolchainModulePath {
			if !validGoToolchainModule(module) {
				return nil, ErrInvalid
			}
			toolchainCount++
		}
		fmt.Fprintf(&body, "\n================================================================================\nModule: %s\nVersion: %s\nFile-Count: %d\n", module.Path, module.Version, len(module.Files))
		previousFile, hasLicense := "", false
		for _, file := range module.Files {
			if !safeNoticeFilename(file.Name) || file.Name <= previousFile || len(file.Body) == 0 || !utf8.Valid(file.Body) || bytes.IndexByte(file.Body, 0) >= 0 {
				return nil, ErrInvalid
			}
			previousFile = file.Name
			upper := strings.ToUpper(file.Name)
			hasLicense = hasLicense || strings.HasPrefix(upper, "LICENSE") || strings.HasPrefix(upper, "COPYING")
			digest := sha256.Sum256(file.Body)
			if body.Len()+len(file.Body)+1_024 > maxNotice {
				return nil, ErrInvalid
			}
			fmt.Fprintf(&body, "\n--------------------------------------------------------------------------------\nSource-File: %s\nContent-Length: %d\nSHA-256: %s\n--------------------------------------------------------------------------------\n", file.Name, len(file.Body), hex.EncodeToString(digest[:]))
			body.Write(file.Body)
			if file.Body[len(file.Body)-1] != '\n' {
				body.WriteByte('\n')
			}
		}
		if !hasLicense {
			return nil, ErrInvalid
		}
	}
	if toolchainCount != 1 || body.Len() > maxNotice {
		return nil, ErrInvalid
	}
	return body.Bytes(), nil
}

func validateNotice(body []byte, targetOS, targetArch string) error {
	if len(body) < 1 || len(body) > maxNotice || !utf8.Valid(body) || bytes.IndexByte(body, 0) >= 0 {
		return ErrSignature
	}
	reader := bytes.NewReader(body)
	if !expectNoticeLine(reader, "DarwinRouter third-party notices") ||
		!expectNoticeLine(reader, "Target: "+targetOS+"/"+targetArch) ||
		!expectNoticeLine(reader, "Generated from the cmd/darwin dependency closure and exact Go build toolchain.") {
		return ErrSignature
	}
	moduleCount, ok := noticeCountLine(reader, "Module-Count: ")
	if !ok || moduleCount < 1 || moduleCount > 10_000 {
		return ErrSignature
	}
	previousModule := ""
	toolchainCount := 0
	for range moduleCount {
		if !expectNoticeLine(reader, "") || !expectNoticeLine(reader, strings.Repeat("=", 80)) {
			return ErrSignature
		}
		module, ok := noticeFieldLine(reader, "Module: ")
		if !ok {
			return ErrSignature
		}
		version, ok := noticeFieldLine(reader, "Version: ")
		if !ok || !safeNoticeModule(module, version) || module+"@"+version <= previousModule {
			return ErrSignature
		}
		previousModule = module + "@" + version
		fileCount, ok := noticeCountLine(reader, "File-Count: ")
		if !ok || fileCount < 1 || fileCount > 1_000 {
			return ErrSignature
		}
		previousFile, hasLicense := "", false
		var observedFiles []noticeFile
		for range fileCount {
			if !expectNoticeLine(reader, "") || !expectNoticeLine(reader, strings.Repeat("-", 80)) {
				return ErrSignature
			}
			name, ok := noticeFieldLine(reader, "Source-File: ")
			if !ok || !safeNoticeFilename(name) || name <= previousFile {
				return ErrSignature
			}
			previousFile = name
			upper := strings.ToUpper(name)
			hasLicense = hasLicense || strings.HasPrefix(upper, "LICENSE") || strings.HasPrefix(upper, "COPYING")
			length, ok := noticeCountLine(reader, "Content-Length: ")
			if !ok || length < 1 || length > maxNotice {
				return ErrSignature
			}
			digestText, ok := noticeFieldLine(reader, "SHA-256: ")
			if !ok || len(digestText) != 64 || strings.ToLower(digestText) != digestText {
				return ErrSignature
			}
			digest, err := hex.DecodeString(digestText)
			if err != nil || !expectNoticeLine(reader, strings.Repeat("-", 80)) {
				return ErrSignature
			}
			content := make([]byte, length)
			if _, err = io.ReadFull(reader, content); err != nil || !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0 {
				return ErrSignature
			}
			actual := sha256.Sum256(content)
			if !bytes.Equal(actual[:], digest) {
				return ErrSignature
			}
			if content[len(content)-1] != '\n' {
				separator, err := reader.ReadByte()
				if err != nil || separator != '\n' {
					return ErrSignature
				}
			}
			observedFiles = append(observedFiles, noticeFile{Name: name, Body: content})
		}
		if !hasLicense {
			return ErrSignature
		}
		if module == goToolchainModulePath {
			if !validGoToolchainModule(noticeModule{Path: module, Version: version, Files: observedFiles}) {
				return ErrSignature
			}
			toolchainCount++
		}
	}
	if toolchainCount != 1 || reader.Len() != 0 {
		return ErrSignature
	}
	return nil
}

func safeNoticeField(value string) bool {
	return value != "" && len(value) <= 512 && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n\x00")
}

func safeNoticeModule(path, version string) bool {
	return safeNoticeField(path) && safeNoticeField(version) && noticeModulePath.MatchString(path) &&
		noticeModuleVersion.MatchString(version) && !strings.Contains(path, "..") &&
		!strings.Contains(path, "//") && !strings.HasSuffix(path, "/")
}

func safeNoticeFilename(value string) bool {
	if !safeNoticeField(value) || filepath.Base(value) != value {
		return false
	}
	upper := strings.ToUpper(value)
	return strings.HasPrefix(upper, "LICENSE") || strings.HasPrefix(upper, "COPYING") || strings.HasPrefix(upper, "NOTICE") || strings.HasPrefix(upper, "PATENTS")
}

func expectNoticeLine(reader *bytes.Reader, want string) bool {
	line, err := noticeLine(reader)
	return err == nil && line == want
}

func noticeFieldLine(reader *bytes.Reader, prefix string) (string, bool) {
	line, err := noticeLine(reader)
	if err != nil || !strings.HasPrefix(line, prefix) {
		return "", false
	}
	value := strings.TrimPrefix(line, prefix)
	return value, safeNoticeField(value)
}

func noticeCountLine(reader *bytes.Reader, prefix string) (int, bool) {
	value, ok := noticeFieldLine(reader, prefix)
	if !ok || len(value) > 6 || (len(value) > 1 && value[0] == '0') {
		return 0, false
	}
	count, err := strconv.Atoi(value)
	return count, err == nil
}

func noticeLine(reader *bytes.Reader) (string, error) {
	line := make([]byte, 0, 128)
	for len(line) <= 1_024 {
		value, err := reader.ReadByte()
		if err != nil {
			return "", ErrSignature
		}
		if value == '\n' {
			return string(line), nil
		}
		line = append(line, value)
	}
	return "", ErrSignature
}
