package main

import (
	"context"
	"testing"
)

func TestInvalidBulkArguments(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"--job", "bad job", "--filter", "type:article"},
		{"--job", "a", "--filter", ""},
		{"--job", "a", "--filter", "type:article", "--limit", "500001"},
		{"--job", "a", "--filter", "type:article", "trailing"},
	} {
		if err := run(context.Background(), args); err == nil {
			t.Fatalf("accepted invalid bulk arguments: %v", args)
		}
	}
}
