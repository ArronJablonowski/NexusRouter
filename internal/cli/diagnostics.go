package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ArronJablonowski/NexusRouter/health"
	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/branding"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
)

var errDiagnosticArguments = errors.New("invalid diagnostic arguments")

func diagnosticConfig(args []string, command string) (config.Settings, error) {
	seen := false
	for _, arg := range args {
		name, _, _ := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		if name == "config" {
			if seen {
				return config.Settings{}, errDiagnosticArguments
			}
			seen = true
		}
	}
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("config", "", "configuration")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *path == "" || !seen {
		return config.Settings{}, errDiagnosticArguments
	}
	return config.Load(config.Options{ProjectFile: *path, Env: config.Environment(os.Environ())})
}

func runModels(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "deprecation" {
		return runModelsDeprecation(args, stdout, stderr)
	}
	return runCatalog("models", args, stdout, stderr)
}

func runCatalog(kind string, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "list" {
		fmt.Fprintf(stderr, "usage: nexus %s list --config path\n", kind)
		return 2
	}
	cfg, err := diagnosticConfig(args[1:], kind)
	if err != nil {
		if errors.Is(err, errDiagnosticArguments) {
			fmt.Fprintf(stderr, "usage: nexus %s list --config path\n", kind)
			return 2
		}
		fmt.Fprintln(stderr, "diagnostic configuration unavailable")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if kind == "providers" {
		type item struct {
			ID                 string `json:"id"`
			Kind               string `json:"kind"`
			CredentialRequired bool   `json:"credential_required"`
			ManageResidency    bool   `json:"manage_residency"`
			RequestTimeout     string `json:"request_timeout"`
		}
		items := make([]item, len(cfg.Providers))
		for i, provider := range cfg.Providers {
			timeout := provider.RequestTimeout
			if timeout == "" && provider.Kind != "codex_app_server" {
				timeout = "5m"
			}
			items[i] = item{provider.ID, provider.Kind, provider.APIKeyEnv != "", provider.ManageResidency, timeout}
		}
		return writeSubmissionJSON(ctx, stdout, items)
	}
	service, err := app.NewService(cfg, os.Getenv)
	if err != nil {
		fmt.Fprintln(stderr, "model catalog unavailable")
		return 1
	}
	catalog, err := service.ConfiguredModelCatalog(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "model catalog unavailable")
		return 1
	}
	return writeSubmissionJSON(ctx, stdout, catalog)
}

func runDoctor(args []string, stdout, stderr io.Writer) int {
	cfg, err := diagnosticConfig(args, "doctor")
	if err != nil {
		if errors.Is(err, errDiagnosticArguments) {
			fmt.Fprintln(stderr, "usage: nexus doctor --config path")
			return 2
		}
		fmt.Fprintln(stderr, "diagnostic configuration unavailable")
		return 1
	}
	client, err := daemonClient(cfg, branding.Getenv("DARWIN_API_TOKEN"))
	if err != nil {
		fmt.Fprintln(stderr, "daemon health unavailable")
		return 1
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, client.base+"/v1/health", nil)
	if err != nil {
		return 1
	}
	request.Header.Set("Authorization", "Bearer "+client.token)
	response, err := client.client.Do(request)
	if err != nil {
		fmt.Fprintln(stderr, "daemon health unavailable")
		return 1
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, (128<<10)+1))
	var report health.Report
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err != nil || len(body) > 128<<10 || response.StatusCode != http.StatusOK || decoder.Decode(&report) != nil || decoder.Decode(&struct{}{}) != io.EOF || report.Validate() != nil {
		fmt.Fprintln(stderr, "daemon health unavailable")
		return 1
	}
	if writeSubmissionJSON(ctx, stdout, report) != 0 {
		return 1
	}
	if !report.Ready {
		return 1
	}
	return 0
}
