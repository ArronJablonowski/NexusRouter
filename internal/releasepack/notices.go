package releasepack

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
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
	ImportPath string               `json:"ImportPath"`
	GoFiles    []string             `json:"GoFiles"`
	Imports    []string             `json:"Imports"`
	Module     *listedModule        `json:"Module"`
	Standard   bool                 `json:"Standard"`
	Incomplete bool                 `json:"Incomplete"`
	Error      *listedPackageError  `json:"Error"`
	DepsErrors []listedPackageError `json:"DepsErrors"`
}

type listedPackageError struct {
	Err string `json:"Err"`
}

type listedModule struct {
	Path     string        `json:"Path"`
	Version  string        `json:"Version"`
	Dir      string        `json:"Dir"`
	Sum      string        `json:"Sum"`
	GoModSum string        `json:"GoModSum"`
	Main     bool          `json:"Main"`
	Replace  *listedModule `json:"Replace"`
}

type noticeModule struct {
	Path, Version string
	Sum, GoModSum string
	Files         []noticeFile
}

// targetClosure binds the module legal-file set to the complete package graph
// selected for one release target. PackageCount counts every decoded node,
// including the standard library and main module.
type targetClosure struct {
	Modules               []noticeModule
	PackageCount          int
	DependencyGraphSHA256 string
}

type dependencyGraphPackage struct {
	ImportPath string   `json:"import_path"`
	Source     string   `json:"source"`
	Imports    []string `json:"imports"`
}

type noticeFile struct {
	Name string
	Body []byte
}

// thirdPartyNotices derives the attribution bundle from the exact target's
// cmd/nexus build closure. It intentionally excludes the main module: the
// project license is a separate release-approval decision.
func thirdPartyNotices(ctx context.Context, source, targetOS, targetArch string, env []string) ([]byte, error) {
	items, err := targetNoticeModules(ctx, source, targetOS, targetArch, env)
	if err != nil {
		return nil, err
	}
	return renderThirdPartyNotices(targetOS, targetArch, items)
}

// targetNoticeClosure derives canonical schema-v3 dependency and legal-file
// evidence from the pinned, private reconstruction workspace.
func targetNoticeClosure(ctx context.Context, source, targetOS, targetArch string, reconstruction *goReconstruction) (targetClosure, error) {
	if ctx == nil || reconstruction == nil || reconstruction.verify() != nil ||
		(targetOS != "darwin" && targetOS != "linux") || (targetArch != "amd64" && targetArch != "arm64") {
		return targetClosure{}, ErrInvalid
	}
	listEnv := append(append([]string(nil), reconstruction.env...), "GOOS="+targetOS, "GOARCH="+targetArch)
	toolchain, goExecutable, err := goToolchainAttribution(ctx, source, reconstruction.env)
	if err != nil || goExecutable != reconstruction.goExecutable.path || toolchain.Sum != "" || toolchain.GoModSum != "" {
		return targetClosure{}, ErrInvalid
	}
	// Omit build/debug metadata while retaining every field used to validate
	// package completeness, module identity and the full dependency graph.
	// This keeps expanding release closures within the unchanged output bound.
	out, err := reconstruction.goOutput(ctx, source, listEnv, "list", "-mod=readonly", "-deps", "-json=ImportPath,Imports,Module,Standard,Incomplete,Error,DepsErrors,GoFiles", "./cmd/nexus")
	if err != nil {
		return targetClosure{}, err
	}
	if _, err = reconstruction.goOutput(ctx, source, listEnv, "mod", "verify"); err != nil {
		return targetClosure{}, err
	}
	packages, err := decodeListedPackages(out)
	if err != nil {
		return targetClosure{}, err
	}
	closure, err := targetClosureFromPackages(packages, toolchain, reconstruction.moduleCache.path)
	if err != nil {
		return targetClosure{}, err
	}
	if _, err = reconstruction.goOutput(ctx, source, listEnv, "mod", "verify"); err != nil {
		return targetClosure{}, err
	}
	if reconstruction.verify() != nil || reconstruction.goExecutable.verify() != nil {
		return targetClosure{}, ErrInvalid
	}
	return closure, nil
}

func decodeListedPackages(out string) ([]listedPackage, error) {
	decoder := json.NewDecoder(strings.NewReader(out))
	var packages []listedPackage
	for len(packages) <= 100_000 {
		var pkg listedPackage
		err := decoder.Decode(&pkg)
		if err == io.EOF {
			if len(packages) == 0 {
				return nil, ErrInvalid
			}
			return packages, nil
		}
		if err != nil {
			return nil, ErrInvalid
		}
		packages = append(packages, pkg)
	}
	return nil, ErrInvalid
}

