package telemetry

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// BenchmarkOutputValidity includes SQLite selection, event decoding and evidence
// verification on a fixed corpus. It is not a full routing or inference SLA.
func BenchmarkOutputValidity(b *testing.B) {
	for _, tasks := range []int{100, 1000} {
		for _, validation := range []string{"", "go_source"} {
			name := validation
			if name == "" {
				name = "text"
			}
			b.Run(fmt.Sprintf("%s/tasks_%d", name, tasks), func(b *testing.B) {
				ctx := context.Background()
				s, err := Open(ctx, filepath.Join(b.TempDir(), "events.db"))
				if err != nil {
					b.Fatal(err)
				}
				defer s.Close()
				for i := 0; i < tasks; i++ {
					id := fmt.Sprintf("task-%d", i)
					events := validityEvents(id, true)
					if validation != "" {
						events = syntaxValidityEvents(id, "package p\n//"+strings.Repeat("x", 4096), true)
					}
					for j, e := range events {
						if err := s.Append(ctx, int64(j), e); err != nil {
							b.Fatal(err)
						}
					}
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					v, err := s.OutputValidity(ctx, validityKey(), validation)
					if err != nil || v.Samples != 100 || v.Failures != 0 {
						b.Fatal(v, err)
					}
				}
			})
		}
	}
}

// Missing models still incur evidence lookup work during cold-start routing.
func BenchmarkOutputValidityMissing(b *testing.B) {
	for _, tasks := range []int{100, 1000} {
		b.Run(fmt.Sprintf("tasks_%d", tasks), func(b *testing.B) {
			ctx := context.Background()
			s, err := Open(ctx, filepath.Join(b.TempDir(), "events.db"))
			if err != nil {
				b.Fatal(err)
			}
			defer s.Close()
			for i := 0; i < tasks; i++ {
				for j, e := range validityEvents(fmt.Sprintf("task-%d", i), true) {
					if err := s.Append(ctx, int64(j), e); err != nil {
						b.Fatal(err)
					}
				}
			}
			key := validityKey()
			key.Model = "cold-model"
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				v, err := s.OutputValidity(ctx, key)
				if err != nil || v.Samples != 0 {
					b.Fatal(v, err)
				}
			}
		})
	}
}
