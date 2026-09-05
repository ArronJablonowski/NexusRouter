package resources

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// profileLinux uses host availability as an upper bound, then accounts for
// every visible cgroup-v2 ancestor. Hidden namespace ancestors, external model
// servers and concurrent kernel changes cannot be inferred from this snapshot.
func profileLinux(ctx context.Context, read func(context.Context, string) ([]byte, error)) (Snapshot, error) {
	bad := func() (Snapshot, error) {
		if ctx != nil && ctx.Err() != nil {
			return Snapshot{}, ctx.Err()
		}
		return Snapshot{}, ErrProfile
	}
	if ctx == nil || read == nil {
		return bad()
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if ctx.Err() != nil {
		return bad()
	}
	s := Snapshot{Time: time.Now().UTC(), CPUs: runtime.NumCPU(), Source: "linux-proc-meminfo-host"}
	body, err := read(ctx, "/proc/meminfo")
	if err != nil {
		return bad()
	}
	s.TotalRAM, s.AvailableRAM, s.SwapUsed, err = parseLinuxMeminfo(body)
	if err != nil {
		return bad()
	}
	membership, err := read(ctx, "/proc/self/cgroup")
	if err != nil {
		return bad()
	}
	group, legacy, err := findUnifiedCgroup(membership)
	if err != nil || legacy {
		return bad()
	}
	if group == "" {
		if ctx.Err() != nil {
			return bad()
		}
		return s, nil
	}
	mounts, err := read(ctx, "/proc/self/mountinfo")
	if err != nil {
		return bad()
	}
	mount, root, err := findCgroupMount(mounts, group)
	if err != nil {
		return bad()
	}
	// Parsers require canonical absolute component paths. Join only their
	// relative suffix so a proc entry can never escape the selected mount.
	relative := strings.TrimPrefix(strings.TrimPrefix(group, root), "/")
	directory := filepath.Join(mount, relative)
	optional := func(path string) ([]byte, bool, error) {
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		b, e := read(ctx, path)
		if errors.Is(e, os.ErrNotExist) {
			return nil, false, nil
		}
		return b, e == nil, e
	}
	for depth := 0; ; depth++ {
		if depth >= 64 {
			return bad()
		}
		// cgroup.controllers exists even when no resource controllers are
		// enabled. Its absence cannot be mistaken for an unlimited cgroup.
		controllers, err := read(ctx, filepath.Join(directory, "cgroup.controllers"))
		if err != nil || len(controllers) > maxProbeBytes || ctx.Err() != nil {
			return bad()
		}
		maximum, hasMax, err := optional(filepath.Join(directory, "memory.max"))
		if err != nil {
			return bad()
		}
		high, hasHigh, err := optional(filepath.Join(directory, "memory.high"))
		if err != nil {
			return bad()
		}
		current, hasCurrent, err := optional(filepath.Join(directory, "memory.current"))
		if err != nil {
			return bad()
		}
		if hasMax || hasHigh || hasCurrent {
			if !hasMax || !hasHigh || !hasCurrent {
				return bad()
			}
			s, err = applyCgroupMemory(s, maximum, high, current)
			if err != nil {
				return bad()
			}
		}
		quota, _, err := optional(filepath.Join(directory, "cpu.max"))
		if err != nil {
			return bad()
		}
		cpuset, _, err := optional(filepath.Join(directory, "cpuset.cpus.effective"))
		if err != nil {
			return bad()
		}
		s, err = applyCgroupCPU(s, quota, cpuset)
		if err != nil {
			return bad()
		}
		if directory == mount {
			break
		}
		parent := filepath.Dir(directory)
		if parent == directory || (mount != string(filepath.Separator) && parent != mount && !strings.HasPrefix(parent, mount+string(filepath.Separator))) {
			return bad()
		}
		directory = parent
	}
	// Reassignment during measurement must not combine different hierarchies.
	check, err := read(ctx, "/proc/self/cgroup")
	if err != nil || !bytes.Equal(check, membership) {
		return bad()
	}
	check, err = read(ctx, "/proc/self/mountinfo")
	if err != nil || !bytes.Equal(check, mounts) || ctx.Err() != nil {
		return bad()
	}
	s.Source = "linux-proc-cgroup-v2"
	return s, nil
}

// Kernel files are byte-bounded and context-checked without orphan reader
// goroutines. Reads are cooperative, not a guarantee against a stalled kernel.
func readLinuxProfileFile(ctx context.Context, path string) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, ErrProfile
	}
	body, err := io.ReadAll(io.LimitReader(f, maxProbeBytes+1))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil || len(body) > maxProbeBytes {
		return nil, ErrProfile
	}
	return body, nil
}
