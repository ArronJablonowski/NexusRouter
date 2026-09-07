package releasepack

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type Options struct{ Version, Commit, Out, Source string }
type Artifact struct {
	OS      string                 `json:"os"`
	Arch    string                 `json:"arch"`
	File    string                 `json:"file"`
	SHA256  string                 `json:"sha256"`
	Entries []archiveEntryMetadata `json:"entries"`
}
type Manifest struct {
	SchemaVersion int        `json:"schema_version"`
	Version       string     `json:"version"`
	Commit        string     `json:"commit"`
	Toolchain     string     `json:"toolchain"`
	Artifacts     []Artifact `json:"artifacts"`
}

var semver = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)
var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

func validate(o Options) error {
	if len(o.Version) > 100 || !semver.MatchString(o.Version) || !commitPattern.MatchString(o.Commit) || o.Out == "" {
		return ErrInvalid
	}
	if i := strings.IndexByte(o.Version, '-'); i >= 0 {
		for _, p := range strings.Split(o.Version[i+1:], ".") {
			if len(p) > 1 && p[0] == '0' && strings.Trim(p, "0123456789") == "" {
				return ErrInvalid
			}
		}
	}
	return nil
}

type boundedOutput struct {
	buffer   bytes.Buffer
	overflow bool
}

func (b *boundedOutput) Len() int       { return b.buffer.Len() }
func (b *boundedOutput) String() string { return b.buffer.String() }
func (b *boundedOutput) Bytes() []byte  { return b.buffer.Bytes() }

func (b *boundedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 1<<20 {
		b.overflow = true
		return 0, ErrInvalid
	}
	return b.buffer.Write(p)
}

func command(ctx context.Context, dir string, env []string, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = dir
	cmd.Env = env
	var out boundedOutput
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil || out.overflow {
		return "", fmt.Errorf("release command failed: %s", filepath.Base(name))
	}
	return strings.TrimSpace(out.String()), nil
}

func environment() []string {
	// Do not inherit GOFLAGS, workspace selection, toolchain downloads, compiler
	// overrides, or architecture tuning from the invoking shell.
	result := []string{"CGO_ENABLED=0", "GOENV=off", "GOFLAGS=", "GOWORK=off", "GOTOOLCHAIN=local", "GOAMD64=v1", "GOARM64=v8.0", "GOEXPERIMENT=", "GODEBUG=", "LANG=C", "LC_ALL=C", "TZ=UTC", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_NO_REPLACE_OBJECTS=1"}
	for _, key := range []string{"PATH", "HOME", "TMPDIR", "SYSTEMROOT"} {
		if v, ok := os.LookupEnv(key); ok {
			result = append(result, key+"="+v)
		}
	}
	return result
}

// Package stages all artifacts before publishing the directory. The exclusive
// sibling lock prevents cooperating packagers from targeting the same output.
func Package(ctx context.Context, o Options) error {
	if ctx == nil || validate(o) != nil {
		return ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	source, err := filepath.Abs(o.Source)
	if err != nil {
		return ErrInvalid
	}
	out, err := filepath.Abs(o.Out)
	if err != nil || out == source {
		return ErrInvalid
	}
	if _, err = os.Lstat(out); !os.IsNotExist(err) {
		return ErrInvalid
	}
	lock, err := os.OpenFile(out+".lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	lock.Close()
	defer os.Remove(out + ".lock")
	env := environment()
	root, err := command(ctx, source, env, "git", "rev-parse", "--show-toplevel")
	if err != nil || root != source {
		return ErrInvalid
	}
	verify := func() error {
		head, e := command(ctx, source, env, "git", "rev-parse", "HEAD")
		if e != nil || head != o.Commit {
			return ErrInvalid
		}
		status, e := command(ctx, source, env, "git", "status", "--porcelain=v1", "--untracked-files=all")
		if e != nil || status != "" {
			return ErrInvalid
		}
		return nil
	}
	if err = verify(); err != nil {
		return err
	}
	toolchain, err := command(ctx, source, env, "go", "env", "GOVERSION")
	if err != nil || !regexp.MustCompile(`^go[0-9]+\.[0-9]+(\.[0-9]+)?$`).MatchString(toolchain) {
		return ErrInvalid
	}
	stage, err := os.MkdirTemp(filepath.Dir(out), ".darwin-release-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	buildSource, err := os.MkdirTemp(filepath.Dir(out), ".darwin-source-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(buildSource)
	if err = snapshot(ctx, source, o.Commit, buildSource, env); err != nil {
		return err
	}
	shared, err := loadCollateral(buildSource)
	if err != nil {
		return err
	}
	manifest := Manifest{SchemaVersion: 2, Version: o.Version, Commit: o.Commit, Toolchain: toolchain}
	var sums strings.Builder
	for _, target := range []struct{ os, arch string }{{"darwin", "amd64"}, {"darwin", "arm64"}, {"linux", "amd64"}, {"linux", "arm64"}} {
		notices, e := thirdPartyNotices(ctx, buildSource, target.os, target.arch, env)
		if e != nil {
			return e
		}
		// go list above materializes the target closure. Verify the module-cache
		// contents, including the legal files just captured, against go.sum before
		// either those notices or compiled code enter a release artifact.
		if _, e = command(ctx, buildSource, env, "go", "mod", "verify"); e != nil {
			return e
		}
		binary := filepath.Join(stage, "darwin")
		buildEnv := append(append([]string(nil), env...), "GOOS="+target.os, "GOARCH="+target.arch)
		_, err = command(ctx, buildSource, buildEnv, "go", "build", "-mod=readonly", "-trimpath", "-buildvcs=false", "-ldflags=-buildid= -X main.version="+o.Version, "-o", binary, "./cmd/darwin")
		if err != nil {
			return err
		}
		info, e := os.Lstat(binary)
		if e != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maxArtifact {
			return ErrInvalid
		}
		data, e := os.ReadFile(binary)
		if e != nil {
			return e
		}
		name := "DarwinRouter_" + o.Version + "_" + target.os + "_" + target.arch + ".tar.gz"
		f, e := os.OpenFile(filepath.Join(stage, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if e != nil {
			return e
		}
		entries, metadata, e := releaseEntries(shared, notices, data)
		if e != nil {
			f.Close()
			return e
		}
		e = Archive(f, entries)
		closeErr := f.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
		archive, e := os.ReadFile(filepath.Join(stage, name))
		if e != nil {
			return e
		}
		digest := sha256.Sum256(archive)
		hash := hex.EncodeToString(digest[:])
		manifest.Artifacts = append(manifest.Artifacts, Artifact{OS: target.os, Arch: target.arch, File: name, SHA256: hash, Entries: metadata})
		fmt.Fprintf(&sums, "%s  %s\n", hash, name)
		if err = os.Remove(binary); err != nil {
			return err
		}
	}
	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if err = os.WriteFile(filepath.Join(stage, "manifest.json"), body, 0644); err != nil {
		return err
	}
	manifestHash := sha256.Sum256(body)
	fmt.Fprintf(&sums, "%x  manifest.json\n", manifestHash)
	if err = os.WriteFile(filepath.Join(stage, "SHA256SUMS"), []byte(sums.String()), 0644); err != nil {
		return err
	}
	if err = verify(); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if _, err = os.Lstat(out); !os.IsNotExist(err) {
		return ErrInvalid
	}
	return publish(stage, out)
}
