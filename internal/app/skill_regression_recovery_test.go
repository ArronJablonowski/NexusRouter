package app

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestSkillRegressionMonitorRecoversHealth(t *testing.T) {
	var calls atomic.Int32
	m, err := startSkillRegressionMonitor(context.Background(), 10*time.Millisecond, func(context.Context) error {
		if calls.Add(1) == 1 {
			return errors.New("transient")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if calls.Load() >= 2 && m.Health().Status == "healthy" {
			if err := m.Close(); err != nil {
				t.Fatal(err)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("health did not recover: %+v", m.Health())
}
