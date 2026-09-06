package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/approvals"
	"github.com/ArronJablonowski/DarwinRouter/internal/processguard"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func TestLeaseProcessBindingAndStoreClose(t *testing.T) {
	s, path := leaseStore(t)
	ctx := context.Background()
	lease, err := s.AcquireLease(ctx, "task", "owner", "scope", false, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := processguard.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var id string
	var body []byte
	if err = s.db.QueryRow(`SELECT process_id,body FROM resource_leases JOIN lease_processes ON id=process_id WHERE token=?`, lease.Token).Scan(&id, &body); err != nil {
		t.Fatal(err)
	}
	expected, _ := json.Marshal(ref)
	if id != ref.ID || string(body) != string(expected) {
		t.Fatal("process binding mismatch")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.RenewLease(ctx, lease.Token, lease.Owner, time.Now(), time.Minute); err != nil {
		t.Fatal(err)
	}
	if err = s.ReleaseLease(ctx, lease.Token, lease.Owner); err != nil {
		t.Fatal(err)
	}
}

func TestLeaseProcessCorruptMetadataFencesMutations(t *testing.T) {
	s, _ := leaseStore(t)
	ctx := context.Background()
	lease, err := s.AcquireLease(ctx, "task", "owner", "scope", false, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE lease_processes SET body='{}'`); err != nil {
		t.Fatal(err)
	}
	if err = s.RenewLease(ctx, lease.Token, lease.Owner, time.Now(), time.Minute); !errors.Is(err, ErrLeaseLost) {
		t.Fatal(err)
	}
	if err = s.ReleaseLease(ctx, lease.Token, lease.Owner); !errors.Is(err, ErrLeaseLost) {
		t.Fatal(err)
	}
	e := event("worker", 2, runtime.WorkerStarted)
	e.WorkerID = "owner"
	if err = s.AppendWorker(ctx, 1, e, lease.Token, lease.Owner, "", ""); !errors.Is(err, runtime.ErrExecutionLeaseLost) {
		t.Fatal(err)
	}
	e = event("finish", 2, runtime.TaskCanceled)
	e.WorkerID = "owner"
	if err = s.FinishLeased(ctx, 1, e, lease.Token, lease.Owner); !errors.Is(err, runtime.ErrExecutionLeaseLost) {
		t.Fatal(err)
	}
	if _, err = s.AcquireLease(ctx, "task", "other", "other", false, time.Now(), time.Minute); !errors.Is(err, ErrLeaseLost) {
		t.Fatal(err)
	}
	var released, count int
	if err = s.db.QueryRow(`SELECT count(*),sum(released) FROM resource_leases`).Scan(&count, &released); err != nil || count != 1 || released != 0 {
		t.Fatal(count, released, err)
	}
	events, err := s.Read(ctx, "task", 0, 100)
	if err != nil || len(events) != 1 {
		t.Fatal(events, err)
	}
}

func TestLeaseProcessMigrationPreservesLegacyUnknown(t *testing.T) {
	s, path := leaseStore(t)
	ctx := context.Background()
	if _, err := s.db.Exec(`DROP TABLE learning_activation_intents; DROP TABLE lease_attention_history; DROP TABLE lease_attention; DROP TABLE lease_recoveries; ALTER TABLE resource_leases DROP COLUMN process_id; DROP TABLE lease_processes; PRAGMA user_version=21`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO resource_leases(token,task_id,owner,scope,writer,expires) VALUES('legacy','task','owner','scope',0,?)`, time.Now().Add(time.Minute).UnixNano()); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var unknown bool
	var count, version int
	if err = s.db.QueryRow(`SELECT process_id IS NULL FROM resource_leases WHERE token='legacy'`).Scan(&unknown); err != nil || !unknown {
		t.Fatal(unknown, err)
	}
	if err = s.db.QueryRow(`SELECT count(*) FROM lease_processes`).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	if err = s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 26 {
		t.Fatal(version, err)
	}
	if err = s.RenewLease(ctx, "legacy", "owner", time.Now(), time.Minute); err != nil {
		t.Fatal(err)
	}
	if err = s.ReleaseLease(ctx, "legacy", "owner"); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow(`SELECT process_id IS NULL FROM resource_leases WHERE token='legacy'`).Scan(&unknown); err != nil || !unknown {
		t.Fatal("legacy backfilled", err)
	}
}

func TestLeaseProcessRegistrationRollsBackWithLease(t *testing.T) {
	s, _ := leaseStore(t)
	ctx := context.Background()
	if _, err := s.db.Exec(`CREATE TRIGGER refuse_lease BEFORE INSERT ON resource_leases BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcquireLease(ctx, "task", "owner", "scope", false, time.Now(), time.Minute); err == nil {
		t.Fatal("accepted")
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM lease_processes`).Scan(&count); err != nil || count != 0 {
		t.Fatal("orphan process metadata", count, err)
	}
}

func TestLeaseProcessForeignHelper(t *testing.T) {
	if os.Getenv("DARWIN_FOREIGN_LEASE_HELPER") != "1" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := Open(ctx, os.Getenv("DARWIN_FOREIGN_LEASE_DB"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	token := os.Getenv("DARWIN_FOREIGN_LEASE_TOKEN")
	if id := os.Getenv("DARWIN_FOREIGN_APPROVAL_ID"); id != "" {
		r, err := s.ReadApproval(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.ConsumeApproval(ctx, r.Request, token, "owner", r.Request.CreatedAt.Add(2*time.Second)); !errors.Is(err, approvals.ErrConflict) {
			t.Fatal("foreign approval consume", err)
		}
		return
	}
	if err = s.RenewLease(ctx, token, "owner", time.Now(), time.Minute); !errors.Is(err, ErrLeaseLost) {
		t.Fatal("foreign renew", err)
	}
	if err = s.ReleaseLease(ctx, token, "owner"); !errors.Is(err, ErrLeaseLost) {
		t.Fatal("foreign release", err)
	}
	e := event("worker", 2, runtime.WorkerStarted)
	e.WorkerID = "owner"
	if err = s.AppendWorker(ctx, 1, e, token, "owner", "", ""); !errors.Is(err, runtime.ErrExecutionLeaseLost) {
		t.Fatal("foreign append", err)
	}
	e = event("finish", 2, runtime.TaskCanceled)
	e.WorkerID = "owner"
	if err = s.FinishLeased(ctx, 1, e, token, "owner"); !errors.Is(err, runtime.ErrExecutionLeaseLost) {
		t.Fatal("foreign finish", err)
	}
}

func TestLeaseProcessRejectsOtherProcessWithKnownToken(t *testing.T) {
	s, path := leaseStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	lease, err := s.AcquireLease(ctx, "task", "owner", "scope", false, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLeaseProcessForeignHelper$")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "DARWIN_PROCESS_OWNER_DIR=" + filepath.Join(t.TempDir(), "owners"), "DARWIN_FOREIGN_LEASE_HELPER=1", "DARWIN_FOREIGN_LEASE_DB=" + path, "DARWIN_FOREIGN_LEASE_TOKEN=" + lease.Token}
	cmd.WaitDelay = time.Second
	if err = cmd.Run(); err != nil {
		t.Fatal("foreign process fixture failed", err)
	}
	var released int
	if err = s.db.QueryRow(`SELECT released FROM resource_leases WHERE token=?`, lease.Token).Scan(&released); err != nil || released != 0 {
		t.Fatal(released, err)
	}
	events, err := s.Read(ctx, "task", 0, 100)
	if err != nil || len(events) != 1 {
		t.Fatal(events, err)
	}
	if err = s.ReleaseLease(ctx, lease.Token, lease.Owner); err != nil {
		t.Fatal("owner lost authority", err)
	}
}

func TestLeaseProcessForeignApprovalConsumption(t *testing.T) {
	s, path, req := approvalFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := s.RequestApproval(ctx, req); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DecideBoundApproval(ctx, approvals.Command{Expected: req, ID: "decision", Allowed: true}, "operator", req.CreatedAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	lease, err := s.AcquireLease(ctx, req.TaskID, "owner", req.Scope, true, req.CreatedAt.Add(time.Second), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLeaseProcessForeignHelper$")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "DARWIN_PROCESS_OWNER_DIR=" + filepath.Join(t.TempDir(), "owners"), "DARWIN_FOREIGN_LEASE_HELPER=1", "DARWIN_FOREIGN_LEASE_DB=" + path, "DARWIN_FOREIGN_LEASE_TOKEN=" + lease.Token, "DARWIN_FOREIGN_APPROVAL_ID=" + req.ID}
	cmd.WaitDelay = time.Second
	if err = cmd.Run(); err != nil {
		t.Fatal("foreign approval fixture failed", err)
	}
	r, err := s.ReadApproval(ctx, req.ID)
	if err != nil || r.State != approvals.Approved {
		t.Fatal(r, err)
	}
	if _, err = s.ConsumeApproval(ctx, req, lease.Token, lease.Owner, req.CreatedAt.Add(2*time.Second)); err != nil {
		t.Fatal("owner consume", err)
	}
}