func targetClosureFromPackages(packages []listedPackage, toolchain noticeModule, moduleCache string) (targetClosure, error) {
	if len(packages) == 0 || len(packages) > 100_000 || !validGoToolchainModule(toolchain) || toolchain.Sum != "" || toolchain.GoModSum != "" {
		return targetClosure{}, ErrInvalid
	}
	allPackages := make(map[string]bool, len(packages))
	graphPackages := make([]dependencyGraphPackage, 0, len(packages))
	modules := make(map[string]listedModule)
	sources := map[string][]string{}
	mainModulePath := ""
	for _, pkg := range packages {
		if !safeNoticeModulePath(pkg.ImportPath) || allPackages[pkg.ImportPath] || pkg.Incomplete || pkg.Error != nil || len(pkg.DepsErrors) != 0 {
			return targetClosure{}, ErrInvalid
		}
		allPackages[pkg.ImportPath] = true
		sourceIdentity := "stdlib"
		if pkg.Module == nil {
			if !pkg.Standard {
				return targetClosure{}, ErrInvalid
			}
			graphPackages = append(graphPackages, dependencyGraphPackage{ImportPath: pkg.ImportPath, Source: sourceIdentity, Imports: []string{}})
			continue
		}
		if pkg.Standard {
			return targetClosure{}, ErrInvalid
		}
		module := *pkg.Module
		if module.Replace != nil || !safeNoticeModulePath(module.Path) || module.Dir == "" {
			return targetClosure{}, ErrInvalid
		}
		if module.Main {
			if module.Version != "" || module.Sum != "" || module.GoModSum != "" {
				return targetClosure{}, ErrInvalid
			}
			if mainModulePath != "" && mainModulePath != module.Path {
				return targetClosure{}, ErrInvalid
			}
			mainModulePath = module.Path
			sourceIdentity = "main:" + module.Path
		} else {
			if !safeNoticeModule(module.Path, module.Version) || !validGoModuleSum(module.Sum) ||
				!validGoModuleSum(module.GoModSum) || !moduleDirectoryInCache(module.Dir, moduleCache) {
				return targetClosure{}, ErrInvalid
			}
			key := module.Path + "@" + module.Version
			if previous, ok := modules[key]; ok {
				if previous.Dir != module.Dir || previous.Sum != module.Sum || previous.GoModSum != module.GoModSum {
					return targetClosure{}, ErrInvalid
				}
			} else {
				modules[key] = module
			}
			paths, err := packageNoticeSources(pkg)
			if err != nil {
				return targetClosure{}, err
			}
			sources[key] = append(sources[key], paths...)
			sourceIdentity = key
		}
		graphPackages = append(graphPackages, dependencyGraphPackage{ImportPath: pkg.ImportPath, Source: sourceIdentity, Imports: []string{}})
	}
	if mainModulePath == "" || len(modules) == 0 || len(graphPackages) != len(packages) {
		return targetClosure{}, ErrInvalid
	}
	graphByPath := make(map[string]*dependencyGraphPackage, len(graphPackages))
	for i := range graphPackages {
		graphByPath[graphPackages[i].ImportPath] = &graphPackages[i]
	}
	for _, pkg := range packages {
		node := graphByPath[pkg.ImportPath]
		seen := map[string]bool{}
		for _, imported := range pkg.Imports {
			if !allPackages[imported] || seen[imported] {
				return targetClosure{}, ErrInvalid
			}
			seen[imported] = true
			node.Imports = append(node.Imports, imported)
		}
		sort.Strings(node.Imports)
	}
	sort.Slice(graphPackages, func(i, j int) bool { return graphPackages[i].ImportPath < graphPackages[j].ImportPath })
	graphBody, err := json.Marshal(graphPackages)
	if err != nil {
		return targetClosure{}, ErrInvalid
	}
	digest := sha256.Sum256(graphBody)
	closure := targetClosure{PackageCount: len(packages), DependencyGraphSHA256: "sha256:" + hex.EncodeToString(digest[:])}
	for _, module := range modules {
		files, err := noticeFilesInModuleCache(moduleCache, module.Dir, nil, sources[module.Path+"@"+module.Version]...)
		if err != nil {
			return targetClosure{}, err
		}
		closure.Modules = append(closure.Modules, noticeModule{
			Path: module.Path, Version: module.Version, Sum: module.Sum, GoModSum: module.GoModSum, Files: files,
		})
	}
	closure.Modules = append(closure.Modules, toolchain)
	sort.Slice(closure.Modules, func(i, j int) bool {
		if closure.Modules[i].Path == closure.Modules[j].Path {
			return closure.Modules[i].Version < closure.Modules[j].Version
		}
		return closure.Modules[i].Path < closure.Modules[j].Path
	})
	return closure, nil
}

