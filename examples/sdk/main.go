// A minimal embedded client. Run from the repository root with:
// go run ./examples/sdk --config examples/local.yaml --model auto --prompt "Say hello"
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
	darwin "github.com/ArronJablonowski/DarwinRouter/sdk/v1"
)

func main() {
	path := flag.String("config", "", "explicit project configuration path")
	model := flag.String("model", "auto", "configured model ID or auto")
	prompt := flag.String("prompt", "", "non-sensitive example prompt (visible in process arguments)")
	flag.Parse()
	if *path == "" || *prompt == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "provide --config and --prompt")
		os.Exit(2)
	}
	client, err := darwin.New(darwin.ConfigOptions{ProjectFile: *path, LookupSecret: os.Getenv})
	if err != nil {
		fmt.Fprintln(os.Stderr, "configuration unavailable")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	result, err := client.RunStream(ctx, darwin.Request{Version: 1, ModelID: *model, Prompt: *prompt}, func(event runtime.Event) error {
		// Never print model-controlled text or tool payloads as terminal controls.
		if event.Kind == runtime.TaskStarted {
			_, err := fmt.Fprintln(os.Stderr, "task started")
			return err
		}
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "task failed; inspect durable history before retrying")
		os.Exit(1)
	}
	// Quoted output is suitable for this diagnostic example; applications choose
	// their own rendering while treating model output as untrusted data.
	if _, err := fmt.Fprintf(os.Stdout, "%q\n", result.Text); err != nil {
		os.Exit(1)
	}
}
