package remotecli

import (
	"context"
	"encoding/json"
	"flag"
	"github.com/ArronJablonowski/NexusRouter/remote"
	"io"
	"time"
)

func discoverUnpaired(ctx context.Context, args []string, output, diagnostic io.Writer) error {
	flags := flag.NewFlagSet("discover", flag.ContinueOnError)
	flags.SetOutput(diagnostic)
	iface := flags.String("interface", "", "explicit IPv4 multicast interface")
	wait := flags.Duration("wait", 0, "explicit browse duration, maximum ten seconds")
	if flags.Parse(args) != nil || flags.NArg() != 0 || *iface == "" || *wait <= 0 || *wait > 10*time.Second {
		return remote.ErrInvalid
	}
	candidates, err := remote.DiscoverUnpaired(ctx, *iface, *wait)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(struct {
		Version    int                         `json:"version"`
		Verified   bool                        `json:"verified"`
		Candidates []remote.DiscoveryCandidate `json:"candidates"`
	}{1, false, candidates})
}