func validGoModuleSum(value string) bool {
	if len(value) != 47 || !strings.HasPrefix(value, "h1:") {
		return false
	}
	digest, err := base64.StdEncoding.Strict().DecodeString(strings.TrimPrefix(value, "h1:"))
	return err == nil && len(digest) == sha256.Size
}

func safeNoticeModulePath(value string) bool {
	return safeNoticeField(value) && noticeModulePath.MatchString(value) && !strings.Contains(value, "..") &&
		!strings.Contains(value, "//") && !strings.HasSuffix(value, "/")
}

func moduleDirectoryInCache(directory, moduleCache string) bool {
	directoryReal, err := filepath.EvalSymlinks(directory)
	if err != nil || !filepath.IsAbs(directoryReal) {
		return false
	}
	cacheReal, err := filepath.EvalSymlinks(moduleCache)
	if err != nil || !filepath.IsAbs(cacheReal) {
		return false
	}
	relative, err := filepath.Rel(cacheReal, directoryReal)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// targetNoticeModules captures the complete target-specific legal-file closure
// once. Callers can therefore derive both structured evidence and the rendered
// notice from the same owned bytes instead of independently re-reading mutable
// module-cache paths.
func targetNoticeModules(ctx context.Context, source, targetOS, targetArch string, env []string) ([]noticeModule, error) {
	listEnv := append(append([]string(nil), env...), "GOOS="+targetOS, "GOARCH="+targetArch)
	toolchain, goExecutable, err := goToolchainAttribution(ctx, source, env)
	if err != nil {
		return nil, err
	}
	out, err := command(ctx, source, listEnv, goExecutable, "list", "-mod=readonly", "-deps", "-json=ImportPath,Imports,Module,Standard,Incomplete,Error,DepsErrors,GoFiles", "./cmd/nexus")
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(strings.NewReader(out))
	modules := map[string]listedModule{}
	sources := map[string][]string{}
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
		paths, err := packageNoticeSources(pkg)
		if err != nil {
			return nil, err
		}
		sources[key] = append(sources[key], paths...)
	}
	if len(modules) == 0 {
		return nil, ErrInvalid
	}
	items := make([]noticeModule, 0, len(modules)+1)
	for _, module := range modules {
		files, err := noticeFiles(module.Dir, sources[module.Path+"@"+module.Version]...)
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
	return items, nil
}

func noticeFiles(dir string, sources ...string) ([]noticeFile, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	files, readErr := noticeFilesFromRoot(root, sources...)
	closeErr := root.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return files, nil
}

// noticeFilesInModuleCache opens directory relative to the pinned cache root,
// reads through that anchored handle, and rejects named-path replacement before
// returning any bytes. afterOpen is a deterministic test seam only.
func noticeFilesInModuleCache(moduleCache, directory string, afterOpen func(), sources ...string) ([]noticeFile, error) {
	cacheAbsolute, err := filepath.Abs(moduleCache)
	if err != nil {
		return nil, ErrInvalid
	}
	directoryAbsolute, err := filepath.Abs(directory)
	if err != nil {
		return nil, ErrInvalid
	}
	cacheInfo, err := os.Lstat(cacheAbsolute)
	if err != nil || !cacheInfo.IsDir() || cacheInfo.Mode()&os.ModeSymlink != 0 {
		return nil, ErrInvalid
	}
	directoryInfo, err := os.Lstat(directoryAbsolute)
	if err != nil || !directoryInfo.IsDir() || directoryInfo.Mode()&os.ModeSymlink != 0 {
		return nil, ErrInvalid
	}
	cacheReal, err := filepath.EvalSymlinks(cacheAbsolute)
	if err != nil || !filepath.IsAbs(cacheReal) {
		return nil, ErrInvalid
	}
	directoryReal, err := filepath.EvalSymlinks(directoryAbsolute)
	if err != nil || !filepath.IsAbs(directoryReal) {
		return nil, ErrInvalid
	}
	relative, err := filepath.Rel(cacheReal, directoryReal)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, ErrInvalid
	}
	cacheRoot, err := os.OpenRoot(cacheReal)
	if err != nil {
		return nil, ErrInvalid
	}
	cacheOpened, cacheStatErr := cacheRoot.Stat(".")
	moduleRoot, openErr := cacheRoot.OpenRoot(relative)
	if cacheStatErr != nil || openErr != nil || !os.SameFile(cacheInfo, cacheOpened) {
		if moduleRoot != nil {
			_ = moduleRoot.Close()
		}
		_ = cacheRoot.Close()
		return nil, ErrInvalid
	}
	moduleOpened, moduleStatErr := moduleRoot.Stat(".")
	if moduleStatErr != nil || !moduleOpened.IsDir() || !os.SameFile(directoryInfo, moduleOpened) {
		_ = moduleRoot.Close()
		_ = cacheRoot.Close()
		return nil, ErrInvalid
	}
	if afterOpen != nil {
		afterOpen()
	}
	files, readErr := noticeFilesFromRoot(moduleRoot, sources...)
	moduleFinal, moduleFinalErr := moduleRoot.Stat(".")
	cacheFinal, cacheFinalErr := cacheRoot.Stat(".")
	cacheNamedFinal, cacheNamedFinalErr := os.Lstat(cacheAbsolute)
	namedFinal, namedFinalErr := os.Lstat(directoryAbsolute)
	moduleCloseErr := moduleRoot.Close()
	cacheCloseErr := cacheRoot.Close()
	finalReal, finalRealErr := filepath.EvalSymlinks(directoryAbsolute)
	cacheRealFinal, cacheRealFinalErr := filepath.EvalSymlinks(cacheAbsolute)
	if readErr != nil || moduleFinalErr != nil || cacheFinalErr != nil || cacheNamedFinalErr != nil || namedFinalErr != nil ||
		moduleCloseErr != nil || cacheCloseErr != nil || finalRealErr != nil ||
		cacheRealFinalErr != nil ||
		!moduleFinal.IsDir() || !namedFinal.IsDir() || namedFinal.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(directoryInfo, moduleFinal) || !os.SameFile(directoryInfo, namedFinal) ||
		!cacheNamedFinal.IsDir() || cacheNamedFinal.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(cacheInfo, cacheFinal) || !os.SameFile(cacheInfo, cacheNamedFinal) ||
		cacheRealFinal != cacheReal || finalReal != directoryReal ||
		!moduleDirectoryInCache(finalReal, cacheReal) {
		return nil, ErrInvalid
	}
	return files, nil
}

