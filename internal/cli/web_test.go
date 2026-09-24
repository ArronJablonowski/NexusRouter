package cli

import (
	"bytes"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWebApprovalCLI(t *testing.T) {
	for _, prefix := range []string{"a", "-", "_"} {
		t.Run(prefix, func(t *testing.T) { testWebApprovalCLI(t, prefix+strings.Repeat("a", 23)) })
	}
}

func testWebApprovalCLI(t *testing.T, id string) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	token := strings.Repeat("t", 32)
	called := make(chan bool, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		valid := request.Method == http.MethodPost && request.URL.Path == "/v1/browser-session/challenges/"+id+"/approve" && request.Header.Get("Authorization") == "Bearer "+token && request.Header.Get("Origin") == ""
		called <- valid
		writer.WriteHeader(http.StatusNoContent)
	})}
	go server.Serve(listener)
	defer server.Close()
	configPath := filepath.Join(t.TempDir(), "darwin.yaml")
	if err := os.WriteFile(configPath, []byte("version: 1\ndaemon:\n  listen: \""+listener.Addr().String()+"\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DARWIN_API_TOKEN", token)
	var stdout, stderr bytes.Buffer
	if code := runWeb([]string{"approve", "--config", configPath, id + ".12345678"}, &stdout, &stderr); code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), `"approved":true`) || !<-called {
		t.Fatal("web approval failed", code, stdout.String(), stderr.String())
	}
}

func TestWebApprovalCLIRejectsMalformedInputWithoutNetwork(t *testing.T) {
	for _, args := range [][]string{{}, {"approve"}, {"approve", "--config", "x", "bad"}, {"approve", "--config", "x", strings.Repeat("a", 24) + ".abcdefgh"}} {
		var stdout, stderr bytes.Buffer
		if code := runWeb(args, &stdout, &stderr); code != 2 || stdout.Len() != 0 {
			t.Fatal("malformed approval accepted", args, code)
		}
	}
}
