package remote

import (
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This helper exercises subprocess stream/lifecycle integration, not SSH's
// cryptography. OpenSSH effective-config validation is a separate test below.
func TestSSHPipeHelper(t *testing.T) {
	if os.Getenv("NEXUS_TEST_SSH_HELPER") != "1" {
		return
	}
	address := ""
	for i, arg := range os.Args {
		if arg == "-W" && i+1 < len(os.Args) {
			address = os.Args[i+1]
		}
	}
	connection, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		os.Exit(2)
	}
	go func() { io.Copy(connection, os.Stdin); connection.Close() }()
	io.Copy(os.Stdout, connection)
	os.Exit(0)
}
func sshFixture(t *testing.T) SSH {
	t.Helper()
	dir := t.TempDir()
	s := SSH{User: "nexus", Port: 22, IdentityFile: filepath.Join(dir, "identity"), KnownHostsFile: filepath.Join(dir, "known_hosts")}
	for _, p := range []string{s.IdentityFile, s.KnownHostsFile} {
		if e := os.WriteFile(p, []byte("fixture\n"), 0600); e != nil {
			t.Fatal(e)
		}
	}
	return s
}
func TestSSHTransportKeepsMTLSOwnershipAndIdempotency(t *testing.T) {
	f := setup(t)
	s := sshFixture(t)
	binary, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	script := "#!/bin/sh\nexec '" + strings.ReplaceAll(binary, "'", "'\\''") + "' -test.run=^TestSSHPipeHelper$ -- \"$@\"\n"
	if e = os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("NEXUS_TEST_SSH_HELPER", "1")
	peer := f.serverPeer
	peer.Transport = "ssh"
	peer.SSH = &s
	writeRegistry(t, f.clientTrust, peer)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	first, e := f.client.Dispatch(ctx, "node-a", "request-ssh-00001", testTask())
	if e != nil {
		t.Fatal(e)
	}
	again, e := f.client.Dispatch(ctx, "node-a", "request-ssh-00001", testTask())
	if e != nil || again.ID != first.ID || f.backend.creates != 1 {
		t.Fatal(again, e)
	}
	state, e := f.client.Cancel(ctx, "node-a", "request-ssh-00001")
	if e != nil || !state.CancelRequested {
		t.Fatal(state, e)
	}
	writeRegistry(t, f.serverTrust)
	if _, e = f.client.Info(ctx, "node-a"); e == nil {
		t.Fatal("SSH bypassed router revocation")
	}
}
func TestSSHOpenSSHConfigurationAndInputGuards(t *testing.T) {
	s := sshFixture(t)
	args, e := s.arguments("127.0.0.1:8443")
	if e != nil {
		t.Fatal(e)
	}
	ssh, e := exec.LookPath("ssh")
	if e != nil {
		t.Skip("OpenSSH is not installed; native configuration qualification unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, e := exec.CommandContext(ctx, ssh, append([]string{"-G"}, args...)...).Output()
	if e != nil {
		t.Fatal(e)
	}
	configuration := string(output)
	for _, line := range []string{"stricthostkeychecking true\n", "batchmode yes\n", "identityagent none\n", "forwardagent no\n", "controlmaster false\n", "passwordauthentication no\n", "permitlocalcommand no\n", "hostname 127.0.0.1\n"} {
		if !strings.Contains(configuration, line) {
			t.Fatalf("missing effective option %q", line)
		}
	}
	for _, path := range []string{"relative", "/tmp/key%h", "/tmp/key${HOME}", "/tmp/a b", "/tmp/a\ncommand"} {
		bad := s
		bad.IdentityFile = path
		if bad.Validate() == nil {
			t.Fatalf("accepted %q", path)
		}
	}
	for _, user := range []string{"-oProxyCommand=x", "name;command", "name@host", "name name"} {
		bad := s
		bad.User = user
		if bad.Validate() == nil {
			t.Fatalf("accepted user %q", user)
		}
	}
	if e = os.Chmod(s.IdentityFile, 0644); e != nil {
		t.Fatal(e)
	}
	if _, e = s.arguments("127.0.0.1:8443"); e == nil {
		t.Fatal("permissive identity accepted")
	}
}
func TestSSHFailureHasNoDirectFallback(t *testing.T) {
	f := setup(t)
	s := sshFixture(t)
	dir := t.TempDir()
	if e := os.WriteFile(filepath.Join(dir, "ssh"), []byte("#!/bin/sh\nexit 255\n"), 0700); e != nil {
		t.Fatal(e)
	}
	t.Setenv("PATH", dir)
	peer := f.serverPeer
	peer.Transport = "ssh"
	peer.SSH = &s
	writeRegistry(t, f.clientTrust, peer)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, e := f.client.Dispatch(ctx, "node-a", "request-ssh-fail1", testTask()); e == nil {
		t.Fatal("failed SSH accepted")
	}
	if f.backend.creates != 0 {
		t.Fatal("fell back to reachable direct endpoint")
	}
}
