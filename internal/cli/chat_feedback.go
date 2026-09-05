package cli

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"darwinrouter/evaluation"
)

func chatFeedbackID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

// runChatFeedback requires explicit operator choices; errors never echo the
// supplied arguments, hook errors, evidence references, or model content.
func runChatFeedback(ctx context.Context, command, argument, task string, hooks chatHooks) string {
	if task == "" {
		return "No completed task is available for feedback.\n"
	}
	fields := strings.Fields(argument)
	accepted := func(text string) (bool, bool) {
		switch text {
		case "accepted":
			return true, true
		case "rejected":
			return false, true
		}
		return false, false
	}
	failed := "Feedback operation failed.\n"
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	switch command {
	case "/feedback":
		if len(fields) != 2 {
			return "Usage: /feedback accepted|rejected COST\n"
		}
		value, valid := accepted(fields[0])
		cost, err := strconv.ParseFloat(fields[1], 64)
		if !valid || err != nil || math.IsNaN(cost) || math.IsInf(cost, 0) || cost < 0 {
			return "Usage: /feedback accepted|rejected COST\n"
		}
		if hooks.Feedback == nil || hooks.Feedback(ctx, task, value, cost) != nil {
			return failed
		}
		return "Feedback recorded.\n"
	case "/feedback-revise":
		if len(fields) != 2 {
			return "Usage: /feedback-revise EXPECTED_ID accepted|rejected\n"
		}
		value, valid := accepted(fields[1])
		if !chatFeedbackID(fields[0]) || !valid {
			return "Usage: /feedback-revise EXPECTED_ID accepted|rejected\n"
		}
		if hooks.ReviseFeedback == nil || hooks.ReviseFeedback(ctx, task, fields[0], value) != nil {
			return failed
		}
		return "Feedback revised.\n"
	case "/feedback-show":
		if len(fields) != 0 {
			return "Usage: /feedback-show\n"
		}
		if hooks.FeedbackHistory == nil {
			return failed
		}
		records, err := hooks.FeedbackHistory(ctx, task)
		if errors.Is(err, sql.ErrNoRows) {
			return "No feedback history.\n"
		}
		if err != nil || len(records) > 1000 {
			return failed
		}
		if len(records) == 0 {
			return "No feedback history.\n"
		}
		var output strings.Builder
		output.WriteString("Feedback history:\n")
		seen := map[string]bool{}
		for _, record := range records {
			if record.Validate() != nil || record.TaskID != task || !chatFeedbackID(record.ID) || seen[record.ID] {
				return failed
			}
			seen[record.ID] = true
			outcome, err := evaluation.Resolve(record.Checks, record.AllowJudge)
			if err != nil {
				return failed
			}
			label := "rejected"
			if outcome.Accepted {
				label = "accepted"
			}
			output.WriteString(record.ID + " " + label + " source=" + string(outcome.Source) + "\n")
		}
		return output.String()
	default:
		return "Unknown feedback command.\n"
	}
}
