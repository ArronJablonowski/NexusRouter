// Command nexus-remote is the compatibility entry point for nexus remote.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/ArronJablonowski/NexusRouter/internal/remotecli"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := remotecli.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
