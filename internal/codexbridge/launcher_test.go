package codexbridge

import (
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/codexrpc"
)

func launcherSpec(t *testing.T) LaunchSpec {
	return LaunchSpec{Executable: "/fixture/codex", CWD: t.TempDir(), Model: "gpt-5.6-sol", Mode: "hybrid", Privacy: "cloud_allowed", Env: []string{"HOME=/fixture/home"}}
}

func launcherMetadata(_ context.Context, _ codexrpc.ProcessSpec, args ...string) ([]byte, error) {
	if reflect.DeepEqual(args, []string{"--version"}) {
		return []byte("codex-cli 0.153.4"), nil
	}
	if reflect.DeepEqual(args, []string{"features", "list"}) {
		return []byte(profileInventory()), nil
	}
	return nil, ErrLaunchObservation
}

func launcherFrames(cwd string) ([]codexrpc.Envelope, []codexrpc.Envelope) {
	features, _, _ := launchProfile([]byte("codex-cli 0.153.4"), []byte(profileInventory()))
	flags := map[string]bool{}
	for _, f := range features {
		flags[f] = f == "skip_host_skill_discovery" || f == "code_mode_host"
	}
	config := marshal(map[string]any{"config": map[string]any{"features": flags, "mcp_servers": map[string]any{}, "plugins": map[string]any{}, "project_doc_max_bytes": 0, "notify": []any{}, "web_search": "disabled"}})
	checks := checkedResponses(cwd, 10)
	checks[0].Result = config
	discovery := []codexrpc.Envelope{sessionResponse("1", `{"userAgent":"fixture"}`), {ID: marshal(10), Result: config}, {ID: marshal(11), Result: checks[2].Result}}
	final := append([]codexrpc.Envelope{sessionResponse("1", `{"userAgent":"fixture"}`)}, checks...)
	return discovery, final
}

func TestLauncherOwnsDiscoveryAndTaskLifetime(t *testing.T) {
	spec := launcherSpec(t)
	firstFrames, finalFrames := launcherFrames(spec.CWD)
	wires := []*scriptedSessionWire{{frames: firstFrames, closed: make(chan struct{})}, {frames: finalFrames, closed: make(chan struct{})}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	var finalContext context.Context
	start := func(c context.Context, p codexrpc.ProcessSpec) (Wire, error) {
		if calls == 1 {
			select {
			case <-wires[0].closed:
			default:
				t.Fatal("overlapping discovery process")
			}
			finalContext = c
			if !strings.Contains(strings.Join(p.Args, " "), "skills.config=[]") {
				t.Fatal("missing explicit skill overrides")
			}
		}
		w := wires[calls]
		calls++
		return w, nil
	}
	s, err := launchChecked(ctx, spec, start, launcherMetadata)
	if err != nil || calls != 2 {
		t.Fatal("launcher failed")
	}
	defer s.Close()
	if finalContext.Err() != nil {
		t.Fatal("setup deadline owns returned process")
	}
	for _, w := range wires {
		for _, e := range w.sent() {
			if e.Method == "thread/start" || e.Method == "turn/start" {
				t.Fatal("launch sent task data")
			}
		}
	}
	cancel()
	<-wires[1].closed
	if s.Close() != nil {
		t.Fatal("idempotent cleanup failed")
	}
}

func TestLauncherFailureClosesOwnedWires(t *testing.T) {
	for _, where := range []string{"discovery", "final_start", "final_check"} {
		t.Run(where, func(t *testing.T) {
			spec := launcherSpec(t)
			a, b := launcherFrames(spec.CWD)
			if where == "discovery" {
				a[1].Result = json.RawMessage(`{"config":{}}`)
			}
			if where == "final_check" {
				b[1].Result = json.RawMessage(`{"config":{}}`)
			}
			wires := []*scriptedSessionWire{{frames: a, closed: make(chan struct{})}, {frames: b, closed: make(chan struct{})}}
			calls := 0
			start := func(context.Context, codexrpc.ProcessSpec) (Wire, error) {
				if calls == 1 && where == "final_start" {
					return nil, io.EOF
				}
				w := wires[calls]
				calls++
				return w, nil
			}
			s, err := launchChecked(context.Background(), spec, start, launcherMetadata)
			if err != ErrLaunchObservation || s != nil {
				t.Fatal("failure returned a usable session")
			}
			for _, w := range wires[:calls] {
				select {
				case <-w.closed:
				default:
					t.Fatal("owned wire leaked")
				}
			}
		})
	}
}

func TestLauncherPrivacyDeniesBeforeAnyProcess(t *testing.T) {
	for _, which := range []string{"local_mode", "unknown_mode", "local_privacy", "unknown_privacy", "model", "environment", "nil_environment", "cancelled"} {
		t.Run(which, func(t *testing.T) {
			spec := launcherSpec(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch which {
			case "local_mode":
				spec.Mode = "local_only"
			case "unknown_mode":
				spec.Mode = ""
			case "local_privacy":
				spec.Privacy = "local_only"
			case "unknown_privacy":
				spec.Privacy = ""
			case "model":
				spec.Model = "other"
			case "environment":
				spec.Env = append(spec.Env, "OPENAI_API_KEY=fixture-secret")
			case "nil_environment":
				spec.Env = nil
			case "cancelled":
				cancel()
			}
			start := func(context.Context, codexrpc.ProcessSpec) (Wire, error) {
				t.Fatal("denial started process")
				return nil, nil
			}
			metadata := func(context.Context, codexrpc.ProcessSpec, ...string) ([]byte, error) {
				t.Fatal("denial started metadata process")
				return nil, nil
			}
			if s, err := launchChecked(ctx, spec, start, metadata); s != nil || err != ErrLaunchObservation {
				t.Fatal("invalid launch admitted")
			}
		})
	}
}

func TestLaunchMetadataCaptureBound(t *testing.T) {
	b := &launchMetadataBuffer{}
	if _, err := io.Copy(b, strings.NewReader(strings.Repeat("x", 64<<10))); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(b, strings.NewReader("x")); err == nil || b.buf.Len() != 64<<10 {
		t.Fatal("unbounded metadata capture")
	}
}
