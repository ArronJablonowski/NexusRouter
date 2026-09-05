package config

import (
	"math"
	"testing"
)

func TestAuditConfigurationAdmission(t *testing.T) {
	s := Defaults()
	s.Evaluation.AutoReviewModel = "missing"
	if s.Validate() == nil {
		t.Fatal("unknown reviewer accepted")
	}
	for _, cost := range []float64{-1, math.NaN(), math.Inf(1)} {
		s := Defaults()
		s.Evaluation.AutoReviewMaxCost = cost
		if s.Validate() == nil {
			t.Fatal("invalid audit cost accepted")
		}
	}
}
