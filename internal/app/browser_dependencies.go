package app

import (
	"context"
	"fmt"
	"github.com/ArronJablonowski/NexusRouter/webui"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"time"
)

// BrowserDependencies inspects metadata only: it never installs tools or starts models.
func (s *Service) BrowserDependencies(ctx context.Context) (webui.DependencyInventory, error) {
	p := webui.DependencyInventory{Version: 1, ObservedAt: time.Now().UTC(), Items: []webui.Dependency{}}
	add := func(name, status, scope, location, note string) {
		p.Items = append(p.Items, webui.Dependency{ID: fmt.Sprintf("dependency-%d", len(p.Items)), Name: name, Status: status, Scope: scope, Location: location, Note: note})
	}
	add("Go runtime", "Bundled", "Required", runtime.Version(), "Included in this NexusRouter binary; no separate Go installation is required.")
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, d := range info.Deps {
			if d.Replace != nil {
				d = d.Replace
			}
			v := d.Version
			if v == "" {
				v = "local build"
			}
			add(d.Path, "Bundled", "Required library", v, "Compiled into this binary. Includes transitive Go dependencies.")
		}
	} else {
		add("Build dependency inventory", "Unavailable", "Required libraries", "This binary", "Build metadata was not retained.")
	}
	check := func(name, path, scope string) {
		found, err := exec.LookPath(path)
		status := "Installed"
		note := "Executable found; service health and compatibility are checked separately."
		if err != nil {
			status = "Not found"
			found = path
			note = "Not found in the service PATH or configured location; may be installed in another environment."
		}
		if os.IsPermission(err) {
			status = "Unavailable"
			note = "Permission denied while inspecting executable."
		}
		add(name, status, scope, found, note)
	}
	for _, name := range []string{"ollama", "vllm", "python3", "git", "ssh", "node", "codex"} {
		check(name, name, "Optional tool")
	}
	for _, v := range s.settings.Providers {
		if v.Executable != "" {
			check("Provider: "+v.ID, v.Executable, "Configured provider")
		}
	}
	for _, v := range s.settings.NativeHarnesses {
		check("Harness: "+v.ID, v.Executable, "Configured harness")
	}
	add("FlashInfer / PyTorch / CUDA", "Environment check required", "Optional vLLM dependencies", "Runner Python environment", "Package availability depends on the runner environment. Not inferred from the system Python; installed packages do not guarantee usable GPU kernels.")
	if err := ctx.Err(); err != nil {
		return webui.DependencyInventory{}, err
	}
	return p, nil
}
