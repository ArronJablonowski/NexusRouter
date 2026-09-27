package cli

import (
	"io"
	"testing"
)

func TestFeedbackRequiresExplicitOutcomeAndCost(t *testing.T) {
	for _, args := range [][]string{
		{}, {"--db", "missing", "--task", "id", "--outcome", "accepted"},
		{"--db", "missing", "--task", "id", "--outcome", "accepted", "--attempt-cost", "NaN"},
		{"--db", "missing", "--task", "id", "--outcome", "maybe", "--attempt-cost", "0"},
		{"--db", "missing", "--task", "id", "--outcome", "withdrawn", "--attempt-cost", "0"},
	} {
		if runFeedback(args, io.Discard, io.Discard) != 2 {
			t.Fatalf("invalid flags accepted: %v", args)
		}
	}
}

func TestFeedbackWithdrawalRequiresExplicitExpectedHead(t *testing.T) {
	for _, args := range [][]string{
		{"revise", "--db", "missing", "--task", "id", "--outcome", "withdrawn"},
		{"show", "--db", "missing", "--task", "id", "--expected", "head", "--outcome", "withdrawn"},
	} {
		if runFeedback(args, io.Discard, io.Discard) != 2 {
			t.Fatal("withdrawal missing authority accepted", args)
		}
	}
}
