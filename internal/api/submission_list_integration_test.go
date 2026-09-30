package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/sessions"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

func TestSubmissionHTTPDiscoveryFindsExpiredWorkWithoutReexecution(t *testing.T) {
	ctx := context.Background()
	cfg := config.Defaults()
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "discovery.db")
	svc, err := app.NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i := 0; i < 3; i++ {
		status, err := svc.Submit(ctx, fmt.Sprintf("discovery-request-%d", i), app.Request{ModelID: "fixture", Prompt: "private-queued-request"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, status.ID)
	}
	status, err := svc.SubmissionStatus(ctx, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := db.ClaimSubmission(ctx, status.ConfigDigest, time.Now(), time.Nanosecond)
	if err != nil || claim.Status.ID != ids[0] {
		t.Fatal(claim.Status, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// Fresh inspection-only service has no runtime or private worker handle.
	reopened, err := app.NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(token, 1, Services{Submissions: reopened.ListSubmissions,
		Run: func(context.Context, app.Request) (app.Result, error) {
			t.Error("listing dispatched work")
			return app.Result{}, nil
		},
		Inspect: func(context.Context, string) (sessions.Snapshot, error) { return sessions.Snapshot{}, nil },
		Health:  func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h)
	defer server.Close()
	get := func(query string) submissions.Page {
		t.Helper()
		r, _ := http.NewRequest("GET", server.URL+"/v1/submissions"+query, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		response, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != 200 {
			t.Fatal(response.StatusCode, string(body), err)
		}
		for _, private := range []string{"private-queued-request", claim.Token, "discovery-request-", `"result"`, `"request"`} {
			if strings.Contains(string(body), private) {
				t.Fatal("private content in discovery response")
			}
		}
		var page submissions.Page
		if json.Unmarshal(body, &page) != nil {
			t.Fatal("invalid page")
		}
		return page
	}
	first := get("?limit=1")
	if len(first.Items) != 1 || first.Items[0].ID != ids[0] || first.Items[0].State != "running" || !first.Items[0].LeaseExpired || !first.HasMore {
		t.Fatal(first)
	}
	last := get("?limit=2&after=" + url.QueryEscape(first.NextCursor))
	if len(last.Items) != 2 || last.Items[0].ID != ids[1] || last.Items[1].ID != ids[2] || last.HasMore {
		t.Fatal(last)
	}
	running := get("?state=running")
	if len(running.Items) != 1 || running.Items[0].ID != ids[0] || !running.Items[0].LeaseExpired {
		t.Fatal(running)
	}
	unchanged, err := reopened.SubmissionStatus(ctx, ids[0])
	if err != nil || unchanged.State != "running" || !unchanged.LeaseExpired || len(unchanged.TaskIDs) != 0 {
		t.Fatal(unchanged, err)
	}
}
