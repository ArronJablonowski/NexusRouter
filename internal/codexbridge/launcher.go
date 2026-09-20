package codexbridge

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/codexrpc"
)

// LaunchSpec is trusted host input, never user prompt or model-generated data.
// The caller must resolve resumed-session privacy before setting Privacy. CWD
// must be a host-owned empty directory; the launcher never removes that directory.
// Env is an explicit allowlist retaining existing login locations, not copied auth.
type LaunchSpec struct {
	Executable, CWD, Model, ReasoningEffort string
	Mode, Privacy                           string
	Env                                     []string
}

// LaunchChecked owns discovery and returns a task-owned checked connection.
// Callers MUST defer Close over the entire runtime loop, including tool and
// persistence failures. This experimental internal entry point does not provide
// descendant containment or full built-in prompt/tool isolation and is not yet
// a production isolation boundary. No task is sent during launch.
func LaunchChecked(ctx context.Context, spec LaunchSpec) (*Session, error) {
	return launchChecked(ctx, spec, func(ctx context.Context, p codexrpc.ProcessSpec) (Wire, error) {
		return codexrpc.StartProcess(ctx, p)
	}, launchMetadata)
}

type launchStart func(context.Context, codexrpc.ProcessSpec) (Wire, error)
type launchReadMetadata func(context.Context, codexrpc.ProcessSpec, ...string) ([]byte, error)

func launchChecked(ctx context.Context, spec LaunchSpec, start launchStart, metadata launchReadMetadata) (_ *Session, err error) {
	if ctx == nil || ctx.Err() != nil || (spec.Mode != "hybrid" && spec.Mode != "cloud_only") || spec.Privacy != "cloud_allowed" || spec.Model != "gpt-5.6-sol" ||
		!filepath.IsAbs(spec.Executable) || !filepath.IsAbs(spec.CWD) || !launchEnvValid(spec.Env) || !validReasoningEffort(spec.ReasoningEffort) {
		return nil, ErrLaunchObservation
	}
	dir, readErr := os.Open(spec.CWD)
	if readErr != nil {
		return nil, ErrLaunchObservation
	}
	entries, readErr := dir.Readdirnames(1)
	_ = dir.Close()
	if readErr != io.EOF || len(entries) != 0 {
		return nil, ErrLaunchObservation
	}
	base := codexrpc.ProcessSpec{Executable: spec.Executable, Dir: spec.CWD, Env: append([]string{}, spec.Env...)}
	setup, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	version, err := metadata(setup, base, "--version")
	if err != nil {
		return nil, ErrLaunchObservation
	}
	inventory, err := metadata(setup, base, "features", "list")
	if err != nil {
		return nil, ErrLaunchObservation
	}
	features, args, err := launchProfile(version, inventory)
	if err != nil {
		return nil, ErrLaunchObservation
	}
	base.Args = args
	w, err := start(setup, base)
	if err != nil {
		return nil, ErrLaunchObservation
	}
	// Own every successfully returned wire before any subsequent operation.
	discovery, err := NewSession(setup, w, Options{Model: spec.Model, CWD: spec.CWD})
	if err != nil {
		_ = w.Close()
		return nil, ErrLaunchObservation
	}
	discovery.allowDisabledStatus = true
	disables, skillDisable, err := discoverLaunch(discovery, setup)
	closeErr := discovery.Close()
	if err != nil || closeErr != nil {
		return nil, ErrLaunchObservation
	}
	base.Args = append([]string(nil), args...)
	for _, override := range append(disables, skillDisable) {
		base.Args = append(base.Args, "-c", override)
	}
	if setup.Err() != nil {
		return nil, ErrLaunchObservation
	}
	// The final process uses the task lifetime, not the shorter setup deadline.
	w, err = start(ctx, base)
	if err != nil {
		return nil, ErrLaunchObservation
	}
	s, err := NewCheckedSession(ctx, w, Options{Model: spec.Model, CWD: spec.CWD, ReasoningEffort: spec.ReasoningEffort}, features)
	if err != nil {
		_ = w.Close()
		return nil, ErrLaunchObservation
	}
	if s.Prepare(setup) != nil {
		_ = s.Close()
		return nil, ErrLaunchObservation
	}
	if setup.Err() != nil || ctx.Err() != nil || s.closed.Load() {
		_ = s.Close()
		return nil, ErrLaunchObservation
	}
	return s, nil
}

func discoverLaunch(s *Session, ctx context.Context) ([]string, string, error) {
	if s.Prepare(ctx) != nil {
		return nil, "", ErrLaunchObservation
	}
	config, err := s.launchCall("config/read", map[string]any{"includeLayers": false})
	if err != nil {
		return nil, "", ErrLaunchObservation
	}
	disables, err := ExtensionDisableOverrides(config)
	if err != nil {
		return nil, "", ErrLaunchObservation
	}
	skills, err := s.launchCall("skills/list", map[string]any{"cwds": []string{s.options.CWD}, "forceReload": true})
	if err != nil {
		return nil, "", ErrLaunchObservation
	}
	skillDisable, err := SkillDisableOverride(skills)
	return disables, skillDisable, err
}

func launchEnvValid(env []string) bool {
	if env == nil || len(env) > 4 {
		return false
	}
	seen := map[string]bool{}
	total := 0
	for _, pair := range env {
		total += len(pair)
		key, value, ok := strings.Cut(pair, "=")
		if !ok || seen[key] || strings.ContainsRune(pair, 0) || total > 8<<10 {
			return false
		}
		seen[key] = true
		switch key {
		case "HOME", "CODEX_HOME", "TMPDIR":
			if !filepath.IsAbs(value) {
				return false
			}
		case "PATH":
		default:
			return false
		}
	}
	return seen["HOME"]
}
