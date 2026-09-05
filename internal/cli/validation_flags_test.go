package cli

import (
	"bytes"
	"testing"
)

func TestRunValidationFlag(t *testing.T) {
	for _, value := range []string{"", "go_source"} {
		_, req, err := parseRunArgs([]string{"--config", "project.yaml", "--model", "auto", "--validate=" + value})
		if err != nil || req.Validation != value {
			t.Fatalf("validation=%q error=%v, want %q", req.Validation, err, value)
		}
	}
	_, req, err := parseRunArgs([]string{"--config", "project.yaml", "--model", "auto"})
	if err != nil || req.Validation != "" {
		t.Fatalf("validation must be opt-in: request=%#v error=%v", req, err)
	}
}

func TestInvalidValidationFlagRejectedBeforeConfiguration(t *testing.T) {
	for _, value := range []string{"go", "Go_source", "go_source ", "null", "true"} {
		t.Run(value, func(t *testing.T) {
			var out, stderr bytes.Buffer
			code := runTask([]string{"--config", "does-not-exist.yaml", "--model", "auto", "--validate", value}, &routingUnreadablePrompt{}, &out, &stderr)
			if code != 2 {
				t.Fatalf("code=%d error=%q", code, stderr.String())
			}
		})
	}
}
