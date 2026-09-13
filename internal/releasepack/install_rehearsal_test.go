package releasepack

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/daemon"
	"github.com/ArronJablonowski/DarwinRouter/internal/stateschema"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/memory"
	darwinruntime "github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/webui"
	_ "modernc.org/sqlite"
)

const rehearsalVersion = "0.0.0-install-rehearsal"

// TestNativeInstallMigrationRehearsal exercises a built command through the
// release archive contract. Every path, process and record belongs to T.TempDir;
// the test neither discovers nor opens an operator configuration or database.
// A real release still needs a separately obtained previous release binary.
func TestNativeInstallMigrationRehearsal(t *testing.T) {
	if testing.Short() {
		t.Skip("native daemon rehearsal")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	source, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	built := filepath.Join(root, "built-darwin")
	env := append(environment(), "GOCACHE="+filepath.Join(root, "go-cache"), "GOPROXY=off", "GOSUMDB=off", "GOOS="+runtime.GOOS, "GOARCH="+runtime.GOARCH)
	if _, err = command(ctx, source, env, "go", "build", "-mod=readonly", "-trimpath", "-buildvcs=false", "-ldflags=-buildid= -X main.version="+rehearsalVersion, "-o", built, "./cmd/darwin"); err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(built)
	if err != nil {
		t.Fatal(err)
	}
	notice, err := renderThirdPartyNotices(runtime.GOOS, runtime.GOARCH, []noticeModule{{Path: "example.invalid/rehearsal", Version: "v1.0.0", Files: []noticeFile{{Name: "LICENSE", Body: []byte("Synthetic test-only license.\n")}}}})
	if err != nil {
		t.Fatal(err)
	}
	toolchain, err := embeddedGoToolchainModule()
	if err != nil {
		t.Fatal(err)
	}
	assets, err := readSBOMSourceFiles(source)
	if err != nil {
		t.Fatal(err)
	}
	binaryDigest := sha256.Sum256(binary)
	sbom, err := renderTargetSBOM(TargetSBOMOptions{
		Version: rehearsalVersion, Commit: strings.Repeat("0", 40), TargetOS: runtime.GOOS, TargetArch: runtime.GOARCH,
		Created: "2026-09-13T18:00:00Z", BinarySHA256: hex.EncodeToString(binaryDigest[:]),
	}, []noticeModule{{Path: "example.invalid/rehearsal", Version: "v1.0.0", Files: []noticeFile{{Name: "LICENSE", Body: []byte("Synthetic test-only license.\n")}}}, toolchain}, assets)
	if err != nil {
		t.Fatal(err)
	}
	entries, metadata, err := releaseEntries(collateral{
		install: []byte("Synthetic installation fixture.\n"),
		license: []byte("Synthetic project license fixture.\n"),
		notes:   []byte("Synthetic release notes fixture.\n"),
		config:  []byte("version: 1\nmode: local_only\n"),
	}, notice, sbom, binary)
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "DarwinRouter_"+rehearsalVersion+"_"+runtime.GOOS+"_"+runtime.GOARCH+".tar.gz")
	f, err := os.OpenFile(archive, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	err = Archive(f, entries)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
	artifact := Artifact{OS: runtime.GOOS, Arch: runtime.GOARCH, File: filepath.Base(archive), SHA256: fileDigest(t, archive), Entries: metadata}
	rehearseNativeInstallAndMigration(t, ctx, source, root, archive, artifact, rehearsalVersion, strings.Repeat("0", 40), filepath.Join(root, "install-rehearsal-evidence.json"))
}

// rehearseNativeInstallAndMigration is also suitable for the native artifact
// selected from a previously verified release directory. Verification and key
// trust remain the caller's responsibility.
func rehearseNativeInstallAndMigration(t *testing.T, ctx context.Context, source, root, archive string, artifact Artifact, version, commit, evidenceOut string) {
	t.Helper()
	if artifact.OS != runtime.GOOS || artifact.Arch != runtime.GOARCH || validate(Options{Version: version, Commit: commit, Out: "evidence"}) != nil || evidenceOut == "" {
		t.Fatal("rehearsal requires the exact native release artifact")
	}
	archiveBody, archiveRawDigest, err := readPinnedRehearsalArchive(archive, artifact)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.SHA256 != archiveRawDigest {
		t.Fatal("native release artifact digest mismatch")
	}
	archiveDigest := "sha256:" + archiveRawDigest
	binary := rehearsalArchiveBinary(t, archiveBody, artifact)
	assertEmbeddedWebUIAssets(t, source, binary)
	installRoot := filepath.Join(root, "installation")
	installPrefix := filepath.Join(installRoot, "releases", version)
	for _, path := range []string{installRoot, filepath.Join(installRoot, "releases"), installPrefix, filepath.Join(installPrefix, "bin")} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal("exclusive install prefix", err)
		}
	}
	installed := filepath.Join(installPrefix, "bin", "darwin")
	if err := writeExclusive(installed, binary, 0755); err != nil {
		t.Fatal(err)
	}
	assertMode(t, installed, 0755)
	if got, err := runInstalled(ctx, installed, root, nil, nil, "version"); err != nil || strings.TrimSpace(got) != "darwin "+version {
		t.Fatalf("installed version mismatch: %q: %v", got, err)
	}

	privateRoot := filepath.Join(root, "private")
	stateRoot, ownerRoot := filepath.Join(privateRoot, "state"), filepath.Join(privateRoot, "process-owners")
	for _, path := range []string{privateRoot, stateRoot, ownerRoot} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	database := filepath.Join(stateRoot, "darwin.db")
	configuration := filepath.Join(privateRoot, "config.yaml")
	address := freeLoopbackAddress(t)
	writeRehearsalConfig(t, configuration, database, address)
	assertMode(t, privateRoot, 0700)
	assertMode(t, stateRoot, 0700)
	assertMode(t, ownerRoot, 0700)
	assertMode(t, configuration, 0600)
	runtimeEnv := []string{"DARWIN_API_TOKEN=" + strings.Repeat("install-rehearsal-token-", 2), "DARWIN_PROCESS_OWNER_DIR=" + ownerRoot, "HOME=" + privateRoot, "TMPDIR=" + root, "PATH=" + os.Getenv("PATH"), "LANG=C", "LC_ALL=C", "TZ=UTC"}
	if got, err := runInstalled(ctx, installed, root, runtimeEnv, nil, "config", "validate", "--config", configuration); err != nil || strings.TrimSpace(got) != "Configuration valid (version 1)" {
		t.Fatalf("installed configuration validation: %q: %v", got, err)
	}
	runOwnedDaemon(t, ctx, source, installed, configuration, address, root, runtimeEnv)
	assertMode(t, database, 0600)
	checkDatabase(t, database, stateschema.Current, "", "")
	seedRehearsalEvents(t, database)

	fact := memory.Fact{Version: 1, ID: "install-rehearsal", Scope: "release-rehearsal", Revision: 1, Content: "Synthetic schema migration evidence.", Provenance: "DAR-52 disposable fixture", Confidence: 1, Privacy: "local_only", Created: time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC), Updated: time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)}
	factBody, err := json.Marshal(fact)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := runInstalled(ctx, installed, root, runtimeEnv, factBody, "memory", "put", "--db", database, "--scope", fact.Scope); err != nil || !strings.Contains(got, fact.ID) {
		t.Fatalf("seed synthetic record: %q: %v", got, err)
	}
	evidenceBefore := databaseEvidence(t, database, fact.Scope, fact.ID)
	downgradeFixtureToSchema29(t, database)
	checkDatabase(t, database, 29, fact.Scope, fact.ID)
	if got := databaseEvidence(t, database, fact.Scope, fact.ID); got != evidenceBefore {
		t.Fatal("schema-29 fixture changed synthetic evidence")
	}
	timingEpoch := taskTimingEpoch(t, database)
	assertQuiescent(t, database)
	backup := filepath.Join(privateRoot, "backups", "darwin-schema29.db")
	if err := os.Mkdir(filepath.Dir(backup), 0700); err != nil {
		t.Fatal(err)
	}
	checkpointAndCopy(t, database, backup)
	assertMode(t, backup, 0600)
	backupDigest := "sha256:" + fileDigest(t, backup)
	checkDatabase(t, backup, 29, fact.Scope, fact.ID)
	if got := taskTimingEpoch(t, backup); got != timingEpoch {
		t.Fatal("schema-29 backup changed task timing epoch")
	}

	address = freeLoopbackAddress(t)
	writeRehearsalConfig(t, configuration, database, address)
	runOwnedDaemon(t, ctx, source, installed, configuration, address, root, runtimeEnv)
	checkDatabase(t, database, stateschema.Current, fact.Scope, fact.ID)
	if got := databaseEvidence(t, database, fact.Scope, fact.ID); got != evidenceBefore {
		t.Fatal("migration changed synthetic evidence")
	}
	if got := taskTimingEpoch(t, database); got != timingEpoch {
		t.Fatal("current-schema migration changed schema-29 task timing epoch")
	}
	assertEmptyUsageLedger(t, database)
	assertMemoryCLI(t, ctx, installed, root, runtimeEnv, database, fact)

	rollback := filepath.Join(stateRoot, "rollback-schema29.db")
	copyExclusive(t, backup, rollback)
	if "sha256:"+fileDigest(t, rollback) != backupDigest {
		t.Fatal("rollback copy differs from immutable backup")
	}
	checkDatabase(t, rollback, 29, fact.Scope, fact.ID)
	if got := taskTimingEpoch(t, rollback); got != timingEpoch {
		t.Fatal("schema-29 rollback copy changed task timing epoch")
	}
	assertMemoryCLI(t, ctx, installed, root, runtimeEnv, rollback, fact)
	checkDatabase(t, rollback, 29, fact.Scope, fact.ID)                  // Read-only inspection must not migrate.
	checkDatabase(t, database, stateschema.Current, fact.Scope, fact.ID) // Rollback must not overwrite the upgraded store.
	rollbackDigest := "sha256:" + fileDigest(t, rollback)
	if rollbackDigest != backupDigest {
		t.Fatal("rollback smoke changed the restored database")
	}
	record := InstallRehearsalEvidence{
		SchemaVersion: installEvidenceSchema, Scope: installEvidenceScope,
		Release:      InstallEvidenceRelease{Version: version, Commit: commit},
		Target:       NativeEvidenceTarget{OS: artifact.OS, Arch: artifact.Arch},
		Artifact:     InstallEvidenceArtifact{Name: artifact.File, SHA256: archiveDigest},
		Installation: InstallEvidenceInstall{BinaryVersion: version, PrivatePermissions: "passed", Configuration: "passed", DaemonStart: "passed", ExactWriterStop: "passed"},
		Source:       InstallEvidenceSource{Schema: 29, QuickCheck: "ok", Quiescence: "passed"},
		Backup:       InstallEvidenceBackup{SHA256: backupDigest, Schema: 29, QuickCheck: "ok"},
		Migration: InstallEvidenceMigration{Schema: stateschema.Current, QuickCheck: "ok", PreservedRecordSHA256: "sha256:" + evidenceBefore,
			TaskTimingPreserved: "passed", LegacyUsageNotFabricated: "passed"},
		Rollback: InstallEvidenceRollback{DatabaseSHA256: rollbackDigest, Schema: 29, Pairing: "current_binary_read_only_schema_fixture", BinaryVersion: version,
			TargetOS: artifact.OS, TargetArch: artifact.Arch, Smoke: "passed"},
	}
	if err := retainInstallRehearsalEvidence(source, evidenceOut, record); err != nil {
		t.Fatal("retain install rehearsal evidence", err)
	}
	body, err := readInstallEvidence(evidenceOut)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = VerifyInstallRehearsalEvidence(evidenceOut, InstallRehearsalExpectations{
		RecordSHA256: installEvidenceDigest(body), Version: version, Commit: commit,
		TargetOS: artifact.OS, TargetArch: artifact.Arch, ArtifactName: artifact.File,
		ArtifactSHA256: archiveDigest, SourceSchema: 29, CurrentSchema: stateschema.Current, BackupSHA256: backupDigest,
	}); err != nil {
		t.Fatal("verify retained install rehearsal evidence", err)
	}
	t.Logf("native install=%s schema=%d; backup_sha256=%s; migration=29->%d; rollback_copy_schema=29", installPrefix, stateschema.Current, backupDigest, stateschema.Current)
}

