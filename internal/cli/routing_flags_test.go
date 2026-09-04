package cli

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"darwinrouter/internal/app"
)

func TestRunRoutingFlagsBuildApplicationRequest(t *testing.T) {
	options, got, err := parseRunArgs([]string{
		"--config", "project.yaml", "--user-config", "user.yaml", "--model", "auto",
		"--continue-task", "previous", "--domain", "code", "--profile", "careful",
		"--capability", "chat", "--capability", "tool-use", "--context-tokens", "8192",
		"--max-cost", "0.25", "--local-required", "--set", "mode=local_only",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := app.Request{ModelID: "auto", ContinueTaskID: "previous", Domain: "code", Profile: "careful", Capabilities: []string{"chat", "tool-use"}, ContextTokens: 8192, MaxCost: .25, LocalRequired: true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("request = %#v, want %#v", got, want)
	}
	if options.ProjectFile != "project.yaml" || options.UserFile != "user.yaml" || options.Flags["mode"] != "local_only" {
		t.Fatalf("configuration options = %#v", options)
	}
}

func TestRunRoutingDefaultsAndExplicitModel(t *testing.T) {
	for _, model := range []string{"auto", "configured-model"} {
		_, got, err := parseRunArgs([]string{"--config", "project.yaml", "--model", model})
		if err != nil || !reflect.DeepEqual(got, app.Request{ModelID: model}) {
			t.Fatalf("default request = %#v, error = %v", got, err)
		}
	}
	_, got, err := parseRunArgs([]string{"--config", "project.yaml", "--model", "configured-model", "--max-cost=0", "--context-tokens=0", "--local-required=false"})
	if err != nil || got.MaxCost != 0 || got.ContextTokens != 0 || got.LocalRequired {
		t.Fatalf("explicit zero defaults = %#v, error = %v", got, err)
	}
}

type routingUnreadablePrompt struct{ read bool }

func (r *routingUnreadablePrompt) Read([]byte) (int, error) {
	r.read = true
	panic("invalid flags must be rejected before reading input")
}

func TestRunInvalidRoutingFlagsFailBeforeConfigurationAndInput(t *testing.T) {
	cases := [][]string{
		{"--context-tokens=-1"}, {"--context-tokens=1.5"}, {"--context-tokens=abc"},
		{"--context-tokens=999999999999999999999999999999"},
		{"--max-cost=-0.01"}, {"--max-cost=NaN"}, {"--max-cost=Inf"}, {"--max-cost=-Inf"},
		{"--max-cost=1e999"}, {"--max-cost=no"}, {"--local-required=maybe"},
		{"--capability="}, {"--capability=chat tools"}, {"--capability=chat,tools"},
		{"--capability=chat", "--capability=bad/name"}, {"--capability=" + strings.Repeat("c", 129)},
		{"--capability=chat", "--capability=chat"},
		{"--domain="}, {"--domain= "}, {"--domain= code"}, {"--domain=code\n"},
		{"--domain=" + strings.Repeat("d", 129)},
		{"--profile="}, {"--profile= "}, {"--profile=default "}, {"--profile=default\t"},
		{"--profile=" + strings.Repeat("p", 129)}, {"--capability"}, {"--max-cost"},
	}
	for _, flags := range cases {
		t.Run(strings.Join(flags, " "), func(t *testing.T) {
			// A missing config would return 1 if argument validation were late.
			args := append([]string{"--config", "does-not-exist.yaml", "--model", "auto"}, flags...)
			var stdout, stderr bytes.Buffer
			stdin := &routingUnreadablePrompt{}
			if code := runTask(args, stdin, &stdout, &stderr); code != 2 || stdin.read || stdout.Len() != 0 {
				t.Fatalf("code=%d input read=%v output=%q error=%q", code, stdin.read, stdout.String(), stderr.String())
			}
		})
	}
}
