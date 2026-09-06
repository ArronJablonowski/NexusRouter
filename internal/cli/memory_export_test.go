package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/memory"
)

type memoryExportUnreadInput struct{}

func (memoryExportUnreadInput) Read([]byte) (int, error) { panic("export must not read stdin") }

func seedMemoryExport(t *testing.T, path string) []memory.Fact {
	t.Helper()
	db, err := telemetry.OpenMemoryControl(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	first := configuredMemoryFact("a-expired")
	first.Expires = first.Created.Add(time.Minute)
	first.Content = "expired private synthetic-export-secret"
	second := configuredMemoryFact("b-shareable")
	second.Privacy = "shareable"
	other := configuredMemoryFact("other-fact")
	other.Scope = "other-scope"
	for _, f := range []memory.Fact{first, second, other} {
		if err := db.PutMemory(context.Background(), f, 0); err != nil {
			t.Fatal(err)
		}
	}
	return []memory.Fact{first, second}
}

func TestConfiguredMemoryExportCompleteReadOnlyAndCompact(t *testing.T) {
	t.Setenv("DARWIN_API_TOKEN", "synthetic-export-secret")
	file, path := configuredMemoryFixture(t, true)
	expected := seedMemoryExport(t, path)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out, errout bytes.Buffer
	code := runMemory([]string{"export", "--config", file}, memoryExportUnreadInput{}, &out, &errout)
	if code != 0 || errout.Len() != 0 {
		t.Fatal(code, errout.String())
	}
	var snapshot memory.ExportSnapshot
	if json.Unmarshal(out.Bytes(), &snapshot) != nil || snapshot.Validate() != nil || snapshot.Scope != "configured-project" || len(snapshot.Facts) != 2 {
		t.Fatal("invalid export")
	}
	expected[0].Content = "expired private [REDACTED]"
	if !reflect.DeepEqual(snapshot.Facts, expected) {
		t.Fatal("export omitted private/expired facts or crossed scope")
	}
	if strings.Contains(out.String(), "synthetic-export-secret") || bytes.Count(out.Bytes(), []byte{'\n'}) != 1 {
		t.Fatal("unredacted or noncompact export")
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil || !bytes.Equal(out.Bytes(), append(encoded, '\n')) {
		t.Fatal("not compact complete envelope", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("export mutated catalog", err)
	}
	if stored := configuredStoredFact(t, path, "a-expired"); stored.Content == expected[0].Content || !stored.LastUse.IsZero() {
		t.Fatal("export changed stored content/use")
	}
}

func TestConfiguredMemoryExportRejectsFlagsWithoutCreation(t *testing.T) {
	file, path := configuredMemoryFixture(t, false)
	for _, flags := range [][]string{{"--db", path}, {"--db", ""}, {"--scope", ""}, {"--id", ""}, {"--expected", "0"}, {"--limit", "100"}, {"--after", ""}, {"--contains", ""}, {"--include-expired=false"}, {"--config", file}} {
		args := append([]string{"export", "--config", file}, flags...)
		var out, errout bytes.Buffer
		if code := runMemory(args, memoryExportUnreadInput{}, &out, &errout); code != 2 || out.Len() != 0 {
			t.Fatal("invalid export flag accepted", flags, code)
		}
	}
	for _, args := range [][]string{{"export", "--db", path, "--scope", "configured-project"}, {"export"}, {"export", "--config", ""}} {
		var out, errout bytes.Buffer
		if code := runMemory(args, memoryExportUnreadInput{}, &out, &errout); code != 2 || out.Len() != 0 {
			t.Fatal("raw/unconfigured export accepted", code)
		}
	}
	var out, errout bytes.Buffer
	if code := runMemory([]string{"export", "--config", file}, memoryExportUnreadInput{}, &out, &errout); code != 1 || out.Len() != 0 {
		t.Fatal("missing storage export", code)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("export created storage", err)
	}
}

func TestConfiguredMemoryExportCorruptionNoPartialOutput(t *testing.T) {
	file, path := configuredMemoryFixture(t, true)
	seedMemoryExport(t, path)
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`UPDATE memory_facts SET body=? WHERE id=?`, []byte(`{"secret":"private-corruption"}`), "b-shareable"); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	raw.Close()
	var out, errout bytes.Buffer
	if code := runMemory([]string{"export", "--config", file}, memoryExportUnreadInput{}, &out, &errout); code != 1 || out.Len() != 0 || strings.Contains(errout.String(), "private-corruption") {
		t.Fatal("partial corrupted export", code)
	}
}

type memoryExportShortWriter struct{}

func (memoryExportShortWriter) Write(body []byte) (int, error) { return len(body) - 1, nil }

func TestMemoryExportOutputBoundary(t *testing.T) {
	var out bytes.Buffer
	failure := func() int { return 1 }
	// String JSON adds two quotes, so this is the exact envelope byte bound.
	value := strings.Repeat("x", memory.ExportMaxBytes-2)
	if code := writeMemoryExport(&out, value, failure); code != 0 || out.Len() != memory.ExportMaxBytes+1 {
		t.Fatal("exact output boundary", code, out.Len())
	}
	out.Reset()
	if code := writeMemoryExport(&out, value+"x", failure); code != 1 || out.Len() != 0 {
		t.Fatal("oversized output published")
	}
	if code := writeMemoryExport(memoryExportShortWriter{}, map[string]int{"version": 1}, failure); code != 1 {
		t.Fatal("short write accepted")
	}
	if code := writeMemoryExport(configuredMemoryBrokenWriter{}, map[string]int{"version": 1}, failure); code != 1 {
		t.Fatal("broken writer accepted")
	}
}

func TestConfiguredMemoryExportOwnedProcess(t *testing.T) {
	file, path := configuredMemoryFixture(t, true)
	seedMemoryExport(t, path)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestConfiguredMemoryProcessChild$", "--", "memory", "export", "--config", file)
	cmd.Env = []string{"DARWIN_TEST_MEMORY_PROCESS=1", "DARWIN_API_TOKEN=synthetic-export-secret", "HOME=" + t.TempDir()}
	cmd.Stdin = io.LimitReader(strings.NewReader("must be ignored"), 0)
	var out, errout bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errout
	if err := cmd.Run(); err != nil || errout.Len() != 0 {
		t.Fatal("process export failed", err, errout.String())
	}
	var snapshot memory.ExportSnapshot
	if json.Unmarshal(out.Bytes(), &snapshot) != nil || snapshot.Validate() != nil || len(snapshot.Facts) != 2 || strings.Contains(out.String(), "synthetic-export-secret") {
		t.Fatal("process export invalid")
	}
}
