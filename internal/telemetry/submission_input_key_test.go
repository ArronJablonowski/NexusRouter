package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestSubmissionInputKeyReadOnlyBinding(t *testing.T) {
	db, path := submissionStore(t)
	status := queuedSubmission(t, db, "upload-key")
	ctx := context.Background()
	read, e := OpenReadOnly(ctx, path)
	if e != nil {
		t.Fatal(e)
	}
	defer read.Close()
	key, e := read.SubmissionInputKey(ctx, status.ID)
	if e != nil || key != submitDigest("upload-key") {
		t.Fatal(key, e)
	}
	if _, e = read.SubmissionInputKey(ctx, "missing"); !errors.Is(e, sql.ErrNoRows) {
		t.Fatal("missing key read", e)
	}
	if _, e = db.db.ExecContext(ctx, `UPDATE submissions SET key_digest='invalid' WHERE id=?`, status.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = read.SubmissionInputKey(ctx, status.ID); e == nil {
		t.Fatal("corrupt key accepted")
	}
}
