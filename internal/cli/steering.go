package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

type steeringReceipt = runtime.SteeringReceipt

func steeringMetadata(m runtime.SteeringMessage) steeringReceipt {
	return m.Receipt()
}

func parseSteeringFlags(args []string, required ...string) (map[string]string, error) {
	values := map[string]string{}
	allowed := map[string]bool{}
	for _, key := range required {
		allowed[key] = true
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "--") {
			return nil, errors.New("invalid steering arguments")
		}
		key, value, equals := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		if !allowed[key] {
			return nil, errors.New("invalid steering arguments")
		}
		if _, exists := values[key]; exists {
			return nil, errors.New("invalid steering arguments")
		}
		if !equals {
			i++
			if i >= len(args) {
				return nil, errors.New("invalid steering arguments")
			}
			value = args[i]
		}
		if value == "" {
			return nil, errors.New("invalid steering arguments")
		}
		values[key] = value
	}
	if len(values) != len(required) {
		return nil, errors.New("invalid steering arguments")
	}
	if !sessions.ValidEventPageID(values["task"]) {
		return nil, errors.New("invalid steering arguments")
	}
	if id, ok := values["id"]; ok && !sessions.ValidEventPageID(id) {
		return nil, errors.New("invalid steering arguments")
	}
	if key, ok := values["key"]; ok && (len(key) > 128 || !utf8.ValidString(key) || strings.TrimSpace(key) == "") {
		return nil, errors.New("invalid steering arguments")
	}
	return values, nil
}

func runSteer(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	ctx, cancel := submissionCLIContext()
	defer cancel()
	return runSteerContext(ctx, args, stdin, stdout, stderr)
}
func runSteerContext(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags, err := parseSteeringFlags(args, "config", "task", "key")
	if err != nil {
		fmt.Fprintln(stderr, "usage: darwin steer --config path --task id --key opaque < guidance.txt")
		return 2
	}
	inputCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	text, err := readSteeringInput(inputCtx, stdin)
	cancel()
	if err != nil || !runtime.ValidSteeringText(text) {
		fmt.Fprintln(stderr, "steering input unavailable")
		return 1
	}
	cfg, err := config.Load(config.Options{ProjectFile: flags["config"], Env: config.Environment(os.Environ())})
	if err != nil {
		fmt.Fprintln(stderr, "steering configuration unavailable")
		return 1
	}
	svc, err := app.NewService(cfg, os.Getenv)
	if err != nil {
		fmt.Fprintln(stderr, "steering configuration unavailable")
		return 1
	}
	message, err := svc.SteerTask(ctx, flags["task"], flags["key"], text)
	if err != nil || message.Validate() != nil {
		fmt.Fprintln(stderr, "steering operation failed")
		return 1
	}
	return writeSubmissionJSON(ctx, stdout, steeringMetadata(message))
}

func runSteering(args []string, stdout, stderr io.Writer) int {
	usage := func() int {
		fmt.Fprintln(stderr, "usage: darwin steering list|show --db path --task id [--id message]")
		return 2
	}
	if len(args) == 0 || (args[0] != "show" && args[0] != "list") {
		return usage()
	}
	required := []string{"db", "task"}
	if args[0] == "show" {
		required = append(required, "id")
	}
	flags, err := parseSteeringFlags(args[1:], required...)
	if err != nil {
		return usage()
	}
	ctx, cancel := submissionCLIContext()
	defer cancel()
	db, err := telemetry.OpenReadOnly(ctx, flags["db"])
	if err != nil {
		fmt.Fprintln(stderr, "steering storage unavailable")
		return 1
	}
	defer db.Close()
	var output any
	if args[0] == "show" {
		var message runtime.SteeringMessage
		message, err = db.SteeringStatus(ctx, flags["task"], flags["id"])
		if err == nil && message.Validate() != nil {
			err = errors.New("invalid record")
		}
		output = message
	} else {
		var messages []runtime.SteeringMessage
		messages, err = db.ListSteering(ctx, flags["task"])
		records := []steeringReceipt{}
		if len(messages) > runtime.MaxSteeringMessages {
			err = errors.New("invalid records")
		}
		for _, m := range messages {
			if m.Validate() != nil || m.TaskID != flags["task"] {
				err = errors.New("invalid record")
				break
			}
			records = append(records, steeringMetadata(m))
		}
		output = records
	}
	if err != nil {
		fmt.Fprintln(stderr, "steering operation failed")
		return 1
	}
	return writeSubmissionJSON(ctx, stdout, output)
}
