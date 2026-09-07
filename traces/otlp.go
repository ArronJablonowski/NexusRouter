package traces

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"time"
)

// MarshalOTLP creates an OTLP/HTTP JSON ExportTraceServiceRequest. Fresh
// random wire IDs prevent durable task identities or stable pseudonyms from
// leaving the process. The input contains no prompt, output, tool, model,
// provider, session, task, turn, attempt, call or worker identity.
func MarshalOTLP(snapshot Snapshot) ([]byte, error) {
	if snapshot.Validate() != nil {
		return nil, ErrInvalid
	}
	spans := make([]otlpSpan, 0)
	for _, trace := range snapshot.Traces {
		traceID, err := randomHex(16)
		if err != nil {
			return nil, ErrInvalid
		}
		ids := make([]string, len(trace.Spans))
		for i := range ids {
			ids[i], err = randomHex(8)
			if err != nil {
				return nil, ErrInvalid
			}
		}
		for i, span := range trace.Spans {
			start, startErr := epochNanos(span.StartedAt)
			end, endErr := epochNanos(span.EndedAt)
			if startErr != nil || endErr != nil {
				return nil, ErrInvalid
			}
			item := otlpSpan{TraceID: traceID, SpanID: ids[i], Name: "darwinrouter." + span.Name, Kind: 1, StartTimeUnixNano: strconv.FormatUint(start, 10), EndTimeUnixNano: strconv.FormatUint(end, 10), Attributes: []otlpAttribute{{Key: "darwinrouter.outcome", Value: otlpValue{StringValue: span.Outcome}}}}
			if span.Parent >= 0 {
				item.ParentSpanID = ids[span.Parent]
			}
			spans = append(spans, item)
		}
	}
	request := otlpRequest{ResourceSpans: []otlpResourceSpans{{Resource: otlpResource{Attributes: []otlpAttribute{{Key: "service.name", Value: otlpValue{StringValue: "DarwinRouter"}}}}, ScopeSpans: []otlpScopeSpans{{Scope: otlpScope{Name: "darwinrouter.traces", Version: "1"}, Spans: spans}}}}}
	body, err := json.Marshal(request)
	if err != nil || len(body) > 256<<10 {
		return nil, ErrInvalid
	}
	return body, nil
}

func randomHex(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func epochNanos(at time.Time) (uint64, error) {
	if at.IsZero() || at.Location() != time.UTC || at.Unix() < 0 || at.Unix() > int64(^uint64(0)/1_000_000_000) {
		return 0, ErrInvalid
	}
	return uint64(at.Unix())*1_000_000_000 + uint64(at.Nanosecond()), nil
}

type otlpRequest struct {
	ResourceSpans []otlpResourceSpans `json:"resourceSpans"`
}
type otlpResourceSpans struct {
	Resource   otlpResource     `json:"resource"`
	ScopeSpans []otlpScopeSpans `json:"scopeSpans"`
}
type otlpResource struct {
	Attributes []otlpAttribute `json:"attributes"`
}
type otlpScopeSpans struct {
	Scope otlpScope  `json:"scope"`
	Spans []otlpSpan `json:"spans"`
}
type otlpScope struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}
type otlpSpan struct {
	TraceID           string          `json:"traceId"`
	SpanID            string          `json:"spanId"`
	ParentSpanID      string          `json:"parentSpanId,omitempty"`
	Name              string          `json:"name"`
	Kind              int             `json:"kind"`
	StartTimeUnixNano string          `json:"startTimeUnixNano"`
	EndTimeUnixNano   string          `json:"endTimeUnixNano"`
	Attributes        []otlpAttribute `json:"attributes"`
}
type otlpAttribute struct {
	Key   string    `json:"key"`
	Value otlpValue `json:"value"`
}
type otlpValue struct {
	StringValue string `json:"stringValue"`
}
