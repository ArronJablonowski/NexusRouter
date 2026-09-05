package telemetry

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

func TestDenseValidityHistoryKeepsExactRecentWindow(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 240; i++ {
		appendValidity(t, s, validityEvents(fmt.Sprintf("task-%d", i), i < 200))
	}
	v, err := s.OutputValidity(ctx, validityKey())
	if err != nil || v.Samples != 100 || v.Failures != 40 {
		t.Fatal(v, err)
	}
}
