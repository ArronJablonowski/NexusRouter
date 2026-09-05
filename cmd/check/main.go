// Command check runs read-only source formatting and size gates.
package main

import (
	"fmt"
	"os"

	"github.com/ArronJablonowski/DarwinRouter/internal/quality"
)

func main() {
	findings, err := quality.Check(".")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, finding := range findings {
		fmt.Fprintln(os.Stderr, finding)
	}
	if len(findings) != 0 {
		os.Exit(1)
	}
	fmt.Println("Source formatting and 1,000-line limit: passed")
}
