package importer

import (
	"encoding/json"
	"testing"

	"github.com/kirbysss369/SciGraphDB/internal/openalex"
)

func TestPrepare(t *testing.T) {
	year := 2024
	work := openalex.Work{
		ID: "https://openalex.org/W1", Title: "A paper", PublicationYear: &year,
		CitedByCount: 2, Topics: []openalex.Topic{{ID: "T1", Name: "Science", Score: 0.8}},
		ReferencedWorks: []string{"W2", "W3"},
	}
	item, ok := prepare(work)
	if !ok {
		t.Fatal("valid work rejected")
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(item.metadata), &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["source"] != "openalex" || metadata["reference_count"] != float64(2) || len(metadata) != 2 {
		t.Fatalf("unexpected metadata: %v", metadata)
	}
	for _, mutate := range []func(*openalex.Work){
		func(w *openalex.Work) { w.ID = " " },
		func(w *openalex.Work) { w.Title = "" },
		func(w *openalex.Work) { w.CitedByCount = -1 },
		func(w *openalex.Work) { bad := 0; w.PublicationYear = &bad },
		func(w *openalex.Work) { w.Topics[0].Score = 1.5 },
	} {
		invalid := work
		invalid.Topics = append([]openalex.Topic(nil), work.Topics...)
		mutate(&invalid)
		if _, ok := prepare(invalid); ok {
			t.Fatalf("invalid work accepted: %+v", invalid)
		}
	}
}
