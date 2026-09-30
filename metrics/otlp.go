package metrics

import (
	"encoding/json"
	"strconv"

	"github.com/ArronJablonowski/NexusRouter/accounting"
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
		items = append(items, otlpMetric{Name: "nexusrouter." + group.Name, Unit: "{record}", Gauge: &otlpGauge{DataPoints: points}})
	}
	if snapshot.TaskDuration != nil {
		items = append(items, otlpTaskDuration(snapshot.TaskDuration, at)...)
	}
	if snapshot.OperationDuration != nil {
		items = append(items, otlpOperationDuration(snapshot.OperationDuration, at)...)
	}
	if snapshot.Resources != nil {
		items = append(items, otlpResources(snapshot.Resources, at)...)
	}
	if snapshot.Accounting != nil {
		items = append(items, otlpAccounting(snapshot.Accounting, at)...)
	}
	request := otlpRequest{ResourceMetrics: []otlpResourceMetrics{{
		Resource:     otlpResource{Attributes: []otlpAttribute{{Key: "service.name", Value: otlpValue{StringValue: "NexusRouter"}}}},
		ScopeMetrics: []otlpScopeMetrics{{Scope: otlpScope{Name: "nexusrouter.metrics", Version: "1"}, Metrics: items}},
	}}}
	body, err := json.Marshal(request)
	if err != nil || len(body) > 64<<10 {
		return nil, ErrInvalid
	}
	return body, nil
}

func otlpAccounting(totals *accounting.Totals, at string) []otlpMetric {
	types := []struct {
		name  string
		total accounting.Total
	}{
		{"primary_execution", totals.Primary}, {"fallback", totals.Fallback},
		{"classifier", totals.Classifier}, {"summarizer", totals.Summarizer},
		{"orchestrator_audit", totals.OrchestratorAudit}, {"optional_judge", totals.Judge},
		{"routed", totals.Routed}, {"auxiliary", totals.Auxiliary}, {"overall", totals.Overall},
	}
	integer := func(name, unit string, value func(accounting.Total) int64) otlpMetric {
		points := make([]otlpPoint, 0, len(types))
		for _, item := range types {
			points = append(points, otlpPoint{Attributes: []otlpAttribute{{Key: "bucket", Value: otlpValue{StringValue: item.name}}}, TimeUnixNano: at, AsInt: strconv.FormatInt(value(item.total), 10)})
		}
		return otlpMetric{Name: "nexusrouter.accounting." + name, Unit: unit, Gauge: &otlpGauge{DataPoints: points}}
	}
	items := []otlpMetric{
		integer("records", "{record}", func(t accounting.Total) int64 { return t.Records }),
		integer("usage.known_records", "{record}", func(t accounting.Total) int64 { return t.KnownUsageRecords }),
		integer("usage.unknown_records", "{record}", func(t accounting.Total) int64 { return t.UnknownUsageRecords }),
		integer("input_tokens.known", "{token}", func(t accounting.Total) int64 { return t.KnownInputTokens }),
		integer("output_tokens.known", "{token}", func(t accounting.Total) int64 { return t.KnownOutputTokens }),
		integer("cost.known_records", "{record}", func(t accounting.Total) int64 { return t.KnownCostRecords }),
		integer("cost.unknown_records", "{record}", func(t accounting.Total) int64 { return t.UnknownCostRecords }),
	}
	costs := make([]otlpPoint, 0, len(types))
	for _, item := range types {
		value := item.total.KnownNormalizedCost
		costs = append(costs, otlpPoint{Attributes: []otlpAttribute{{Key: "bucket", Value: otlpValue{StringValue: item.name}}}, TimeUnixNano: at, AsDouble: &value})
	}
	return append(items, otlpMetric{Name: "nexusrouter.accounting.normalized_cost.known", Unit: "USD", Gauge: &otlpGauge{DataPoints: costs}})
}

func otlpResources(resources *Resources, at string) []otlpMetric {
	items := make([]otlpMetric, 0, len(resources.Measurements)+1)
	availability := make([]otlpPoint, 0, len(resources.Measurements))
	for i, measurement := range resources.Measurements {
		available := int64(0)
		if measurement.Available {
			available = 1
			items = append(items, otlpMetric{
				Name:  "nexusrouter.resource." + measurement.Name,
				Unit:  resourceDefinitions[i].unit,
				Gauge: &otlpGauge{DataPoints: []otlpPoint{{Attributes: []otlpAttribute{}, TimeUnixNano: at, AsInt: strconv.FormatInt(measurement.Value, 10)}}},
			})
		}
		availability = append(availability, otlpPoint{
			Attributes:   []otlpAttribute{{Key: "resource", Value: otlpValue{StringValue: measurement.Name}}},
			TimeUnixNano: at, AsInt: strconv.FormatInt(available, 10),
		})
	}
	items = append(items, otlpMetric{Name: "nexusrouter.resource.available", Unit: "{bool}", Gauge: &otlpGauge{DataPoints: availability}})
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
	AsInt        string          `json:"asInt,omitempty"`
	AsDouble     *float64        `json:"asDouble,omitempty"`
}
type otlpAttribute struct {
	Key   string    `json:"key"`
	Value otlpValue `json:"value"`
}
type otlpValue struct {
	StringValue string `json:"stringValue"`
}
