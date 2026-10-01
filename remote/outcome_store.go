package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/ArronJablonowski/NexusRouter/harness"
)

// Record stores provenance before appending the immutable execution. Each paired
// destination and caller identity gets its own ledger, so identical configurations
// on different systems cannot silently pool votes. No review is created. A retry
// after partial persistence must use this same root and receipt.
func (v VerifiedOutcome) Record(ctx context.Context, root string, now time.Time) error {
	ledger, err := v.recordLedger(ctx, root, now)
	if err != nil {
		return err
	}
	return ledger.Close()
}

func (v VerifiedOutcome) recordLedger(ctx context.Context, root string, now time.Time) (*harness.EvidenceStore, error) {
	if !v.verified || ctx == nil || ctx.Err() != nil || v.receipt.Execution.Validate() != nil || now.IsZero() || v.receipt.Execution.CompletedAt.After(now) {
		return nil, ErrInvalid
	}
	// Reuse the strict private-directory and parent-sync checks; this root must be
	// dedicated to remote evidence, never the local runtime's learning ledger.
	base, err := OpenRouteStore(root)
	if err != nil {
		return nil, err
	}
	scope := hash(struct{ Destination, Caller string }{v.receipt.Route.Destination, v.receipt.Route.CallerFingerprint})
	dir, err := OpenRouteStore(filepath.Join(base.directory, scope))
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(v.receipt)
	if err != nil {
		return nil, err
	}
	key := certificateDigest([]byte(v.receipt.Route.RequestID))
	if err = immutableReceipt(dir, filepath.Join(dir.directory, key+".outcome.json"), body); err != nil {
		return nil, err
	}
	ledger, err := harness.OpenEvidenceStore(filepath.Join(dir.directory, "ledger"))
	if err != nil {
		return nil, err
	}
	if err = ledger.AppendExecution(ctx, v.receipt.Execution, now); err != nil {
		ledger.Close()
		return nil, err
	}
	return ledger, nil
}

func immutableReceipt(dir *RouteStore, path string, body []byte) error {
	if len(body) > 32768 {
		return ErrInvalid
	}
	compare := func() error {
		st, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Size() > 32768 {
			return ErrDenied
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		actual, err := f.Stat()
		if err != nil || !os.SameFile(st, actual) {
			return ErrDenied
		}
		saved, err := io.ReadAll(io.LimitReader(f, 32769))
		if err != nil {
			return err
		}
		if !bytes.Equal(saved, body) {
			return ErrConflict
		}
		return nil
	}
	if err := compare(); err == nil {
		return dir.syncDirectory()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.CreateTemp(dir.directory, ".outcome-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(body); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Link(f.Name(), path); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if err = compare(); err != nil {
		return err
	}
	return dir.syncDirectory()
}