func seedRehearsalEvents(t *testing.T, path string) {
	t.Helper()
	db, err := telemetry.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 9, 7, 12, 1, 0, 0, time.UTC)
	events := []darwinruntime.Event{
		{Version: 1, ID: "install-rehearsal-start", TaskID: "install-rehearsal-task", SessionID: "install-rehearsal-session", CorrelationID: "install-rehearsal-task", Sequence: 1, Time: now, Kind: darwinruntime.TaskStarted},
		{Version: 1, ID: "install-rehearsal-complete", TaskID: "install-rehearsal-task", SessionID: "install-rehearsal-session", CorrelationID: "install-rehearsal-task", Sequence: 2, Time: now.Add(time.Second), Kind: darwinruntime.TaskCompleted, CausationID: "install-rehearsal-start"},
	}
	for i, event := range events {
		if err := db.Append(context.Background(), int64(i), event); err != nil {
			t.Fatal(err)
		}
	}
}

func readPinnedRehearsalArchive(archive string, artifact Artifact) ([]byte, string, error) {
	if filepath.Base(archive) != artifact.File || filepath.Base(artifact.File) != artifact.File {
		return nil, "", errors.New("archive path does not match authenticated artifact name")
	}
	before, err := os.Lstat(archive)
	if err != nil || !before.Mode().IsRegular() || before.Size() < 1 || before.Size() > maxArchive {
		return nil, "", errors.New("unsafe rehearsal archive")
	}
	f, err := os.Open(archive)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) || before.Size() != after.Size() {
		return nil, "", errors.New("rehearsal archive identity changed")
	}
	body, err := io.ReadAll(io.LimitReader(f, maxArchive+1))
	if err != nil || int64(len(body)) != after.Size() {
		return nil, "", errors.New("cannot snapshot rehearsal archive")
	}
	digest := sha256.Sum256(body)
	return body, hex.EncodeToString(digest[:]), nil
}

