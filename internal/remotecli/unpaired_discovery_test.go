package remotecli

import (
	"bytes"
	"context"
	"testing"
)

func TestUnpairedDiscoveryRequiresExplicitBoundedInterface(t *testing.T) {
	for _, args := range [][]string{{"discover"}, {"discover", "--interface", "en0"}, {"discover", "--interface", "en0", "--wait", "0s"}, {"discover", "--interface", "en0", "--wait", "11s"}, {"discover", "--trust", "private"}, {"discover", "--interface", "nexus-no-such-interface", "--wait", "1ms"}} {
		var output, diagnostic bytes.Buffer
		if err := Run(context.Background(), args, bytes.NewReader(nil), &output, &diagnostic); err == nil || output.Len() != 0 {
			t.Fatal(args, err, output.String())
		}
	}
}

func TestAdvertisementFlagsRequireExplicitServeConfiguration(t *testing.T) {
	for _, args := range [][]string{{"peers", "--advertise-interface", "en0", "--advertise-name", "node.local"}, {"serve", "--advertise-interface", "en0"}, {"serve", "--advertise-name", "node.local"}, {"serve", "--advertise-interface", "en0", "--advertise-name", "node.local", "--advertise-ssh-port", "65536"}} {
		var output, diagnostic bytes.Buffer
		if err := Run(context.Background(), args, bytes.NewReader(nil), &output, &diagnostic); err == nil || output.Len() != 0 {
			t.Fatal(args, err, output.String())
		}
	}
}
