package v1_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	sdk "github.com/ArronJablonowski/DarwinRouter/sdk/v1"
)

func TestSDKSessionTaskListIsReadOnlyAndCancelable(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.db")
	client, err := sdk.New(sdk.ConfigOptions{Overrides: map[string]string{"telemetry.database": missing}})
	if err != nil {
		t.Fatal(err)
	}
	page, err := client.ListSessionTasks(context.Background(), "session", sdk.SessionTaskListOptions{Limit: 25})
	if err != nil || page.Validate() != nil || page.SessionID != "session" || len(page.Items) != 0 {
		t.Fatal(page, err)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("SDK session task listing created storage", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	page, err = client.ListSessionTasks(canceled, "session", sdk.SessionTaskListOptions{Limit: 25})
	if !errors.Is(err, context.Canceled) || page.Version != 0 {
		t.Fatal(page, err)
	}
	var nilClient *sdk.Client
	if _, err = nilClient.ListSessionTasks(context.Background(), "session", sdk.SessionTaskListOptions{Limit: 25}); !errors.Is(err, sdk.ErrAdmission) {
		t.Fatal(err)
	}
	if _, err = client.ListSessionTasks(context.Background(), "bad:session", sdk.SessionTaskListOptions{Limit: 25}); !errors.Is(err, sdk.ErrAdmission) {
		t.Fatal(err)
	}
}
