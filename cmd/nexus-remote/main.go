// Command nexus-remote is the compatibility entry point for nexus remote.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/ArronJablonowski/NexusRouter/internal/processaudit"
	"github.com/ArronJablonowski/NexusRouter/internal/remotecli"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
)

func main() {
	home, err := os.UserHomeDir()
	if err != nil || processaudit.Enable(filepath.Join(home, ".NexusRouter", "data", "process-audit")) != nil {
		fmt.Fprintln(os.Stderr, "NexusRouter: subprocess audit initialization failed")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := remotecli.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
