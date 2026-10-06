package policy

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http/httptrace"
	"os"
	"sync"
	"time"
)

// DNSAudit observes resolver operations, not individual DNS packets. External
// processes and encrypted DNS implemented outside this transport are not visible.
type DNSAudit struct {
	Path   string
	Source string
}
type dnsRecord struct {
	Version    int       `json:"version"`
	Time       time.Time `json:"time"`
	Host       string    `json:"host"`
	PID        int       `json:"pid"`
	Source     string    `json:"source"`
	Query      string    `json:"query"`
	Outcome    string    `json:"outcome"`
	Addresses  []string  `json:"addresses,omitempty"`
	DurationMS int64     `json:"duration_ms"`
	Coalesced  bool      `json:"coalesced"`
}

var dnsFileMu sync.Mutex
var ErrDNSAudit = errors.New("DNS audit log unavailable")

func (a DNSAudit) append(record dnsRecord) error {
	dnsFileMu.Lock()
	defer dnsFileMu.Unlock()
	// OpenRoot confines all file operations and rejects escaping symlinks.
	root, err := os.OpenRoot(a.Path)
	if err != nil {
		return ErrDNSAudit
	}
	defer root.Close()
	name := "dns-" + record.Time.UTC().Format("2006-01-02") + ".jsonl"
	if st, e := root.Lstat(name); e == nil && st.Mode()&os.ModeSymlink != 0 {
		return ErrDNSAudit
	}
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
	if err != nil {
		return ErrDNSAudit
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return ErrDNSAudit
	}
	if err = json.NewEncoder(f).Encode(record); err != nil {
		return ErrDNSAudit
	}
	if err = f.Sync(); err != nil {
		return ErrDNSAudit
	}
	return nil
}

func (a DNSAudit) dial(ctx context.Context, network, address string, dial func(context.Context, string, string) (net.Conn, error)) (net.Conn, error) {
	var mu sync.Mutex
	var query string
	var start time.Time
	var logErr error
	host, _ := os.Hostname()
	trace := &httptrace.ClientTrace{
		DNSStart: func(v httptrace.DNSStartInfo) { mu.Lock(); defer mu.Unlock(); query = v.Host; start = time.Now() },
		DNSDone: func(v httptrace.DNSDoneInfo) {
			mu.Lock()
			defer mu.Unlock()
			rec := dnsRecord{Version: 1, Time: time.Now().UTC(), Host: host, PID: os.Getpid(), Source: a.Source, Query: query, Outcome: "resolved", DurationMS: time.Since(start).Milliseconds(), Coalesced: v.Coalesced}
			if v.Err != nil {
				rec.Outcome = "failed"
			}
			for _, ip := range v.Addrs {
				rec.Addresses = append(rec.Addresses, ip.String())
			}
			if err := a.append(rec); err != nil {
				logErr = err
			}
		},
	}
	connection, err := dial(httptrace.WithClientTrace(ctx, trace), network, address)
	mu.Lock()
	auditErr := logErr
	mu.Unlock()
	if auditErr != nil {
		if connection != nil {
			connection.Close()
		}
		return nil, auditErr
	}
	return connection, err
}
