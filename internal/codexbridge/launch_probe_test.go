package codexbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/codexrpc"
)

// Explicit diagnostic only. Default CI never starts Codex. This probe makes no
// thread/turn or model request; startup can still refresh auth or write Codex
// state. It never reads credential files or prints raw protocol/configuration.
func TestLiveCodexLaunchConfigProbe(t *testing.T) {
	if os.Getenv("DARWIN_CODEX_LIVE_PROBE") != "1" {
		t.Skip("explicit no-inference probe only")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	bin, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal("Codex executable unavailable")
	}
	version, err := exec.CommandContext(ctx, bin, "--version").Output()
	if err != nil || strings.TrimSpace(string(version)) != "codex-cli 0.153.4" {
		t.Fatal("unsupported probe CLI version")
	}
	inventory, err := exec.CommandContext(ctx, bin, "features", "list").Output()
	if err != nil || len(inventory) > 64<<10 {
		t.Fatal("feature inventory unavailable")
	}
	features := []string{}
	overrides := []string{}
	for _, line := range strings.Split(strings.TrimSpace(string(inventory)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || !namePattern.MatchString(fields[0]) {
			t.Fatal("invalid feature metadata")
		}
		name := fields[0]
		features = append(features, name)
		value := "false"
		if name == "skip_host_skill_discovery" {
			value = "true"
		}
		overrides = append(overrides, name+"="+value)
	}
	args := []string{"app-server", "--strict-config", "--listen", "stdio://", "-c", "features={" + strings.Join(overrides, ",") + "}"}
	// This CLI version exposes view_image as a feature; strict config rejects
	// the legacy tools.view_image key still present in the public reference.
	for _, override := range []string{`mcp_servers={}`, `plugins={}`, `project_doc_max_bytes=0`, `notify=[]`, `web_search="disabled"`} {
		args = append(args, "-c", override)
	}
	env := []string{}
	for _, name := range []string{"HOME", "PATH", "TMPDIR", "CODEX_HOME"} {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	// These are the existing host locations, not replacement credential roots.
	p, err := codexrpc.StartProcess(ctx, codexrpc.ProcessSpec{Executable: bin, Args: args, Env: env, Dir: t.TempDir()})
	if err != nil {
		t.Fatal("probe process unavailable")
	}
	defer p.Close()
	notifications := 0
	request := func(id, method string, params json.RawMessage) json.RawMessage {
		t.Helper()
		if p.Write(codexrpc.Envelope{ID: json.RawMessage(id), Method: method, Params: params}) != nil {
			t.Fatal("probe request failed")
		}
		for range 32 {
			e, err := p.Read()
			if err != nil {
				t.Fatal("probe response unavailable")
			}
			kind, _ := e.Kind()
			if kind == codexrpc.Response && bytes.Equal(e.ID, []byte(id)) {
				return e.Result
			}
			if kind == codexrpc.Notification {
				notifications++
				continue
			}
			// Do not answer requests, print errors, or create threads to make a
			// failed probe pass. Unexpected traffic is a diagnostic failure.
			t.Fatal("unexpected probe response")
		}
		t.Fatal("probe response frame limit exceeded")
		return nil
	}
	request("1", "initialize", json.RawMessage(`{"clientInfo":{"name":"darwin_router_probe","version":"0.1.0"},"capabilities":{"experimentalApi":true}}`))
	if p.Write(codexrpc.Envelope{Method: "initialized"}) != nil {
		t.Fatal("probe initialization failed")
	}
	result := request("2", "config/read", json.RawMessage(`{"includeLayers":false}`))
	report, err := InspectLaunchConfig(result, features)
	if err != nil {
		t.Fatal("configuration could not be safely projected")
	}
	t.Logf("configuration observation only (not launch approval): %+v", report)
	t.Logf("bounded unsolicited notifications: %d", notifications)
	// Only names already supplied by the pinned CLI inventory may be logged.
	// Configuration keys and values themselves are never diagnostic output.
	var observed struct {
		Config struct {
			Features map[string]json.RawMessage `json:"features"`
		} `json:"config"`
	}
	if decodePayload(result, &observed) != nil {
		t.Fatal("configuration feature metadata unavailable")
	}
	for _, name := range features {
		if _, ok := observed.Config.Features[name]; !ok {
			t.Logf("inventory feature absent from configuration observation: %s", name)
		}
	}
}
