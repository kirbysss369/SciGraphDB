package search

import "testing"

func TestRecallAtK(t *testing.T) {
	twenty := make([]Result, 20)
	missOne := make([]Result, 20)
	for i := range twenty {
		twenty[i] = Result{ID: int64(i + 1)}
		missOne[i] = twenty[i]
	}
	missOne[19] = Result{ID: 99}
	for _, tc := range []struct {
		name   string
		exact  []Result
		approx []Result
		k      int
		want   float64
		ok     bool
	}{
		{"perfect", []Result{{ID: 1}, {ID: 2}}, []Result{{ID: 2}, {ID: 1}}, 10, 1, true},
		{"partial", []Result{{ID: 1}, {ID: 2}, {ID: 3}}, []Result{{ID: 1}, {ID: 4}}, 2, 0.5, true},
		{"recall at twenty", twenty, missOne, 20, 0.95, true},
		{"duplicate does not inflate", []Result{{ID: 1}, {ID: 2}}, []Result{{ID: 1}, {ID: 1}}, 2, 0.5, true},
		{"empty corpus", nil, nil, 10, 0, false},
		{"invalid k", []Result{{ID: 1}}, nil, 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := RecallAtK(tc.exact, tc.approx, tc.k)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("recall=%v ok=%v, want %v %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}
