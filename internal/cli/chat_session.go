package cli

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/tools"
)

func runChatSession(ctx context.Context, base app.Request, hooks chatHooks, lines <-chan chatLine, signals <-chan os.Signal, out io.Writer) int {
	type completion struct {
		result app.Result
		err    error
	}
	type update struct {
		event  runtime.Event
		text   string
		isText bool
	}
	// A single unbuffered channel preserves lifecycle/text ordering and applies
	// backpressure without retaining a second copy of the complete answer.
	events := make(chan update)
	var done chan completion
	var cancel context.CancelFunc
	var task string
	last := base.ContinueTaskID
	// Feedback targets the most recently displayed successful answer, never a
	// previous answer hidden behind a failed run or an initial continuation ID.
	feedbackTask := ""
	failedOutput := false
	eof := false
	live := hooks.RunLive != nil
	liveOpen := false
	liveLineStart := true
	liveBytes := 0
	var textFilter chatTextFilter
	var pendingApproval *chatApprovalRequest
	var approvalDone <-chan struct{}
	approvalRequests := hooks.Approvals
	clearApproval := func() {
		if pendingApproval != nil {
			pendingApproval.respond(false, tools.ErrDenied)
		}
		pendingApproval, approvalDone = nil, nil
	}
	rawWrite := func(text string) (ok bool) {
		if failedOutput {
			return false
		}
		defer func() {
			if recover() != nil {
				failedOutput = true
				ok = false
			}
		}()
		n, err := io.WriteString(out, text)
		if err != nil || n != len(text) {
			failedOutput = true
			return false
		}
		return true
	}
	// Trusted status messages are separated from provisional output and are not
	// fed through the model's stateful terminal-control filter.
	write := func(text string) bool {
		if liveOpen {
			liveOpen = false
			if !rawWrite("\n") {
				return false
			}
		}
		return rawWrite(chatSafeText(text))
	}
	join := func() {
		clearApproval()
		if cancel != nil {
			cancel()
			<-done
			cancel = nil
			done = nil
		}
	}
	defer func() {
		if cancel != nil {
			join()
		}
	}()
	if !write("Chat ready. /help for commands.\n") {
		return 1
	}
	if live && !write("Live assistant text is provisional until [task completed].\n") {
		return 1
	}
	for {
		select {
		case <-approvalDone:
			clearApproval()
			if !write("Approval no longer pending.\n") {
				join()
				return 1
			}
		case proposal, ok := <-approvalRequests:
			if !ok {
				approvalRequests = nil
				continue
			}
			if proposal.ctx == nil || proposal.ctx.Err() != nil || proposal.request.Validate() != nil || proposal.request.TaskID != task || pendingApproval != nil || cancel == nil || eof {
				proposal.respond(false, tools.ErrDenied)
				continue
			}
			pendingApproval, approvalDone = &proposal, proposal.ctx.Done()
			if !write(proposal.preview) {
				join()
				return 1
			}
		case <-ctx.Done():
			join()
			return 1
		case signal, ok := <-signals:
			if !ok {
				signals = nil
				continue
			}
			if signal == syscall.SIGTERM {
				join()
				return 0
			}
			if signal == os.Interrupt {
				if cancel == nil {
					return 0
				}
				cancel()
				if !write("Cancel requested.\n") {
					join()
					return 1
				}
			}
		case item := <-events:
			if item.isText {
				if !utf8.ValidString(item.text) || len(item.text) > (1<<20)-liveBytes {
					join()
					write("Live output unavailable or exceeded its display limit. Partial text is not an accepted answer.\n")
					return 1
				}
				liveBytes += len(item.text)
				text := textFilter.Write(item.text)
				if text != "" {
					if !liveOpen {
						if !write("[assistant provisional]\n") {
							join()
							return 1
						}
						liveOpen = true
						liveLineStart = true
					}
					if !rawWrite(chatQuotedText(text, &liveLineStart)) {
						join()
						return 1
					}
				}
				continue
			}
			event := item.event
			switch event.Kind {
			case runtime.TaskStarted:
				task = event.TaskID
				write("[task " + task + "]\n")
			case runtime.TurnStarted:
				write("[turn started]\n")
			case runtime.ToolStarted:
				write("[tool " + event.Data.ToolName + " started]\n")
			case runtime.ToolCompleted:
				write("[tool " + event.Data.ToolName + " completed]\n")
			case runtime.SteeringApplied:
				write("[guidance applied]\n")
			}
			if failedOutput {
				join()
				return 1
			}
		case finished := <-done:
			clearApproval()
			cancel()
			cancel = nil
			done = nil
			task = ""
			// Drain metadata before permitting another run to reuse the event channel.
			for len(events) > 0 {
				<-events
			}
			if finished.err == nil {
				if finished.result.TaskID != "" {
					last = finished.result.TaskID
					feedbackTask = finished.result.TaskID
					base.Compaction = nil
					base.SummaryAttemptID = ""
				}
				answer := finished.result.Text + "\n"
				if live {
					// Text already arrived from the provider. Reprinting Result.Text
					// would duplicate the final answer and mislabel prior turns.
					answer = "[task completed]\n"
				}
				if !write(answer) {
					return 1
				}
			} else {
				if !write("Task did not complete successfully. Previous successful context retained.\n") {
					return 1
				}
			}
			if eof {
				return 0
			}
		case line, ok := <-lines:
			if !ok {
				clearApproval()
				lines = nil
				eof = true
				if cancel == nil {
					return 0
				}
				continue
			}
			if line.Err != nil {
				if errors.Is(line.Err, io.EOF) {
					clearApproval()
					lines = nil
					eof = true
					if cancel == nil {
						return 0
					}
					continue
				}
				write("Input unavailable.\n")
				join()
				return 1
			}
			text := line.Text
			if strings.TrimSpace(text) == "" {
				continue
			}
			escaped := strings.HasPrefix(text, "//")
			if escaped {
				text = text[1:]
			}
			if !escaped && strings.HasPrefix(text, "/") {
				command, argument, _ := strings.Cut(text, " ")
				argument = strings.TrimSpace(argument)
				if command == "/approve" || command == "/deny" {
					if pendingApproval == nil || argument != pendingApproval.request.ID || pendingApproval.ctx.Err() != nil {
						write("Approval decision not accepted: an exact active request ID is required.\n")
					} else {
						pendingApproval.respond(command == "/approve", nil)
						pendingApproval, approvalDone = nil, nil
						write("Approval decision submitted; this is not a file-creation result.\n")
					}
					if failedOutput {
						join()
						return 1
					}
					continue
				}
				if command == "/feedback" || command == "/feedback-show" || command == "/feedback-revise" {
					if cancel != nil {
						write("Feedback not accepted while a task is active.\n")
					} else if feedbackTask == "" {
						write("Feedback requires a successful answer from this conversation.\n")
					} else {
						write(runChatFeedback(ctx, command, argument, feedbackTask, hooks))
					}
					if failedOutput {
						join()
						return 1
					}
					continue
				}
				if command != "/steer" && argument != "" {
					if !write("Command takes no arguments.\n") {
						join()
						return 1
					}
					continue
				}
				switch command {
				case "/help":
					write("Enter text to start a task. /status /new /cancel /steer TEXT /quit. Use // for a literal slash.\n/feedback accepted|rejected COST, /feedback-show, /feedback-revise EXPECTED_ID accepted|rejected target the latest successful answer before starting another task.\n")
					if hooks.Approvals != nil {
						write("File creation requires review: /approve REQUEST_ID or /deny REQUEST_ID. No blanket approvals.\n")
					}
				case "/status":
					if cancel != nil {
						if task == "" {
							write("Task starting.\n")
						} else {
							write("Task active: " + task + "\n")
						}
					} else {
						write("Idle.\n")
					}
				case "/new":
					if cancel != nil {
						write("Cannot reset while a task is active.\n")
					} else {
						last = ""
						base.ContinueTaskID = ""
						base.Compaction = nil
						base.SummaryAttemptID = ""
						feedbackTask = ""
						write("New conversation.\n")
					}
				case "/cancel":
					if cancel != nil {
						cancel()
						write("Cancel requested.\n")
					} else {
						write("No active task.\n")
					}
				case "/quit":
					join()
					return 0
				case "/steer":
					if cancel == nil || task == "" || argument == "" || hooks.Steer == nil {
						write("Guidance not accepted: an active task ID and text are required.\n")
					} else {
						steerCtx, stop := context.WithTimeout(ctx, 5*time.Second)
						message, err := hooks.Steer(steerCtx, task, rand.Text(), argument)
						stop()
						if err != nil {
							if errors.Is(err, runtime.ErrSteeringClosed) {
								write("Guidance not accepted: the task no longer accepts steering.\n")
							} else {
								write("Guidance not accepted.\n")
							}
						} else if message.Validate() != nil || message.TaskID != task {
							write("Guidance not accepted.\n")
						} else {
							write("Guidance queued: " + message.ID + "\n")
						}
					}
				default:
					write("Unknown command. Use /help.\n")
				}
				if failedOutput {
					join()
					return 1
				}
				continue
			}
			if cancel != nil {
				if !write("Task busy. Use /steer TEXT for active guidance.\n") {
					join()
					return 1
				}
				continue
			}
			request := base
			liveBytes = 0
			textFilter.Reset()
			feedbackTask = ""
			request.Prompt = text
			request.Messages = nil
			request.ContinueTaskID = last
			runCtx, stop := context.WithTimeout(ctx, 5*time.Minute)
			cancel = stop
			done = make(chan completion, 1)
			completionChannel := done
			go func() {
				deliver := func(item update) error {
					select {
					case events <- item:
						return nil
					case <-runCtx.Done():
						return runCtx.Err()
					}
				}
				emitEvent := func(event runtime.Event) error { return deliver(update{event: event}) }
				var result app.Result
				var err error
				if hooks.RunLive != nil {
					result, err = hooks.RunLive(runCtx, request, emitEvent, func(text string) error {
						return deliver(update{text: text, isText: true})
					})
				} else if hooks.Run != nil {
					result, err = hooks.Run(runCtx, request, emitEvent)
				} else {
					err = app.ErrAdmission
				}
				completionChannel <- completion{result, err}
			}()
		}
	}
}
