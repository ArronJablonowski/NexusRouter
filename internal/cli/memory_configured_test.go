package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/memory"
	"go.yaml.in/yaml/v3"
)

func configuredMemoryFixture(t *testing.T, initialize bool) (string, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "memory.db")
	file := filepath.Join(dir, "config.yaml")
	cfg := config.Defaults()
	cfg.Telemetry.Database = path
	cfg.Memory.Scope = "configured-project"
	cfg.Memory.Enabled = false
	body, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(file, body, 0600); err != nil {
		t.Fatal(err)
	}
	if initialize {
		db, err := telemetry.Open(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		if err = db.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return file, path
}

func configuredMemoryFact(id string) memory.Fact {
	now := time.Now().UTC().Add(-time.Hour)
	return memory.Fact{Version: 1, ID: id, Scope: "configured-project", Revision: 1, Content: "Use Go", Provenance: "operator", Confidence: 1, Privacy: "local_only", Created: now, Updated: now}
}

func invokeConfiguredMemory(t *testing.T, file, action string, input any, flags ...string) (int, string, string) {
	t.Helper()
	var body []byte
	if raw, ok := input.([]byte); ok {
		body = raw
	} else if input != nil {
		var err error
		body, err = json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
	}
	args := append([]string{action, "--config", file}, flags...)
	var out, errout bytes.Buffer
	code := runMemory(args, bytes.NewReader(body), &out, &errout)
	return code, out.String(), errout.String()
}

func configuredStoredFact(t *testing.T, path, id string) memory.Fact {
	t.Helper()
	db, err := telemetry.OpenReadOnly(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	f, err := db.GetMemory(context.Background(), "configured-project", id)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestConfiguredMemoryCLIRedactionCorrectionsAndRetirement(t *testing.T) {
	const token = "configured-cli-synthetic-token"
	t.Setenv("DARWIN_API_TOKEN", token)
	file, path := configuredMemoryFixture(t, true)
	f := configuredMemoryFact("preference")
	f.Content = "Prefer Go " + token
	f.Provenance = "operator " + token
	if code, _, errout := invokeConfiguredMemory(t, file, "put", f, "--expected", "0"); code != 0 {
		t.Fatal(code, errout)
	}
	stored := configuredStoredFact(t, path, f.ID)
	if strings.Contains(stored.Content+stored.Provenance, token) || !strings.Contains(stored.Content, "[REDACTED]") {
		t.Fatal("secret entered memory", stored)
	}
	for _, action := range []string{"show", "list"} {
		flags := []string{}
		if action == "show" {
			flags = []string{"--id", f.ID}
		}
		if code, out, errout := invokeConfiguredMemory(t, file, action, nil, flags...); code != 0 || strings.Contains(out+errout, token) || !strings.Contains(out, "[REDACTED]") {
			t.Fatal(code, out, errout)
		}
	}
	if code, out, errout := invokeConfiguredMemory(t, file, "list", nil, "--contains", token); code != 1 || strings.Contains(out+errout, token) {
		t.Fatal("secret filter accepted or disclosed", code, out, errout)
	}
	f.Revision = 2
	f.Updated = f.Updated.Add(time.Minute)
	f.Content = "Use Go and SQLite"
	f.Provenance = "operator correction"
	f.Privacy = "shareable"
	if code, _, _ := invokeConfiguredMemory(t, file, "put", f, "--expected", "1"); code != 1 {
		t.Fatal("privacy downgrade accepted", code)
	}
	f.Privacy = "local_only"
	if code, _, errout := invokeConfiguredMemory(t, file, "put", f, "--expected", "1"); code != 0 {
		t.Fatal(code, errout)
	}
	if got := configuredStoredFact(t, path, f.ID); got.Revision != 2 || got.Privacy != "local_only" || got.Content != f.Content {
		t.Fatal(got)
	}
	if code, _, _ := invokeConfiguredMemory(t, file, "delete", nil, "--id", f.ID, "--expected", "1"); code != 1 {
		t.Fatal("stale delete accepted")
	}
	if code, _, errout := invokeConfiguredMemory(t, file, "delete", nil, "--id", f.ID, "--expected", "2"); code != 0 {
		t.Fatal(code, errout)
	}
	f.Revision = 1
	if code, _, _ := invokeConfiguredMemory(t, file, "put", f, "--expected", "0"); code != 1 {
		t.Fatal("retired identifier reused")
	}
	if code, out, errout := invokeConfiguredMemory(t, file, "list", nil); code != 0 || strings.TrimSpace(out) != "[]" {
		t.Fatal(code, out, errout)
	}
}

func TestConfiguredMemoryCLIExportRedactsLegacyRowsAndPages(t *testing.T) {
	const token = "legacy-cli-synthetic-token"
	t.Setenv("DARWIN_API_TOKEN", token)
	file, path := configuredMemoryFixture(t, true)
	db, err := telemetry.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a-current", "b-expired", "c-current"} {
		f := configuredMemoryFact(id)
		f.Content = "legacy " + token
		f.Provenance = "import " + token
		if id == "b-expired" {
			f.Expires = f.Created.Add(time.Minute)
		}
		if err = db.PutMemory(context.Background(), f, 0); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	code, out, errout := invokeConfiguredMemory(t, file, "list", nil, "--limit", "1")
	var first []memory.Fact
	if code != 0 || json.Unmarshal([]byte(out), &first) != nil || len(first) != 1 || first[0].ID != "a-current" || strings.Contains(out+errout, token) {
		t.Fatal(code, out, errout)
	}
	code, out, errout = invokeConfiguredMemory(t, file, "list", nil, "--after", "a-current", "--limit", "1")
	var next []memory.Fact
	if code != 0 || json.Unmarshal([]byte(out), &next) != nil || len(next) != 1 || next[0].ID != "c-current" {
		t.Fatal(code, out, errout)
	}
	code, out, errout = invokeConfiguredMemory(t, file, "list", nil, "--after", "a-current", "--limit", "1", "--include-expired")
	if code != 0 || json.Unmarshal([]byte(out), &next) != nil || len(next) != 1 || next[0].ID != "b-expired" || strings.Contains(out+errout, token) {
		t.Fatal(code, out, errout)
	}
	if code, out, errout = invokeConfiguredMemory(t, file, "show", nil, "--id", "b-expired"); code != 0 || strings.Contains(out+errout, token) || !strings.Contains(out, "[REDACTED]") {
		t.Fatal(code, out, errout)
	}
	if got := configuredStoredFact(t, path, "b-expired"); !strings.Contains(got.Content, token) {
		t.Fatal("inspection rewrote durable imported row")
	}
}

func TestConfiguredMemoryCLIMissingDatabaseAndScopeMismatch(t *testing.T) {
	file, path := configuredMemoryFixture(t, false)
	for _, action := range []string{"list", "show", "put", "delete"} {
		flags := []string{}
		var input any
		switch action {
		case "show":
			flags = []string{"--id", "fact"}
		case "put":
			flags = []string{"--expected", "0"}
			input = configuredMemoryFact("fact")
		case "delete":
			flags = []string{"--id", "fact", "--expected", "1"}
		}
		code, out, errout := invokeConfiguredMemory(t, file, action, input, flags...)
		if code != 1 || out != "" || strings.Contains(errout, path) || strings.Contains(strings.ToLower(errout), "sqlite") {
			t.Fatal(action, code, out, errout)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("management created missing database", action, err)
		}
	}
	file, path = configuredMemoryFixture(t, true)
	f := configuredMemoryFact("mismatch")
	f.Scope = "other-project"
	if code, _, _ := invokeConfiguredMemory(t, file, "put", f, "--expected", "0"); code != 1 {
		t.Fatal("scope override accepted")
	}
	if code, out, errout := invokeConfiguredMemory(t, file, "list", nil); code != 0 || strings.TrimSpace(out) != "[]" {
		t.Fatal(code, out, errout)
	}
}

func TestConfiguredMemoryCLIRejectsAmbiguousFlags(t *testing.T) {
	file, _ := configuredMemoryFixture(t, true)
	for _, tc := range []struct {
		action string
		flags  []string
	}{
		{"list", []string{"--db", ""}}, {"list", []string{"--scope", ""}},
		{"list", []string{"--id", ""}}, {"list", []string{"--expected", "0"}},
		{"show", []string{"--id", "fact", "--limit", "100"}},
		{"show", []string{"--id", "fact", "--after", ""}},
		{"put", []string{"--expected", "0", "--contains", ""}},
		{"delete", []string{"--id", "fact", "--expected", "1", "--include-expired=false"}},
		{"put", nil}, {"delete", []string{"--id", "fact"}},
		{"list", []string{"--config", file}},
		{"put", []string{"--expected", "0", "--expected", "0"}},
		{"list", []string{"--limit=1", "--limit", "1"}},
	} {
		code, _, _ := invokeConfiguredMemory(t, file, tc.action, configuredMemoryFact("fact"), tc.flags...)
		if code != 2 {
			t.Fatal(tc, code)
		}
	}
	if code, _, _ := invokeConfiguredMemory(t, "", "list", nil); code != 2 {
		t.Fatal("empty config accepted", code)
	}
	// A literal filter value beginning with -- is not another parsed flag.
	if code, out, errout := invokeConfiguredMemory(t, file, "list", nil, "--contains", "--config"); code != 0 || strings.TrimSpace(out) != "[]" {
		t.Fatal("flag-looking string value misparsed", code, out, errout)
	}
}

func TestConfiguredMemoryCLIRejectsNoncanonicalJSONWithoutWrite(t *testing.T) {
	file, _ := configuredMemoryFixture(t, true)
	f := configuredMemoryFact("fact")
	body, _ := json.Marshal(f)
	valid := string(body)
	for _, input := range []string{
		strings.Replace(valid, `"version":1`, `"version":1,"version":1`, 1),
		strings.Replace(valid, `"version":1`, `"Version":1`, 1),
		strings.Replace(valid, `"version":1`, `"unknown":true,"version":1`, 1),
		strings.Replace(valid, `"confidence":1,`, "", 1),
		strings.Replace(valid, `"content":"Use Go"`, `"content":null`, 1),
		strings.Replace(valid, `"content":"Use Go"`, `"content":"\ud800"`, 1),
		strings.Replace(valid, `"content":"Use Go"`, `"content":"`+string([]byte{0xff})+`"`, 1),
		valid + ` {}`, strings.Repeat(" ", 128<<10) + valid,
	} {
		if code, _, _ := invokeConfiguredMemory(t, file, "put", []byte(input), "--expected", "0"); code != 1 {
			t.Fatal("noncanonical JSON accepted", code)
		}
	}
	if code, _, _ := invokeConfiguredMemory(t, file, "put", f, "--expected", "0", "--id", "different"); code != 1 {
		t.Fatal("explicit ID mismatch accepted", code)
	}
	if code, out, errout := invokeConfiguredMemory(t, file, "list", nil); code != 0 || strings.TrimSpace(out) != "[]" {
		t.Fatal(code, out, errout)
	}
}

type configuredMemoryBrokenWriter struct{}

func (configuredMemoryBrokenWriter) Write([]byte) (int, error) {
	return 0, errors.New("synthetic output failure")
}

func TestConfiguredMemoryCLIOutputFailureIsNotSuccessfulAcknowledgement(t *testing.T) {
	file, path := configuredMemoryFixture(t, true)
	f := configuredMemoryFact("fact")
	body, _ := json.Marshal(f)
	var errout bytes.Buffer
	if code := runMemory([]string{"put", "--config", file, "--expected", "0"}, bytes.NewReader(body), configuredMemoryBrokenWriter{}, &errout); code != 1 {
		t.Fatal(code)
	}
	if got := configuredStoredFact(t, path, "fact"); got.Revision != 1 {
		t.Fatal("successful mutation lost with output error", got)
	}
	if code := runMemory([]string{"list", "--config", file}, strings.NewReader(""), configuredMemoryBrokenWriter{}, io.Discard); code != 1 {
		t.Fatal(code)
	}
}
