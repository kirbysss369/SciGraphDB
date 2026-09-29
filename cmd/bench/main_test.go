package main

import (
	"encoding/json"
	"testing"

	"github.com/kirbysss369/SciGraphDB/internal/search"
)

func TestFixedQueryAndPlanParsing(t *testing.T) {
	_, _, queries, err := loadQueries("../../experiments/exact_v1/baseline.json")
	if err != nil || len(queries) != 3 || len(queries[0].Vector) != search.Dimension {
		t.Fatalf("fixed queries: count=%d err=%v", len(queries), err)
	}
	plan := json.RawMessage(`[{"Plan":{"Node Type":"Limit","Plans":[{"Node Type":"Index Scan","Index Name":"papers_embedding_hnsw_cosine_idx"}]}}]`)
	if !containsIndex(plan, search.HNSWIndexName) || containsIndex(plan, "other_index") {
		t.Fatal("index walk did not inspect nested JSON plans")
	}
}
