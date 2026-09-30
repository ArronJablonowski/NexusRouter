package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

func TestSubmissionRecoveriesCLIReadsWithoutChangingStorage(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "recoveries.db")
	db, err := telemetry.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{}`)
	sum := sha256.Sum256(body)
	digest := strings.Repeat("a", 64)
	status, err := db.CreateSubmission(ctx, strings.Repeat("b", 64), hex.EncodeToString(sum[:]), digest, body)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	claim, err := db.ClaimSubmission(ctx, digest, time.Now().UTC(), time.Nanosecond)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if ok, err := db.RecoverUndispatched(ctx, status.ID, digest, time.Now().UTC()); err != nil || !ok {
		db.Close()
		t.Fatal(ok, err)
	}
	want, err := db.RecoveryHistory(ctx, status.ID)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	beforeStatus, err := db.Submission(ctx, status.ID)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out, diagnostic bytes.Buffer
	args := []string{"submissions", "recoveries", "--db", path, "--id", status.ID}
	if code := Run(args, &out, &diagnostic, "test"); code != 0 {
		t.Fatal(code, diagnostic.String())
	}
	var got []submissions.Recovery
	if json.Unmarshal(out.Bytes(), &got) != nil || len(got) != 1 || !reflect.DeepEqual(got, want) {
		t.Fatal(out.String())
	}
	if strings.Contains(out.String(), claim.Token) {
		t.Fatal("recovery output exposed lease token")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("read command changed database", err)
	}
	ro, err := telemetry.OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	afterStatus, err := ro.Submission(ctx, status.ID)
	if err != nil || !reflect.DeepEqual(afterStatus, beforeStatus) {
		t.Fatal("read command recovered or mutated submission", afterStatus, err)
	}
	for _, writer := range []io.Writer{submissionBrokenOutput{}, submissionPanicOutput{}} {
		if code := Run(args, writer, &diagnostic, "test"); code != 1 {
			t.Fatal("output failure ignored", code)
		}
	}
}

func TestSubmissionRecoveriesCLIRequiresExplicitReadOnlyArguments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private-missing.db")
	var out, diagnostic bytes.Buffer
	if code := Run([]string{"submissions", "recoveries", "--db", path, "--id", "missing"}, &out, &diagnostic, "test"); code != 1 || out.Len() != 0 || strings.Contains(diagnostic.String(), path) {
		t.Fatal(code, out.String(), diagnostic.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("inspection created database", err)
	}
	for _, extra := range [][]string{{}, {"--id", ""}, {"--id", "../private"}, {"--id", "a:b"}, {"--id", "a", "--id", "b"}, {"--id", "a", "--limit", "1"}, {"--id", "a", "--state", "queued"}, {"--id", "a", "--after", "private-cursor"}, {"--id", "a", "--recover"}} {
		args := append([]string{"submissions", "recoveries", "--db", path}, extra...)
		out.Reset()
		diagnostic.Reset()
		if code := Run(args, &out, &diagnostic, "test"); code != 2 || out.Len() != 0 || strings.Contains(diagnostic.String(), "private-cursor") {
			t.Fatal(args, code, out.String(), diagnostic.String())
		}
	}
	if code := Run([]string{"submissions", "recover", "--db", path, "--id", "a"}, &out, &diagnostic, "test"); code != 2 {
		t.Fatal("direct recovery mutation exposed", code)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("invalid command created database", err)
	}
}
