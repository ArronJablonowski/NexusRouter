package hostresources

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/processguard"
	"github.com/ArronJablonowski/DarwinRouter/resources"
	_ "modernc.org/sqlite"
)

func fixture(t *testing.T, max int) (*Coordinator, *Coordinator, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "host.db")
	limits := resources.Limits{MaxConcurrent: max, RAMPercent: 100, VRAMPercent: 100, MaxAge: time.Minute}
	a, err := OpenPath(context.Background(), path, limits)
	if err != nil {
		t.Fatal(err)
	}
	b, err := OpenPath(context.Background(), path, limits)
	if err != nil {
		a.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	return a, b, path
}

func request(t *testing.T, id string, now time.Time) resources.ReservationRequest {
	t.Helper()
	owner, err := processguard.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return resources.ReservationRequest{Version: 1, ReservationID: id, HostScope: "local-host", Owner: resources.ReservationOwner{ProcessID: owner.ID, DaemonID: "daemon-a"}, TaskID: "task-" + id, SessionID: "session-" + id, ProviderID: "provider", ModelID: "model", Profile: "default", RAMBytes: 10, ContextTokens: 1024, ConfigDigest: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", RequestedAt: now.UTC(), TTL: time.Second}
}

func snapshot(now time.Time) resources.Snapshot {
	return resources.Snapshot{Time: now.UTC(), TotalRAM: 100, AvailableRAM: 100, CPUs: 8}
}

func TestIndependentStoresContendForOneSlot(t *testing.T) {
	a, b, _ := fixture(t, 1)
	now := time.Unix(100, 0).UTC()
	first := request(t, "first", now)
	second := request(t, "second", now)
	if _, err := a.Acquire(context.Background(), snapshot(now), first, now); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Acquire(context.Background(), snapshot(now), second, now); !errors.Is(err, resources.ErrCapacity) {
		t.Fatalf("second admission=%v", err)
	}
	status, err := b.Snapshot(context.Background(), now)
	if err != nil || status.Active != 1 || status.Expired != 0 {
		t.Fatalf("snapshot=%+v err=%v", status, err)
	}
}

func TestAcquireReplayAndDrift(t *testing.T) {
	a, b, _ := fixture(t, 2)
	now := time.Unix(200, 0).UTC()
	r := request(t, "replay", now)
	first, err := a.Acquire(context.Background(), snapshot(now), r, now)
	if err != nil {
		t.Fatal(err)
	}
	r.RequestedAt = now.Add(time.Second)
	again, err := b.Acquire(context.Background(), snapshot(now), r, now)
	if err != nil || again.RequestDigest != first.RequestDigest || !again.ExpiresAt.Equal(first.ExpiresAt) {
		t.Fatalf("replay=%+v err=%v", again, err)
	}
	drift := r
	drift.ContextTokens++
	if _, err = b.Acquire(context.Background(), snapshot(now), drift, now); !errors.Is(err, resources.ErrReservationConflict) {
		t.Fatalf("drift=%v", err)
	}
	var rows int
	if err = a.db.QueryRow("SELECT count(*) FROM reservations").Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("rows=%d err=%v", rows, err)
	}
}

