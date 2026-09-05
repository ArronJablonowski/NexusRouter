package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
)

type modelsDeprecationArgs struct {
	config, model, domain, profile string
	policy                         evaluation.DeprecationPolicy
}

func parseModelsDeprecationArgs(args []string) (modelsDeprecationArgs, error) {
	var out modelsDeprecationArgs
	invalid := errors.New("invalid model deprecation arguments")
	seen := map[string]bool{}
	for i := 0; i < len(args); i++ {
		name, _, equal := strings.Cut(strings.TrimLeft(args[i], "-"), "=")
		if !strings.HasPrefix(args[i], "-") || seen[name] {
			return out, invalid
		}
		seen[name] = true
		if !equal {
			i++
		}
	}
	fs := flag.NewFlagSet("models deprecation", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&out.config, "config", "", "configuration")
	fs.StringVar(&out.model, "model", "", "configured model ID")
	fs.StringVar(&out.domain, "domain", "general", "task domain")
	fs.StringVar(&out.profile, "profile", "default", "execution profile")
	fs.IntVar(&out.policy.Window, "window", 50, "trailing evidence window")
	fs.IntVar(&out.policy.MinSamples, "minimum-samples", 20, "minimum evidence samples")
	fs.Float64Var(&out.policy.FailureThreshold, "failure-threshold", .35, "failure fraction")
	if fs.Parse(args) != nil || fs.NArg() != 0 || strings.TrimSpace(out.config) == "" || !deprecationIdentifier(out.model) || !deprecationIdentifier(out.domain) || !deprecationIdentifier(out.profile) || out.policy.Validate() != nil {
		return out, invalid
	}
	return out, nil
}

func deprecationIdentifier(s string) bool {
	return strings.TrimSpace(s) == s && s != "" && len(s) <= 512 && !strings.ContainsFunc(s, unicode.IsControl)
}

func runModelsDeprecation(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "deprecation" {
		fmt.Fprintln(stderr, "usage: darwin models deprecation --config path --model id [--domain general --profile default --window 50 --minimum-samples 20 --failure-threshold 0.35]")
		return 2
	}
	parsed, err := parseModelsDeprecationArgs(args[1:])
	if err != nil {
		fmt.Fprintln(stderr, "invalid model deprecation arguments")
		return 2
	}
	cfg, err := config.Load(config.Options{ProjectFile: parsed.config, Env: config.Environment(os.Environ())})
	if err != nil {
		fmt.Fprintln(stderr, "model deprecation configuration unavailable")
		return 1
	}
	svc, err := app.NewService(cfg, os.Getenv)
	if err != nil {
		fmt.Fprintln(stderr, "model deprecation configuration invalid")
		return 1
	}
	ctx, cancel := submissionCLIContext()
	defer cancel()
	report, err := svc.ModelDeprecation(ctx, parsed.model, parsed.domain, parsed.profile, parsed.policy)
	if err != nil {
		fmt.Fprintln(stderr, "model deprecation report unavailable")
		return 1
	}
	if json.NewEncoder(stdout).Encode(report) != nil {
		return 1
	}
	return 0
}
