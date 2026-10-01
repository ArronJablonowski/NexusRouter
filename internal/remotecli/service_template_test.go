package remotecli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestServiceTemplateDoesNotRequireOrCreateLiveFiles(t *testing.T) {
	args := []string{"service-template", "--platform", "launchd", "--executable", "/missing/nexus", "--working-directory", "/missing/runtime", "--owner-directory", "/missing/shared", "--instance", "node-a", "--listen", "192.168.1.20:8443", "--config", "/missing/config", "--journal", "/missing/journal", "--trust", "/missing/trust", "--cert", "/missing/cert", "--key", "/missing/key", "--ca", "/missing/ca"}
	var output, diagnostic bytes.Buffer
	if err := Run(context.Background(), args, bytes.NewReader(nil), &output, &diagnostic); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "ProgramArguments") || diagnostic.Len() != 0 {
		t.Fatal(output.String(), diagnostic.String())
	}
	output.Reset()
	args = append(args, "--advertise-interface", "en0")
	if err := Run(context.Background(), args, bytes.NewReader(nil), &output, &diagnostic); err == nil || output.Len() != 0 {
		t.Fatal("unknown service option accepted")
	}
}
