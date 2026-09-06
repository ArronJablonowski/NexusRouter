# Linux thermal admission

Linux host profiling now reads the fixed `/sys/class/thermal` tree after its
mandatory memory/cgroup observations, under one three-second cooperative deadline.
It performs no network calls, subprocess launches or sysfs writes. It does not
change governors, enable sensors, emulate temperatures or stress hardware.

The kernel publishes current and trip temperatures in millidegrees Celsius;
trip attributes are optional. DarwinRouter recognizes `passive`, `hot` and
`critical` trip types and compares each usable positive threshold against that
zone's current temperature. Equality is treated conservatively as pressure.
See the [Linux thermal ABI](https://www.kernel.org/doc/Documentation/ABI/testing/sysfs-class-thermal).
Zero is not assumed to be an enabled trip: the
[x86 package driver](https://cdn.kernel.org/doc/html/latest/driver-api/thermal/x86_pkg_temperature_thermal.html)
uses zero to disable notifications.

## Observation semantics

- `ThermalPressure: true`: at least one valid recognized threshold was reached.
  This positive evidence survives an unrelated unreadable or unsupported zone.
  Existing resource policy denies new local reservations.
- `ThermalPressure: false`: discovery completed within bounds, every visible
  zone supplied usable passive-threshold coverage, all recognized pairs were
  valid, and no recognized threshold was reached.
- `ThermalPressure: null`: no usable zones, absent sysfs, incomplete readings,
  malformed fields or discovery overflow without usable positive evidence.
  A critical-only zone below shutdown temperature cannot establish absence of
  earlier pressure. Active fan trips do not establish either pressure or
  passive coverage. Other capacity/privacy checks still apply to unknowns.

The macOS-specific `thermal_state` label is not invented for Linux. Memory/CPU
source labels remain unchanged. Missing thermal information does not erase valid
memory measurements; caller cancellation or expiry of the shared deadline fails
the whole profile. A kernel read remains cooperative rather than forcibly
interruptible, and no timeout goroutine is abandoned.

These are sampled threshold observations, not an atomic multi-file snapshot or
proof of kernel throttling/cooling state. Firmware, thresholds and temperatures
can change between reads. Hysteresis, previously latched cooling, hidden sensors,
GPU-specific thermal telemetry and physically accurate sensors are not inferred.
No kernel thermal protections are replaced. Pressure blocks new work; it does
not preempt already-running inference. Existing queue/offload behavior remains
subject to deployment and privacy policy.

## Bounds and qualification

Each directory is limited to 256 entries, with at most 64 zones and 64 distinct
trip indices per zone. Names must use canonical unsigned 32-bit indices; fields
are at most 64 bytes with strict signed 32-bit decimal temperature parsing.
Temperatures below absolute zero and nonpositive trip thresholds are rejected.
Only generated safe path components enter reads. Kernel class-directory symlinks
are intentionally followed; this is a trusted sysfs interface, not an arbitrary
user-controlled filesystem sandbox. Policy, emulation, hysteresis and cooling
control attributes are not read.

Fixture tests exercise threshold equality, optional/malformed observations,
positive evidence alongside unknown zones, exact and excessive discovery bounds,
symlinks, cancellation and actual reservation denial. They pass with race
detection on macOS and execute as a CGO-free Linux/arm64 test binary in an
unprivileged, read-only, network-disabled container with temporary fixture files.
That container's rebuilt `darwin resources` command also measured its real
512 MiB cgroup limit and two CPUs, preserving `ThermalPressure: null` because no
usable sensors were exposed. This is Linux execution and absence handling, not
physical hot-sensor qualification. No user model or user data was accessed.
