package main

import (
	"encoding/json"
	"math"
	"slices"
	"testing"

	"github.com/kirbysss369/SciGraphDB/internal/search"
)

func TestFilterSelectionUsesActualCounts(t *testing.T) {
	// 40 eligible rows without years plus 3000 in exact strata.
	years := []yearCount{{2005, 30}, {2004, 120}, {2003, 150}, {2002, 450}, {2001, 750}, {2000, 1500}}
	cases, skipped := selectFilterCases(3040, years, []int{100, 50, 25, 10, 5, 1}, 20)
	if len(cases) != 6 || len(skipped) != 0 {
		t.Fatalf("cases=%v skipped=%v", cases, skipped)
	}
	wanted := []int64{3040, 1500, 750, 300, 150, 30}
	for i, c := range cases {
		if c.Matching != wanted[i] || math.Abs(c.Actual-float64(wanted[i])/3040) > 1e-12 {
			t.Fatalf("case %d: %+v", i, c)
		}
	}
	cases, skipped = selectFilterCases(100, []yearCount{{2020, 50}, {2019, 50}}, []int{100, 50, 25, 10, 5, 1}, 20)
	if len(cases) != 2 || len(skipped) != 4 {
		t.Fatalf("small dataset: %v %v", cases, skipped)
	}
}

func TestFilteredConfigValidation(t *testing.T) {
	cfg := filteredConfig{SchemaVersion: 2, ExperimentID: "filtered-test", DatasetSnapshot: "fixture-v2",
		QuerySet: "experiments/exact_v1/baseline.json", Seed: 42, K: []int{10, 20},
		TargetPercentages: []int{100, 50, 25, 10, 5, 1}, WarmupIterations: 1, MeasuredIterations: 3,
		IVFLists: 3, IVFProbes: 1, IVFMaxProbes: 3, HNSWEFSearch: 80,
		HNSWIterativeScan: "strict_order", IVFIterativeScan: "relaxed_order"}
	if err := cfg.validate(); err != nil {
		t.Fatal(err)
	}
	mutations := []func(*filteredConfig){
		func(c *filteredConfig) { c.IVFProbes = 3 },
		func(c *filteredConfig) { c.IVFLists = 1 },
		func(c *filteredConfig) { c.IVFMaxProbes = 0 },
		func(c *filteredConfig) { c.HNSWIterativeScan = "anything" },
		func(c *filteredConfig) { c.IVFIterativeScan = "strict_order" },
		func(c *filteredConfig) { c.K = []int{10} },
		func(c *filteredConfig) { c.TargetPercentages = []int{100, 50} },
	}
	for i, mutate := range mutations {
		other := cfg
		mutate(&other)
		if err := other.validate(); err == nil {
			t.Fatalf("invalid config %d accepted", i)
		}
	}
}

func TestFilteredQueryShapesAndIndexDetection(t *testing.T) {
	plan := json.RawMessage(`[{"Plan":{"Node Type":"Limit","Plans":[{"Node Type":"Index Scan","Index Name":"papers_embedding_ivfflat_cosine_idx"}]}}]`)
	if got := planIndexes(plan); !slices.Equal(got, []string{search.IVFFlatIndexName}) {
		t.Fatalf("index names=%v", got)
	}
}

func TestFilteredShortResults(t *testing.T) {
	row := filteredQuery{K: 20, Filter: filterCase{Matching: 30}, Samples: []filteredSample{
		{LatencyMS: 2, Recall: .15, ReturnedCount: 3},
		{LatencyMS: 1, Recall: 1, ReturnedCount: 20},
		{LatencyMS: 3, Recall: .5, ReturnedCount: 10},
	}}
	if err := summarizeFiltered(&row); err != nil {
		t.Fatal(err)
	}
	if row.ReturnedCount != 3 || row.ReturnedCountMin != 3 || row.ReturnedCountMax != 20 ||
		row.TooFewSamples != 2 || row.P50MS != 2 || row.P95MS != 3 || row.P99MS != 3 {
		t.Fatalf("incorrect short-result summary: %+v", row)
	}
}
