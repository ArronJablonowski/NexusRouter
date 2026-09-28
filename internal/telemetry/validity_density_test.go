package telemetry

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// The model-start population only chooses a query plan. Newer off-domain
// records cannot consume the eligible window on either side of the threshold.
func TestOutputValidityDensityThresholdPreservesFilteredWindow(t *testing.T) {
	for _, population := range []int{199, 200, 201, 350} {
		t.Run(fmt.Sprint(population), func(t *testing.T) {
			ctx := context.Background()
			s, err := Open(ctx, filepath.Join(t.TempDir(), "density.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			// Reverse lexical identities and timestamps relative to insertion.
			// Ten old failures must fall out; the last failure must remain.
			for i := 0; i < 110; i++ {
				events := validityEvents(fmt.Sprintf("target-%03d", 109-i), i >= 10 && i != 109)
				for j := range events {
					events[j].Time = time.Unix(int64(1000-i), int64(j)).UTC()
				}
				appendValidity(t, s, events)
			}
			for i := 110; i < population; i++ {
				events := validityEvents(fmt.Sprintf("distractor-%03d", i), true)
				events[0].Data.Domain = "other-domain"
				if i%2 == 0 {
					events[0].Data.Domain = "code"
					events[0].Data.Profile = "other-profile"
				}
				appendValidity(t, s, events)
			}
			before := workflowSourceRawBodies(t, s)
			out, err := s.OutputValidity(ctx, validityKey())
			if err != nil || out.Samples != 100 || out.Failures != 1 || !out.Updated.Equal(time.Unix(990, 3).UTC()) {
				t.Fatal("query plan changed eligible rowid window", out, err)
			}
			if !reflect.DeepEqual(before, workflowSourceRawBodies(t, s)) {
				t.Fatal("validity read mutated evidence")
			}
			// An old excluded malformed verdict is not part of the window.
			if _, err := s.db.Exec(`UPDATE events SET body=json_set(body,'$.data.accepted',json('true')) WHERE id='target-109-3'`); err != nil {
				t.Fatal(err)
			}
			if got, err := s.OutputValidity(ctx, validityKey()); err != nil || got != out {
				t.Fatal("excluded history expanded the sample", got, err)
			}
			// A selected check with a foreign attempt must not disappear from
			// selection merely because a count query is optimized.
			if _, err := s.db.Exec(`UPDATE events SET body=json_set(body,'$.attempt_id','foreign') WHERE id='target-000-3'`); err != nil {
				t.Fatal(err)
			}
			if _, err := s.OutputValidity(ctx, validityKey()); err == nil {
				t.Fatal("selected malformed evidence was hidden")
			}
			// Verify the reader itself never appends any event rows.
			after := workflowSourceRawBodies(t, s)
			if len(after) != len(before) {
				t.Fatal("validity reader changed journal cardinality")
			}
		})
	}
}

func TestOutputValidityColdAndUnfinishedStartsDoNotInventSamples(t *testing.T) {
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "starts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 205; i++ {
		events := validityEvents(fmt.Sprintf("unfinished-%03d", i), true)
		appendValidity(t, s, events[:2])
	}
	for _, model := range []string{"candidate", "missing"} {
		key := validityKey()
		key.Model = model
		got, err := s.OutputValidity(context.Background(), key)
		if err != nil || got.Samples != 0 || got.Failures != 0 || !got.Updated.IsZero() {
			t.Fatal("unrated starts became validity evidence", got, err)
		}
	}
	// A completed eligible task remains reachable after the dense start count.
	finished := validityEvents("finished", true)
	appendValidity(t, s, finished)
	if got, err := s.OutputValidity(context.Background(), validityKey()); err != nil || got.Samples != 1 {
		t.Fatal(got, err)
	}
}

// A model can be hot while the requested card scope is entirely cold. The
// exact-scope lookup must neither borrow another domain nor invent validity.
func TestOutputValidityDenseModelMissingCardScope(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "cold-scope.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 250; i++ {
		appendValidity(t, s, validityEvents(fmt.Sprintf("known-%d", i), true))
	}
	key := validityKey()
	key.Domain = "commandline"
	key.Profile = "benchmark"
	v, err := s.OutputValidity(ctx, key)
	if err != nil || v.Samples != 0 || v.Failures != 0 {
		t.Fatal(v, err)
	}
	key = validityKey()
	v, err = s.OutputValidity(ctx, key)
	if err != nil || v.Samples != 100 || v.Failures != 0 {
		t.Fatal(v, err)
	}
}
