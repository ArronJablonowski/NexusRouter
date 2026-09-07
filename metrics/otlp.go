package metrics

import (
	"encoding/json"
	"strconv"
)

// MarshalOTLP encodes an OTLP/HTTP JSON ExportMetricsServiceRequest, without
// sending it. All names and labels come from Snapshot's closed vocabulary.
// Gauges describe durable current counts, not cumulative activity or quality.
// Wire definitions: https://opentelemetry.io/docs/specs/otlp/ and
// https://github.com/open-telemetry/opentelemetry-proto/blob/main/opentelemetry/proto/metrics/v1/metrics.proto
func MarshalOTLP(snapshot Snapshot) ([]byte, error) {
	if snapshot.Validate() != nil {
		return nil, ErrInvalid
	}
	nanos, err := epochNanos(snapshot.ObservedAt)
	if err != nil {
		return nil, ErrInvalid
	}
	at := strconv.FormatUint(nanos, 10)
	items := make([]otlpMetric, 0, len(snapshot.Groups))
	for _, group := range snapshot.Groups {
		if !group.Available {
			continue
		}
		points := make([]otlpPoint, 0, len(group.Counts))
		for _, count := range group.Counts {
			points = append(points, otlpPoint{
				Attributes:   []otlpAttribute{{Key: "state", Value: otlpValue{StringValue: count.State}}},
				TimeUnixNano: at, AsInt: strconv.FormatInt(count.Value, 10),
			})
		}
		items = append(items, otlpMetric{Name: "darwinrouter." + group.Name, Unit: "{record}", Gauge: &otlpGauge{DataPoints: points}})
	}
	if snapshot.TaskDuration != nil {
		items = append(items, otlpTaskDuration(snapshot.TaskDuration, at)...)
	}
	if snapshot.Resources != nil {
		items = append(items, otlpResources(snapshot.Resources, at)...)
	}
	request := otlpRequest{ResourceMetrics: []otlpResourceMetrics{{
		Resource:     otlpResource{Attributes: []otlpAttribute{{Key: "service.name", Value: otlpValue{StringValue: "DarwinRouter"}}}},
		ScopeMetrics: []otlpScopeMetrics{{Scope: otlpScope{Name: "darwinrouter.metrics", Version: "1"}, Metrics: items}},
	}}}
	body, err := json.Marshal(request)
	if err != nil || len(body) > 64<<10 {
		return nil, ErrInvalid
	}
	return body, nil
}

func otlpResources(resources *Resources, at string) []otlpMetric {
	items := make([]otlpMetric, 0, len(resources.Measurements)+1)
	availability := make([]otlpPoint, 0, len(resources.Measurements))
	for i, measurement := range resources.Measurements {
		available := int64(0)
		if measurement.Available {
			available = 1
			items = append(items, otlpMetric{
				Name:  "darwinrouter.resource." + measurement.Name,
				Unit:  resourceDefinitions[i].unit,
				Gauge: &otlpGauge{DataPoints: []otlpPoint{{Attributes: []otlpAttribute{}, TimeUnixNano: at, AsInt: strconv.FormatInt(measurement.Value, 10)}}},
			})
		}
		availability = append(availability, otlpPoint{
			Attributes:   []otlpAttribute{{Key: "resource", Value: otlpValue{StringValue: measurement.Name}}},
			TimeUnixNano: at, AsInt: strconv.FormatInt(available, 10),
		})
	}
	items = append(items, otlpMetric{Name: "darwinrouter.resource.available", Unit: "{bool}", Gauge: &otlpGauge{DataPoints: availability}})
	return items
}

type otlpRequest struct {
	ResourceMetrics []otlpResourceMetrics `json:"resourceMetrics"`
}
type otlpResourceMetrics struct {
	Resource     otlpResource       `json:"resource"`
	ScopeMetrics []otlpScopeMetrics `json:"scopeMetrics"`
}
type otlpResource struct {
	Attributes []otlpAttribute `json:"attributes"`
}
type otlpScopeMetrics struct {
	Scope   otlpScope    `json:"scope"`
	Metrics []otlpMetric `json:"metrics"`
}
type otlpScope struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}
type otlpMetric struct {
	Name      string         `json:"name"`
	Unit      string         `json:"unit"`
	Gauge     *otlpGauge     `json:"gauge,omitempty"`
	Histogram *otlpHistogram `json:"histogram,omitempty"`
}
type otlpGauge struct {
	DataPoints []otlpPoint `json:"dataPoints"`
}
type otlpPoint struct {
	Attributes   []otlpAttribute `json:"attributes"`
	TimeUnixNano string          `json:"timeUnixNano"`
	AsInt        string          `json:"asInt"`
}
type otlpAttribute struct {
	Key   string    `json:"key"`
	Value otlpValue `json:"value"`
}
type otlpValue struct {
	StringValue string `json:"stringValue"`
}
