package telemetry

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

// BenchmarkDurableTaskStartAppend measures individual committed SQLite/WAL
// event transactions with the production FULL synchronization setting. It does
// not model contention, multi-event task completion, filesystem power-loss
// behavior, migrations, or provider work.
func BenchmarkDurableTaskStartAppend(b *testing.B) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(b.TempDir(), "append.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	stamp := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		id := fmt.Sprintf("benchmark-task-%09d", i)
		e := runtime.Event{Version: 1, ID: id + "-start", TaskID: id, SessionID: id, CorrelationID: id, Sequence: 1, Time: stamp, Kind: runtime.TaskStarted}
		if err := store.Append(ctx, 0, e); err != nil {
			b.Fatal(err)
		}
	}
}
