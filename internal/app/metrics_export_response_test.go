package app

import "testing"

func TestMetricsExportAcknowledgement(t *testing.T) {
	for _, body := range []string{`{}`, `{"future":{"value":[1,true,null]}}`, `{"partialSuccess":null}`, `{"partialSuccess":{}}`, `{"partialSuccess":{"rejectedDataPoints":"0","errorMessage":"warning"}}`, `{"partialSuccess":{"rejectedDataPoints":0}}`, `{"partialSuccess":{"rejectedDataPoints":"0e3"}}`, `{"partialSuccess":{"rejectedDataPoints":0.0e2}}`} {
		if !metricsExportAcknowledged([]byte(body)) {
			t.Fatal("valid acknowledgement", body)
		}
	}
	for _, body := range []string{``, `null`, `[]`, `{} {}`, `{"partialSuccess":{},"partialSuccess":{}}`, `{"partialSuccess":{"rejectedDataPoints":"1"}}`, `{"partialSuccess":{"rejectedDataPoints":-1}}`, `{"partialSuccess":{"rejectedDataPoints":true}}`, `{"partialSuccess":{"rejectedDataPoints":"0.1"}}`, `{"partialSuccess":{"rejectedDataPoints":"1e-999999"}}`, `{"partialSuccess":{"rejectedDataPoints":"0x0"}}`, `{"partialSuccess":{"rejectedDataPoints":0,"rejectedDataPoints":1}}`, `{"partialSuccess":{"errorMessage":{}}}`, `{"partialSuccess":[]}`} {
		if metricsExportAcknowledged([]byte(body)) {
			t.Fatal("invalid acknowledgement", body)
		}
	}
}
