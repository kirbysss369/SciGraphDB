package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/kirbysss369/SciGraphDB/internal/search"
)

func TestReadVectorFile(t *testing.T) {
	data, err := os.ReadFile("../../experiments/exact_v1/query-positive-x.json")
	if err != nil {
		t.Fatal(err)
	}
	vector, err := readVector(bytes.NewReader(data))
	if err != nil || len(vector) != search.Dimension || vector[0] != 1 {
		t.Fatalf("vector length=%d err=%v", len(vector), err)
	}
	for _, invalid := range []string{
		strings.Replace(string(data), search.ModelRevision, "other-revision", 1),
		string(data) + "{}",
		strings.Repeat(" ", maxFileSize+1) + string(data),
	} {
		if _, err := readVector(strings.NewReader(invalid)); err == nil {
			t.Fatal("invalid query vector file accepted")
		}
	}
}
