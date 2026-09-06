# macOS thermal admission

The built-in macOS profiler now reads `NSProcessInfo.thermalState` alongside
its existing RAM, swap and unified-memory observations. The reported state is
available in `darwin resources` and the host profiler extension. No model
inference, stress load, administrator access or power-setting changes are needed.

| OS report | `ThermalPressure` | New local reservation |
| --- | --- | --- |
| nominal | false | Other admission checks apply |
| fair | false | Other admission checks apply |
| serious | true | Denied by the existing budget |
| critical | true | Denied by the existing budget |
| Unknown, failed or malformed probe | null | Thermal state is not inferred; other checks apply |

The optional `thermal_state` label is omitted for unavailable readings. A custom
profiler may retain the older boolean-only contract; if it supplies a label,
the adapter requires this closed vocabulary and a matching non-null boolean.
Arbitrary extension text cannot enter that field.

[Apple's thermal guidance](https://developer.apple.com/library/archive/documentation/Performance/Conceptual/power_efficiency_guidelines_osx/RespondToThermalStateChanges.html)
recommends corrective action at serious and critical levels. It also warns that
unsupported or unknown device thermals can be reported as nominal. Therefore a
nominal API response is not a guarantee of sensor support or physical temperature.
The mapping does not measure degrees, fan speed or per-device GPU temperature.

## Probe and failure boundary

Darwin executes one fixed Foundation-only JavaScript expression using the
absolute `/usr/bin/osascript` path. Named framework constants are converted to
fixed labels. No user data is inserted into the script, and it does not automate
applications or access a repository. The existing process helper supplies a
sanitized environment, one-second deadline, bounded output and pipe-drain limit;
the entire macOS profile retains its three-second deadline.

Optional thermal-probe errors preserve valid RAM observations and leave thermal
fields unknown. Caller cancellation or an exhausted overall deadline fails the
profile. This remains a point-in-time observation: it does not preempt already
running inference or reserve thermal headroom against other processes. Existing
application pressure policies determine whether denied work is queued, offloaded
where privacy permits, or rejected. Unknown thermal state alone does not deny
otherwise viable work under the existing budget policy.

## Evidence and remaining work

On the development Mac, the rebuilt `darwin resources` returned `nominal`,
`ThermalPressure: false`, 16 CPU threads and 48 GiB unified memory. The old
`pmset -g therm` reported no recorded thermal data and is not used as a cool
signal. This was a read-only observation, not a physical throttling test.

Injected-probe tests exercise all states through the actual budget reservation
method, malformed and failed probes, preservation of memory data, fixed command
arguments and cancellation. Custom-profiler tests reject unknown labels and
inconsistent pressure. [Linux thermal measurement](linux-thermal-profiling.md)
now samples published trip points separately. Physical hot-state
qualification, progressive fair-state throttling, active-inference preemption
and cross-platform thermal parity remain unfinished.
