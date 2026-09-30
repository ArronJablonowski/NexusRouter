package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

type taskStreamRunner func(context.Context, app.Request, func(runtime.Event) error) (app.Result, error)

// runTaskJSON delivers one JSON object per line. A failed sink is latched so
// cancellation cleanup cannot retry a partial line or append another result.
func runTaskJSON(ctx context.Context, request app.Request, run taskStreamRunner, stdout, stderr io.Writer) (exitCode int) {
	// Go otherwise exits immediately on a broken fd 1/2 pipe, preventing the
	// runtime from canceling and journaling cleanup after a consumer closes.
	pipeSignals := make(chan os.Signal, 1)
	signal.Notify(pipeSignals, syscall.SIGPIPE)
	defer signal.Stop(pipeSignals)
	output, cleanup, setupErr := prepareStreamOutput(ctx, stdout)
	if setupErr != nil {
		fmt.Fprintln(stderr, "cannot prepare task output")
		return 1
	}
	defer func() {
		if cleanup() != nil {
			exitCode = 1
		}
	}()
	stdout = output
	broken := false
	write := func(value any) (err error) {
		if broken {
			return app.ErrEventDelivery
		}
		defer func() {
			if recover() != nil {
				err = app.ErrEventDelivery
			}
			if err != nil {
				broken = true
			}
		}()
		body, err := json.Marshal(value)
		if err != nil {
			return app.ErrEventDelivery
		}
		body = append(body, '\n')
		n, err := stdout.Write(body)
		if err != nil || n != len(body) {
			return app.ErrEventDelivery
		}
		return nil
	}
	result, err := run(ctx, request, func(event runtime.Event) error {
		if event.Validate() != nil {
			broken = true
			return app.ErrEventDelivery
		}
		return write(struct {
			Version int           `json:"version"`
			Type    string        `json:"type"`
			Event   runtime.Event `json:"event"`
		}{1, "event", event})
	})
	code := ""
	var usage any
	if result.Usage != nil {
		usage = map[string]int64{"input_tokens": result.Usage.InputTokens, "output_tokens": result.Usage.OutputTokens}
	}
	public := map[string]any{"task_id": result.TaskID, "text": result.Text, "turns": result.Turns, "finish_reason": result.FinishReason, "usage": usage, "previous_task_ids": result.PreviousTaskIDs, "route_estimated_cost": result.RouteEstimatedCost, "audit_id": result.AuditID, "audit_status": result.AuditStatus}
	if err != nil || broken {
		code = "task_failed"
		switch {
		case broken || errors.Is(err, app.ErrEventDelivery):
			code = "event_delivery_failed"
		case errors.Is(err, app.ErrAdmission):
			code = "admission_denied"
		case errors.Is(err, context.Canceled):
			code = "canceled"
		case errors.Is(err, context.DeadlineExceeded):
			code = "deadline_exceeded"
		}
		public = map[string]any{"task_id": result.TaskID, "previous_task_ids": result.PreviousTaskIDs, "route_estimated_cost": result.RouteEstimatedCost, "audit_id": result.AuditID}
	}
	finalErr := write(struct {
		Version int            `json:"version"`
		Type    string         `json:"type"`
		Result  map[string]any `json:"result"`
		Error   string         `json:"error,omitempty"`
	}{1, "result", public, code})
	if err != nil || finalErr != nil || broken {
		fmt.Fprintln(stderr, "task failed; inspect local task history before retrying")
		return 1
	}
	return 0
}
