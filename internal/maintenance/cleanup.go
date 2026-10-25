// Package maintenance binds the local Web UI to the reviewed cleanup utility.
package maintenance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"time"

	"github.com/ArronJablonowski/NexusRouter/scripts"
)

var ErrChanged = errors.New("cleanup preview changed")
var tokenPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Item struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}
type Run struct {
	Complete bool     `json:"complete"`
	Time     string   `json:"time"`
	Deleted  []string `json:"deleted"`
	Errors   int      `json:"errors"`
}
type Preview struct {
	Token    string   `json:"token"`
	Items    []Item   `json:"items"`
	Retained []string `json:"retained"`
	Skipped  int      `json:"skipped"`
}
type Report struct {
	Version int      `json:"version"`
	LastRun *Run     `json:"last_run"`
	Preview *Preview `json:"preview"`
	Result  *Run     `json:"result"`
}

type Service struct{ Home string }

// Invoke never accepts a browser-provided path, executable, or shell command.
func (s Service) Invoke(ctx context.Context, action, token string) (Report, error) {
	var out Report
	if runtime.GOOS != "darwin" || (action != "status" && action != "preview" && action != "run") || (action == "run" && !tokenPattern.MatchString(token)) || (action != "run" && token != "") {
		return out, errors.New("cleanup unavailable")
	}
	script := filepath.Join(s.Home, "tools", "maintenance", "cleanup-nexus-home.py")
	info, err := os.Lstat(script)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 {
		return out, errors.New("cleanup not installed")
	}
	source, err := os.ReadFile(script)
	if err != nil || sha256.Sum256(source) != sha256.Sum256(scripts.CleanupSource) {
		return out, errors.New("cleanup utility needs qualification")
	}
	args := []string{"-B", script, "--home", s.Home, "--json"}
	if action == "status" {
		args = append(args, "--status")
	}
	if action == "run" {
		args = append(args, "--apply", "--expected-plan", token)
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/python3", args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "HOME=" + filepath.Dir(s.Home), "LANG=en_US.UTF-8"}
	stdout := &limitedBuffer{}
	cmd.Stdout = stdout
	cmd.Stderr = io.Discard
	err = cmd.Run()
	var failure struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(stdout.Bytes(), &failure)
	if failure.Error == "preview_changed" {
		return out, ErrChanged
	}
	if json.Unmarshal(stdout.Bytes(), &out) != nil || out.Version != 1 {
		return Report{}, fmt.Errorf("cleanup unavailable: %w", err)
	}
	if err != nil && (out.Result == nil || out.Result.Errors == 0) {
		return Report{}, errors.New("cleanup outcome unavailable; inspect last run before retrying")
	}
	return out, nil
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 2<<20 {
		return 0, errors.New("cleanup response too large")
	}
	return b.Buffer.Write(p)
}
