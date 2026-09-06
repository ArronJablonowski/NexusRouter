package metrics

import (
	"strings"
	"testing"
)

func TestExportOptions(t *testing.T) {
	for _, endpoint := range []string{"http://localhost:4318/v1/metrics", "http://127.0.0.1:4318/custom", "http://[::1]:4318/v1/metrics", "https://collector.example/v1/metrics"} {
		if (ExportOptions{Endpoint: endpoint, APIKeyEnv: "COLLECTOR_KEY"}).Validate() != nil {
			t.Fatal("valid endpoint", endpoint)
		}
	}
	for _, endpoint := range []string{"", "http://collector.example/v1/metrics", "http://192.168.1.1/v1/metrics", "file:///v1/metrics", "https://collector.example", "https://user:secret@collector.example/v1/metrics", "https://collector.example/v1/metrics?", "https://collector.example/v1/metrics?secret=x", "https://collector.example/v1/metrics#", "https://collector.example/v1/metrics#x", "http://localhost:0/v1/metrics", "http://localhost:65536/v1/metrics", "http://localhost:/v1/metrics", "http://[::1%25en0]/v1/metrics", " http://localhost/v1/metrics", strings.Repeat("x", 2049)} {
		if (ExportOptions{Endpoint: endpoint}).Validate() == nil {
			t.Fatal("invalid endpoint accepted", endpoint)
		}
	}
	for _, name := range []string{"literal-key!", "1KEY", "bad name", "KEY\n", strings.Repeat("A", 129)} {
		if (ExportOptions{Endpoint: "http://localhost/v1/metrics", APIKeyEnv: name}).Validate() == nil {
			t.Fatal("invalid environment name", name)
		}
	}
}
