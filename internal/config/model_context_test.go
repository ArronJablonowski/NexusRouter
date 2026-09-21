package config

import "testing"

func TestModelWorkingContextPreservesAdvertisedMaximum(t *testing.T) {
	model := Model{ContextTokens: 262144, DefaultContextTokens: 32768}
	if model.WorkingContextTokens() != 32768 || model.ContextTokens != 262144 {
		t.Fatal(model)
	}
	model.DefaultContextTokens = 0
	if model.WorkingContextTokens() != 262144 {
		t.Fatal(model)
	}
}
