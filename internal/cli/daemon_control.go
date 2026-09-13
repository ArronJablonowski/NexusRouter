package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/daemon"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/policy"
)

var errDaemonControl = errors.New("daemon control unavailable")

type daemonControlClient struct {
	client      *http.Client
	transport   *policy.Transport
	base, token string
}

func daemonClient(cfg config.Settings, token string) (*daemonControlClient, error) {
	host, port, err := net.SplitHostPort(cfg.Daemon.Listen)
	n, portErr := strconv.Atoi(port)
	if host == "localhost" {
		host = "127.0.0.1"
	}
	ip := net.ParseIP(host)
	if err != nil || portErr != nil || n < 1 || n > 65535 || ip == nil || !ip.IsLoopback() || len(token) < 32 || strings.ContainsAny(token, "\r\n") {
		return nil, errDaemonControl
	}
	base := "http://" + net.JoinHostPort(host, strconv.Itoa(n))
	transport, err := policy.NewTransport(true, []string{base})
	if err != nil {
		return nil, errDaemonControl
	}
	return &daemonControlClient{client: &http.Client{Transport: transport, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errDaemonControl }}, transport: transport, base: base, token: token}, nil
}

func (c *daemonControlClient) Close() { c.transport.CloseIdleConnections() }
func (c *daemonControlClient) Status(ctx context.Context) (daemon.Status, error) {
	return c.request(ctx, http.MethodGet, "/v1/daemon/status", nil, "")
}
func (c *daemonControlClient) Stop(ctx context.Context, id string) (daemon.Status, error) {
	if len(id) != 64 {
		return daemon.Status{}, errDaemonControl
	}
	for _, ch := range id {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
			return daemon.Status{}, errDaemonControl
		}
	}
	body, _ := json.Marshal(struct {
		InstanceID string `json:"instance_id"`
	}{id})
	return c.request(ctx, http.MethodPost, "/v1/daemon/stop", body, id)
}

func (c *daemonControlClient) ApproveBrowserChallenge(ctx context.Context, id, code string) error {
	body, err := json.Marshal(map[string]any{"version": 1, "display_code": code})
	if err != nil {
		return errDaemonControl
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/v1/browser-session/challenges/"+id+"/approve", bytes.NewReader(body))
	if err != nil {
		return errDaemonControl
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	response, err := c.client.Do(req)
	if err != nil {
		return errDaemonControl
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1025))
	if err != nil || len(raw) > 1024 || response.StatusCode != http.StatusNoContent || len(raw) != 0 {
		return errDaemonControl
	}
	return nil
}

func (c *daemonControlClient) request(ctx context.Context, method, path string, body []byte, expected string) (daemon.Status, error) {
	bad := func() (daemon.Status, error) { return daemon.Status{}, errDaemonControl }
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(body))
	if err != nil {
		return bad()
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
		req.GetBody = nil
	} // Never replay stop after a transport failure.
	response, err := c.client.Do(req)
	if err != nil {
		return bad()
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return bad()
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil || len(raw) > 4096 {
		return bad()
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return bad()
	}
	seen := map[string]bool{}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return bad()
		}
		name, ok := key.(string)
		if !ok || seen[name] || (name != "version" && name != "instance_id" && name != "state" && name != "started_at") {
			return bad()
		}
		seen[name] = true
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return bad()
		}
	}
	if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
		return bad()
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || len(seen) != 4 {
		return bad()
	}
	var status daemon.Status
	if json.Unmarshal(raw, &status) != nil || status.Validate() != nil || (expected != "" && (status.InstanceID != expected || status.State != "stopping")) {
		return bad()
	}
	return status, nil
}

func runDaemonControl(args []string, stdout, stderr io.Writer) int {
	invalid := func() int {
		fmt.Fprintln(stderr, "usage: darwin daemon start|status|stop --config path (requires DARWIN_API_TOKEN)")
		return 2
	}
	if len(args) < 1 || (args[0] != "start" && args[0] != "status" && args[0] != "stop") {
		return invalid()
	}
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("config", "", "configuration")
	seen := false
	for i := 1; i < len(args); i++ {
		name, _, eq := strings.Cut(strings.TrimLeft(args[i], "-"), "=")
		if !strings.HasPrefix(args[i], "-") || name != "config" || seen {
			return invalid()
		}
		seen = true
		if !eq {
			i++
		}
	}
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || strings.TrimSpace(*path) == "" {
		return invalid()
	}
	cfg, err := config.Load(config.Options{ProjectFile: *path, Env: config.Environment(os.Environ())})
	if err != nil {
		fmt.Fprintln(stderr, "daemon configuration unavailable")
		return 1
	}
	token := os.Getenv("DARWIN_API_TOKEN")
	client, err := daemonClient(cfg, token)
	if err != nil {
		fmt.Fprintln(stderr, "daemon control unavailable")
		return 1
	}
	defer client.Close()
	ctx, cancel := submissionCLIContext()
	defer cancel()
	var status daemon.Status
	if args[0] == "start" {
		status, err = runDaemonStart(ctx, cfg, *path, token)
	} else {
		status, err = client.Status(ctx)
		if err == nil && args[0] == "stop" {
			status, err = client.Stop(ctx, status.InstanceID)
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, "daemon control unavailable")
		return 1
	}
	if json.NewEncoder(stdout).Encode(status) != nil {
		return 1
	}
	return 0
}
