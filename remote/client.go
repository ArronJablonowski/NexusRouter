package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/ArronJablonowski/NexusRouter/internal/scheduleview"
	"github.com/ArronJablonowski/NexusRouter/internal/usagestats"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ArronJablonowski/NexusRouter/sessions"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

type Client struct {
	// UsageFile enables durable caller-side remote usage receipts.
	UsageFile string

	Trust       TrustFile
	Credentials Credentials
}

func (c *Client) Info(ctx context.Context, destination string) (Info, error) {
	var out Info
	err := c.call(ctx, destination, "info", "GET", "/v1/remote/info", nil, nil, &out)
	if err == nil && (out.Version != Version || out.Instance != destination || !validHostname(out.Hostname) || out.ValidateRouting() != nil) {
		err = ErrInvalid
	}
	return out, err
}

// Dispatch never retries automatically. Persist the caller's key and exact task
// before sending; after any uncertain response, retry only that same pair.
func (c *Client) Dispatch(ctx context.Context, destination, key string, task Task) (submissions.Status, error) {
	var out submissions.Status
	if !requestID(key) || task.Validate() != nil {
		return out, ErrInvalid
	}
	err := c.call(ctx, destination, "dispatch", "POST", "/v1/remote/tasks/"+key, &task, nil, &out)
	return out, err
}
func (c *Client) Status(ctx context.Context, destination, key string) (submissions.Status, error) {
	return c.control(ctx, destination, key, "inspect", "GET", "")
}
func (c *Client) Cancel(ctx context.Context, destination, key string) (submissions.Status, error) {
	return c.control(ctx, destination, key, "cancel", "POST", "/cancel")
}
func (c *Client) control(ctx context.Context, destination, key, op, method, suffix string) (submissions.Status, error) {
	var out submissions.Status
	if !requestID(key) {
		return out, ErrInvalid
	}
	err := c.call(ctx, destination, op, method, "/v1/remote/tasks/"+key+suffix, nil, nil, &out)
	return out, err
}
func (c *Client) Events(ctx context.Context, destination, key, task string, after int64) (sessions.EventPage, error) {
	var out sessions.EventPage
	if !requestID(key) || !name(task) || after < 0 {
		return out, ErrInvalid
	}
	err := c.call(ctx, destination, "inspect", "GET", "/v1/remote/tasks/"+key+"/events", nil, map[string]string{"X-Nexus-Task": task, "X-Nexus-After": strconv.FormatInt(after, 10)}, &out)
	if err == nil && (out.Validate() != nil || out.TaskID != task || out.FromSequence != after) {
		err = ErrUnavailable
	}
	return out, err
}
func (c *Client) call(ctx context.Context, destination, op, method, path string, task *Task, headers map[string]string, out any) error {
	return c.callPinned(ctx, destination, op, method, path, task, headers, out, "")
}
func (c *Client) callPinned(ctx context.Context, destination, op, method, path string, task *Task, headers map[string]string, out any, callerPin string) error {
	if c == nil || ctx == nil || !id(destination) {
		return ErrInvalid
	}
	r, err := c.Trust.Read()
	if err != nil {
		return err
	}
	p, err := r.peer(destination)
	if err != nil || !p.permits(op) {
		return ErrDenied
	}
	if task != nil && !p.permitsTask(*task) {
		return ErrDenied
	}
	if path == "/v1/remote/harness-identity" || path == "/v1/remote/harness-capacity" || path == "/v1/remote/harness-readiness" {
		n, e := strconv.Atoi(headers["X-Nexus-Context"])
		if e != nil || !p.permitsIdentity(HarnessIdentityRequest{headers["X-Nexus-Model"], headers["X-Nexus-Harness"], n}) {
			return ErrDenied
		}
	}
	address, err := p.address()
	if err != nil {
		return err
	}
	tlsConfig, err := c.Credentials.clientTLS(p)
	if err != nil {
		return err
	}
	if callerPin != "" && (len(tlsConfig.Certificates) != 1 || len(tlsConfig.Certificates[0].Certificate) == 0 || certificateDigest(tlsConfig.Certificates[0].Certificate[0]) != callerPin) {
		return ErrConflict
	}
	var body []byte
	if task != nil {
		body, err = json.Marshal(task)
		if err != nil {
			return ErrInvalid
		}
	}
	request, err := http.NewRequestWithContext(ctx, method, p.Endpoint+path, bytes.NewReader(body))
	if err != nil {
		return ErrInvalid
	}
	request.Header.Set("X-Nexus-Instance", destination)
	if hostname, e := os.Hostname(); e == nil && validHostname(hostname) {
		request.Header.Set("X-Nexus-Hostname", hostname)
	}
	if task != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		request.Header.Set(k, v)
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	// No proxy, redirect, DNS resolution, connection reuse or TLS session cache.
	// Every call takes fresh registry/credentials and verifies the pinned endpoint.
	transport := &http.Transport{TLSClientConfig: tlsConfig, DisableKeepAlives: true, ForceAttemptHTTP2: false, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 20 * time.Second, MaxResponseHeaderBytes: 16384, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		if network != "tcp" || addr != address {
			return nil, ErrDenied
		}
		if p.Transport == "ssh" {
			return p.SSH.dial(ctx, address)
		}
		return dialer.DialContext(ctx, network, address)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 25 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrDenied }}
	response, err := client.Do(request)
	if err != nil {
		return ErrUnavailable
	}
	defer response.Body.Close()
	if response.Header.Get("X-Nexus-Instance") != destination {
		return ErrDenied
	}
	switch response.StatusCode {
	case http.StatusTooManyRequests:
		return ErrRateLimited
	case 200:
	case 400:
		return ErrInvalid
	case 403:
		return ErrDenied
	case 409:
		return ErrConflict
	default:
		return ErrUnavailable
	}
	maxBytes := 8 << 20
	if op == "logs" {
		maxBytes = maxLogPageBytes
	}
	bytes, err := io.ReadAll(io.LimitReader(response.Body, int64(maxBytes)+1))
	if err != nil || len(bytes) > maxBytes {
		return ErrUnavailable
	}
	if json.Unmarshal(bytes, out) != nil {
		return ErrUnavailable
	}
	if status, ok := out.(*submissions.Status); ok && c.UsageFile != "" {
		var envelope struct {
			Caller string                  `json:"caller"`
			Usage  *usagestats.RemoteUsage `json:"remote_usage"`
		}
		if json.Unmarshal(bytes, &envelope) != nil {
			return ErrUnavailable
		}
		key := strings.TrimSuffix(strings.TrimPrefix(path, "/v1/remote/tasks/"), "/cancel")
		if envelope.Caller != "" {
			if !id(envelope.Caller) || !requestID(key) || status.Version != Version || !name(status.ID) {
				return ErrUnavailable
			}
			if envelope.Usage == nil {
				return usagestats.SaveMissingRemoteUsage(ctx, c.UsageFile, destination, envelope.Caller, key, status.State)
			}
			return usagestats.SaveRemoteUsage(ctx, c.UsageFile, destination, envelope.Caller, key, status.State, *envelope.Usage)
		}
	}
	return nil
}

func (c *Client) OSSchedules(ctx context.Context, destination string) (scheduleview.Page, error) {
	var out scheduleview.Page
	err := c.call(ctx, destination, "inspect", "GET", "/v1/remote/os-schedules", nil, nil, &out)
	if err == nil {
		err = out.Validate()
	}
	return out, err
}
