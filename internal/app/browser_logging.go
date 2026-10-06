package app

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/ArronJablonowski/NexusRouter/webui"
)

// BrowserLogging inventories known storage locations without opening log contents.
func (s *Service) BrowserLogging(ctx context.Context) (webui.LoggingPage, error) {
	if err := ctx.Err(); err != nil {
		return webui.LoggingPage{}, err
	}
	p := webui.LoggingPage{Version: 1, ObservedAt: time.Now().UTC(), Items: []webui.LogLocation{}}
	add := func(id, name, path, scope, format, note string, records ...string) {
		if path == "" {
			return
		}
		path = filepath.Clean(path)
		status := "Not present"
		if st, err := os.Stat(path); err == nil {
			status = "Present"
			if st.IsDir() {
				status = "Directory present"
			}
		} else if !os.IsNotExist(err) {
			status = "Unavailable"
		}
		p.Items = append(p.Items, webui.LogLocation{ID: id, Name: name, Location: path, Scope: scope, Format: format, Status: status, Records: records, Note: note})
	}
	db := s.settings.Telemetry.Database
	add("runtime", "Task history and approval audit", db, "Local", "SQLite", "Configured runtime database. Values are recorded when supplied; missing usage is not zero.", "Task, session, turn and tool lifecycle events", "Routing decisions, model/provider identity, timing, recorded token usage and cost", "Approval requests, attributed decisions, timestamps and execution state", "Submissions, workboard state and browser-operation receipts")
	add("stats", "Usage trip markers", db+".stats.db", "Local", "SQLite", "Trip reset markers only. Token totals are derived from runtime events, not duplicated here.", "Trip/reset boundaries for usage counters")
	if trust := s.settings.WebUI.RemoteTrustFile; trust != "" {
		add("remote-usage", "Remote usage accounting", trust+".usage.db", "Remote-derived · stored locally", "SQLite", "Usage reported through the configured remote transport; not every job on a remote host.", "Reported input/output tokens and usage coverage for remote calls")
	}
	dir := filepath.Dir(db)
	if s.settings.Telemetry.DNSLogging != "" {
		add("dns", "NexusRouter DNS audit", dir, "Local managed transport", "Daily dns-YYYY-MM-DD.jsonl", "Resolver operations, not raw DNS packets. No entries for pooled connections or literal IPs. Excludes independent subprocess DNS. Full capture requires separate administrator setup and is not verified by this setting.", "Timestamp, hostname query, executing host and process ID, resolution outcome, addresses, latency and coalescing")
	}
	for _, x := range []struct{ id, name, file string }{{"application", "Application output", "darwin.log"}, {"stdout", "Service standard output", "launchd.stdout.log"}, {"stderr", "Service standard error", "launchd.stderr.log"}} {
		path := filepath.Join(dir, x.file)
		if _, err := os.Stat(path); err == nil {
			add(x.id, x.name, path, "Local", "Text", "Detected installation file. Presence does not establish the active writer or a fixed metric schema.", "Process output and diagnostic messages; contents depend on the writer")
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		add("process-audit", "Subprocess audit", filepath.Join(home, ".NexusRouter", "data", "process-audit"), "Local direct subprocesses", "Daily process-YYYY-MM-DD.jsonl", "NexusRouter executable logs direct launches. Descendants started inside harnesses or service managers require OS-level monitoring. Conventional credential arguments and inline scripts/prompts are redacted; environment and process output are excluded. Retained until operator archival/removal.", "Launch ID, timestamps, hostname, parent PID and child PID", "Executable, redacted argv, working directory, launch outcome and exit code")
		root := filepath.Join(home, ".NexusRouter", "data", "central-logs")
		add("collector", "Collected remote logs", filepath.Join(root, "logs.sqlite"), "Remote replica · stored locally", "SQLite", "Known collector location. Presence does not establish current collector health. Excludes OS logs and standalone learning stores.", "Authorized runtime and security event records, source host and stream", "Task/session IDs, event and collection timestamps, record hashes and collection cursors")
		for _, suffix := range []string{"stdout", "stderr"} {
			add("collector-"+suffix, "Collector "+suffix, filepath.Join(root, "collector."+suffix+".log"), "Local collector", "Text", "Known service output location; no fixed metric schema.", "Collector process output, errors and diagnostic messages")
		}
		add("qa", "Build, QA and deployment evidence", filepath.Join(home, ".NexusRouter", "resources"), "Local", "Directory · JSON and text", "Operator-created artifacts, not an automatically populated runtime metric store.", "Test results and failures, build hashes, deployment state and verification reports")
	}
	export := func(id, name string, enabled bool, configured bool, records ...string) {
		status := "Not configured"
		if configured {
			status = "Disabled"
			if enabled {
				status = "Enabled in configuration"
			}
		}
		p.Items = append(p.Items, webui.LogLocation{ID: id, Name: name, Location: "External collector configured in runtime settings (endpoint withheld)", Scope: "External", Format: "OTLP over HTTP", Status: status, Records: records, Note: "Configuration state is not delivery confirmation. Destination credentials are never exposed."})
	}
	m := s.settings.Telemetry.MetricsExport
	export("metrics", "Metrics export", m != nil && m.Enabled, m != nil, "Accounting records, known/unknown usage and cost coverage, input/output tokens and normalized cost", "CPU thread count, total/available RAM and VRAM, swap bytes, thermal-pressure and unified-memory flags", "Reservation counts, reserved RAM/VRAM and resource availability flags (when available)")
	t := s.settings.Telemetry.TraceExport
	export("traces", "Trace export", t != nil && t.Enabled, t != nil, "Task/turn/tool execution spans, timing and correlation metadata")
	return p, p.Validate()
}
