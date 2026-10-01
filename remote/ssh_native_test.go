package remote

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// Opt-in because sshd availability, platform login policy and privilege
// separation are host prerequisites. Never changes the system SSH service.
func TestSSHNativeLoopback(t *testing.T) {
	if os.Getenv("NEXUS_REMOTE_SSH_NATIVE") != "1" {
		t.Skip("native sshd qualification requires NEXUS_REMOTE_SSH_NATIVE=1")
	}
	sshd := "/usr/sbin/sshd"
	if _, e := os.Stat(sshd); e != nil {
		t.Fatal(e)
	}
	who, e := user.Current()
	if e != nil {
		t.Fatal(e)
	}
	dir, e := os.MkdirTemp("", "nexus-ssh-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(dir)
	hostKey := filepath.Join(dir, "host")
	clientKey := filepath.Join(dir, "client")
	for _, path := range []string{hostKey, clientKey} {
		if output, e := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", path).CombinedOutput(); e != nil {
			t.Fatalf("key generation: %v %s", e, output)
		}
	}
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	cfg := fmt.Sprintf("Port %d\nListenAddress 127.0.0.1\nHostKey %s\nPidFile %s\nAuthorizedKeysFile %s.pub\nStrictModes no\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nUsePAM no\nAllowUsers %s\nAllowTcpForwarding local\nAllowAgentForwarding no\nX11Forwarding no\nPermitTTY no\n", port, hostKey, filepath.Join(dir, "pid"), clientKey, who.Username)
	configPath := filepath.Join(dir, "sshd_config")
	if e = os.WriteFile(configPath, []byte(cfg), 0600); e != nil {
		t.Fatal(e)
	}
	log, e := os.Create(filepath.Join(dir, "sshd.log"))
	if e != nil {
		t.Fatal(e)
	}
	defer log.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	server := exec.CommandContext(ctx, sshd, "-D", "-e", "-f", configPath)
	server.Stderr = log
	if e = server.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { server.Process.Kill(); server.Wait() }()
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	for {
		conn, e := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if e == nil {
			conn.Close()
			break
		}
		select {
		case <-ctx.Done():
			body, _ := os.ReadFile(log.Name())
			t.Fatalf("sshd unavailable: %s", body)
		case <-time.After(25 * time.Millisecond):
		}
	}
	pub, e := os.ReadFile(hostKey + ".pub")
	if e != nil {
		t.Fatal(e)
	}
	known := filepath.Join(dir, "known_hosts")
	if e = os.WriteFile(known, append([]byte("[127.0.0.1]:"+strconv.Itoa(port)+" "), pub...), 0600); e != nil {
		t.Fatal(e)
	}
	f := setup(t)
	peer := f.serverPeer
	peer.Transport = "ssh"
	peer.SSH = &SSH{User: who.Username, Port: port, IdentityFile: clientKey, KnownHostsFile: known}
	writeRegistry(t, f.clientTrust, peer)
	first, e := f.client.Dispatch(ctx, "node-a", "native-ssh-000001", testTask())
	if e != nil {
		body, _ := os.ReadFile(log.Name())
		t.Fatalf("dispatch %v; sshd: %s", e, body)
	}
	retry, e := f.client.Dispatch(ctx, "node-a", "native-ssh-000001", testTask())
	if e != nil || retry.ID != first.ID || f.backend.creates != 1 {
		t.Fatal(retry, e)
	}
	// Use another valid key as the expected host key: refusal must be SSH-native,
	// and must not fall back to the directly reachable TLS endpoint.
	wrong, _ := os.ReadFile(clientKey + ".pub")
	wrong = bytes.TrimSpace(wrong)
	if e = os.WriteFile(known, []byte("[127.0.0.1]:"+strconv.Itoa(port)+" "+string(wrong)+"\n"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = f.client.Dispatch(ctx, "node-a", "native-ssh-000002", testTask()); e == nil {
		t.Fatal("wrong SSH host key accepted")
	}
	if f.backend.creates != 1 {
		t.Fatal("direct fallback after host-key failure")
	}
	if ctx.Err() != nil {
		t.Fatal(ctx.Err())
	}
	if e = os.WriteFile(known, append([]byte("[127.0.0.1]:"+strconv.Itoa(port)+" "), pub...), 0600); e != nil {
		t.Fatal(e)
	}
	peer.SSH.IdentityFile = hostKey
	writeRegistry(t, f.clientTrust, peer)
	if _, e = f.client.Dispatch(ctx, "node-a", "native-ssh-000003", testTask()); e == nil {
		t.Fatal("unauthorized SSH client key accepted")
	}
	if ctx.Err() != nil || f.backend.creates != 1 {
		t.Fatal("client key refusal or no-fallback check failed", ctx.Err())
	}
	peer.SSH.IdentityFile = clientKey
	writeRegistry(t, f.clientTrust, peer)
	canceled, e := f.client.Cancel(ctx, "node-a", "native-ssh-000001")
	if e != nil || !canceled.CancelRequested {
		t.Fatal(canceled, e)
	}
}
