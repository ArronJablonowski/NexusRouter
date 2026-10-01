package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/ArronJablonowski/NexusRouter/internal/remotecli"
	"io"
	"os"
	"os/signal"
	"syscall"
)

func runRemote(args []string, input io.Reader, output, errorOutput io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := remotecli.Run(ctx, args, input, output, errorOutput); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(errorOutput, err)
		return 1
	}
	return 0
}
