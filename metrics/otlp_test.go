package metrics

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func TestMarshalOTLPWireShapePrecisionAndOwnership(t *testing.T) {
	s := NewSnapshot(28, time.Unix(1_800_000_000, 123456789).UTC())
	s.Groups[0].Counts[0].Value = math.MaxInt64
	before, _ := json.Marshal(s)
	body, err := MarshalOTLP(s)
	if err != nil || len(body) > 64<<10 {
		t.Fatal(len(body), err)
	}
	var wire map[string]any
	if json.Unmarshal(body, &wire) != nil || len(wire) != 1 {
		t.Fatal(string(body))
	}
	resource := wire["resourceMetrics"].([]any)[0].(map[string]any)
	attr := resource["resource"].(map[string]any)["attributes"].([]any)[0].(map[string]any)
	if attr["key"] != "service.name" || attr["value"].(map[string]any)["stringValue"] != "DarwinRouter" {
		t.Fatal(attr)
	}
	scope := resource["scopeMetrics"].([]any)[0].(map[string]any)
	if !reflect.DeepEqual(scope["scope"], map[string]any{"name": "darwinrouter.metrics", "version": "1"}) {
		t.Fatal(scope)
	}
	metrics := scope["metrics"].([]any)
	if len(metrics) != len(s.Groups)+2 {
		t.Fatal(metrics)
	}
	for i, item := range metrics[:len(s.Groups)] {
		metric := item.(map[string]any)
		if len(metric) != 3 || metric["name"] != "darwinrouter."+s.Groups[i].Name || metric["unit"] != "{record}" {
			t.Fatal(metric)
		}
		points := metric["gauge"].(map[string]any)["dataPoints"].([]any)
		if len(points) != len(s.Groups[i].Counts) {
			t.Fatal(points)
		}
		for j, point := range points {
			p := point.(map[string]any)
			if len(p) != 3 || p["timeUnixNano"] != "1800000000123456789" || p["asInt"] != strconv.FormatInt(s.Groups[i].Counts[j].Value, 10) {
				t.Fatal(p)
			}
			attributes := p["attributes"].([]any)
			want := map[string]any{"key": "state", "value": map[string]any{"stringValue": s.Groups[i].Counts[j].State}}
			if len(attributes) != 1 || !reflect.DeepEqual(attributes[0], want) {
				t.Fatal(attributes)
			}
		}
	}
	operation := metrics[len(s.Groups)].(map[string]any)
	unavailable := metrics[len(s.Groups)+1].(map[string]any)
	if operation["name"] != "darwinrouter.operation.duration" || unavailable["name"] != "darwinrouter.operation.duration.unavailable" {
		t.Fatal(metrics[len(s.Groups):])
	}
	after, _ := json.Marshal(s)
	if !bytes.Equal(before, after) {
		t.Fatal("snapshot mutated")
	}
	body[0] = 'x'
	again, err := MarshalOTLP(s)
	if err != nil || !json.Valid(again) {
		t.Fatal("output aliases", err)
	}
}

func TestMarshalOTLPTimestampBounds(t *testing.T) {
	maxSeconds := int64(uint64(math.MaxUint64) / 1_000_000_000)
	maxNanos := int64(uint64(math.MaxUint64) % 1_000_000_000)
	for _, tc := range []struct {
		at   time.Time
		want string
	}{
		{time.Unix(0, 0).UTC(), "0"},
		{time.Unix(0, 1).UTC(), "1"},
		{time.Unix(maxSeconds, maxNanos).UTC(), "18446744073709551615"},
		{time.Unix(9_223_372_037, 0).UTC(), "9223372037000000000"},
	} {
		body, err := MarshalOTLP(NewSnapshot(1, tc.at))
		if err != nil || !bytes.Contains(body, []byte(`"timeUnixNano":"`+tc.want+`"`)) {
			t.Fatal(tc.at, string(body), err)
		}
	}
	for _, at := range []time.Time{time.Unix(-1, 999999999).UTC(), time.Unix(maxSeconds, maxNanos+1).UTC(), time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)} {
		if body, err := MarshalOTLP(NewSnapshot(1, at)); !errors.Is(err, ErrInvalid) || body != nil {
			t.Fatal(at, string(body), err)
		}
	}
}

func TestMarshalOTLPLegacyAndClosedLabels(t *testing.T) {
	s := NewSnapshot(1, time.Unix(1, 0).UTC())
	body, err := MarshalOTLP(s)
	if err != nil {
		t.Fatal(err)
	}
	var request otlpRequest
	if json.Unmarshal(body, &request) != nil {
		t.Fatal(string(body))
	}
	items := request.ResourceMetrics[0].ScopeMetrics[0].Metrics
	if len(items) != 3 || items[0].Name != "darwinrouter.tasks" || items[1].Name != "darwinrouter.runtime_events" || items[2].Name != "darwinrouter.runtime_operations" {
		t.Fatal("unavailable fabricated", items)
	}
	for _, mutate := range []func(*Snapshot){func(s *Snapshot) { s.Groups[0].Name = "private-task" }, func(s *Snapshot) { s.Groups[0].Counts[0].State = "secret-token" }, func(s *Snapshot) { s.Groups[0].Counts[0].Value = -1 }, func(s *Snapshot) { s.Groups[1].Counts = []Count{{State: "queued", Value: 1}} }, func(s *Snapshot) { s.Version++ }} {
		bad := NewSnapshot(1, s.ObservedAt)
		mutate(&bad)
		if body, err := MarshalOTLP(bad); !errors.Is(err, ErrInvalid) || body != nil {
			t.Fatal("invalid snapshot released", string(body), err)
		}
	}
}

func TestMarshalOTLPAccountingUsesClosedIdentifierFreeBuckets(t *testing.T) {
	s := NewSnapshot(31, time.Unix(100, 0).UTC())
	body, err := MarshalOTLP(s)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte("task_id")) || bytes.Contains(body, []byte("session_id")) {
		t.Fatal("accounting identity leaked", string(body))
	}
	var request otlpRequest
	if json.Unmarshal(body, &request) != nil {
		t.Fatal(string(body))
	}
	items := request.ResourceMetrics[0].ScopeMetrics[0].Metrics
	wantNames := map[string]bool{
		"darwinrouter.accounting.records":               true,
		"darwinrouter.accounting.usage.known_records":   true,
		"darwinrouter.accounting.usage.unknown_records": true,
		"darwinrouter.accounting.input_tokens.known":    true,
		"darwinrouter.accounting.output_tokens.known":   true,
		"darwinrouter.accounting.cost.known_records":    true,
		"darwinrouter.accounting.cost.unknown_records":  true,
		"darwinrouter.accounting.normalized_cost.known": true,
	}
	wantBuckets := []string{"primary_execution", "fallback", "classifier", "summarizer", "orchestrator_audit", "optional_judge", "routed", "auxiliary", "overall"}
	for _, item := range items {
		if !wantNames[item.Name] {
			continue
		}
		delete(wantNames, item.Name)
		if item.Gauge == nil || len(item.Gauge.DataPoints) != len(wantBuckets) {
			t.Fatal(item.Name, item.Gauge)
		}
		for i, point := range item.Gauge.DataPoints {
			if len(point.Attributes) != 1 || point.Attributes[0].Key != "bucket" || point.Attributes[0].Value.StringValue != wantBuckets[i] {
				t.Fatal(item.Name, point)
			}
		}
	}
	if len(wantNames) != 0 {
		t.Fatal("missing accounting metrics", wantNames)
	}
}