func rehearsalArchiveBinary(t *testing.T, archive []byte, artifact Artifact) []byte {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	if len(artifact.Entries) != len(archiveContract) {
		t.Fatal("archive contract mismatch")
	}
	var binary []byte
	for i, contract := range archiveContract {
		header, err := reader.Next()
		if err != nil || !canonicalArchiveHeader(header, contract.name, int64(contract.mode), contract.max) {
			t.Fatal("invalid archive entry", contract.name, err)
		}
		body, err := io.ReadAll(io.LimitReader(reader, contract.max+1))
		if err != nil || int64(len(body)) != header.Size || !validEntryMetadata(artifact.Entries[i], i, body) {
			t.Fatal("invalid archive payload", contract.name, err)
		}
		if contract.name == noticeName && validateNotice(body, artifact.OS, artifact.Arch) != nil {
			t.Fatal("invalid notice payload")
		}
		if contract.name == "darwin" {
			binary = body
		}
	}
	if _, err := reader.Next(); err != io.EOF || len(binary) == 0 {
		t.Fatal("invalid archive termination", err)
	}
	return binary
}

// assertEmbeddedWebUIAssets binds the complete checked-in frontend inventory to
// the executable member whose digest is already covered by the release manifest
// and SHA256SUMS. It discovers the directory so qualification covers every
// checked-in asset, including future additions.
func assertEmbeddedWebUIAssets(t *testing.T, source string, binary []byte) string {
	t.Helper()
	assetRoot := filepath.Join(source, "webui", "assets", webui.ShellAssetVersion)
	entries, err := os.ReadDir(assetRoot)
	if err != nil || len(entries) == 0 {
		t.Fatal("cannot inventory release WebUI assets", err)
	}
	hash := sha256.New()
	for _, entry := range entries {
		info, infoErr := entry.Info()
		if infoErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			t.Fatal("unsafe WebUI release asset", entry.Name(), infoErr)
		}
		body, readErr := os.ReadFile(filepath.Join(assetRoot, entry.Name()))
		if readErr != nil || len(body) == 0 || !bytes.Contains(binary, body) {
			t.Fatal("release binary does not contain exact WebUI asset", entry.Name(), readErr)
		}
		_, _ = hash.Write([]byte("assets/" + webui.ShellAssetVersion + "/" + entry.Name()))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(body)
		_, _ = hash.Write([]byte{0})
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if embeddedDigest, err := webui.ShellAssetDigest(); err != nil || embeddedDigest == "" {
		t.Fatal("release WebUI embedded manifest unavailable", embeddedDigest, err)
	}
	return digest
}

