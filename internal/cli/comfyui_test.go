package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestComfyCLIReceiptEndpointAndDownload(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/prompt":
			w.Write([]byte(`{"prompt_id":"job-1","number":0}`))
		case "/history/job-1":
			w.Write([]byte(`{"job-1":{"status":{"status_str":"success","completed":true},"outputs":{"2":{"images":[{"filename":"test.png","subfolder":"","type":"output"}]}}}}`))
		case "/view":
			w.Write([]byte("image-data"))
		default:
			t.Error("unexpected route")
		}
	}))
	defer s.Close()
	dir := t.TempDir()
	workflow := filepath.Join(dir, "w.json")
	receipt := filepath.Join(dir, "r.json")
	output := filepath.Join(dir, "o.png")
	os.WriteFile(workflow, []byte(`{"1":{"class_type":"EmptyImage","inputs":{}}}`), 0600)
	run := func(args ...string) int {
		var out, err bytes.Buffer
		return RunWithInput(append([]string{"comfy"}, args...), strings.NewReader(""), &out, &err, "test")
	}
	if run("submit", "--endpoint", s.URL, "--workflow", workflow, "--receipt", receipt) != 0 {
		t.Fatal("submit")
	}
	if run("status", "--receipt", receipt) != 0 {
		t.Fatal("receipt status")
	}
	if run("status", "--receipt", receipt, "--endpoint", "http://127.0.0.1:1") == 0 {
		t.Fatal("receipt endpoint mismatch")
	}
	if run("download", "--receipt", receipt, "--node", "2", "--output", output) != 0 {
		t.Fatal("download")
	}
	if run("download", "--receipt", receipt, "--node", "2", "--output", output) == 0 {
		t.Fatal("overwrote output")
	}
	if run("submit", "--endpoint", s.URL, "--workflow", workflow, "--receipt", receipt) == 0 {
		t.Fatal("duplicate submit")
	}
}
