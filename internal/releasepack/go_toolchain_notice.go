package releasepack

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

const goToolchainModulePath = "go.dev/toolchain"

var goReleaseVersion = regexp.MustCompile(`^go(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(\.(0|[1-9][0-9]*))?$`)

// These are the reviewed upstream Go license and patent-grant bytes. Packaging
// also requires the exact files from the selected toolchain installation; the
// embedded copies prevent a locally substituted file from becoming authority.
const goLicenseText = `Copyright 2009 The Go Authors.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are
met:

   * Redistributions of source code must retain the above copyright
notice, this list of conditions and the following disclaimer.
   * Redistributions in binary form must reproduce the above
copyright notice, this list of conditions and the following disclaimer
in the documentation and/or other materials provided with the
distribution.
   * Neither the name of Google LLC nor the names of its
contributors may be used to endorse or promote products derived from
this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
`

const goPatentsText = `Additional IP Rights Grant (Patents)

"This implementation" means the copyrightable works distributed by
Google as part of the Go project.

Google hereby grants to You a perpetual, worldwide, non-exclusive,
no-charge, royalty-free, irrevocable (except as stated in this section)
patent license to make, have made, use, offer to sell, sell, import,
transfer and otherwise run, modify and propagate the contents of this
implementation of Go, where such license applies only to those patent
claims, both currently owned or controlled by Google and acquired in
the future, licensable by Google that are necessarily infringed by this
implementation of Go.  This grant does not include claims that would be
infringed only as a consequence of further modification of this
implementation.  If you or your agent or exclusive licensee institute or
order or agree to the institution of patent litigation against any
entity (including a cross-claim or counterclaim in a lawsuit) alleging
that this implementation of Go or any code incorporated within this
implementation of Go constitutes direct or contributory patent
infringement, or inducement of patent infringement, then any patent
rights granted to you under this License for this implementation of Go
shall terminate as of the date such litigation is filed.
`

func goToolchainAttribution(ctx context.Context, source string, env []string) (noticeModule, string, error) {
	path, err := releaseExecutable(env, "go")
	if err != nil {
		return noticeModule{}, "", ErrInvalid
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return noticeModule{}, "", ErrInvalid
	}
	body, err := command(ctx, source, env, path, "env", "-json", "GOVERSION", "GOROOT")
	if err != nil {
		return noticeModule{}, "", err
	}
	var values struct {
		GoVersion string `json:"GOVERSION"`
		GoRoot    string `json:"GOROOT"`
	}
	if json.Unmarshal([]byte(body), &values) != nil || !goReleaseVersion.MatchString(values.GoVersion) || values.GoVersion != runtime.Version() || !filepath.IsAbs(values.GoRoot) {
		return noticeModule{}, "", ErrInvalid
	}
	root, err := filepath.EvalSymlinks(values.GoRoot)
	if err != nil {
		return noticeModule{}, "", ErrInvalid
	}
	installed, err := os.Stat(filepath.Join(root, "bin", "go"))
	if err != nil || !installed.Mode().IsRegular() || !os.SameFile(info, installed) {
		return noticeModule{}, "", ErrInvalid
	}
	versionOutput, err := command(ctx, source, env, path, "version")
	wantVersionOutput := "go version " + values.GoVersion + " " + runtime.GOOS + "/" + runtime.GOARCH
	if err != nil || versionOutput != wantVersionOutput {
		return noticeModule{}, "", ErrInvalid
	}
	files, err := validatedGoLegalFiles(root)
	if err != nil {
		return noticeModule{}, "", err
	}
	return noticeModule{Path: goToolchainModulePath, Version: "v" + values.GoVersion[2:], Files: files}, path, nil
}

func releaseExecutable(env []string, name string) (string, error) {
	pathValue := ""
	for _, value := range env {
		if strings.HasPrefix(value, "PATH=") {
			if pathValue != "" {
				return "", ErrInvalid
			}
			pathValue = strings.TrimPrefix(value, "PATH=")
		}
	}
	if pathValue == "" || filepath.Base(name) != name {
		return "", ErrInvalid
	}
	for _, directory := range filepath.SplitList(pathValue) {
		if directory == "" || !filepath.IsAbs(directory) {
			return "", ErrInvalid
		}
		candidate := filepath.Join(directory, name)
		info, err := os.Stat(candidate)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			return "", ErrInvalid
		}
		resolved, err := filepath.EvalSymlinks(candidate)
		if err != nil || !filepath.IsAbs(resolved) {
			return "", ErrInvalid
		}
		return resolved, nil
	}
	return "", ErrInvalid
}

func embeddedGoToolchainModule() (noticeModule, error) {
	version := runtime.Version()
	if !goReleaseVersion.MatchString(version) {
		return noticeModule{}, ErrInvalid
	}
	return noticeModule{Path: goToolchainModulePath, Version: "v" + version[2:], Files: []noticeFile{
		{Name: "LICENSE", Body: []byte(goLicenseText)},
		{Name: "PATENTS", Body: []byte(goPatentsText)},
	}}, nil
}

func validatedGoLegalFiles(root string) ([]noticeFile, error) {
	expected := []noticeFile{{Name: "LICENSE", Body: []byte(goLicenseText)}, {Name: "PATENTS", Body: []byte(goPatentsText)}}
	result := make([]noticeFile, len(expected))
	for i, legal := range expected {
		paths := []string{filepath.Join(root, legal.Name)}
		// Homebrew keeps LICENSE at the formula-version root while GOROOT is
		// its libexec child. This is the only supported split installation.
		if filepath.Base(root) == "libexec" {
			paths = append(paths, filepath.Join(filepath.Dir(root), legal.Name))
		}
		var found bool
		for _, path := range paths {
			info, err := os.Lstat(path)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil || !info.Mode().IsRegular() || info.Size() != int64(len(legal.Body)) {
				return nil, ErrInvalid
			}
			file, err := os.Open(path)
			if err != nil {
				return nil, ErrInvalid
			}
			actual, statErr := file.Stat()
			body, readErr := io.ReadAll(io.LimitReader(file, int64(len(legal.Body)+1)))
			closeErr := file.Close()
			if statErr != nil || !os.SameFile(info, actual) || readErr != nil || closeErr != nil || !bytes.Equal(body, legal.Body) {
				return nil, ErrInvalid
			}
			found = true
		}
		if !found {
			return nil, ErrInvalid
		}
		result[i] = legal
	}
	return result, nil
}

func validGoToolchainModule(module noticeModule) bool {
	expected, err := embeddedGoToolchainModule()
	if err != nil || module.Path != expected.Path || module.Version != expected.Version || len(module.Files) != len(expected.Files) {
		return false
	}
	for i := range expected.Files {
		if module.Files[i].Name != expected.Files[i].Name || !bytes.Equal(module.Files[i].Body, expected.Files[i].Body) {
			return false
		}
	}
	return true
}
