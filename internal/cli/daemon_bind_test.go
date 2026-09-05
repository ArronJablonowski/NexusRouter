package cli

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServeDuplicateBindingCannotInitializeStorage(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	dir := t.TempDir()
	database, configuration := filepath.Join(dir, "absent.db"), filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configuration, []byte(fmt.Sprintf("daemon:\n  listen: %q\ntelemetry:\n  database: %q\n", l.Addr().String(), database)), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DARWIN_API_TOKEN", strings.Repeat("fixture", 8))
	if runServe([]string{"--config", configuration}, io.Discard, io.Discard) != 1 {
		t.Fatal("duplicate serve accepted")
	}
	if _, err := os.Stat(database); !os.IsNotExist(err) {
		t.Fatal("duplicate bind initialized storage")
	}
}