func TestTerminalReplayNeverAuthorizes(t *testing.T) {
	a, _, _ := fixture(t, 1)
	now := time.Unix(250, 0).UTC()
	r := request(t, "terminal-replay", now)
	if _, err := a.Acquire(context.Background(), snapshot(now), r, now); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Release(context.Background(), r.ReservationID, r.Owner, now.Add(time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	r.RequestedAt = now.Add(2 * time.Millisecond)
	if _, err := a.Acquire(context.Background(), snapshot(r.RequestedAt), r, r.RequestedAt); !errors.Is(err, resources.ErrReservationExpired) {
		t.Fatalf("terminal replay returned authorization: %v", err)
	}
}

func TestAdaptiveStoresShareDerivedOneSlotTier(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "adaptive.db")
	limits := resources.Limits{MaxConcurrent: 4, RAMPercent: 100, VRAMPercent: 100, MaxAge: time.Minute}
	a, err := OpenPathAdaptive(context.Background(), path, limits)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := OpenPathAdaptive(context.Background(), path, limits)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	now := time.Unix(275, 0).UTC()
	small := resources.Snapshot{Time: now, TotalRAM: 12 << 30, AvailableRAM: 12 << 30, CPUs: 8}
	if _, err = a.Acquire(context.Background(), small, request(t, "adaptive-first", now), now); err != nil {
		t.Fatal(err)
	}
	if _, err = b.Acquire(context.Background(), small, request(t, "adaptive-second", now), now); !errors.Is(err, resources.ErrCapacity) {
		t.Fatalf("adaptive one-slot tier was bypassed: %v", err)
	}
}

func TestCoordinatorRejectsIncompatibleHostPolicy(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "policy.db")
	strict := resources.Limits{MaxConcurrent: 1, RAMPercent: 80, VRAMPercent: 80, MaxAge: time.Minute}
	first, err := OpenPath(context.Background(), path, strict)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	permissive := strict
	permissive.MaxConcurrent = 4
	if second, openErr := OpenPath(context.Background(), path, permissive); !errors.Is(openErr, resources.ErrReservationConflict) || second != nil {
		if second != nil {
			second.Close()
		}
		t.Fatalf("incompatible host policy opened: %v", openErr)
	}
	if adaptive, openErr := OpenPathAdaptive(context.Background(), path, strict); !errors.Is(openErr, resources.ErrReservationConflict) || adaptive != nil {
		if adaptive != nil {
			adaptive.Close()
		}
		t.Fatalf("fixed/adaptive policy mismatch opened: %v", openErr)
	}
}

func TestRenewReleaseOwnerFenceAndNoRevival(t *testing.T) {
	a, _, _ := fixture(t, 1)
	now := time.Unix(300, 0).UTC()
	r := request(t, "owner", now)
	binding, err := a.Acquire(context.Background(), snapshot(now), r, now)
	if err != nil {
		t.Fatal(err)
	}
	wrong := r.Owner
	wrong.DaemonID = "daemon-b"
	if _, err = a.Renew(context.Background(), r.ReservationID, wrong, now.Add(time.Millisecond), time.Second); !errors.Is(err, resources.ErrReservationOwner) {
		t.Fatalf("wrong renew=%v", err)
	}
	renewed, err := a.Renew(context.Background(), r.ReservationID, r.Owner, now.Add(500*time.Millisecond), time.Second)
	if err != nil || !renewed.ExpiresAt.After(binding.ExpiresAt) {
		t.Fatalf("renew=%+v err=%v", renewed, err)
	}
	if _, err = a.Release(context.Background(), r.ReservationID, wrong, now.Add(time.Second)); !errors.Is(err, resources.ErrReservationOwner) {
		t.Fatalf("wrong release=%v", err)
	}
	released, err := a.Release(context.Background(), r.ReservationID, r.Owner, now.Add(time.Second))
	if err != nil || released.State != resources.ReservationReleased {
		t.Fatalf("release=%+v err=%v", released, err)
	}
	if _, err = a.Renew(context.Background(), r.ReservationID, r.Owner, now.Add(time.Second), time.Second); !errors.Is(err, resources.ErrReservationExpired) {
		t.Fatalf("revival=%v", err)
	}
}

