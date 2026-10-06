# DNS logging

Settings offers Off, Level 1 (managed), and Level 2 (administrator capture). Preferences are stored in `telemetry.dns_logging` and take effect after restart. Full mode includes managed logging but never silently requests elevation or starts system monitoring.

## Level 1

Daily `dns-YYYY-MM-DD.jsonl` files beside the runtime database record resolver operations in the instrumented policy HTTP transports: provider calls, native harness upstream gateways, discovery/health and exports. Records contain UTC time, hostname, host/process ID, outcome, addresses, latency and coalescing. No URL paths, headers, credentials or response content are recorded. Files require private permissions; write failures surface as audit errors. Query names and addresses are sensitive. Daily files are retained until the operator archives/removes them.

These are resolver callbacks, not individual A/AAAA wire packets. Cached connections and literal IPs produce no DNS entries. Independent browser, shell, SSH, Codex and third-party subprocess resolution is outside this level. Task attribution is not fabricated.

## Level 2

Selecting Full saves the requested mode and enables Level 1 after restart. The UI explicitly reports that privileged capture is not verified active. On each executing host, an administrator can run the bundled helper:

```
sudo python3 scripts/dns-capture.py --interface INTERFACE --directory /absolute/new/private-capture-directory
```

Use `tcpdump -D` to choose an interface. The helper refuses existing destinations, uses private permissions, captures TCP/UDP port 53 into PCAP, records host/interface/start/stop metadata and tcpdump statistics, and stops after 100,000 packets or operator interruption. Capture is system-wide on the selected interface, including unrelated applications, not process-attributed. Do not upload these records to the central collector implicitly. Multiple interfaces may require separate captures; loopback and tunnel traffic may use different interfaces. This helper has not been live-qualified here because administrator access is unavailable.

**Full coverage is not achieved by this helper alone.** It cannot decode encrypted DNS (DoH/DoT), observe cache hits, or prove task attribution. Complete controlled subprocess coverage still needs platform integration/enforcement as described in the recorded harness DNS design. Saving the setting is not a claim that complete capture is operational.
