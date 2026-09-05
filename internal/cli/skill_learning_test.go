package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/skills"
	"go.yaml.in/yaml/v3"
)

func TestSkillLearningStatusIsReadOnlyAndDisabledSafe(t *testing.T) {
	cfg := config.Defaults()
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "state.db")
	cfg.Skills.Root, cfg.Skills.Scope = cliSkillPath(t), "project"
	cfg.Skills.Learning.Name = "learner"
	cfg.Skills.Enabled, cfg.Skills.AutoDraft = false, false
	body, _ := yaml.Marshal(cfg)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	invoke := func() (int, []byte) {
		var out, diagnostics bytes.Buffer
		code := runSkills([]string{"learning", "status", "--config", path}, nil, &out, &diagnostics)
		return code, out.Bytes()
	}
	if code, _ := invoke(); code != 1 {
		t.Fatal("missing state should not be invented", code)
	}
	if _, err := os.Stat(cfg.Telemetry.Database); !os.IsNotExist(err) {
		t.Fatal("inspection created storage", err)
	}
	db, err := telemetry.Open(context.Background(), cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	state := skills.LearningState{Version: 1, Scope: "project", Name: "learner", Domain: "code", PolicyDigest: strings.Repeat("a", 64), Revision: 1, Phase: "discover"}
	if err := db.PutLearningState(context.Background(), state, 0); err != nil {
		t.Fatal(err)
	}
	db.Close()
	code, output := invoke()
	var got skills.LearningState
	if code != 0 || json.Unmarshal(output, &got) != nil || got != state {
		t.Fatal("disabled inspection failed", code, string(output))
	}
	if _, err := os.Stat(cfg.Skills.Root); !os.IsNotExist(err) {
		t.Fatal("inspection opened skill catalog", err)
	}
	t.Setenv("DARWIN_API_TOKEN", "learner")
	if code, output := invoke(); code != 1 || len(output) != 0 {
		t.Fatal("current credential in metadata leaked", code, string(output))
	}
}

func TestSkillLearningStatusRejectsUnexpectedFlags(t *testing.T) {
	for _, args := range [][]string{{}, {"run"}, {"status"}, {"status", "--config", ""}, {"status", "--config", "a", "--config", "b"}, {"status", "--scope=other"}} {
		var out, diagnostics bytes.Buffer
		if code := runSkillLearning(args, &out, &diagnostics); code != 2 || out.Len() != 0 {
			t.Fatal("unexpected status authority", args, code)
		}
	}
}
