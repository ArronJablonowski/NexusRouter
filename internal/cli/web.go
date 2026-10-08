package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/branding"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	contract "github.com/ArronJablonowski/NexusRouter/webui"
)

func runWeb(args []string, stdout, stderr io.Writer) int {
	return runWebWithDiscovery(args, stdout, stderr, discoverWebInstallations)
}

func runWebWithDiscovery(args []string, stdout, stderr io.Writer, discover func() ([]webInstallation, error)) int {
	invalid := func() int {
		fmt.Fprintln(stderr, "usage: nexus web approve [--config path] [--service label] CHALLENGE_ID.DISPLAY_CODE")
		return 2
	}
	if len(args) < 2 || args[0] != "approve" {
		return invalid()
	}
	fs := flag.NewFlagSet("web approve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("config", branding.Getenv("DARWIN_CONFIG"), "configuration")
	service := fs.String("service", "", "installed macOS service label")
	// Challenge IDs are URL-safe base64 and may begin with '-'. Parse the
	// required trailing credential separately so flag parsing cannot mistake
	// a valid one-time code for an option.
	if fs.Parse(args[1:len(args)-1]) != nil || fs.NArg() != 0 {
		return invalid()
	}
	id, code, ok := strings.Cut(args[len(args)-1], ".")
	request := contract.BrowserChallengeApprovalRequest{Version: 1, DisplayCode: code}
	if !ok || strings.Contains(code, ".") || (contract.BrowserSessionRequest{Version: 1, ChallengeID: id}).Validate() != nil || request.Validate() != nil {
		return invalid()
	}
	token := branding.Getenv("DARWIN_API_TOKEN")
	if *path == "PATH" {
		fmt.Fprintln(stderr, "PATH is a placeholder. Use nexus web approve CODE for the installed local service, or supply its actual --config filename.")
		return 1
	}
	home, _ := os.UserHomeDir()
	userConfig, _ := os.UserConfigDir()
	installation, err := resolveWebInstallation(*path, *service, token, home, userConfig, config.Environment(os.Environ()), discover)
	if err != nil {
		fmt.Fprintln(stderr, errWebInstallation)
		return 1
	}
	cfg, err := config.Load(config.Options{ProjectFile: installation.path, Env: installation.env})
	if err != nil || !cfg.WebUI.Enabled {
		fmt.Fprintln(stderr, "Web UI configuration unavailable; use --config with the running daemon configuration")
		return 1
	}
	client, err := daemonClient(cfg, installation.token)
	if err != nil {
		fmt.Fprintln(stderr, "browser approval credentials unavailable; set NEXUS_API_TOKEN for the selected daemon or select its running macOS service")
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

// Only reads the current user's explicitly named installed service. Never logs
// launchctl output or exports its credential to the parent shell.
func webLaunchToken(output string) string {
	matches := regexp.MustCompile(`(?:^|\s)(?:NEXUS|DARWIN)_API_TOKEN\s*(?:=>|=)\s*([^\s]+)`).FindAllStringSubmatch(output, -1)
	if len(matches) != 1 || len(matches[0][1]) < 32 {
		return ""
	}
	return matches[0][1]
}
