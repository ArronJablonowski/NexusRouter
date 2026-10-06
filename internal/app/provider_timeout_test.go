package app

import (
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"testing"
	"time"
)

func TestHTTPProviderTimeoutDefaultAndOverride(t *testing.T) {
	if httpProviderTimeout(config.Provider{}) != 30*time.Minute {
		t.Fatal("default")
	}
	if httpProviderTimeout(config.Provider{RequestTimeout: "12m"}) != 12*time.Minute {
		t.Fatal("override")
	}
}
