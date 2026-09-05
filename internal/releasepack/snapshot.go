package releasepack

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// snapshot materializes only regular blobs from the exact commit tree. Unlike
// git archive/checkout it does not apply export attributes, filters or hooks.
// Symlinks and gitlinks are deliberately unsupported release inputs.
func snapshot(ctx context.Context, source, commit, destination string, env []string) error {
	if !commitPattern.MatchString(commit) {
		return ErrInvalid
	}
	tree, err := command(ctx, source, env, "git", "ls-tree", "-r", "-z", commit)
	if err != nil {
		return err
	}
	records := strings.Split(tree, "\x00")
	if len(records) < 2 || records[len(records)-1] != "" || len(records) > 10001 {
		return ErrInvalid
	}
	total := 0
	for _, record := range records[:len(records)-1] {
		metadata, name, ok := strings.Cut(record, "\t")
		fields := strings.Fields(metadata)
		if !ok || len(fields) != 3 || (fields[0] != "100644" && fields[0] != "100755") || fields[1] != "blob" || !commitPattern.MatchString(fields[2]) || !snapshotPath(name) {
			return ErrInvalid
		}
		blobCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		cmd := exec.CommandContext(blobCtx, "git", "cat-file", "blob", fields[2])
		cmd.Dir = source
		cmd.Env = env
		cmd.WaitDelay = 2 * time.Second
		var body boundedOutput
		cmd.Stdout = &body
		cmd.Stderr = &boundedOutput{}
		err = cmd.Run()
		cancel()
		if err != nil || body.overflow {
			return ErrInvalid
		}
		total += body.Len()
		if total > 64<<20 {
			return ErrInvalid
		}
		target := filepath.Join(destination, filepath.FromSlash(name))
		if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		mode := os.FileMode(0644)
		if fields[0] == "100755" {
			mode = 0755
		}
		file, e := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if e != nil {
			return e
		}
		_, e = file.Write(body.Bytes())
		closeErr := file.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return validateModule(ctx, destination, env)
}

func snapshotPath(name string) bool {
	if name == "" || len(name) > 512 || name == "." || path.Clean(name) != name || strings.HasPrefix(name, "/") || strings.HasPrefix(name, "../") || name == ".." || strings.ContainsAny(name, "\\:") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if strings.EqualFold(part, ".git") {
			return false
		}
	}
	for _, r := range name {
		if r < 32 || r > 126 {
			return false
		}
	}
	return true
}

func validateModule(ctx context.Context, source string, env []string) error {
	body, err := command(ctx, source, env, "go", "mod", "edit", "-json")
	if err != nil {
		return ErrInvalid
	}
	var module struct {
		Replace []struct {
			New struct{ Path, Version string }
		}
	}
	if json.Unmarshal([]byte(body), &module) != nil {
		return ErrInvalid
	}
	// Refuse all filesystem replacements, including in-tree ones, so dependencies
	// are resolved by module version/checksum rather than ambient filesystem.
	for _, r := range module.Replace {
		if r.New.Version == "" {
			return ErrInvalid
		}
	}
	return nil
}
