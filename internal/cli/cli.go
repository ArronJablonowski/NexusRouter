// Package cli adapts terminal input and output to application services.
package cli

import (
	"fmt"
	"io"
	"os"
)

const usage = `NexusRouter — adaptive, local-first agent runtime

Usage:
  nexus version   Print the build version
  nexus help      Show this help
  nexus config validate|show [--config path] [--set key=value]
  nexus run --config path --model id [--validate go_source] [--json] < prompt.txt
    go_source validation expects output containing a raw full Go source file
    --json streams committed events and a final result as versioned JSON lines
  nexus chat --config path --model id
    Line-oriented conversation: /help /status /new /cancel /steer TEXT /quit
    Rate the latest answer with /feedback accepted|rejected COST
  nexus submit --config path --key idempotency-key --model id < prompt.txt
    Store queued work only; an independently running daemon executes it
  nexus branch --config path --key idempotency-key --task id --session id --sequence n --event id --model id < prompt.txt
    Queue a direct child from an exact completed task head; does not run inference
  nexus resume --config path --key idempotency-key --task id --session id --sequence n --event id --model id < prompt.txt
    Queue new work from an exact recovered-history head; does not run inference
  nexus submissions list --db path [--state state --after cursor --limit 25]
  nexus submissions show|cancel|recoveries --db path --id submission-id
  nexus resources  Inspect current host memory and CPU capacity
  nexus doctor --config path  Inspect the running daemon and dependency health
  nexus providers list --config path  List safe configured provider metadata
  nexus models list --config path  List configured model routing metadata
  nexus resources leases --db path --scope scope  Inspect overlapping lease holders
  nexus resources attention --db path  Inspect durable lease attention records
  nexus resources attention-history --db path --id ID  Inspect attention transitions
  nexus models deprecation --config path --model id [--domain general --profile default --window 50 --minimum-samples 20 --failure-threshold 0.35]
    Read-only recommendation; never disables or removes a model
  nexus metrics --db path  Read metadata-only lifecycle counts as JSON
  nexus metrics export --config path --endpoint URL [--api-key-env ENV_NAME]
  nexus traces export --config path --endpoint URL [--api-key-env ENV_NAME] [--limit 16]
  nexus logs --config path [--task id --after position --limit 100 --follow --include-content]
    Read structured durable activity; --include-content adds private redacted I/O
    --follow starts at current activity unless --task or --after is given
    Without --follow, read one bounded page from --after (default: 0)
  nexus task show --db path --task id  Inspect durable conversation state
  nexus task list --db path [--state state --after cursor --limit 25]
  nexus session tasks --db path --session id [--after cursor --limit 25]
    List content-free task lineage for one durable session
  nexus task continuation --db path --task id  Inspect continuation readiness
  nexus task route --db path --task id         Inspect metadata-only route decision
  nexus task leases --db path --task id        Inspect lease and recovery counts
  nexus steer --config path --task id --key idempotency-key < guidance.txt
    Queue guidance for a running task; does not interrupt current tools
  nexus steering list --db path --task id
  nexus steering show --db path --task id --id message-id
  nexus approvals list --db path --task id [--after call-id --limit 25]
  nexus approvals show --db path --task id --id approval-id
  nexus approvals execution --db path --task id --id approval-id
  nexus approval-decision --config path < decision.json
  nexus web approve --config path CHALLENGE_ID.DISPLAY_CODE
  nexus serve --config path  Run the authenticated loopback HTTP service
  nexus daemon start|status|stop --config path  Control an authenticated local daemon
  nexus memory list|show|put|delete --config path
  nexus memory list|show|put|delete --db path --scope scope (raw storage)
  nexus skills list|show|history|state|draft|rollback --root path --scope scope
  nexus skills compare --config path < request.json
  nexus skills compare-select --config path < request.json
  nexus skill-generations list --db path --scope id [--after id --limit 25]
  nexus skill-generations show --db path --scope id --id generation-id
  nexus skill-generations discover --config path --domain id [--after cursor --scan-limit 20]
  nexus skill-generations generate --config path --id attempt-id --model id --name skill-name --tasks task-a,task-b [--max-cost amount]
  nexus skill-generations publish --config path --id attempt-id
    Generate saves a proposal; publish creates an inactive version, never activates it
  nexus feedback --db path --task id --outcome accepted|rejected --attempt-cost amount
  nexus feedback show|revise --db path --task id [--expected evaluation-id --outcome accepted|rejected|withdrawn]
  nexus audits list|show|attempts --db path [--task id] [--id audit-id]
  nexus audit --config path --task id --reviewer model-id [--max-cost amount]
  nexus summary --config path --task id --model id --keep n [--max-cost amount] [--idempotency-key key]
    Generate a stored summary draft; does not activate compaction
  nexus summaries list|show|recoveries --db path [--task id --after id --limit n] [--id id]
  nexus summary-review --config path --attempt id [--expected review-id] --decision approved|rejected --note text
  nexus summary-reviews --db path --attempt id
  nexus run --config path --model id --continue-task id --summary-attempt approved-attempt-id < prompt.txt

Development status: use --model auto for constrained automatic routing.
JSON lifecycle streaming and separate-command steering are available, plus line-oriented chat.
`

// Run executes a CLI invocation and returns its process exit code.
// Usage errors return 2, output failures return 1, and success returns 0.
func Run(args []string, stdout, stderr io.Writer, version string) int {
	return RunWithInput(args, os.Stdin, stdout, stderr, version)
}

func RunWithInput(args []string, stdin io.Reader, stdout, stderr io.Writer, version string) int {
	if len(args) > 0 && args[0] == "daemon" {
		return runDaemonControl(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "models" {
		return runModels(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "providers" {
		return runCatalog("providers", args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "doctor" {
		return runDoctor(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "skill-generations" {
		return runSkillGenerations(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "approval-decision" {
		return runApprovalDecision(args[1:], stdin, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "web" {
		return runWeb(args[1:], stdout, stderr)
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
	if len(args) > 0 && args[0] == "traces" {
		return runTraces(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "logs" {
		return runLogs(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "submit" {
		return runSubmit(args[1:], stdin, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "branch" {
		return runBranch(args[1:], stdin, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "resume" {
		return runResume(args[1:], stdin, stdout, stderr)
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
	if len(args) > 0 && args[0] == "session" {
		return runSessionInspection(args[1:], stdout, stderr)
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
		if _, err := fmt.Fprintf(stdout, "nexus %s\n", version); err != nil {
			return 1
		}
		return 0
	}
	// Do not echo unrecognized arguments: they may contain credentials.
	_, _ = io.WriteString(stderr, "nexus: invalid command or arguments; run 'nexus help'\n")
	return 2
}
