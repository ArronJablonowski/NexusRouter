package remotecli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/remote"
)

func TestMembershipCLIRequiresExplicitValidatedScope(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "peers.json")
	peer := remote.Peer{ID: "node-a", Endpoint: "https://127.0.0.1:8443", ServerName: "node-a", Pins: []string{strings.Repeat("a", 64)}, Operations: []string{"info"}, Models: []string{"chat"}, MaxContextTokens: 8192}
	body, _ := json.Marshal(peer)
	invoke := func(args []string, input []byte, wantSuccess bool) string {
		t.Helper()
		var output, stderr bytes.Buffer
		err := Run(context.Background(), args, bytes.NewReader(input), &output, &stderr)
		if (err == nil) != wantSuccess {
			t.Fatal(args, err, output.String(), stderr.String())
		}
		return strings.TrimSpace(output.String())
	}
	args := []string{"pair", "--trust", path, "--expected", "absent"}
	for _, input := range [][]byte{[]byte(`{"unrecognized":true}`), append(append([]byte{}, body...), []byte(`{}`)...)} {
		invoke(args, input, false)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("invalid scope created trust", err)
		}
	}
	digest := invoke(args, body, true)
	var listed struct {
		Registry remote.Registry `json:"registry"`
		Digest   string          `json:"digest"`
	}
	if err := json.Unmarshal([]byte(invoke([]string{"peers", "--trust", path}, nil, true)), &listed); err != nil || listed.Digest != digest || len(listed.Registry.Peers) != 1 {
		t.Fatal(listed, err)
	}
	// Unknown fields on whole-registry replacement must not silently become a
	// different effective policy after decoding.
	invoke([]string{"replace-trust", "--trust", path, "--expected", digest}, []byte(`{"version":1,"peers":[],"unexpected":true}`), false)
	invoke([]string{"revoke", "--trust", path, "--instance", "node-a", "--expected", "absent"}, nil, false)
	invoke([]string{"revoke", "--trust", path, "--instance", "node-a", "--expected", digest}, nil, true)
	current, err := remote.TrustFile(path).Read()
	if err != nil || len(current.Peers) != 0 {
		t.Fatal(current, err)
	}
}
