package search

import (
	"os"
	"strings"
	"testing"
)

func TestHNSWIndexAndQueryMatchPinnedModel(t *testing.T) {
	migration, err := os.ReadFile("../../db/migrations/004_paper_hnsw.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, predicate := range []string{
		"embedding_model = '" + ModelID + "'",
		"embedding_revision = '" + ModelRevision + "'",
		"embedding_text_version = '" + TextVersion + "'",
	} {
		if !strings.Contains(HNSWSQL, predicate) || !strings.Contains(string(migration), predicate) {
			t.Errorf("index and HNSW query must share predicate %s", predicate)
		}
	}
}
