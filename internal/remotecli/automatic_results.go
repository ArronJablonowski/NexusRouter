package remotecli

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/ArronJablonowski/NexusRouter/remote"
	"io"
	"time"
)

func automaticResultOperation(ctx context.Context, client *remote.Client, operation, routes, evidence, key, reviewFile string, input io.Reader) (any, error) {
	body, err := io.ReadAll(io.LimitReader(input, remote.MaxBody+1))
	if err != nil || len(body) > remote.MaxBody {
		return nil, remote.ErrInvalid
	}
	var request remote.AutomaticRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, remote.ErrInvalid
	}
	store, err := remote.OpenRouteStore(routes)
	if err != nil {
		return nil, err
	}
	switch operation {
	case "auto-review-state":
		return client.InspectAutomaticReview(ctx, store, evidence, key, request)
	case "auto-status":
		return client.AutomaticStatus(ctx, store, key, request)
	case "auto-cancel":
		return client.CancelAutomatic(ctx, store, key, request)
	case "auto-review":
		review, err := readOutcomeReview(reviewFile)
		if err != nil {
			return nil, err
		}
		return review, client.ReviewAutomaticOutcome(ctx, store, evidence, key, request, review)
	case "auto-output", "auto-reconcile":
		verified, err := client.AutomaticOutcome(ctx, store, key, request)
		if err != nil {
			return nil, err
		}
		if operation == "auto-reconcile" {
			if err = verified.Record(ctx, evidence, time.Now().UTC()); err != nil {
				return nil, err
			}
		}
		receipt := verified.Receipt()
		receiptHash, err := receipt.Digest()
		if err != nil {
			return nil, err
		}
		executionHash, err := receipt.Execution.Digest()
		if err != nil {
			return nil, err
		}
		if operation == "auto-output" {
			return struct{ Text, ReceiptSHA256, ExecutionSHA256 string }{verified.Output(), receiptHash, executionHash}, nil
		}
		return struct {
			ReceiptSHA256, ExecutionSHA256 string
			Receipt                        remote.OutcomeReceipt
		}{receiptHash, executionHash, receipt}, nil
	default:
		return nil, remote.ErrInvalid
	}
}