func TestExpiryBoundaryReuseAndClockRollback(t *testing.T) {
	a, b, _ := fixture(t, 1)
	now := time.Unix(400, 0).UTC()
	r := request(t, "expiring", now)
	if _, err := a.Acquire(context.Background(), snapshot(now), r, now); err != nil {
		t.Fatal(err)
	}
	before := request(t, "before", now.Add(time.Second-time.Millisecond))
	if _, err := b.Acquire(context.Background(), snapshot(before.RequestedAt), before, before.RequestedAt); !errors.Is(err, resources.ErrCapacity) {
		t.Fatalf("before expiry=%v", err)
	}
	at := request(t, "at", now.Add(time.Second))
	if _, err := b.Acquire(context.Background(), snapshot(at.RequestedAt), at, at.RequestedAt); err != nil {
		t.Fatalf("at expiry=%v", err)
	}
	status, err := a.Status(context.Background(), r.ReservationID, at.RequestedAt)
	if err != nil || status.State != resources.ReservationExpired {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	if _, err = a.Snapshot(context.Background(), now.Add(-MaxClockRollback-time.Nanosecond)); !errors.Is(err, ErrClockRollback) {
		t.Fatalf("rollback=%v", err)
	}
}

func TestPathOverrideMigrationAndCorruption(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "override.db")
	t.Setenv(environmentPath, path)
	resolved, err := Path()
	if err != nil || resolved != path {
		t.Fatalf("path=%q err=%v", resolved, err)
	}
	limits := resources.Limits{MaxConcurrent: 1, RAMPercent: 100, VRAMPercent: 100, MaxAge: time.Minute}
	c, err := Open(context.Background(), limits)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec("DROP INDEX reservations_capacity"); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	if reopened, err := Open(context.Background(), limits); err == nil || reopened != nil {
		if reopened != nil {
			reopened.Close()
		}
		t.Fatal("corrupt schema reopened")
	}
}

func TestSameVersionSchemaWithoutTableConstraintsFailsClosed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "constraints.db")
	limits := resources.Limits{MaxConcurrent: 1, RAMPercent: 100, VRAMPercent: 100, MaxAge: time.Minute}
	coordinator, err := OpenPath(context.Background(), path, limits)
	if err != nil {
		t.Fatal(err)
	}
	if err = coordinator.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec(`DROP TABLE coordinator_clock;
		CREATE TABLE coordinator_clock(singleton INTEGER PRIMARY KEY,last_seen_ns INTEGER NOT NULL);
		INSERT INTO coordinator_clock VALUES(1,0)`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, openErr := OpenPath(context.Background(), path, limits); openErr == nil || reopened != nil {
		if reopened != nil {
			reopened.Close()
		}
		t.Fatal("same-version schema without clock constraints reopened")
	}
}

func TestPositiveProcessProofRecovery(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "recovery.db")
	command := exec.Command(os.Args[0], "-test.run=^TestRecoveryOwnerProcess$")
	command.Env = append(os.Environ(), "DARWIN_HOSTRESOURCE_RECOVERY_HELPER="+path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("owner process: %v %s", err, output)
	}
	limits := resources.Limits{MaxConcurrent: 1, RAMPercent: 100, VRAMPercent: 100, MaxAge: time.Minute}
	coordinator, err := OpenPath(context.Background(), path, limits)
	if err != nil {
		t.Fatal(err)
	}
	defer coordinator.Close()
	status, err := coordinator.Recover(context.Background(), "recoverable", time.Unix(601, 0).UTC())
	if err != nil || status.State != resources.ReservationExpired {
		t.Fatalf("recovery=%+v err=%v", status, err)
	}
	var receipts, events int
	if coordinator.db.QueryRow("SELECT count(*) FROM reservation_recoveries").Scan(&receipts) != nil || coordinator.db.QueryRow("SELECT count(*) FROM reservation_events WHERE kind='recovered'").Scan(&events) != nil || receipts != 1 || events != 1 {
		t.Fatalf("receipts=%d events=%d", receipts, events)
	}
}

func TestRecoveryOwnerProcess(t *testing.T) {
	path := os.Getenv("DARWIN_HOSTRESOURCE_RECOVERY_HELPER")
	if path == "" {
		t.Skip("helper only")
	}
	limits := resources.Limits{MaxConcurrent: 1, RAMPercent: 100, VRAMPercent: 100, MaxAge: time.Minute}
	coordinator, err := OpenPath(context.Background(), path, limits)
	if err != nil {
		t.Fatal(err)
	}
	defer coordinator.Close()
	now := time.Unix(600, 0).UTC()
	r := request(t, "recoverable", now)
	if _, err = coordinator.Acquire(context.Background(), snapshot(now), r, now); err != nil {
		t.Fatal(err)
	}
}