func runOwnedDaemon(t *testing.T, ctx context.Context, source, binary, configuration, address, dir string, env []string) {
	t.Helper()
	instance := strings.Repeat("a", 64)
	cmd := exec.CommandContext(ctx, binary, "serve", "--config", configuration, "--instance-id", instance)
	cmd.Dir, cmd.Env, cmd.WaitDelay = dir, env, 2*time.Second
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	stopped := false
	defer func() {
		if !stopped {
			_ = cmd.Process.Kill()
			<-done
		}
	}()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	var status daemon.Status
	for status.State != "ready" {
		select {
		case err := <-done:
			stopped = true
			t.Fatalf("daemon exited before readiness: %v: %s", err, output.String())
		case <-deadline.C:
			t.Fatal("daemon readiness timeout")
		case <-time.After(50 * time.Millisecond):
			body, err := runInstalled(ctx, binary, dir, env, nil, "daemon", "status", "--config", configuration)
			if err == nil {
				_ = json.Unmarshal([]byte(body), &status)
			}
		}
	}
	if status.Validate() != nil || status.InstanceID != instance {
		t.Fatal("invalid daemon status", status)
	}
	assertInstalledWebUISmoke(t, ctx, source, address)
	body, err := runInstalled(ctx, binary, dir, env, nil, "daemon", "stop", "--config", configuration)
	var stopping daemon.Status
	if err != nil || json.Unmarshal([]byte(body), &stopping) != nil || stopping.Validate() != nil || stopping.InstanceID != instance || stopping.State != "stopping" {
		t.Fatal("daemon stop failed", err, body)
	}
	select {
	case err := <-done:
		stopped = true
		if err != nil {
			t.Fatalf("owned daemon did not stop cleanly: %v: %s", err, output.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("owned daemon did not exit after stop")
	}
}

func assertInstalledWebUISmoke(t *testing.T, ctx context.Context, source, address string) {
	t.Helper()
	client := &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return errors.New("release WebUI smoke must not redirect")
	}}
	for endpoint, name := range map[string]string{
		"/app/bootstrap":                  "bootstrap.html",
		"/app/bootstrap/v1/bootstrap.css": "bootstrap.css",
		"/app/bootstrap/v1/bootstrap.js":  "bootstrap.js",
	} {
		expected, err := os.ReadFile(filepath.Join(source, "webui", "assets", webui.ShellAssetVersion, name))
		if err != nil {
			t.Fatal("read WebUI smoke fixture", err)
		}
		expected = bytes.ReplaceAll(expected, []byte("__DARWIN_BASE_PATH__"), []byte("/app"))
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+endpoint, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal("installed WebUI request failed", endpoint, err)
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil || response.StatusCode != http.StatusOK || !bytes.Equal(body, expected) {
			t.Fatal("installed WebUI asset mismatch", endpoint, response.StatusCode, readErr, closeErr)
		}
		if response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("X-Content-Type-Options") != "nosniff" ||
			!strings.Contains(response.Header.Get("Content-Security-Policy"), "default-src 'self'") {
			t.Fatal("installed WebUI security headers missing", endpoint)
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/app/assets/v1/app.js", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal("installed authenticated shell probe failed", err)
	}
	_, readErr := io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatal("installed WebUI shell did not fail closed", response.StatusCode, readErr, closeErr)
	}
}

