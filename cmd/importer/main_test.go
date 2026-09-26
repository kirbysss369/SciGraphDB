package main

import (
	"context"
	"testing"
)

func TestInvalidFlags(t *testing.T) {
	for _, args := range [][]string{
		{}, {"--search", "x", "--limit", "0"}, {"--search", "x", "--from-year", "2025", "--to-year", "2020"},
		{"--search", "x", "--from-year", "2101"}, {"--search", "x", "trailing"},
	} {
		if _, err := run(context.Background(), args, nil); err == nil {
			t.Fatalf("invalid arguments accepted: %v", args)
		}
	}
}
