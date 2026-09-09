package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	contract "github.com/ArronJablonowski/DarwinRouter/webui"
)

func runWeb(args []string, stdout, stderr io.Writer) int {
	invalid := func() int {
		fmt.Fprintln(stderr, "usage: darwin web approve --config path CHALLENGE_ID.DISPLAY_CODE")
		return 2
	}
	if len(args) < 1 || args[0] != "approve" {
		return invalid()
	}
	fs := flag.NewFlagSet("web approve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("config", "", "configuration")
	if fs.Parse(args[1:]) != nil || *path == "" || fs.NArg() != 1 {
		return invalid()
	}
	id, code, ok := strings.Cut(fs.Arg(0), ".")
	request := contract.BrowserChallengeApprovalRequest{Version: 1, DisplayCode: code}
	if !ok || strings.Contains(code, ".") || (contract.BrowserSessionRequest{Version: 1, ChallengeID: id}).Validate() != nil || request.Validate() != nil {
		return invalid()
	}
	cfg, err := config.Load(config.Options{ProjectFile: *path, Env: config.Environment(os.Environ())})
	if err != nil || !cfg.WebUI.Enabled {
		fmt.Fprintln(stderr, "Web UI configuration unavailable")
		return 1
	}
	client, err := daemonClient(cfg, os.Getenv("DARWIN_API_TOKEN"))
	if err != nil {
		fmt.Fprintln(stderr, "browser approval unavailable")
		return 1
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.ApproveBrowserChallenge(ctx, id, code); err != nil {
		if !errors.Is(err, errDaemonControl) {
			return 1
		}
		fmt.Fprintln(stderr, "browser approval unavailable")
		return 1
	}
	if _, err := fmt.Fprintln(stdout, `{"version":1,"approved":true}`); err != nil {
		return 1
	}
	return 0
}