func runInstalled(ctx context.Context, binary, dir string, env []string, input []byte, args ...string) (string, error) {
	command := exec.CommandContext(ctx, binary, args...)
	command.Dir, command.Env, command.WaitDelay = dir, env, 2*time.Second
	command.Stdin = bytes.NewReader(input)
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	err := command.Run()
	return output.String(), err
}

func writeRehearsalConfig(t *testing.T, path, database, address string) {
	t.Helper()
	body := fmt.Sprintf("version: 1\nmode: local_only\ndaemon:\n  listen: %q\nweb_ui:\n  enabled: true\n  path_prefix: /app\nmemory:\n  scope: release-rehearsal\ntelemetry:\n  database: %q\n", address, database)
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func freeLoopbackAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err = listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func openRehearsalDatabase(t *testing.T, path, mode string) *sql.DB {
	t.Helper()
	dsn := url.URL{Scheme: "file", Path: path, RawQuery: "mode=" + mode}
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	return db
}

func checkDatabase(t *testing.T, path string, schema int, scope, id string) {
	t.Helper()
	db := openRehearsalDatabase(t, path, "ro")
	defer db.Close()
	var version int
	var mode, check string
	if db.QueryRow("PRAGMA user_version").Scan(&version) != nil || db.QueryRow("PRAGMA journal_mode").Scan(&mode) != nil || db.QueryRow("PRAGMA quick_check").Scan(&check) != nil || version != schema || strings.ToLower(mode) != "wal" || check != "ok" {
		t.Fatal("database validation failed", version, mode, check)
	}
	var streamMapping bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='submission_stream_events')`).Scan(&streamMapping); err != nil || streamMapping != (schema >= 32) {
		t.Fatal("submission stream schema boundary invalid", streamMapping, err)
	}
	checkBrowserMigrationObjects(t, db, schema)
	checkRehearsalEventLog(t, db, schema)
	if scope != "" {
		var content string
		if err := db.QueryRow(`SELECT content FROM memory_facts WHERE scope=? AND id=?`, scope, id).Scan(&content); err != nil || content != "Synthetic schema migration evidence." {
			t.Fatal("synthetic record unavailable", err)
		}
	}
}

func checkBrowserMigrationObjects(t *testing.T, db *sql.DB, schema int) {
	t.Helper()
	boundaries := map[int][]string{
		35: {"workboard_boards", "workboard_columns", "workboard_cards", "workboard_events", "workboard_operations"},
		37: {"workspace_identity"},
		38: {"browser_operation_recoveries", "legacy_browser_workboard_operations"},
		40: {"workboard_reassignments"},
		41: {"workboard_task_start_claims"},
	}
	for boundary, names := range boundaries {
		for _, name := range names {
			var exists bool
			err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name=?)`, name).Scan(&exists)
			if err != nil || exists != (schema >= boundary) {
				t.Fatalf("schema %d migration object %s boundary %d invalid: exists=%t: %v", schema, name, boundary, exists, err)
			}
		}
	}
	if schema >= 36 {
		var cardIDColumns int
		if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('workboard_events') WHERE name='card_id' AND type='TEXT'`).Scan(&cardIDColumns); err != nil || cardIDColumns != 1 {
			t.Fatal("schema 36 workboard event card binding unavailable", cardIDColumns, err)
		}
	}
}

func checkRehearsalEventLog(t *testing.T, db *sql.DB, schema int) {
	t.Helper()
	var tableSQL string
	err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='event_log'`).Scan(&tableSQL)
	if schema < stateschema.Current {
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatal("pre-current fixture retained event log", err)
		}
		return
	}
	const canonical = `CREATE TABLE event_log (
		position INTEGER PRIMARY KEY AUTOINCREMENT CHECK(position>0),
		event_id TEXT NOT NULL UNIQUE,
		task_id TEXT NOT NULL,
		task_sequence INTEGER NOT NULL CHECK(task_sequence>0),
		body_digest TEXT NOT NULL)`
	if err != nil || strings.Join(strings.Fields(tableSQL), " ") != strings.Join(strings.Fields(canonical), " ") {
		t.Fatal("current event log schema is not canonical", err)
	}
	rows, err := db.Query(`SELECT l.position,l.event_id,l.task_id,l.task_sequence,l.body_digest,e.body
		FROM event_log l JOIN events e ON e.id=l.event_id AND e.task_id=l.task_id AND e.sequence=l.task_sequence ORDER BY l.position`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	position := int64(0)
	for rows.Next() {
		position++
		var gotPosition, sequence int64
		var eventID, taskID, digest string
		var body []byte
		if err := rows.Scan(&gotPosition, &eventID, &taskID, &sequence, &digest, &body); err != nil || gotPosition != position {
			t.Fatal("event log position gap", gotPosition, position, err)
		}
		var event darwinruntime.Event
		canonicalBody, decodeErr := func() ([]byte, error) {
			if err := json.Unmarshal(body, &event); err != nil {
				return nil, err
			}
			return event.Encode()
		}()
		sum := sha256.Sum256(body)
		if decodeErr != nil || !bytes.Equal(canonicalBody, body) || event.ID != eventID || event.TaskID != taskID || event.Sequence != sequence || digest != hex.EncodeToString(sum[:]) {
			t.Fatal("event log backfill is not canonically bound", eventID, decodeErr)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	var eventCount int64
	if err := db.QueryRow(`SELECT count(*) FROM events`).Scan(&eventCount); err != nil || eventCount != position {
		t.Fatal("event log did not preserve every event", eventCount, position, err)
	}
}

func databaseEvidence(t *testing.T, path, scope, id string) string {
	t.Helper()
	db := openRehearsalDatabase(t, path, "ro")
	defer db.Close()
	var body []byte
	if err := db.QueryRow(`SELECT body FROM memory_facts WHERE scope=? AND id=?`, scope, id).Scan(&body); err != nil || len(body) == 0 {
		t.Fatal("cannot inspect synthetic evidence", err)
	}
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

func downgradeFixtureToSchema29(t *testing.T, path string) {
	t.Helper()
	db := openRehearsalDatabase(t, path, "rw")
	defer db.Close()
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND
		(name GLOB 'workboard_*' OR name IN('workspace_identity','browser_operation_recoveries','legacy_browser_workboard_operations')) ORDER BY name DESC`)
	if err != nil {
		t.Fatal(err)
	}
	var browserTables []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		browserTables = append(browserTables, name)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`DROP TABLE event_log; DROP TABLE submission_stream_events; DROP INDEX evaluations_routing_key; DROP TABLE usage_corrections; DROP TABLE usage_heads; DROP TABLE usage_records; DROP TABLE usage_metadata; DROP TRIGGER IF EXISTS workboard_auxiliary_review_settlement_immutable_delete; DROP TRIGGER IF EXISTS workboard_auxiliary_review_settlement_immutable_update; DROP TRIGGER IF EXISTS workboard_auxiliary_review_settlement_binding; DROP INDEX IF EXISTS workboard_auxiliary_review_settlements_board; DROP TABLE IF EXISTS workboard_auxiliary_review_settlements; DROP TRIGGER IF EXISTS workboard_auxiliary_review_admission_immutable_delete; DROP TRIGGER IF EXISTS workboard_auxiliary_review_admission_immutable_update; DROP TRIGGER IF EXISTS workboard_auxiliary_review_admission_binding; DROP INDEX IF EXISTS workboard_auxiliary_review_admissions_board; DROP TABLE IF EXISTS workboard_auxiliary_review_admissions; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_settlement_binding; DROP INDEX IF EXISTS workboard_execution_settlements_board; DROP TABLE IF EXISTS workboard_execution_settlements; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_admission_binding; DROP TRIGGER IF EXISTS workboard_execution_admission_no_active; DROP INDEX IF EXISTS workboard_execution_admissions_card; DROP INDEX IF EXISTS workboard_execution_admissions_board; DROP INDEX IF EXISTS workboard_execution_admissions_global; DROP TABLE IF EXISTS workboard_execution_admissions; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_delete; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_update; DROP INDEX IF EXISTS workboard_task_start_claims_board; DROP TABLE IF EXISTS workboard_task_start_claims; PRAGMA user_version=29`); err != nil {
		t.Fatal(err)
	}
	for _, name := range browserTables {
		if _, err = tx.Exec(`DROP TABLE IF EXISTS "` + strings.ReplaceAll(name, `"`, `""`) + `"`); err != nil {
			t.Fatal("drop post-schema-29 browser table", name, err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func taskTimingEpoch(t *testing.T, path string) string {
	t.Helper()
	db := openRehearsalDatabase(t, path, "ro")
	defer db.Close()
	var epoch string
	if err := db.QueryRow(`SELECT started_at FROM task_timing_metadata WHERE singleton=1 AND version=1`).Scan(&epoch); err != nil {
		t.Fatal("cannot inspect schema-29 task timing epoch", err)
	}
	if at, err := time.Parse(time.RFC3339Nano, epoch); err != nil || at.UTC().Format(time.RFC3339Nano) != epoch {
		t.Fatal("invalid schema-29 task timing epoch", epoch, err)
	}
	return epoch
}

func assertEmptyUsageLedger(t *testing.T, path string) {
	t.Helper()
	db := openRehearsalDatabase(t, path, "ro")
	defer db.Close()
	var version, records, heads, corrections int
	var epoch string
	if err := db.QueryRow(`SELECT version,started_at FROM usage_metadata WHERE singleton=1`).Scan(&version, &epoch); err != nil || version != 1 {
		t.Fatal("cannot inspect schema-30 usage metadata", version, epoch, err)
	}
	// SQLite's strftime("%f") emits exactly millisecond precision, including
	// significant trailing zeroes that RFC3339Nano formatting would trim.
	if at, err := time.Parse(time.RFC3339Nano, epoch); err != nil || at.UTC().Format("2006-01-02T15:04:05.000Z") != epoch {
		t.Fatal("invalid schema-30 usage epoch", epoch, err)
	}
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM usage_records),(SELECT count(*) FROM usage_heads),(SELECT count(*) FROM usage_corrections)`).Scan(&records, &heads, &corrections); err != nil || records != 0 || heads != 0 || corrections != 0 {
		t.Fatal("migration fabricated schema-30 usage history", records, heads, corrections, err)
	}
}

func assertQuiescent(t *testing.T, path string) {
	t.Helper()
	db := openRehearsalDatabase(t, path, "ro")
	defer db.Close()
	queries := []string{
		`SELECT count(*) FROM task_heads WHERE state='running'`,
		`SELECT count(*) FROM submissions WHERE state IN ('queued','running')`,
		`SELECT count(*) FROM resource_leases WHERE released=0`,
	}
	for _, query := range queries {
		var count int
		if err := db.QueryRow(query).Scan(&count); err != nil || count != 0 {
			t.Fatal("database is not quiescent", count, err)
		}
	}
}

func checkpointAndCopy(t *testing.T, source, destination string) {
	t.Helper()
	db := openRehearsalDatabase(t, source, "rw")
	var busy, logFrames, checkpointed int
	if err := db.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointed); err != nil || busy != 0 || logFrames != checkpointed {
		db.Close()
		t.Fatal("WAL checkpoint failed", busy, logFrames, checkpointed, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal("cannot close checkpoint connection", err)
	}
	copyExclusive(t, source, destination)
}

func copyExclusive(t *testing.T, source, destination string) {
	t.Helper()
	in, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(out, in)
	syncErr, closeErr := out.Sync(), out.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil {
		t.Fatal(copyErr, syncErr, closeErr)
	}
	directory, err := os.Open(filepath.Dir(destination))
	if err != nil {
		t.Fatal(err)
	}
	syncErr, closeErr = directory.Sync(), directory.Close()
	if syncErr != nil || closeErr != nil {
		t.Fatal(syncErr, closeErr)
	}
}

func writeExclusive(path string, body []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err = f.Write(body); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != want {
		t.Fatal("unexpected path mode", path, info, err)
	}
}

func fileDigest(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

func assertMemoryCLI(t *testing.T, ctx context.Context, binary, dir string, env []string, database string, want memory.Fact) {
	t.Helper()
	body, err := runInstalled(ctx, binary, dir, env, nil, "memory", "show", "--db", database, "--scope", want.Scope, "--id", want.ID)
	var got memory.Fact
	if err != nil || json.Unmarshal([]byte(body), &got) != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("memory record changed", err, body)
	}
}

func TestExclusiveRehearsalCopyRejectsOverwrite(t *testing.T) {
	root := t.TempDir()
	source, destination := filepath.Join(root, "source"), filepath.Join(root, "destination")
	if err := os.WriteFile(source, []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	if out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600); err == nil {
		out.Close()
		t.Fatal("existing destination replaced")
	} else if !errors.Is(err, os.ErrExist) {
		t.Fatal(err)
	}
	body, err := os.ReadFile(destination)
	if err != nil || string(body) != "retained" {
		t.Fatal("destination changed", err)
	}
}

func TestPinnedRehearsalArchiveRejectsUnsafeIdentity(t *testing.T) {
	root := t.TempDir()
	name := "DarwinRouter_1.0.0_darwin_arm64.tar.gz"
	path := filepath.Join(root, name)
	body := []byte("one immutable descriptor snapshot")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	got, digest, err := readPinnedRehearsalArchive(path, Artifact{File: name})
	want := sha256.Sum256(body)
	if err != nil || !bytes.Equal(got, body) || digest != hex.EncodeToString(want[:]) {
		t.Fatal("regular archive snapshot failed", err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatal("captured archive changed after pathname replacement")
	}
	link := filepath.Join(root, "linked.tar.gz")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	for caseName, candidate := range map[string]struct {
		path     string
		artifact Artifact
	}{
		"basename":  {path: path, artifact: Artifact{File: "other.tar.gz"}},
		"symlink":   {path: link, artifact: Artifact{File: filepath.Base(link)}},
		"directory": {path: root, artifact: Artifact{File: filepath.Base(root)}},
	} {
		t.Run(caseName, func(t *testing.T) {
			if _, _, err := readPinnedRehearsalArchive(candidate.path, candidate.artifact); err == nil {
				t.Fatal("unsafe archive accepted")
			}
		})
	}
}
