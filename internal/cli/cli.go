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
  darwin run --config path --model id [--validate go_source] < prompt.txt
    go_source validation expects output containing a raw full Go source file
  darwin resources  Inspect current host memory and CPU capacity
  darwin task show --db path --task id  Inspect durable conversation state
  darwin serve --config path  Run the authenticated loopback HTTP service
  darwin memory list|show|put|delete --db path --scope scope
  darwin skills list|show|history|draft|rollback --root path --scope scope
  darwin feedback --db path --task id --outcome accepted|rejected --attempt-cost amount
  darwin feedback show|revise --db path --task id [--expected evaluation-id --outcome accepted|rejected]
  darwin audits list|show|attempts --db path [--task id] [--id audit-id]
  darwin audit --config path --task id --reviewer model-id [--max-cost amount]

Development status: use --model auto for constrained automatic routing.
Interactive and live streaming are not yet implemented.
`

// Run executes a CLI invocation and returns its process exit code.
// Usage errors return 2, output failures return 1, and success returns 0.
func Run(args []string, stdout, stderr io.Writer, version string) int {
	return RunWithInput(args, os.Stdin, stdout, stderr, version)
}

func RunWithInput(args []string, stdin io.Reader, stdout, stderr io.Writer, version string) int {
	if len(args) > 0 && args[0] == "audit" {
		return runAudit(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "audits" {
		return runAudits(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "feedback" {
		return runFeedback(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "skills" {
		return runSkills(args[1:], stdin, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "memory" {
		return runMemory(args[1:], stdin, stdout, stderr)
	}
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
