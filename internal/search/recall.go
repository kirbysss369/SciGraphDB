package search

// RecallAtK counts unique approximate IDs found in the exact Top-K set.
// If fewer than k exact results exist, the exact result count is the
// denominator. ok is false when the corpus has no eligible results.
func RecallAtK(exact, approximate []Result, k int) (score float64, ok bool) {
	if k <= 0 || len(exact) == 0 {
		return 0, false
	}
	if k > len(exact) {
		k = len(exact)
	}
	wanted := make(map[int64]struct{}, k)
	for _, result := range exact[:k] {
		wanted[result.ID] = struct{}{}
	}
	seen := make(map[int64]struct{}, k)
	matches := 0
	for i, result := range approximate {
		if i >= k {
			break
		}
		if _, duplicate := seen[result.ID]; duplicate {
			continue
		}
		seen[result.ID] = struct{}{}
		if _, found := wanted[result.ID]; found {
			matches++
		}
	}
	return float64(matches) / float64(len(wanted)), true
}
