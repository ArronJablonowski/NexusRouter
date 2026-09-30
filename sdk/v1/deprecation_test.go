package v1_test

import (
	"context"
	"os"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/providers"
	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
)

func sdkDeprecationRequest() sdk.DeprecationRequest {
	return sdk.DeprecationRequest{Version: 1, ModelID: "chat", Domain: "code", Profile: "default", Policy: sdk.DeprecationPolicy{Window: 50, MinSamples: 1, FailureThreshold: .35}}
}

func TestSDKDeprecationGuardsAndMissingStorage(t *testing.T) {
	request := sdkDeprecationRequest()
	for _, client := range []*sdk.Client{nil, {}} {
		if report, err := client.ModelDeprecation(context.Background(), request); err != sdk.ErrAdmission || !reflect.DeepEqual(report, sdk.DeprecationReport{}) {
			t.Fatal(report, err)
		}
	}
	options, path := sdkToolOptions(t)
	client, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, ctx := range []context.Context{nil, ctx, context.Background()} {
		if report, err := client.ModelDeprecation(ctx, request); err != sdk.ErrAdmission || !reflect.DeepEqual(report, sdk.DeprecationReport{}) {
			t.Fatal(report, err)
		}
	}
	for _, version := range []int{0, 2, -1} {
		bad := request
		bad.Version = version
		if _, err := client.ModelDeprecation(context.Background(), bad); err != sdk.ErrAdmission {
			t.Fatal("invalid version accepted")
		}
	}
	bad := request
	bad.Policy.Window = 1001
	if _, err := client.ModelDeprecation(context.Background(), bad); err != sdk.ErrAdmission {
		t.Fatal("unbounded request accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("inspection created database")
	}
}

func TestSDKDeprecationReadsCurrentFeedbackWithoutInference(t *testing.T) {
	options, path := sdkToolOptions(t)
	var builds, turns atomic.Int32
	options.ProviderFactory = sdkProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		builds.Add(1)
		return sdkProviderStream(func(ctx context.Context, request providers.Request, emit func(providers.Chunk) error) error {
			turns.Add(1)
			return emit(providers.Chunk{Text: "private-answer", Done: true, FinishReason: "stop"})
		}), nil
	})
	client, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	result, err := client.Run(ctx, sdk.Request{Version: 1, ModelID: "chat", Prompt: "private-prompt", Domain: "code", Profile: "default"})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Feedback(ctx, result.TaskID, false, 0); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	report, err := client.ModelDeprecation(ctx, sdkDeprecationRequest())
	if err != nil || report.Validate() != nil || !report.Candidate || !report.ApprovalRequired || report.Failures != 1 || report.ConfiguredModelID != "chat" {
		t.Fatal(report, err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) || builds.Load() != 1 || turns.Load() != 1 {
		t.Fatal("inspection mutated storage or dispatched inference")
	}
	history, err := client.FeedbackHistory(ctx, result.TaskID)
	if err != nil || len(history) != 1 {
		t.Fatal(history, err)
	}
	if err := client.ReviseFeedback(ctx, result.TaskID, history[0].ID, true); err != nil {
		t.Fatal(err)
	}
	revised, err := client.ModelDeprecation(ctx, sdkDeprecationRequest())
	if err != nil || revised.Validate() != nil || revised.Candidate || revised.Failures != 0 || revised.Sampled != 1 || revised.EvidenceDigest == report.EvidenceDigest || builds.Load() != 1 || turns.Load() != 1 {
		t.Fatal(revised, err)
	}
}
