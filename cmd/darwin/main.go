// Command darwin is the deprecated compatibility entry point; use nexus.
package main

import (
	"fmt"
	"github.com/ArronJablonowski/NexusRouter/internal/processaudit"
	"os"
	"path/filepath"

	"github.com/ArronJablonowski/NexusRouter/internal/cli"
)

// version can be set by release builds using -ldflags.
var version = "dev"

func main() {
	home, err := os.UserHomeDir()
	if err != nil || processaudit.Enable(filepath.Join(home, ".NexusRouter", "data", "process-audit")) != nil {
		fmt.Fprintln(os.Stderr, "NexusRouter: subprocess audit initialization failed")
		os.Exit(1)
	}
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr, version))
}
