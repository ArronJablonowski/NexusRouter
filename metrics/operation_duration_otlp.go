package metrics

import "strconv"

func otlpOperationDuration(d *OperationDuration, at string) []otlpMetric {
	start, _ := epochNanos(d.StartedAt)
	histogram := &otlpHistogram{AggregationTemporality: 2, DataPoints: []otlpHistogramPoint{}}
	unavailable := &otlpGauge{DataPoints: []otlpPoint{}}
	for _, group := range d.Groups {
		attributes := []otlpAttribute{{Key: "operation", Value: otlpValue{StringValue: group.State}}}
		point := otlpHistogramPoint{
			Attributes: attributes, StartTimeUnixNano: strconv.FormatUint(start, 10), TimeUnixNano: at,
			Count: strconv.FormatInt(group.Count, 10), Sum: group.SumSeconds,
			ExplicitBounds: append([]float64(nil), d.BoundsSeconds...), BucketCounts: []string{},
		}
		for _, count := range group.BucketCounts {
			point.BucketCounts = append(point.BucketCounts, strconv.FormatInt(count, 10))
		}
		histogram.DataPoints = append(histogram.DataPoints, point)
		for _, reason := range group.Unavailable {
			unavailable.DataPoints = append(unavailable.DataPoints, otlpPoint{
				Attributes:   []otlpAttribute{attributes[0], {Key: "reason", Value: otlpValue{StringValue: reason.State}}},
				TimeUnixNano: at, AsInt: strconv.FormatInt(reason.Value, 10),
			})
		}
	}
	return []otlpMetric{
		{Name: "darwinrouter.operation.duration", Unit: "s", Histogram: histogram},
		{Name: "darwinrouter.operation.duration.unavailable", Unit: "{record}", Gauge: unavailable},
	}
}
