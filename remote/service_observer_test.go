package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Independent physical observer is optional; all connection settings are
// explicitly supplied and strict-key SSH executes only the hash-verified CLI.
func serviceDiscoveryCandidates(ctx context.Context, localInterface string) ([]DiscoveryCandidate, error) {
	host := os.Getenv("NEXUS_REMOTE_OBSERVER_HOST")
	if host == "" {
		return DiscoverUnpaired(ctx, localInterface, 2*time.Second)
	}
	ip, err := netip.ParseAddr(host)
	user := os.Getenv("NEXUS_REMOTE_OBSERVER_USER")
	key, known, binary := os.Getenv("NEXUS_REMOTE_OBSERVER_KEY"), os.Getenv("NEXUS_REMOTE_OBSERVER_KNOWN_HOSTS"), os.Getenv("NEXUS_REMOTE_OBSERVER_BINARY")
	digest, iface := os.Getenv("NEXUS_REMOTE_OBSERVER_SHA256"), os.Getenv("NEXUS_REMOTE_OBSERVER_INTERFACE")
	if err != nil || !ip.IsPrivate() || ip.IsLoopback() || !id(user) || !filepath.IsAbs(key) || !filepath.IsAbs(known) || !filepath.IsAbs(binary) || !hexDigest(digest) || !id(iface) {
		return nil, ErrInvalid
	}
	script := `import sys,json,pathlib,hashlib,subprocess; p=json.load(sys.stdin); assert hashlib.sha256(pathlib.Path(p["binary"]).read_bytes()).hexdigest()==p["sha256"]; r=subprocess.run([p["binary"],"remote","discover","--interface",p["interface"],"--wait","2s"],capture_output=True,timeout=5); sys.stdout.buffer.write(r.stdout); sys.exit(r.returncode)`
	payload, _ := json.Marshal(map[string]string{"binary": binary, "sha256": digest, "interface": iface})
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	command := exec.CommandContext(bounded, "ssh", "-F", "/dev/null", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "IdentitiesOnly=yes", "-o", "IdentityAgent=none", "-o", "ConnectTimeout=5", "-o", "UserKnownHostsFile="+known, "-i", key, user+"@"+host, "python3 -c '"+script+"'")
	command.Stdin = bytes.NewReader(payload)
	output, err := command.Output()
	if err != nil {
		return nil, err
	}
	var page struct {
		Version    int                  `json:"version"`
		Verified   bool                 `json:"verified"`
		Candidates []DiscoveryCandidate `json:"candidates"`
	}
	if json.Unmarshal(output, &page) != nil || page.Version != 1 || page.Verified || len(page.Candidates) > 64 {
		return nil, ErrInvalid
	}
	return page.Candidates, nil
}
