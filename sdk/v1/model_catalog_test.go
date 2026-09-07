package v1_test

import (
	"context"
	"errors"
	"testing"

	sdk "github.com/ArronJablonowski/DarwinRouter/sdk/v1"
)

func TestSDKConfiguredModelCatalog(t *testing.T) {
	client, err := sdk.New(sdk.ConfigOptions{})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := client.ConfiguredModelCatalog(context.Background())
	if err != nil || catalog.Validate() != nil || catalog.Models == nil || len(catalog.Models) != 0 {
		t.Fatal(catalog, err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = client.ConfiguredModelCatalog(canceled); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	var nilClient *sdk.Client
	if _, err = nilClient.ConfiguredModelCatalog(context.Background()); !errors.Is(err, sdk.ErrAdmission) {
		t.Fatal(err)
	}
}
