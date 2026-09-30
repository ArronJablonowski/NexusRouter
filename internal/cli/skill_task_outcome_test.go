package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/skills"
	"go.yaml.in/yaml/v3"
)

func TestSkillTaskOutcomeCLIReadOnly(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.Telemetry.Database = filepath.Join(dir, "task.db")
	cfg.Skills.Scope = "project"
	cfg.Skills.Root = filepath.Join(dir, "not-created-skills")
	body, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.yaml")
	if err = os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"task", "skill-outcome", "--config", path, "--task", "task"}
	var out, diagnostic bytes.Buffer
	if Run(args, &out, &diagnostic, "dev") != 1 || out.Len() != 0 {
		t.Fatal("missing task accepted")
	}
	if _, err = os.Stat(cfg.Telemetry.Database); !os.IsNotExist(err) {
		t.Fatal("inspection created storage")
	}
	db, err := telemetry.Open(context.Background(), cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	for i, kind := range []runtime.Kind{runtime.TaskStarted, runtime.TaskCompleted} {
		e := runtime.Event{Version: 1, ID: string(kind), TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: int64(i + 1), Time: time.Now().UTC(), Kind: kind}
		if i == 0 {
			e.Data.Messages = []providers.Message{{Role: "user", Content: "private-task-body"}}
			e.Data.SkillContext = &runtime.SkillContextUse{Version: 1, Complete: true, References: []runtime.SkillReference{{Scope: "project", Name: "workflow", Version: strings.Repeat("a", 32), Digest: strings.Repeat("b", 64)}}}
		}
		if err = db.Append(context.Background(), int64(i), e); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	out.Reset()
	diagnostic.Reset()
	if code := Run(args, &out, &diagnostic, "dev"); code != 0 {
		t.Fatal(code, diagnostic.String())
	}
	var got skills.TaskOutcome
	if json.Unmarshal(out.Bytes(), &got) != nil || got.Validate() != nil || got.SkillContext == nil || len(got.SkillContext.References) != 1 || got.Quality != nil || strings.Contains(out.String(), "private-") {
		t.Fatal(out.String())
	}
	if Run(args, brokenWriter{}, &diagnostic, "dev") != 1 {
		t.Fatal("ignored writer failure")
	}
	for _, flags := range [][]string{{"--config", path}, {"--config", path, "--task", "bad:id"}, {"--config", path, "--task", "task", "--task", "other"}, {"--db", cfg.Telemetry.Database, "--task", "task"}, {"--config", path, "--task", "task", "--extra", "private-value"}} {
		out.Reset()
		diagnostic.Reset()
		if Run(append([]string{"task", "skill-outcome"}, flags...), &out, &diagnostic, "dev") != 2 || out.Len() != 0 || strings.Contains(diagnostic.String(), "private-value") {
			t.Fatal("invalid flags accepted")
		}
	}
}
