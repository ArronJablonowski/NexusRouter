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
  darwin run --config path --model id [--validate go_source] [--json] < prompt.txt
    go_source validation expects output containing a raw full Go source file
    --json streams committed events and a final result as versioned JSON lines
  darwin chat --config path --model id
    Line-oriented conversation: /help /status /new /cancel /steer TEXT /quit
    Rate the latest answer with /feedback accepted|rejected COST
  darwin submit --config path --key idempotency-key --model id < prompt.txt
    Store queued work only; an independently running daemon executes it
  darwin submissions list --db path [--state state --after cursor --limit 25]
  darwin submissions show|cancel|recoveries --db path --id submission-id
  darwin resources  Inspect current host memory and CPU capacity
  darwin metrics --db path  Read metadata-only lifecycle counts as JSON
  darwin task show --db path --task id  Inspect durable conversation state
  darwin steer --config path --task id --key idempotency-key < guidance.txt
    Queue guidance for a running task; does not interrupt current tools
  darwin steering list --db path --task id
  darwin steering show --db path --task id --id message-id
  darwin approvals list --db path --task id [--after call-id --limit 25]
  darwin approvals show --db path --task id --id approval-id
  darwin approvals execution --db path --task id --id approval-id
  darwin approval-decision --config path < decision.json
  darwin serve --config path  Run the authenticated loopback HTTP service
  darwin memory list|show|put|delete --db path --scope scope
  darwin skills list|show|history|state|draft|rollback --root path --scope scope
  darwin feedback --db path --task id --outcome accepted|rejected --attempt-cost amount
  darwin feedback show|revise --db path --task id [--expected evaluation-id --outcome accepted|rejected]
  darwin audits list|show|attempts --db path [--task id] [--id audit-id]
  darwin audit --config path --task id --reviewer model-id [--max-cost amount]
  darwin summary --config path --task id --model id --keep n [--max-cost amount]
    Generate a stored summary draft; does not activate compaction
  darwin summaries list|show --db path [--task id --after id --limit n] [--id id]
  darwin summary-review --config path --attempt id [--expected review-id] --decision approved|rejected --note text
  darwin summary-reviews --db path --attempt id
  darwin run --config path --model id --continue-task id --summary-attempt approved-attempt-id < prompt.txt

Development status: use --model auto for constrained automatic routing.
JSON lifecycle streaming and separate-command steering are available, plus line-oriented chat.
`

// Run executes a CLI invocation and returns its process exit code.
// Usage errors return 2, output failures return 1, and success returns 0.
func Run(args []string, stdout, stderr io.Writer, version string) int {
	return RunWithInput(args, os.Stdin, stdout, stderr, version)
}

func RunWithInput(args []string, stdin io.Reader, stdout, stderr io.Writer, version string) int {
	if len(args) > 0 && args[0] == "approval-decision" {
		return runApprovalDecision(args[1:], stdin, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "approvals" {
		return runApprovals(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "chat" {
		return runChat(args[1:], stdin, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "steer" {
		return runSteer(args[1:], stdin, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "steering" {
		return runSteering(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "metrics" {
		return runMetrics(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "submit" {
		return runSubmit(args[1:], stdin, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "submissions" {
		return runSubmissions(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "summary-review" {
		return runSummaryReview(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "summary-reviews" {
		return runSummaryReviews(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "summary" {
		return runSummary(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "summaries" {
		return runSummaries(args[1:], stdout, stderr)
	}
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