func noticeFilesFromRoot(root *os.Root, sources ...string) ([]noticeFile, error) {
	if root == nil {
		return nil, ErrInvalid
	}
	directory, err := root.Open(".")
	if err != nil {
		return nil, ErrInvalid
	}
	entries, readDirErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if readDirErr != nil || closeErr != nil {
		return nil, ErrInvalid
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
	files := make([]noticeFile, 0, len(names))
	total := 0
	for _, name := range names {
		info, err := root.Stat(name)
		if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maxNotice {
			return nil, ErrInvalid
		}
		file, err := root.Open(name)
		if err != nil {
			return nil, ErrInvalid
		}
		actual, statErr := file.Stat()
		body, readErr := io.ReadAll(io.LimitReader(file, info.Size()+1))
		final, finalStatErr := file.Stat()
		closeErr := file.Close()
		namedFinal, namedFinalErr := root.Stat(name)
		if statErr != nil || finalStatErr != nil || closeErr != nil || !actual.Mode().IsRegular() ||
			!os.SameFile(info, actual) || !os.SameFile(actual, final) || actual.Size() != info.Size() ||
			final.Size() != actual.Size() || namedFinalErr != nil || !os.SameFile(actual, namedFinal) ||
			readErr != nil || int64(len(body)) != actual.Size() ||
			!utf8.Valid(body) || bytes.IndexByte(body, 0) >= 0 {
			return nil, ErrInvalid
		}
		total += len(body)
		if total > maxNotice {
			return nil, ErrInvalid
		}
		files = append(files, noticeFile{Name: name, Body: body})
	}
	return appendSourceNotices(root, files, sources)
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
	fmt.Fprintf(&body, "NexusRouter third-party notices\nTarget: %s/%s\nGenerated from the cmd/nexus dependency closure and exact Go build toolchain.\nModule-Count: %d\n", targetOS, targetArch, len(ordered))
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
	if !expectNoticeLine(reader, "NexusRouter third-party notices") ||
		!expectNoticeLine(reader, "Target: "+targetOS+"/"+targetArch) ||
		!expectNoticeLine(reader, "Generated from the cmd/nexus dependency closure and exact Go build toolchain.") {
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