type nativeProcessResult struct {
	Result    string `json:"result"`
	ProcessID string `json:"process_id,omitempty"`
}

type nativeProcess struct {
	command *exec.Cmd
	input   io.WriteCloser
	output  *json.Decoder
	stderr  *bytes.Buffer
}

func startNativeProcess(t *testing.T, database, owners, mode, id, staleOwner string) nativeProcess {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestNativeProcessHelper$")
	command.Env = append(os.Environ(),
		"DARWIN_HOSTRESOURCE_NATIVE_HELPER=1",
		"DARWIN_HOSTRESOURCE_NATIVE_DB="+database,
		"DARWIN_PROCESS_OWNER_DIR="+owners,
		"DARWIN_HOSTRESOURCE_NATIVE_MODE="+mode,
		"DARWIN_HOSTRESOURCE_NATIVE_ID="+id,
		"DARWIN_HOSTRESOURCE_STALE_OWNER="+staleOwner)
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := &bytes.Buffer{}
	command.Stderr = stderr
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	return nativeProcess{command: command, input: input, output: json.NewDecoder(output), stderr: stderr}
}

func finishNativeProcess(t *testing.T, process nativeProcess) nativeProcessResult {
	t.Helper()
	var result nativeProcessResult
	if err := process.output.Decode(&result); err != nil {
		_ = process.command.Process.Kill()
		_ = process.command.Wait()
		t.Fatalf("decode helper: %v %s", err, process.stderr.String())
	}
	if err := process.command.Wait(); err != nil {
		t.Fatalf("wait helper: %v %s", err, process.stderr.String())
	}
	return result
}

func TestTwoNativeProcessesRaceForOneSlot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(root, "race.db")
	first := startNativeProcess(t, database, filepath.Join(root, "owners-one"), "race", "one", "")
	second := startNativeProcess(t, database, filepath.Join(root, "owners-two"), "race", "two", "")
	if _, err := first.input.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	_ = first.input.Close()
	if _, err := second.input.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	_ = second.input.Close()
	results := []nativeProcessResult{finishNativeProcess(t, first), finishNativeProcess(t, second)}
	counts := map[string]int{}
	for _, result := range results {
		counts[result.Result]++
	}
	if counts["admitted"] != 1 || counts["capacity"] != 1 || len(counts) != 2 {
		t.Fatalf("race results=%v", counts)
	}
}

func TestSIGKILLFencesUntilPositiveRecovery(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(root, "kill.db")
	holder := startNativeProcess(t, database, filepath.Join(root, "owners-holder"), "hold", "held", "")
	if _, err := holder.input.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	var held nativeProcessResult
	if err := holder.output.Decode(&held); err != nil {
		t.Fatal(err)
	}
	if held.Result != "held" || held.ProcessID == "" {
		t.Fatalf("held=%+v", held)
	}
	contender := startNativeProcess(t, database, filepath.Join(root, "owners-before"), "race", "before", "")
	if _, err := contender.input.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	_ = contender.input.Close()
	if result := finishNativeProcess(t, contender); result.Result != "capacity" {
		t.Fatalf("before kill=%+v", result)
	}
	if err := holder.command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := holder.command.Wait(); err == nil {
		t.Fatal("SIGKILL reported success")
	}
	_ = holder.input.Close()
	afterKill := startNativeProcess(t, database, filepath.Join(root, "owners-after-kill"), "race", "after-kill", "")
	if _, err := afterKill.input.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	_ = afterKill.input.Close()
	if result := finishNativeProcess(t, afterKill); result.Result != "capacity" {
		t.Fatalf("dead owner was silently reclaimed=%+v", result)
	}
	recoverer := startNativeProcess(t, database, filepath.Join(root, "owners-recover"), "recover", "after", "")
	if _, err := recoverer.input.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	_ = recoverer.input.Close()
	if result := finishNativeProcess(t, recoverer); result.Result != "recovered_admitted" {
		t.Fatalf("recovery=%+v", result)
	}
	stale := startNativeProcess(t, database, filepath.Join(root, "owners-stale"), "stale", "held", held.ProcessID)
	if _, err := stale.input.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	_ = stale.input.Close()
	if result := finishNativeProcess(t, stale); result.Result != "stale_denied" {
		t.Fatalf("stale=%+v", result)
	}
}

