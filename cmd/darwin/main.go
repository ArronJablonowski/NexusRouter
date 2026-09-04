// Command darwin is the DarwinRouter command-line entry point.
package main

import (
	"os"

	"darwinrouter/internal/cli"
)

// version can be set by release builds using -ldflags.
var version = "dev"

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr, version))
}
