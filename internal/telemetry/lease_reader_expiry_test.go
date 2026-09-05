package telemetry

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestExpiredReaderFencesWriterAcrossConnectionsAndRestart(t *testing.T) {
	ctx := context.Background()
	s, path := leaseStore(t)
	now := time.Unix(100, 0).UTC()
	reader, err := s.AcquireLease(ctx, "task", "reader", "scope", false, now, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	later := now.Add(time.Hour)
	if err = s.RenewLease(ctx, reader.Token, "reader", later, time.Second); !errors.Is(err, ErrLeaseLost) {
		t.Fatal("expired read revived", err)
	}
	other, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	for _, db := range []*Store{s, other} {
		if _, err = db.AcquireLease(ctx, "task", "writer", "scope", true, later, time.Second); !errors.Is(err, ErrLeaseBusy) {
			t.Fatal("expired reader overlapped writer", err)
		}
	}
	// Another reader remains safe, but owns an independent unreleased claim.
	second, err := other.AcquireLease(ctx, "task", "second", "scope", false, later, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	leases, err := reopened.InspectLeases(ctx, "scope")
	if err != nil || len(leases) != 2 {
		t.Fatal("restart lost reader claims", leases, err)
	}
	if _, err = reopened.AcquireLease(ctx, "task", "writer", "scope", true, later.Add(time.Hour), time.Second); !errors.Is(err, ErrLeaseBusy) {
		t.Fatal("restart released reader", err)
	}
	if err = reopened.ReleaseLease(ctx, reader.Token, "wrong"); !errors.Is(err, ErrLeaseLost) {
		t.Fatal("wrong owner release", err)
	}
	if err = reopened.ReleaseLease(ctx, reader.Token, "reader"); err != nil {
		t.Fatal(err)
	}
	if _, err = reopened.AcquireLease(ctx, "task", "writer", "scope", true, later.Add(time.Hour), time.Second); !errors.Is(err, ErrLeaseBusy) {
		t.Fatal("second reader ignored", err)
	}
	if err = reopened.ReleaseLease(ctx, second.Token, "second"); err != nil {
		t.Fatal(err)
	}
	if _, err = reopened.AcquireLease(ctx, "task", "writer", "scope", true, later.Add(time.Hour), time.Second); err != nil {
		t.Fatal("released scope not reusable", err)
	}
}

func TestWorkspaceConflictsWithLegacyCreateScopes(t *testing.T) {
	legacy := "create_" + strings.Repeat("a", 64)
	for _, expired := range []bool{false, true} {
		for _, reader := range []bool{false, true} {
			s, _ := leaseStore(t)
			ctx := context.Background()
			now := time.Unix(100, 0).UTC()
			later := now
			if expired {
				later = later.Add(time.Hour)
			}
			old, err := s.AcquireLease(ctx, "task", "old", legacy, true, now, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			observed, err := s.InspectLeases(ctx, "workspace")
			if err != nil || len(observed) != 1 || observed[0].Scope != legacy || observed[0].Token != old.Token {
				t.Fatal("legacy overlap invisible", observed, err)
			}
			if _, err = s.AcquireLease(ctx, "task", "new", "workspace", !reader, later, time.Second); !errors.Is(err, ErrLeaseBusy) {
				t.Fatal("legacy holder bypassed", expired, reader, err)
			}
			if err = s.ReleaseLease(ctx, old.Token, "old"); err != nil {
				t.Fatal(err)
			}
			if _, err = s.AcquireLease(ctx, "task", "new", "workspace", !reader, later, time.Second); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, expired := range []bool{false, true} {
		s, _ := leaseStore(t)
		ctx := context.Background()
		now := time.Unix(100, 0).UTC()
		later := now
		if expired {
			later = later.Add(time.Hour)
		}
		reader, err := s.AcquireLease(ctx, "task", "reader", "workspace", false, now, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		observed, err := s.InspectLeases(ctx, legacy)
		if err != nil || len(observed) != 1 || observed[0].Scope != "workspace" || observed[0].Token != reader.Token {
			t.Fatal("workspace overlap invisible", observed, err)
		}
		if _, err = s.AcquireLease(ctx, "task", "writer", legacy, true, later, time.Second); !errors.Is(err, ErrLeaseBusy) {
			t.Fatal("workspace reader bypassed", expired, err)
		}
		if _, err = s.AcquireLease(ctx, "task", "unrelated", "other-resource", true, later, time.Second); err != nil {
			t.Fatal("generic exact scopes changed", err)
		}
		if err = s.ReleaseLease(ctx, reader.Token, "reader"); err != nil {
			t.Fatal(err)
		}
		if _, err = s.AcquireLease(ctx, "task", "writer", legacy, true, later, time.Second); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDistinctLegacyFilesystemScopesMutuallyOverlap(t *testing.T) {
	a, b := "create_"+strings.Repeat("a", 64), "create_"+strings.Repeat("b", 64)
	for _, expired := range []bool{false, true} {
		s, _ := leaseStore(t)
		ctx := context.Background()
		now := time.Unix(100, 0).UTC()
		later := now
		if expired {
			later = later.Add(time.Hour)
		}
		lease, err := s.AcquireLease(ctx, "task", "writer-a", a, true, now, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		for _, writer := range []bool{false, true} {
			if _, err = s.AcquireLease(ctx, "task", "holder-b", b, writer, later, time.Second); !errors.Is(err, ErrLeaseBusy) {
				t.Fatal("legacy alias bypass", expired, writer, err)
			}
		}
		observed, err := s.InspectLeases(ctx, b)
		if err != nil || len(observed) != 1 || observed[0].Scope != a || observed[0].Token != lease.Token {
			t.Fatal(observed, err)
		}
		if _, err = s.AcquireLease(ctx, "task", "unrelated", "other-resource", true, later, time.Second); err != nil {
			t.Fatal("generic scope changed", err)
		}
		if err = s.ReleaseLease(ctx, lease.Token, "writer-a"); err != nil {
			t.Fatal(err)
		}
		if _, err = s.AcquireLease(ctx, "task", "holder-b", b, true, later, time.Second); err != nil {
			t.Fatal("released alias blocked", err)
		}
	}
}
