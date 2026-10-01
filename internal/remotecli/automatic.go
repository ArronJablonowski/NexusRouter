package remotecli

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/remote"
	"github.com/ArronJablonowski/NexusRouter/submissions"
	"io"
)

func automaticOperation(ctx context.Context, client *remote.Client, operation, routes, evidence, key string, input io.Reader) (any, error) {
	body, err := io.ReadAll(io.LimitReader(input, remote.MaxBody+1))
	if err != nil || len(body) > remote.MaxBody {
		return nil, remote.ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if operation == "auto-dispatch" {
		var request remote.AutomaticRequest
		if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF || request.Routing.AllowExploration {
			return nil, remote.ErrInvalid
		}
		store, err := remote.OpenRouteStore(routes)
		if err != nil {
			return nil, err
		}
		status, choice, err := client.DispatchDiscovered(ctx, store, evidence, key, request, harness.DefaultPolicy(), 0)
		return struct {
			Status submissions.Status
			Choice remote.AutomaticChoice
		}{status, choice}, err
	}
	var request harness.Request
	if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF || request.AllowExploration {
		return nil, remote.ErrInvalid
	}
	discovered, err := client.DiscoverCandidates(ctx, request)
	if err != nil {
		return nil, err
	}
	if operation == "candidates" {
		return discovered, nil
	}
	selected, err := client.RankRecordedCandidates(ctx, evidence, request, harness.DefaultPolicy(), discovered.Candidates, 0)
	return struct {
		Discovery remote.CandidateDiscovery
		Selection remote.DestinationSelection
	}{discovered, selected}, err
}
