// Command darwin is the deprecated compatibility entry point; use nexus.
package main

import (
	"os"

	"github.com/ArronJablonowski/NexusRouter/internal/cli"
)

// version can be set by release builds using -ldflags.
var version = "dev"

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr, version))
}
