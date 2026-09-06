package telemetry

import (
	"context"
	"math"
	"strconv"
	"testing"
	"time"
)

func TestLeaseAttentionSweepSelectedRowCannotFallThrough(t *testing.T) {
	for _, mode := range []string{"removed", "renewed"} {
		t.Run(mode, func(t *testing.T) {
			s, _, now := attentionSweepFixture(t, 3)
			ctx := context.Background()
			ids, err := s.attentionSweepCandidates(ctx, 1, now, 1)
			if err != nil || len(ids) != 1 || ids[0] != 2 {
				t.Fatal("wrong selection", ids, err)
			}
			// Change eligibility after the read snapshot closes, before the
			// exact-row write transaction starts. Row 3 remains eligible.
			if mode == "removed" {
				_, err = s.db.Exec(`DELETE FROM resource_leases WHERE rowid=2`)
			} else {
				_, err = s.db.Exec(`UPDATE resource_leases SET expires=? WHERE rowid=2`, now.Add(time.Hour).UnixNano())
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, n, err := s.observeLeaseAttentionPage(ctx, "1", now, 1, ids[0]); err != nil || n != 0 {
				t.Fatal("missing or ineligible row fell through", n, err)
			}
			var count int
			if err := s.db.QueryRow(`SELECT count(*) FROM lease_attention`).Scan(&count); err != nil || count != 0 {
				t.Fatal("bounded observation changed another row", count, err)
			}
			if next, n, err := s.SweepLeaseAttentionPage(ctx, "2", now, 1); err != nil || n != 1 || next != "3" {
				t.Fatal("later row unavailable on its own visit", next, n, err)
			}
		})
	}
}

func TestLeaseAttentionSweepMaximumRowID(t *testing.T) {
	s, _, now := attentionSweepFixture(t, 1)
	ctx := context.Background()
	if _, err := s.db.Exec(`UPDATE resource_leases SET rowid=?`, int64(math.MaxInt64)); err != nil {
		t.Fatal(err)
	}
	maximum := strconv.FormatInt(math.MaxInt64, 10)
	if next, n, err := s.SweepLeaseAttentionPage(ctx, "", now, 1); err != nil || n != 1 || next != maximum {
		t.Fatal("maximum candidate overflow", next, n, err)
	}
	if next, n, err := s.SweepLeaseAttentionPage(ctx, maximum, now, 1); err != nil || n != 0 || next != "" {
		t.Fatal("maximum cursor failed to wrap", next, n, err)
	}
}
