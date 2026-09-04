// Package cli adapts terminal input and output to application services.
package cli

import (
	"fmt"
	"io"
	"os"
)

const usage = `DarwinRouter — adaptive, local-first agent runtime

Usage:
  darwin version   Print the build version
  darwin help      Show this help
  darwin config validate|show [--config path] [--set key=value]
  darwin run --config path --model id < prompt.txt
  darwin resources  Inspect current host memory and CPU capacity
  darwin task show --db path --task id  Inspect durable conversation state
  darwin serve --config path  Run the authenticated loopback HTTP service

Development status: explicit-model headless tasks are available. Automatic
routing and interactive streaming are not yet implemented.
`

// Run executes a CLI invocation and returns its process exit code.
// Usage errors return 2, output failures return 1, and success returns 0.
func Run(args []string, stdout, stderr io.Writer, version string) int {
	return RunWithInput(args, os.Stdin, stdout, stderr, version)
}

func RunWithInput(args []string, stdin io.Reader, stdout, stderr io.Writer, version string) int {
	if len(args) > 0 && args[0] == "serve" {
		return runServe(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "task" {
		return runTaskInspection(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "resources" {
		return runResources(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "run" {
		return runTask(args[1:], stdin, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "config" {
		return runConfig(args[1:], stdout, stderr)
	}
	if len(args) == 0 || (len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h")) {
		if _, err := io.WriteString(stdout, usage); err != nil {
			return 1
		}
		return 0
	}
	if len(args) == 1 && (args[0] == "version" || args[0] == "--version") {
		if _, err := fmt.Fprintf(stdout, "darwin %s\n", version); err != nil {
			return 1
		}
		return 0
	}
	// Do not echo unrecognized arguments: they may contain credentials.
	_, _ = io.WriteString(stderr, "darwin: invalid command or arguments; run 'darwin help'\n")
	return 2
}
