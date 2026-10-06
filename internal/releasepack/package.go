package releasepack

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/ArronJablonowski/NexusRouter/internal/processaudit"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

type Options struct{ Version, Commit, Out, Source, InstallEvidenceOut string }
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
	Created       string     `json:"created"`
	Toolchain     string     `json:"toolchain"`
	Artifacts     []Artifact `json:"artifacts"`
}

const releaseManifestSchema = 3

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
	if err := processaudit.Run(cmd); err != nil || out.overflow {
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

type releaseExecutableIdentity struct {
	path string
	info os.FileInfo
}

func pinReleaseExecutable(env []string, name string) (releaseExecutableIdentity, error) {
	path, err := releaseExecutable(env, name)
	if err != nil {
		return releaseExecutableIdentity{}, ErrInvalid
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return releaseExecutableIdentity{}, ErrInvalid
	}
	return releaseExecutableIdentity{path: path, info: info}, nil
}

func (identity releaseExecutableIdentity) verify() error {
	if !filepath.IsAbs(identity.path) || identity.info == nil {
		return ErrInvalid
	}
	info, err := os.Lstat(identity.path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 || !os.SameFile(identity.info, info) {
		return ErrInvalid
	}
	return nil
}

func executableOnlyPATH(env []string, executable string) []string {
	result := make([]string, 0, len(env))
	for _, value := range env {
		if !strings.HasPrefix(value, "PATH=") {
			result = append(result, value)
		}
	}
	return append(result, "PATH="+filepath.Dir(executable))
}

// Package stages all artifacts before publishing the directory. The exclusive
// sibling lock prevents cooperating packagers from targeting the same output.
func Package(ctx context.Context, o Options) (resultErr error) {
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
	reconstruction, err := newGoReconstruction(env, source, out, stage, buildSource)
	if err != nil {
		return err
	}
	reconstructionClosed := false
	defer func() {
		if !reconstructionClosed {
			closeErr := reconstruction.close()
			if resultErr == nil && closeErr != nil {
				resultErr = closeErr
			}
		}
	}()
	goIdentity := reconstruction.goExecutable
	goExecutable := goIdentity.path
	reconstructionEnv := reconstruction.env
	toolchain, err := command(ctx, buildSource, reconstructionEnv, goExecutable, "env", "GOVERSION")
	if err != nil || !signedToolchain.MatchString(toolchain) || toolchain != runtime.Version() || reconstruction.verify() != nil {
		return ErrInvalid
	}
	shared, err := loadCollateral(buildSource)
	if err != nil {
		return err
	}
	created, err := releaseCommitCreated(ctx, source, o.Commit, env)
	if err != nil {
		return err
	}
	shared.notes, err = renderFinalReleaseNotes(shared.notes, o.Version, o.Commit, created)
	if err != nil {
		return err
	}
	sbomSources, err := discoverSBOMSourceFiles(ctx, buildSource, reconstructionEnv)
	if err != nil {
		return err
	}
	manifest := Manifest{SchemaVersion: releaseManifestSchema, Version: o.Version, Commit: o.Commit, Created: created, Toolchain: toolchain}
	var sums strings.Builder
	for _, target := range []struct{ os, arch string }{{"darwin", "amd64"}, {"darwin", "arm64"}, {"linux", "amd64"}, {"linux", "arm64"}} {
		if err = reconstruction.verify(); err != nil {
			return err
		}
		closure, e := targetNoticeClosure(ctx, buildSource, target.os, target.arch, reconstruction)
		if e != nil {
			return e
		}
		modules := closure.Modules
		notices, e := renderThirdPartyNotices(target.os, target.arch, modules)
		if e != nil {
			return e
		}
		// go list above materializes the target closure. Verify the module-cache
		// contents, including the legal files just captured, against go.sum before
		// either those notices or compiled code enter a release artifact.
		if err = reconstruction.verify(); err != nil {
			return err
		}
		if _, e = command(ctx, buildSource, reconstructionEnv, goExecutable, "mod", "verify"); e != nil {
			return e
		}
		binary := filepath.Join(stage, "nexus")
		buildEnv := append(append([]string(nil), reconstructionEnv...), "GOOS="+target.os, "GOARCH="+target.arch)
		if err = goIdentity.verify(); err != nil {
			return err
		}
		_, err = command(ctx, buildSource, buildEnv, goExecutable, "build", "-mod=readonly", "-trimpath", "-buildvcs=false", "-ldflags=-buildid= -X main.version="+o.Version, "-o", binary, "./cmd/nexus")
		if err != nil {
			return err
		}
		// Re-verify the module cache after compilation so ordinary corruption or
		// concurrent cache drift cannot silently separate the recorded module
		// closure from the bytes that just entered the executable. This is not a
		// hermetic-build attestation against a malicious same-user cache actor.
		if _, e = command(ctx, buildSource, reconstructionEnv, goExecutable, "mod", "verify"); e != nil {
			return e
		}
		if err = goIdentity.verify(); err != nil {
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
		name := "NexusRouter_" + o.Version + "_" + target.os + "_" + target.arch + ".tar.gz"
		binaryDigest := sha256.Sum256(data)
		sbom, e := renderTargetSBOM(TargetSBOMOptions{
			Version: o.Version, Commit: o.Commit, TargetOS: target.os, TargetArch: target.arch,
			Created: created, BinarySHA256: hex.EncodeToString(binaryDigest[:]),
		}, modules, sbomSources)
		if e != nil {
			return e
		}
		f, e := os.OpenFile(filepath.Join(stage, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if e != nil {
			return e
		}
		entries, metadata, e := releaseEntries(shared, notices, sbom, data)
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
	if err = reconstruction.verify(); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if _, err = os.Lstat(out); !os.IsNotExist(err) {
		return ErrInvalid
	}
	// A failed call must never leave a visible signable directory. Close the
	// private reconstruction workspace before the no-replace publish so cleanup
	// failure cannot turn a successful publication into an ambiguous error.
	if err = reconstruction.close(); err != nil {
		return err
	}
	reconstructionClosed = true
	return publish(stage, out)
}

func releaseCommitCreated(ctx context.Context, source, commit string, env []string) (string, error) {
	value, err := command(ctx, source, env, "git", "show", "--no-patch", "--format=%cI", commit)
	if err != nil {
		return "", err
	}
	created, err := time.Parse(time.RFC3339, value)
	if err != nil || created.Before(time.Unix(0, 0)) {
		return "", ErrInvalid
	}
	return created.UTC().Truncate(time.Second).Format("2006-01-02T15:04:05Z"), nil
}
