package main

import (
	"encoding/json"
	"testing"

	"github.com/kirbysss369/SciGraphDB/internal/search"
)

func TestFixedQueryAndPlanParsing(t *testing.T) {
	vector, err := loadQuery("../../experiments/exact_v1/query-positive-x.json")
	if err != nil || len(vector) != search.Dimension {
		t.Fatalf("fixed vector: length=%d err=%v", len(vector), err)
	}
	plan := json.RawMessage(`[{"Plan":{"Node Type":"Limit","Plans":[{"Node Type":"Index Scan","Index Name":"papers_embedding_hnsw_cosine_idx"}]}}]`)
	if !containsIndex(plan, search.HNSWIndexName) || containsIndex(plan, "other_index") {
		t.Fatal("index walk did not inspect nested JSON plans")
	}
}
