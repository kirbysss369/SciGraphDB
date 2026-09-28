package search

import (
	"math"
	"strings"
	"testing"
)

func TestVectorLiteralAndLimit(t *testing.T) {
	valid := make([]float64, Dimension)
	valid[0] = 1
	literal, err := VectorLiteral(valid)
	if err != nil || !strings.HasPrefix(literal, "[1,0,0,") {
		t.Fatalf("valid embedding: %q, %v", literal, err)
	}
	for _, test := range []struct {
		name   string
		vector []float64
	}{
		{"too short", valid[:Dimension-1]},
		{"zero", make([]float64, Dimension)},
		{"NaN", replace(valid, math.NaN())},
		{"infinite", replace(valid, math.Inf(1))},
		{"float32 overflow", replace(valid, math.MaxFloat64)},
		{"float32 underflow", replace(make([]float64, Dimension), math.SmallestNonzeroFloat64)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := VectorLiteral(test.vector); err == nil {
				t.Fatal("expected invalid embedding")
			}
		})
	}
	for _, limit := range []int{0, -1, MaxLimit + 1} {
		if err := ValidateLimit(limit); err == nil {
			t.Fatalf("accepted limit %d", limit)
		}
	}
	if err := ValidateLimit(MaxLimit); err != nil {
		t.Fatal(err)
	}
	for _, ef := range []int{0, -1, MaxEFSearch + 1} {
		if err := ValidateEFSearch(ef); err == nil {
			t.Fatalf("accepted ef_search %d", ef)
		}
	}
	if err := ValidateEFSearch(DefaultEFSearch); err != nil {
		t.Fatal(err)
	}
}

func replace(vector []float64, value float64) []float64 {
	copyOf := append([]float64(nil), vector...)
	copyOf[0] = value
	return copyOf
}