func TestNativeProcessHelper(t *testing.T) {
	if os.Getenv("DARWIN_HOSTRESOURCE_NATIVE_HELPER") != "1" {
		t.Skip("helper only")
	}
	var gate [1]byte
	if _, err := io.ReadFull(os.Stdin, gate[:]); err != nil {
		t.Fatal(err)
	}
	database, mode, id := os.Getenv("DARWIN_HOSTRESOURCE_NATIVE_DB"), os.Getenv("DARWIN_HOSTRESOURCE_NATIVE_MODE"), os.Getenv("DARWIN_HOSTRESOURCE_NATIVE_ID")
	limits := resources.Limits{MaxConcurrent: 1, RAMPercent: 100, VRAMPercent: 100, MaxAge: time.Minute}
	coordinator, err := OpenPath(context.Background(), database, limits)
	if err != nil {
		t.Fatal(err)
	}
	defer coordinator.Close()
	now := time.Now().UTC()
	ownerRef, err := processguard.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	owner := resources.ReservationOwner{ProcessID: ownerRef.ID, DaemonID: "native-daemon-" + id}
	makeRequest := func(reservationID string, owner resources.ReservationOwner) resources.ReservationRequest {
		return resources.ReservationRequest{Version: 1, ReservationID: reservationID, HostScope: "native-host", Owner: owner, TaskID: "task-" + reservationID, SessionID: "session-" + reservationID, ProviderID: "provider", ModelID: "model", Profile: "default", RAMBytes: 10, ContextTokens: 1024, ConfigDigest: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", RequestedAt: now, TTL: 2 * time.Minute}
	}
	result := nativeProcessResult{Result: "error"}
	switch mode {
	case "race":
		_, err = coordinator.Acquire(context.Background(), snapshot(now), makeRequest(id, owner), now)
		if err == nil {
			result.Result = "admitted"
		} else if errors.Is(err, resources.ErrCapacity) {
			result.Result = "capacity"
		}
	case "hold":
		_, err = coordinator.Acquire(context.Background(), snapshot(now), makeRequest(id, owner), now)
		if err == nil {
			result.Result, result.ProcessID = "held", ownerRef.ID
		}
		_ = json.NewEncoder(os.Stdout).Encode(result)
		if err != nil {
			return
		}
		_, _ = io.Copy(io.Discard, os.Stdin)
		return
	case "recover":
		var recovered int
		if recovered, err = coordinator.RecoverStopped(context.Background(), now); err == nil && recovered == 1 {
			_, err = coordinator.Acquire(context.Background(), snapshot(now), makeRequest(id, owner), now)
			if err == nil {
				result.Result = "recovered_admitted"
			}
		}
	case "stale":
		staleOwner := resources.ReservationOwner{ProcessID: os.Getenv("DARWIN_HOSTRESOURCE_STALE_OWNER"), DaemonID: "native-daemon-held"}
		_, renewErr := coordinator.Renew(context.Background(), "held", staleOwner, now, time.Minute)
		_, releaseErr := coordinator.Release(context.Background(), "held", staleOwner, now)
		if errors.Is(renewErr, resources.ErrReservationOwner) && errors.Is(releaseErr, resources.ErrReservationOwner) {
			result.Result = "stale_denied"
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		t.Fatal(err)
	}
}
