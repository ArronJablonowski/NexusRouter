package sessions

import (
	"math"

	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

// MaxTerminalRouteAttempts leaves room in the 66-history recovery envelope for
// a final root plus 16 worker/inference pairs.
const MaxTerminalRouteAttempts = 32

// ProjectTerminalTree reconstructs a bounded, fully terminal submission tree.
// History order is durable task-start order, not child completion order. It
// projects the parent answer only; it never executes or resumes any node.
func ProjectTerminalTree(histories [][]runtime.Event) (TerminalOutcome, error) {
	bad := func() (TerminalOutcome, error) { return TerminalOutcome{}, ErrHistory }
	if len(histories) < 1 || len(histories) > 66 {
		return bad()
	}
	type node struct {
		history  []runtime.Event
		worker   bool
		out      TerminalOutcome
		text     string
		accepted bool
		children []string
	}
	nodes := make(map[string]*node, len(histories))
	var order []string
	bytes, count := 0, 0
	submission := ""
	for _, history := range histories {
		if len(history) < 2 {
			return bad()
		}
		start := history[0]
		if nodes[start.TaskID] != nil || !ValidEventPageID(start.TaskID) {
			return bad()
		}
		if submission == "" {
			submission = start.Data.SubmissionID
		}
		if !ValidEventPageID(submission) || start.Data.SubmissionID != submission {
			return bad()
		}
		for _, event := range history {
			body, err := event.Encode()
			if err != nil || len(body) > (8<<20)-bytes || count >= 10000 {
				return bad()
			}
			bytes += len(body)
			count++
		}
		n := &node{history: history, worker: start.WorkerID != ""}
		var err error
		if n.worker {
			n.text, n.accepted, err = projectWorkerTerminal(history)
		} else {
			n.out, err = ProjectTerminalSubmission(history)
		}
		if err != nil {
			return bad()
		}
		nodes[start.TaskID] = n
		order = append(order, start.TaskID)
	}
	var roots []string
	for _, id := range order {
		n := nodes[id]
		start := n.history[0]
		parent := nodes[start.Data.ParentTaskID]
		if n.worker {
			if parent == nil || parent.worker || start.Data.RetryOfTaskID != "" {
				return bad()
			}
			parent.children = append(parent.children, id)
		} else if parent != nil {
			if !parent.worker || start.Data.RetryOfTaskID != "" {
				return bad()
			}
			parent.children = append(parent.children, id)
		} else {
			// A root may continue an external session. In-tree edges must always
			// alternate root -> work -> inference; no implicit nested delegation.
			roots = append(roots, id)
		}
	}
	if len(roots) < 1 || len(roots) > MaxTerminalRouteAttempts {
		return bad()
	}
	rootSet := make(map[string]bool)
	for _, id := range roots {
		rootSet[id] = true
	}
	for _, id := range order {
		n := nodes[id]
		if n.worker {
			if !rootSet[n.history[0].Data.ParentTaskID] || len(n.children) > 1 {
				return bad()
			}
			parent := nodes[n.history[0].Data.ParentTaskID]
			if n.history[0].SessionID != parent.history[0].SessionID {
				return bad()
			}
			for _, childID := range n.children {
				privacy := nodes[childID].history[0].Data.Privacy
				if privacy != "local_only" && privacy != "cloud_allowed" {
					return bad()
				}
				if parent.history[0].Data.Privacy != "cloud_allowed" && privacy != "local_only" {
					return bad()
				}
			}
			if n.accepted {
				if len(n.children) != 1 {
					return bad()
				}
				child := nodes[n.children[0]]
				if child.out.State != "succeeded" || child.out.Result == nil || child.out.Result.Text != n.text {
					return bad()
				}
			}
		} else if !rootSet[id] && len(n.children) != 0 {
			return bad()
		}
	}
	for i, id := range roots {
		n := nodes[id]
		if i == 0 {
			if n.history[0].Data.RetryOfTaskID != "" {
				return bad()
			}
		} else if n.history[0].Data.RetryOfTaskID != roots[i-1] {
			return bad()
		}
		if i == len(roots)-1 {
			continue
		}
		last := n.history[len(n.history)-1]
		if last.Kind != runtime.TaskFailed || (last.Data.Code != "provider_retryable_no_output" && last.Data.Code != "provider_failed_before_tools") || len(n.children) != 0 {
			return bad()
		}
		turns := 0
		for _, event := range n.history {
			if event.Kind == runtime.TurnStarted {
				turns++
			}
			if event.Kind == runtime.SteeringApplied || event.Kind == runtime.TurnCompleted || event.Kind == runtime.ToolStarted || event.Kind == runtime.ToolCompleted || len(event.Data.ToolCalls) > 0 || (event.Kind == runtime.ModelDelta && event.Data.Text != "" && last.Data.Code != "provider_failed_before_tools") {
				return bad()
			}
		}
		if turns != 1 {
			return bad()
		}
	}
	out := nodes[roots[len(roots)-1]].out
	if out.Result != nil {
		out.Result.PreviousTaskIDs = append([]string{}, roots[:len(roots)-1]...)
		costKnown, usageKnown := true, true
		cost := 0.0
		usage := providers.Usage{}
		for _, id := range roots {
			result := nodes[id].out.Result
			if result == nil || result.RouteEstimatedCost == nil || cost > math.MaxFloat64-*result.RouteEstimatedCost {
				costKnown = false
			} else if costKnown {
				cost += *result.RouteEstimatedCost
			}
			if result == nil || result.Usage == nil || result.Usage.InputTokens > math.MaxInt64-usage.InputTokens || result.Usage.OutputTokens > math.MaxInt64-usage.OutputTokens {
				usageKnown = false
			} else if usageKnown {
				usage.InputTokens += result.Usage.InputTokens
				usage.OutputTokens += result.Usage.OutputTokens
			}
		}
		out.Result.RouteEstimatedCost = nil
		out.Result.Usage = nil
		if costKnown {
			out.Result.RouteEstimatedCost = &cost
		}
		if usageKnown {
			out.Result.Usage = &usage
		}
	}
	return out, nil
}
