package v1_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	sdk "github.com/ArronJablonowski/DarwinRouter/sdk/v1"
)

func TestSDKTaskListIsReadOnlyAndCancelable(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.db")
	client, err := sdk.New(sdk.ConfigOptions{Overrides: map[string]string{"telemetry.database": missing}})
	if err != nil {
		t.Fatal(err)
	}
	page, err := client.ListTasks(context.Background(), sdk.TaskListOptions{Limit: 25})
	if err != nil || page.Validate() != nil || len(page.Items) != 0 {
		t.Fatal(page, err)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("SDK listing created storage", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	page, err = client.ListTasks(canceled, sdk.TaskListOptions{Limit: 25})
	if !errors.Is(err, context.Canceled) || page.Version != 0 {
		t.Fatal(page, err)
	}
	var nilClient *sdk.Client
	if _, err = nilClient.ListTasks(context.Background(), sdk.TaskListOptions{Limit: 25}); !errors.Is(err, sdk.ErrAdmission) {
		t.Fatal(err)
	}
}
