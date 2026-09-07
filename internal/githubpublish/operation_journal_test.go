package githubpublish

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type observerFunc func(context.Context, OperationIdentity) (RemoteObservation, error)

func (f observerFunc) Observe(ctx context.Context, identity OperationIdentity) (RemoteObservation, error) {
	return f(ctx, identity)
}

func operationFixture() ExpectedDraftState {
	return ExpectedDraftState{
		Identity: OperationIdentity{
			AuthorizationSHA256: "sha256:" + strings.Repeat("a", 64),
			Repository:          "ArronJablonowski/DarwinRouter",
			Tag:                 "v1.0.0-rc.1",
		},
		Commit:             strings.Repeat("b", 40),
		ReleaseTitle:       "DarwinRouter v1.0.0-rc.1",
		ReleaseNotesSHA256: "sha256:" + strings.Repeat("c", 64),
		Prerelease:         true,
		Assets: []ExpectedAsset{
			{Name: "darwinrouter_checksums.txt", Size: 200, SHA256: "sha256:" + strings.Repeat("d", 64)},
			{Name: "darwinrouter_macos.tar.gz", Size: 400, SHA256: "sha256:" + strings.Repeat("e", 64)},
		},
	}
}

func exactObservation(expected ExpectedDraftState) RemoteObservation {
	assets := make([]ObservedAsset, len(expected.Assets))
	for i, asset := range expected.Assets {
		assets[i] = ObservedAsset{ID: int64(i + 10), Name: asset.Name, Size: asset.Size, SHA256: asset.SHA256}
	}
	return RemoteObservation{
		Tag: &ObservedTag{Commit: expected.Commit},
		Release: &ObservedRelease{
			ID: 41, Tag: expected.Identity.Tag, Commit: expected.Commit,
			Title: expected.ReleaseTitle, ReleaseNotesSHA256: expected.ReleaseNotesSHA256,
			Draft: true, Prerelease: expected.Prerelease,
		},
		Assets: assets,
	}
}

func fixedObserver(observed RemoteObservation) RemoteObserver {
	return observerFunc(func(_ context.Context, _ OperationIdentity) (RemoteObservation, error) {
		return observed, nil
	})
}

func createJournal(t *testing.T, expected ExpectedDraftState) (string, *OperationJournal) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "publication-operation.jsonl")
	journal, err := CreateOperationJournal(path, expected)
	if err != nil {
		t.Fatalf("CreateOperationJournal: %v", err)
	}
	return path, journal
}

func TestOperationJournalPersistsTransitionsAndReconciles(t *testing.T) {
	expected := operationFixture()
	path, journal := createJournal(t, expected)
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("journal mode = %v, %v", info, err)
	}
	if _, err := CreateOperationJournal(path, expected); !errors.Is(err, ErrOperationJournal) {
		t.Fatalf("duplicate create error = %v", err)
	}

	mutations := [][2]string{{"create_tag", ""}, {"create_draft", ""}}
	for _, asset := range expected.Assets {
		mutations = append(mutations, [2]string{"upload_asset", asset.Name})
	}
	for _, mutation := range mutations {
		if err := journal.RecordIntent(mutation[0], mutation[1]); err != nil {
			t.Fatalf("RecordIntent(%v): %v", mutation, err)
		}
		if err := journal.RecordResult(mutation[0], mutation[1], "confirmed"); err != nil {
			t.Fatalf("RecordResult(%v): %v", mutation, err)
		}
	}
	if err := journal.RecordConfirmation(); err != nil {
		t.Fatalf("RecordConfirmation: %v", err)
	}
	if err := journal.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	result, err := ReconcileOperation(context.Background(), path, expected, fixedObserver(exactObservation(expected)))
	if err != nil {
		t.Fatalf("ReconcileOperation: %v", err)
	}
	if result.Classification != ReconciliationConfirmed || result.RetryAllowed || result.JournalTorn ||
		result.Events != len(mutations)*2+1 || result.ReleaseID != 41 || result.ObservedAssets != len(expected.Assets) {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestReconciliationClassifiesRemoteState(t *testing.T) {
	expected := operationFixture()
	exact := exactObservation(expected)
	wrongTag := exactObservation(expected)
	wrongTag.Tag.Commit = strings.Repeat("f", 40)
	extraAsset := exactObservation(expected)
	extraAsset.Assets = append(extraAsset.Assets, ObservedAsset{ID: 99, Name: "unexpected.bin", Size: 1, SHA256: "sha256:" + strings.Repeat("f", 64)})
	releaseWithoutTag := exactObservation(expected)
	releaseWithoutTag.Tag = nil
	partialAssets := exactObservation(expected)
	partialAssets.Assets = partialAssets.Assets[:1]
	tagOnly := RemoteObservation{Tag: &ObservedTag{Commit: expected.Commit}, Assets: []ObservedAsset{}}

	tests := []struct {
		name      string
		observed  RemoteObservation
		want      string
		wantRetry bool
	}{
		{name: "absent", observed: RemoteObservation{Assets: []ObservedAsset{}}, want: ReconciliationAbsent, wantRetry: true},
		{name: "tag only", observed: tagOnly, want: ReconciliationPartialExact},
		{name: "partial assets", observed: partialAssets, want: ReconciliationPartialExact},
		{name: "exact draft", observed: exact, want: ReconciliationExactDraft},
		{name: "wrong tag", observed: wrongTag, want: ReconciliationConflict},
		{name: "extra asset", observed: extraAsset, want: ReconciliationConflict},
		{name: "release without tag", observed: releaseWithoutTag, want: ReconciliationConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path, journal := createJournal(t, expected)
			if err := journal.Close(); err != nil {
				t.Fatal(err)
			}
			result, err := ReconcileOperation(context.Background(), path, expected, fixedObserver(test.observed))
			if err != nil {
				t.Fatalf("ReconcileOperation: %v", err)
			}
			if result.Classification != test.want || result.RetryAllowed != test.wantRetry {
				t.Fatalf("got %+v, want class=%s retry=%t", result, test.want, test.wantRetry)
			}
			if test.want != ReconciliationAbsent && result.RetryAllowed {
				t.Fatal("observed remote state was declared retryable")
			}
		})
	}
}

