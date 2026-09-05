package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/user"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/approvals"
	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
)

// Input contains the exact previewed request and a stable decision ID. Actor
// attribution comes from the invoking OS identity, never model-authored JSON.
func runApprovalDecision(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 2 || args[0] != "--config" || args[1] == "" {
		fmt.Fprintln(stderr, "usage: darwin approval-decision --config path < decision.json")
		return 2
	}
	ctx, cancel := submissionCLIContext()
	defer cancel()
	// Existing bounded input helper observes cancellation without consuming more
	// than the command limit. ParseCommand supplies strict field validation.
	inputCtx, inputCancel := context.WithTimeout(ctx, 5*time.Second)
	body, err := readApprovalDecisionInput(inputCtx, stdin)
	inputCancel()
	command, parseErr := approvals.ParseCommand(body)
	if err != nil || parseErr != nil {
		fmt.Fprintln(stderr, "invalid approval decision")
		return 1
	}
	identity, err := user.Current()
	if err != nil || identity.Uid == "" {
		fmt.Fprintln(stderr, "operator identity unavailable")
		return 1
	}
	cfg, err := config.Load(config.Options{ProjectFile: args[1], Env: config.Environment(os.Environ())})
	if err != nil {
		fmt.Fprintln(stderr, "approval configuration unavailable")
		return 1
	}
	service, err := app.NewService(cfg, os.Getenv)
	if err != nil {
		fmt.Fprintln(stderr, "approval configuration unavailable")
		return 1
	}
	record, err := service.DecideApproval(ctx, command, "local_uid:"+identity.Uid)
	if err != nil {
		fmt.Fprintln(stderr, "approval decision rejected; inspect state before retrying")
		return 1
	}
	return writeSubmissionJSON(ctx, stdout, record)
}

func readApprovalDecisionInput(ctx context.Context, input io.Reader) (body []byte, err error) {
	defer func() {
		if recover() != nil {
			body = nil
			err = approvals.ErrInvalid
		}
	}()
	if input == nil || ctx.Err() != nil {
		return nil, approvals.ErrInvalid
	}
	reader, cleanup, err := prepareSteeringInput(ctx, input)
	if err != nil {
		return nil, approvals.ErrInvalid
	}
	defer func() {
		if cleanup() != nil {
			body = nil
			err = approvals.ErrInvalid
		}
	}()
	body, err = io.ReadAll(io.LimitReader(reader, (16<<10)+1))
	if err != nil || ctx.Err() != nil || len(body) > 16<<10 {
		return nil, approvals.ErrInvalid
	}
	return body, nil
}
