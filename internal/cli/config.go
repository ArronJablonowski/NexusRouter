package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
)

type overrides map[string]string

func (o overrides) String() string { return "" }
func (o overrides) Set(s string) error {
	k, v, ok := strings.Cut(s, "=")
	if !ok || k == "" {
		return fmt.Errorf("expected setting=value")
	}
	o[k] = v
	return nil
}

func runConfig(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "validate" && args[0] != "show") {
		_, _ = fmt.Fprintln(stderr, "usage: nexus config validate|show [--config path] [--user-config path] [--set key=value]")
		return 2
	}
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	project := fs.String("config", "", "project configuration")
	user := fs.String("user-config", "", "user configuration")
	values := overrides{}
	fs.Var(values, "set", "override scalar setting")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "invalid configuration arguments")
		return 2
	}
	if *project == "" {
		if _, err := os.Stat("config.yaml"); err == nil {
			*project = "config.yaml"
		} else if !os.IsNotExist(err) {
			_, _ = fmt.Fprintln(stderr, "cannot inspect project configuration")
			return 1
		}
	}
	if *user == "" {
		if home, err := os.UserHomeDir(); err == nil {
			candidate := filepath.Join(home, ".NexusRouter", "config", "config.yaml")
			if _, err := os.Stat(candidate); err == nil {
				*user = candidate
			} else if !os.IsNotExist(err) {
				_, _ = fmt.Fprintln(stderr, "cannot inspect user configuration")
				return 1
			}
		}
	}
	if *user == "" {
		if dir, err := os.UserConfigDir(); err == nil {
			candidate := filepath.Join(dir, "nexusrouter", "config.yaml")
			if _, err := os.Stat(candidate); os.IsNotExist(err) {
				candidate = filepath.Join(dir, "darwinrouter", "config.yaml")
			}
			if _, err := os.Stat(candidate); err == nil {
				*user = candidate
			} else if !os.IsNotExist(err) {
				_, _ = fmt.Fprintln(stderr, "cannot inspect user configuration")
				return 1
			}
		}
	}
	s, err := config.Load(config.Options{UserFile: *user, ProjectFile: *project, Env: config.Environment(os.Environ()), Flags: values})
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	if args[0] == "validate" {
		_, err = fmt.Fprintln(stdout, "Configuration valid (version 1)")
	} else {
		var data []byte
		data, err = s.RedactedJSON()
		if err == nil {
			_, err = fmt.Fprintln(stdout, string(data))
		}
	}
	if err != nil {
		return 1
	}
	return 0
}