func TestOperationJournalUncertainAndPendingAttemptsAreNotRetryable(t *testing.T) {
	for _, outcome := range []string{"pending", "uncertain"} {
		t.Run(outcome, func(t *testing.T) {
			expected := operationFixture()
			path, journal := createJournal(t, expected)
			if err := journal.RecordIntent("create_tag", ""); err != nil {
				t.Fatal(err)
			}
			if outcome == "uncertain" {
				if err := journal.RecordResult("create_tag", "", "uncertain"); err != nil {
					t.Fatal(err)
				}
			}
			if err := journal.Close(); err != nil {
				t.Fatal(err)
			}
			result, err := ReconcileOperation(context.Background(), path, expected, fixedObserver(RemoteObservation{Assets: []ObservedAsset{}}))
			if err != nil || result.Classification != ReconciliationAbsent || result.RetryAllowed {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestOperationJournalRejectsInvalidTransitionAndReplacement(t *testing.T) {
	expected := operationFixture()
	path, journal := createJournal(t, expected)
	if err := journal.RecordResult("create_tag", "", "confirmed"); !errors.Is(err, ErrOperationJournal) {
		t.Fatalf("result without intent error = %v", err)
	}
	if err := journal.RecordConfirmation(); !errors.Is(err, ErrOperationJournal) {
		t.Fatalf("empty confirmation error = %v", err)
	}
	if err := journal.RecordIntent("upload_asset", "bad/name"); !errors.Is(err, ErrOperationJournal) {
		t.Fatalf("invalid asset error = %v", err)
	}
	if err := journal.RecordIntent("upload_asset", "unapproved.bin"); !errors.Is(err, ErrOperationJournal) {
		t.Fatalf("unapproved asset error = %v", err)
	}

	moved := path + ".moved"
	if err := os.Rename(path, moved); err != nil {
		t.Fatal(err)
	}
	replacement, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := replacement.Close(); err != nil {
		t.Fatal(err)
	}
	if err := journal.RecordIntent("create_tag", ""); !errors.Is(err, ErrOperationJournal) {
		t.Fatalf("replacement error = %v", err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOperationJournalRejectsNonCanonicalCompleteLine(t *testing.T) {
	expected := operationFixture()
	path, journal := createJournal(t, expected)
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body = []byte(strings.TrimSuffix(string(body), "}\n") + `,"unknown":true}` + "\n")
	if err = os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = ReconcileOperation(context.Background(), path, expected, fixedObserver(RemoteObservation{})); !errors.Is(err, ErrOperationJournal) {
		t.Fatalf("noncanonical line error = %v", err)
	}
}

func TestOperationJournalRejectsSymlinkAndWrongBinding(t *testing.T) {
	expected := operationFixture()
	path, journal := createJournal(t, expected)
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(filepath.Dir(path), "linked.jsonl")
	if err := os.Symlink(path, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := ReconcileOperation(context.Background(), symlink, expected, fixedObserver(RemoteObservation{})); !errors.Is(err, ErrOperationJournal) {
		t.Fatalf("symlink error = %v", err)
	}
	changed := expected
	changed.ReleaseNotesSHA256 = "sha256:" + strings.Repeat("f", 64)
	if _, err := ReconcileOperation(context.Background(), path, changed, fixedObserver(RemoteObservation{})); !errors.Is(err, ErrOperationJournal) {
		t.Fatalf("binding error = %v", err)
	}
}

func TestOperationJournalTornSuffixIsFailClosed(t *testing.T) {
	expected := operationFixture()
	path, journal := createJournal(t, expected)
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.WriteString(`{"schema_version":1`); err != nil {
		t.Fatal(err)
	}
	if err = file.Sync(); err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	result, err := ReconcileOperation(context.Background(), path, expected, fixedObserver(exactObservation(expected)))
	if err != nil || result.Classification != ReconciliationExactDraft || !result.JournalTorn || result.RetryAllowed {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestOperationJournalObserverFailureAndCancellationFailClosed(t *testing.T) {
	expected := operationFixture()
	path, journal := createJournal(t, expected)
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	observerErr := observerFunc(func(context.Context, OperationIdentity) (RemoteObservation, error) {
		return RemoteObservation{}, errors.New("offline")
	})
	if _, err := ReconcileOperation(context.Background(), path, expected, observerErr); !errors.Is(err, ErrOperationJournal) {
		t.Fatalf("observer error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	observer := observerFunc(func(context.Context, OperationIdentity) (RemoteObservation, error) {
		called = true
		return RemoteObservation{}, nil
	})
	if _, err := ReconcileOperation(ctx, path, expected, observer); !errors.Is(err, ErrOperationJournal) || called {
		t.Fatalf("canceled error=%v called=%t", err, called)
	}
}

func TestOperationJournalCrashIntentIsDurable(t *testing.T) {
	if os.Getenv("DARWIN_OPERATION_JOURNAL_HELPER") == "1" {
		expected := operationFixture()
		journal, err := CreateOperationJournal(os.Getenv("DARWIN_OPERATION_JOURNAL_PATH"), expected)
		if err != nil || journal.RecordIntent("create_tag", "") != nil {
			os.Exit(2)
		}
		if os.Getenv("DARWIN_OPERATION_JOURNAL_STAGE") == "result" &&
			journal.RecordResult("create_tag", "", "confirmed") != nil {
			os.Exit(3)
		}
		_, _ = os.Stdout.WriteString("ready\n")
		select {}
	}

	expected := operationFixture()
	for _, test := range []struct {
		stage      string
		wantEvents int
	}{{stage: "intent", wantEvents: 1}, {stage: "result", wantEvents: 2}} {
		t.Run(test.stage, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "publication-operation.jsonl")
			command := exec.Command(os.Args[0], "-test.run=^TestOperationJournalCrashIntentIsDurable$")
			command.Env = append(os.Environ(), "DARWIN_OPERATION_JOURNAL_HELPER=1",
				"DARWIN_OPERATION_JOURNAL_PATH="+path, "DARWIN_OPERATION_JOURNAL_STAGE="+test.stage)
			stdout, err := command.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err = command.Start(); err != nil {
				t.Fatal(err)
			}
			ready := make(chan error, 1)
			go func() {
				line, readErr := bufio.NewReader(stdout).ReadString('\n')
				if readErr == nil && line != "ready\n" {
					readErr = errors.New("unexpected helper output")
				}
				ready <- readErr
			}()
			select {
			case err = <-ready:
				if err != nil {
					_ = command.Process.Kill()
					_ = command.Wait()
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				_ = command.Process.Kill()
				_ = command.Wait()
				t.Fatal("helper did not persist transition")
			}
			if err = command.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = command.Wait()

			result, err := ReconcileOperation(context.Background(), path, expected, fixedObserver(RemoteObservation{Assets: []ObservedAsset{}}))
			if err != nil || result.Events != test.wantEvents || result.Classification != ReconciliationAbsent || result.RetryAllowed {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}
