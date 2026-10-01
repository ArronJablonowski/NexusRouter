package remotecli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/remote"
)

func TestLocalAuditNeedsNoCredentialsOrRuntime(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "journal")
	j, err := remote.OpenJournal(dir, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	err = Run(context.Background(), []string{"audit", "--journal", dir, "--instance", "node-a"}, nil, &out, &stderr)
	if err != nil {
		t.Fatal(err, stderr.String())
	}
	var page remote.AuditPage
	if err = json.Unmarshal(out.Bytes(), &page); err != nil || page.Instance != "node-a" || page.Entries == nil {
		t.Fatal(page, err)
	}
	for _, extra := range [][]string{{"--through", "1"}, {"--after", "1"}, {"extra"}} {
		out.Reset()
		args := append([]string{"audit", "--journal", dir, "--instance", "node-a"}, extra...)
		if err = Run(context.Background(), args, nil, &out, &stderr); err == nil || out.Len() != 0 {
			t.Fatal("invalid scan produced output", err)
		}
	}
}
