package app

import (
	"context"
	"testing"
)

func TestRemoteRoutingInspectionRejectsInvalidContext(t *testing.T) {
	s := submissionService(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, candidate := range []context.Context{nil, ctx} {
		if _, err := s.RemoteRoutingInspection(candidate, nil); err == nil {
			t.Fatal("invalid context accepted")
		}
	}
	if _, err := s.RemoteRoutingInspection(context.Background(), make([]string, 4097)); err == nil {
		t.Fatal("unbounded inventory accepted")
	}
}
