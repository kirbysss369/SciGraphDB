package main

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/kirbysss369/SciGraphDB/internal/search"
)

func TestPercentilesNearestRank(t *testing.T) {
	values := []float64{100, 1, 5, 2, 3, 4, 6, 7, 8, 9}
	for _, tc := range []struct {
		p    int
		want float64
	}{{50, 5}, {95, 100}, {99, 100}, {1, 1}} {
		got, err := percentile(values, tc.p)
		if err != nil || got != tc.want {
			t.Fatalf("p%d = %v, %v; want %v", tc.p, got, err, tc.want)
		}
	}
	if values[0] != 100 {
		t.Fatal("percentile mutated samples")
	}
	for _, tc := range []struct {
		values []float64
		p      int
	}{{nil, 50}, {values, 0}, {[]float64{math.NaN()}, 50}} {
		if _, err := percentile(tc.values, tc.p); err == nil {
			t.Fatal("expected invalid percentile")
		}
	}
}

func TestRecallAtKForRepeatedSamples(t *testing.T) {
	exact := []search.Result{{ID: 1}, {ID: 2}, {ID: 3}}
	for _, tc := range []struct {
		approx []search.Result
		want   float64
	}{
		{[]search.Result{{ID: 1}, {ID: 2}}, 1},
		{[]search.Result{{ID: 2}, {ID: 9}}, .5},
		{[]search.Result{{ID: 2}, {ID: 2}}, .5},
	} {
		got, ok := search.RecallAtK(exact, tc.approx, 2)
		if !ok || got != tc.want {
			t.Fatalf("recall=%v, ok=%v; want=%v", got, ok, tc.want)
		}
	}
}

func TestInvalidConfig(t *testing.T) {
	base := config{SchemaVersion: 1, ExperimentID: "experiment-1", DatasetSnapshot: "snapshot-1",
		QuerySet: "experiments/exact_v1/baseline.json", Seed: 3, K: []int{10, 20},
		WarmupIterations: 1, MeasuredIterations: 3, HNSWEFSearch: 80}
	if err := base.validate(); err != nil {
		t.Fatal(err)
	}
	cases := []func(*config){
		func(c *config) { c.SchemaVersion = 2 },
		func(c *config) { c.QuerySet = "../private.json" },
		func(c *config) { c.K = []int{10, 10} },
		func(c *config) { c.MeasuredIterations = 0 },
		func(c *config) { c.WarmupIterations = -1 },
		func(c *config) { c.HNSWEFSearch = 0 },
		func(c *config) { c.Seed = -1 },
	}
	for i, mutate := range cases {
		c := base
		mutate(&c)
		if err := c.validate(); err == nil {
			t.Fatalf("case %d accepted", i)
		}
	}
	var c config
	if err := decodeStrict([]byte(`{"schema_version":1,"unexpected":true}`), &c); err == nil {
		t.Fatal("unknown field accepted")
	}
	if err := decodeStrict([]byte(`{} {}`), &c); err == nil {
		t.Fatal("trailing object accepted")
	}
}

func TestRedactPlan(t *testing.T) {
	literal := "[" + strings.Repeat("0.125,", search.Dimension-1) + "0.125]"
	input, _ := json.Marshal([]any{map[string]any{"Plan": map[string]any{
		"Index Name": search.HNSWIndexName, "Order By": "embedding <=> '" + literal + "'::vector",
	}}})
	if !containsIndex(input, search.HNSWIndexName) {
		t.Fatal("index detection")
	}
	output, err := redactPlan(input)
	if err != nil || strings.Contains(string(output), literal) || !strings.Contains(string(output), "REDACTED_VECTOR") ||
		!containsIndex(output, search.HNSWIndexName) {
		t.Fatalf("plan was not safely redacted: %v, %v", string(output), err)
	}
}
