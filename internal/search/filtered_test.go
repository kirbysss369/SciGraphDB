package search

import (
	"strings"
	"testing"
)

func TestFilteredQueryShapes(t *testing.T) {
	cutoff := 2020
	for _, method := range []string{"exact", "hnsw", "ivfflat"} {
		sql, args, err := filteredStatement(method, &cutoff, "[1]", 10)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(sql, "publication_year >=") || len(args) == 0 {
			t.Fatalf("%s missing inner filter", method)
		}
		if method == "exact" && !strings.Contains(sql, "AS MATERIALIZED") {
			t.Fatal("exact filtered query is not materialized")
		}
		if method == "ivfflat" && !strings.Contains(sql, "public.bench_ivf_vector(embedding)::public.vector(384) <=>") {
			t.Fatal("IVFFlat expression does not match its index")
		}
	}
	if _, _, err := filteredStatement("unknown", nil, "[1]", 10); err == nil {
		t.Fatal("unknown search method accepted")
	}
	for _, mode := range []string{"off", "strict_order", "relaxed_order"} {
		if err := (FilterOptions{EFSearch: 80, Probes: 1, MaxProbes: 3, IterativeScan: mode}).Validate(); err != nil {
			t.Fatal(err)
		}
	}
}
