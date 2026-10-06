package gridcollab

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/remote"
	"github.com/ArronJablonowski/NexusRouter/submissions"
	"github.com/ArronJablonowski/NexusRouter/webui"
)

type fakeTrust struct{ reg remote.Registry }

func (f fakeTrust) Read() (remote.Registry, error) { return f.reg, nil }

type fakeClient struct {
	calls, cancels int
	failure        error
	task           remote.Task
	status         submissions.Status
	info           remote.Info
}

func (f *fakeClient) Info(context.Context, string) (remote.Info, error) { return f.info, nil }
func (f *fakeClient) DispatchRecorded(_ context.Context, _ *remote.RouteStore, _, _ string, t remote.Task) (submissions.Status, error) {
	f.calls++
	f.task = t
	return f.status, f.failure
}
func (f *fakeClient) Status(context.Context, string, string) (submissions.Status, error) {
	return f.status, nil
}
func (f *fakeClient) Cancel(context.Context, string, string) (submissions.Status, error) {
	f.cancels++
	return f.status, nil
}
func fixture() (*Bridge, *fakeClient) {
	z := 0.0
	c := &fakeClient{info: remote.Info{Available: true, HybridVersion: 2, Routing: &webui.RoutingInspection{CommanderID: "chief"}, Models: []remote.Model{{ID: "chief", Local: true, EstimatedCost: &z, ContextTokens: 8192}, {ID: "worker", Local: true, EstimatedCost: &z, ContextTokens: 8192}}}, status: submissions.Status{State: "completed", Result: &submissions.Result{TaskID: "remote-task", Text: "answer"}}}
	return &Bridge{Client: c, Trust: fakeTrust{remote.Registry{Peers: []remote.Peer{{ID: "peer", AllowPrivate: true, MaxContextTokens: 8192, Models: []string{"chief", "worker"}, Operations: []string{"info", "inspect", "dispatch", "cancel"}}}}}}, c
}
func TestConsultRecordedBoundedAndNoRetry(t *testing.T) {
	b, c := fixture()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out, err := b.Consult(ctx, "peer", "identity", "bounded prompt", "text", 4096, true)
	if err != nil || out.Text != "answer" || c.calls != 1 || c.task.MaxCost != 0 || c.task.Execution.Depth != 1 || c.task.Execution.MaxCalls != 0 || !c.task.Private {
		t.Fatal("unbounded or missing dispatch", err)
	}
	c.failure = remote.ErrUnavailable
	if _, err = b.Consult(ctx, "peer", "other", "prompt", "text", 4096, true); err == nil || c.calls != 2 {
		t.Fatal("uncertain delivery retried")
	}
}
func TestConsultRejectsPermissionsAndCapacity(t *testing.T) {
	for _, kind := range []string{"permission", "model", "context", "legacy", "cost"} {
		t.Run(kind, func(t *testing.T) {
			b, c := fixture()
			trust := b.Trust.(fakeTrust)
			switch kind {
			case "permission":
				trust.reg.Peers[0].Operations = []string{"info"}
			case "model":
				trust.reg.Peers[0].Models = []string{"worker"}
			case "context":
				trust.reg.Peers[0].MaxContextTokens = 1
			case "legacy":
				c.info.HybridVersion = 0
			case "cost":
				v := 1.0
				c.info.Models[0].EstimatedCost = &v
			}
			b.Trust = trust
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err := b.Consult(ctx, "peer", "id", "prompt", "text", 4096, true)
			if err == nil || c.calls != 0 {
				t.Fatal("unauthorized dispatch")
			}
		})
	}
}
func TestConsultCancellationPropagates(t *testing.T) {
	b, c := fixture()
	c.status.State = "running"
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := b.Consult(ctx, "peer", "id", "prompt", "text", 4096, true)
	if !errors.Is(err, context.DeadlineExceeded) || c.cancels != 1 {
		t.Fatal("missing cancellation", err)
	}
}

func TestCloudConsultRequiresExplicitPermission(t *testing.T) {
	b, c := fixture()
	c.info.Models[0].Local = false
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := b.Consult(ctx, "peer", "id", "prompt", "text", 4096, false); err == nil {
		t.Fatal("cloud permission bypass")
	}
	trust := b.Trust.(fakeTrust)
	trust.reg.Peers[0].AllowCloudInference = true
	b.Trust = trust
	if _, err := b.Consult(ctx, "peer", "id", "prompt", "text", 4096, true); err == nil {
		t.Fatal("private request sent to cloud")
	}
	if _, err := b.Consult(ctx, "peer", "id", "prompt", "text", 4096, false); err != nil || c.task.Private {
		t.Fatal("authorized zero-cost consultation failed", err)
	}
}
