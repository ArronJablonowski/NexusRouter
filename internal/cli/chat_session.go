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

	"darwinrouter/internal/app"
	"darwinrouter/runtime"
)

func runChatSession(ctx context.Context, base app.Request, hooks chatHooks, lines <-chan chatLine, signals <-chan os.Signal, out io.Writer) int {
	type completion struct {
		result app.Result
		err    error
	}
	events := make(chan runtime.Event)
	var done chan completion
	var cancel context.CancelFunc
	var task string
	last := base.ContinueTaskID
	// Feedback targets the most recently displayed successful answer, never a
	// previous answer hidden behind a failed run or an initial continuation ID.
	feedbackTask := ""
	failedOutput := false
	eof := false
	write := func(text string) (ok bool) {
		if failedOutput {
			return false
		}
		defer func() {
			if recover() != nil {
				failedOutput = true
				ok = false
			}
		}()
		text = chatSafeText(text)
		n, err := io.WriteString(out, text)
		if err != nil || n != len(text) {
			failedOutput = true
			return false
		}
		return true
	}
	join := func() {
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
	for {
		select {
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
		case event := <-events:
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
				if !write(finished.result.Text + "\n") {
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
				lines = nil
				eof = true
				if cancel == nil {
					return 0
				}
				continue
			}
			if line.Err != nil {
				if errors.Is(line.Err, io.EOF) {
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
			feedbackTask = ""
			request.Prompt = text
			request.Messages = nil
			request.ContinueTaskID = last
			runCtx, stop := context.WithTimeout(ctx, 5*time.Minute)
			cancel = stop
			done = make(chan completion, 1)
			completionChannel := done
			go func() {
				result, err := hooks.Run(runCtx, request, func(event runtime.Event) error {
					select {
					case events <- event:
						return nil
					case <-runCtx.Done():
						return runCtx.Err()
					}
				})
				completionChannel <- completion{result, err}
			}()
		}
	}
}
