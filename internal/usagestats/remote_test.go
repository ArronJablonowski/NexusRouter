package usagestats

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRemoteReceiptsReplayRestartCorrectionsAndIsolation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "remote.db")
	u := EmptyRemoteUsage()
	u.Local = Count{Input: "100", Output: "20", Measured: 1}
	u.Cloud = Count{Input: "50", Output: "5", Measured: 1, Unknown: 1, Partial: 1}
	save := func(destination, caller, key, state string, v RemoteUsage) {
		t.Helper()
		if e := SaveRemoteUsage(ctx, path, destination, caller, key, state, v); e != nil {
			t.Fatal(e)
		}
	}
	for i := 0; i < 3; i++ {
		save("spark", "mac", "request", "succeeded", u)
	}
	save("other", "mac", "request", "failed", u)
	save("spark", "another-caller", "request", "canceled", u)
	got, e := ReadRemoteUsage(ctx, path)
	if e != nil || got.Requests != 3 || got.Local.Input != "300" || got.Cloud.Output != "15" || got.Cloud.Unknown != 3 {
		t.Fatal(got, e)
	}
	next := u
	next.ObservedAt = u.ObservedAt.Add(time.Second)
	next.Local.Input = "90"
	save("spark", "mac", "request", "succeeded", next)
	save("spark", "mac", "request", "running", u) // stale response cannot regress a correction
	if e = SaveMissingRemoteUsage(ctx, path, "spark", "mac", "request", "unknown"); e != nil {
		t.Fatal(e)
	}
	got, e = ReadRemoteUsage(ctx, path)
	if e != nil || got.Local.Input != "290" || got.Pending != 0 || got.Unavailable != 0 {
		t.Fatal(got, e)
	}
	if e = MarkRemoteSync(ctx, path, true); e != nil {
		t.Fatal(e)
	}
	got, e = ReadRemoteUsage(ctx, path)
	if e != nil || !got.SyncError || got.LastSync == "" {
		t.Fatal(got, e)
	}
	invalid := u
	invalid.Local.Input = "-1"
	if SaveRemoteUsage(ctx, path, "spark", "mac", "invalid", "succeeded", invalid) == nil {
		t.Fatal("negative tokens accepted")
	}
	st, e := os.Stat(path)
	if e != nil || st.Mode().Perm() != 0600 {
		t.Fatal(st, e)
	}
}
func TestRemoteUsageOverflowAndUnavailable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "remote.db")
	if e := SaveMissingRemoteUsage(ctx, path, "spark", "mac", "request", "running"); e != nil {
		t.Fatal(e)
	}
	got, e := ReadRemoteUsage(ctx, path)
	if e != nil || got.Unavailable != 1 || got.Pending != 1 || got.Local.Measured != 0 {
		t.Fatal(got, e)
	}
	u := EmptyRemoteUsage()
	u.Local = Count{Input: "9223372036854775807", Output: "0", Measured: 1}
	if e = SaveRemoteUsage(ctx, path, "spark", "mac", "a", "succeeded", u); e != nil {
		t.Fatal(e)
	}
	u.Local.Input = "1"
	if e = SaveRemoteUsage(ctx, path, "spark", "mac", "b", "succeeded", u); e != nil {
		t.Fatal(e)
	}
	if _, e = ReadRemoteUsage(ctx, path); e == nil {
		t.Fatal("overflow accepted")
	}
}
