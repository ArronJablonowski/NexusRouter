package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/branding"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	contract "github.com/ArronJablonowski/NexusRouter/webui"
)

func runWeb(args []string, stdout, stderr io.Writer) int {
	invalid := func() int {
		fmt.Fprintln(stderr, "usage: nexus web approve [--config path] CHALLENGE_ID.DISPLAY_CODE")
		return 2
	}
	if len(args) < 2 || args[0] != "approve" {
		return invalid()
	}
	fs := flag.NewFlagSet("web approve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("config", "", "configuration")
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
	home, _ := os.UserHomeDir()
	installedPath := filepath.Join(home, "Library/Application Support/NexusRouter/live-test/config.yaml")
	serviceLabel := "com.nexusrouter.live-test"
	if _, err := os.Stat(installedPath); os.IsNotExist(err) {
		installedPath = filepath.Join(home, "Library/Application Support/DarwinRouter/live-test/config.yaml")
		serviceLabel = "com.darwinrouter.live-test"
	}
	if *path == "PATH" {
		fmt.Fprintln(stderr, "PATH is a placeholder. Use nexus web approve CODE for the installed local service, or supply its actual --config filename.")
		return 1
	}
	if *path == "" {
		*path = installedPath
	}
	if token == "" && runtime.GOOS == "darwin" && filepath.Clean(*path) == installedPath {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		raw, probeErr := exec.CommandContext(ctx, "/bin/launchctl", "print", "gui/"+strconv.Itoa(os.Getuid())+"/"+serviceLabel).Output()
		cancel()
		if probeErr == nil {
			token = webLaunchToken(string(raw))
		}
	}
	env := config.Environment(os.Environ())
	cfg, err := config.Load(config.Options{ProjectFile: *path, Env: env})
	if err != nil || !cfg.WebUI.Enabled {
		fmt.Fprintln(stderr, "Web UI configuration unavailable")
		return 1
	}
	client, err := daemonClient(cfg, token)
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

// Only reads the current user's explicitly named installed service. Never logs
// launchctl output or exports its credential to the parent shell.
func webLaunchToken(output string) string {
	matches := regexp.MustCompile(`(?:^|\s)(?:NEXUS|DARWIN)_API_TOKEN\s*(?:=>|=)\s*([^\s]+)`).FindAllStringSubmatch(output, -1)
	if len(matches) != 1 || len(matches[0][1]) < 32 {
		return ""
	}
	return matches[0][1]
}
