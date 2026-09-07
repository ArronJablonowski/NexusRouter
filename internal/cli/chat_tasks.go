package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func runChatTasks(ctx context.Context, after string, hooks chatHooks) string {
	if hooks.ListTasks == nil {
		return "Saved task list unavailable.\n"
	}
	options := sessions.TaskListOptions{After: after, Limit: 10}
	if options.Validate() != nil {
		return "Saved task list unavailable.\n"
	}
	page, err := hooks.ListTasks(ctx, options)
	if err != nil || ctx.Err() != nil || page.Validate() != nil || len(page.Items) > options.Limit {
		return "Saved task list unavailable.\n"
	}
	if len(page.Items) == 0 {
		return "No saved tasks found.\n"
	}
	var out strings.Builder
	out.WriteString("Saved tasks (newest first; inspect eligibility before resume):\n")
	for _, item := range page.Items {
		fmt.Fprintf(&out, "%s  %s  sequence %d  %s\n", item.TaskID, item.State, item.Sequence, item.StartedAt.UTC().Format("2006-01-02T15:04:05Z07:00"))
	}
	if page.HasMore {
		out.WriteString("More: /tasks ")
		out.WriteString(page.NextCursor)
		out.WriteByte('\n')
	}
	return out.String()
}
