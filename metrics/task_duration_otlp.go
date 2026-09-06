package metrics

import "strconv"

type otlpHistogram struct {
	DataPoints             []otlpHistogramPoint `json:"dataPoints"`
	AggregationTemporality int                  `json:"aggregationTemporality"`
}

type otlpHistogramPoint struct {
	Attributes        []otlpAttribute `json:"attributes"`
	StartTimeUnixNano string          `json:"startTimeUnixNano"`
	TimeUnixNano      string          `json:"timeUnixNano"`
	Count             string          `json:"count"`
	Sum               float64         `json:"sum"`
	BucketCounts      []string        `json:"bucketCounts"`
	ExplicitBounds    []float64       `json:"explicitBounds"`
}

// Input is already canonical and validated by MarshalOTLP.
func otlpTaskDuration(d *TaskDuration, at string) []otlpMetric {
	start, _ := epochNanos(d.StartedAt)
	h := &otlpHistogram{AggregationTemporality: 2, DataPoints: []otlpHistogramPoint{}}
	u := &otlpGauge{DataPoints: []otlpPoint{}}
	for _, g := range d.Groups {
		attrs := []otlpAttribute{{Key: "state", Value: otlpValue{StringValue: g.State}}}
		p := otlpHistogramPoint{Attributes: attrs, StartTimeUnixNano: strconv.FormatUint(start, 10), TimeUnixNano: at, Count: strconv.FormatInt(g.Count, 10), Sum: g.SumSeconds, ExplicitBounds: append([]float64(nil), d.BoundsSeconds...), BucketCounts: []string{}}
		for _, n := range g.BucketCounts {
			p.BucketCounts = append(p.BucketCounts, strconv.FormatInt(n, 10))
		}
		h.DataPoints = append(h.DataPoints, p)
		for _, reason := range g.Unavailable {
			u.DataPoints = append(u.DataPoints, otlpPoint{Attributes: []otlpAttribute{attrs[0], {Key: "reason", Value: otlpValue{StringValue: reason.State}}}, TimeUnixNano: at, AsInt: strconv.FormatInt(reason.Value, 10)})
		}
	}
	return []otlpMetric{{Name: "darwinrouter.task.duration", Unit: "s", Histogram: h}, {Name: "darwinrouter.task.duration.unavailable", Unit: "{record}", Gauge: u}}
}
