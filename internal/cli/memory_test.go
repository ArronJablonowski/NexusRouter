package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/memory"
)

func TestMemoryCLI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	now := time.Now().UTC()
	f := memory.Fact{Version: 1, ID: "preference", Scope: "project", Revision: 1, Content: "Use Go", Provenance: "operator", Confidence: 1, Privacy: "local_only", Created: now, Updated: now}
	invoke := func(action string, input string, extra ...string) (int, string) {
		t.Helper()
		args := []string{action, "--db", path, "--scope", "project"}
		args = append(args, extra...)
		var out, errout bytes.Buffer
		code := runMemory(args, strings.NewReader(input), &out, &errout)
		return code, out.String()
	}
	body, _ := json.Marshal(f)
	if code, _ := invoke("put", string(body)); code != 0 {
		t.Fatal("put failed")
	}
	if code, out := invoke("show", "", "--id", f.ID); code != 0 || !strings.Contains(out, "Use Go") {
		t.Fatal(code, out)
	}
	f.Revision = 2
	f.Updated = now.Add(time.Second)
	f.Content = "Use Go and SQLite"
	body, _ = json.Marshal(f)
	if code, _ := invoke("put", string(body), "--expected", "1"); code != 0 {
		t.Fatal("correction failed")
	}
	if code, _ := invoke("delete", "", "--id", f.ID, "--expected", "1"); code != 1 {
		t.Fatal("stale delete succeeded")
	}
	if code, _ := invoke("delete", "", "--id", f.ID, "--expected", "2"); code != 0 {
		t.Fatal("delete failed")
	}
	if code, out := invoke("list", ""); code != 0 || strings.TrimSpace(out) != "[]" {
		t.Fatal(code, out)
	}
}
func TestMemoryReadDoesNotCreateAndScopeCannotChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")
	var out, errout bytes.Buffer
	if code := runMemory([]string{"list", "--db", path, "--scope", "s"}, strings.NewReader(""), &out, &errout); code != 1 {
		t.Fatal(code)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("created database on read")
	}
	now := time.Now().UTC()
	fact := memory.Fact{Version: 1, ID: "fact", Scope: "other", Revision: 1, Content: "private", Provenance: "user", Confidence: 1, Privacy: "local_only", Created: now, Updated: now}
	body, _ := json.Marshal(fact)
	if code := runMemory([]string{"put", "--db", path, "--scope", "s"}, bytes.NewReader(body), &out, &errout); code != 1 {
		t.Fatal("scope mismatch accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("invalid put created database")
	}
}
