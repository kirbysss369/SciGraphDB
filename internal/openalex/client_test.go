package openalex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fixtureClient(t *testing.T, handler http.HandlerFunc, key string, timeout time.Duration) (*Client, func()) {
	t.Helper()
	server := httptest.NewServer(handler)
	client, err := New(Config{BaseURL: server.URL, APIKey: key, Timeout: timeout}, server.Client())
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	return client, server.Close
}

func TestSearchFieldsAndAbstract(t *testing.T) {
	client, closeServer := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/works" || r.URL.Query().Get("search") != "graph science" ||
			r.URL.Query().Get("cursor") != "*" || r.URL.Query().Get("per_page") != "2" ||
			r.URL.Query().Get("select") != fields || r.Header.Get("Authorization") != "Bearer private-key" {
			t.Errorf("incorrect OpenAlex request")
		}
		fmt.Fprint(w, `{"meta":{"next_cursor":null},"results":[{"id":"https://openalex.org/W1","doi":"https://doi.org/10.1/x","display_name":"One","publication_year":2024,"cited_by_count":7,"abstract_inverted_index":{"again":[8],"First":[0],"word":[2,7]},"topics":[{"id":"T1","display_name":"Topic","score":0.9}],"referenced_works":["W2"]},{"id":"W2","display_name":"Two","abstract_inverted_index":null}]}`)
	}, "private-key", time.Second)
	defer closeServer()
	works, err := client.Search(context.Background(), "graph science", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(works) != 2 || works[0].ID != "https://openalex.org/W1" || works[0].DOI != "https://doi.org/10.1/x" ||
		works[0].Title != "One" || works[0].PublicationYear == nil || *works[0].PublicationYear != 2024 ||
		works[0].CitedByCount != 7 || works[0].Abstract == nil || *works[0].Abstract != "First word word again" ||
		len(works[0].Topics) != 1 || works[0].Topics[0].Score != 0.9 ||
		len(works[0].ReferencedWorks) != 1 || works[0].ReferencedWorks[0] != "W2" ||
		works[1].Abstract != nil || works[1].PublicationYear != nil {
		t.Fatalf("unexpected decoded works: %+v", works)
	}
}

func TestSearchCursorPages(t *testing.T) {
	var calls atomic.Int32
	client, closeServer := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		var results []map[string]string
		if call == 1 {
			if r.URL.Query().Get("cursor") != "*" || r.URL.Query().Get("per_page") != "100" {
				t.Errorf("first page query: %s", r.URL.RawQuery)
			}
			for i := range 100 {
				results = append(results, map[string]string{"id": fmt.Sprint(i), "display_name": "Paper"})
			}
		} else {
			if r.URL.Query().Get("cursor") != "next+cursor" || r.URL.Query().Get("per_page") != "1" {
				t.Errorf("second page query: %s", r.URL.RawQuery)
			}
			results = append(results, map[string]string{"id": "100", "display_name": "Last"})
		}
		cursor := any(nil)
		if call == 1 {
			cursor = "next+cursor"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"meta": map[string]any{"next_cursor": cursor}, "results": results})
	}, "", time.Second)
	defer closeServer()
	works, err := client.Search(context.Background(), "test", 101)
	if err != nil || len(works) != 101 || calls.Load() != 2 || works[100].Title != "Last" {
		t.Fatalf("pagination: len=%d calls=%d err=%v", len(works), calls.Load(), err)
	}
}

func TestRetryStatuses(t *testing.T) {
	for _, tt := range []struct {
		name      string
		status    int
		header    string
		wantCalls int32
		wantError string
	}{
		{"rate limit", 429, "0", 2, ""},
		{"server error", 503, "0", 2, ""},
		{"bad request", 400, "", 1, "HTTP 400"},
		{"daily budget", 429, "remaining-zero", 1, "budget exhausted"},
		{"long retry", 429, "3600", 1, "retry delay exceeds"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			client, closeServer := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					if tt.header == "remaining-zero" {
						w.Header().Set("X-RateLimit-Remaining", "0")
					} else if tt.header != "" {
						w.Header().Set("Retry-After", tt.header)
					}
					w.WriteHeader(tt.status)
					fmt.Fprint(w, `{"error":"private-key"}`)
					return
				}
				fmt.Fprint(w, `{"meta":{"next_cursor":null},"results":[{"id":"W1","display_name":"One"}]}`)
			}, "private-key", time.Second)
			defer closeServer()
			works, err := client.Search(context.Background(), "test", 1)
			if calls.Load() != tt.wantCalls || strings.Contains(fmt.Sprint(err), "private-key") {
				t.Fatalf("calls=%d, error=%v", calls.Load(), err)
			}
			if tt.wantError == "" {
				if err != nil || len(works) != 1 {
					t.Fatalf("retry failed: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("expected %q, got %v", tt.wantError, err)
			}
		})
	}
}

func TestMalformedJSON(t *testing.T) {
	client, closeServer := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"results":[`)
	}, "", time.Second)
	defer closeServer()
	if _, err := client.Search(context.Background(), "test", 1); err == nil || !strings.Contains(err.Error(), "invalid OpenAlex JSON") {
		t.Fatalf("unexpected decode result: %v", err)
	}
}

func TestRequestTimeout(t *testing.T) {
	client, closeServer := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}, "", 20*time.Millisecond)
	defer closeServer()
	_, err := client.Search(context.Background(), "test", 1)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected request timeout, got %v", err)
	}
}

func TestCancellationDuringBackoff(t *testing.T) {
	started := make(chan struct{})
	client, closeServer := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusTooManyRequests)
		close(started)
	}, "", time.Second)
	defer closeServer()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := client.Search(ctx, "test", 1); result <- err }()
	<-started
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancellation, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("request did not stop on cancellation")
	}
}

func TestConfigAndInputValidation(t *testing.T) {
	for _, tc := range []struct{ name, value string }{
		{"OPENALEX_TIMEOUT", "0s"},
		{"OPENALEX_TIMEOUT", "abc"},
		{"OPENALEX_BASE_URL", "https://example.org/?api_key=private-key"},
		{"OPENALEX_BASE_URL", "ftp://example.org"},
	} {
		t.Run(tc.name+tc.value, func(t *testing.T) {
			t.Setenv(tc.name, tc.value)
			if _, err := LoadConfig(); err == nil || strings.Contains(err.Error(), "private-key") {
				t.Fatalf("expected safe config error, got %v", err)
			}
		})
	}
	client, err := New(Config{BaseURL: "https://api.openalex.org", Timeout: time.Second}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		query string
		limit int
	}{{"", 1}, {"test", 0}, {"test", 10001}} {
		if _, err := client.Search(context.Background(), tc.query, tc.limit); err == nil {
			t.Fatalf("expected validation error for %q, %d", tc.query, tc.limit)
		}
	}
}

func TestAbstractEmptyAndNegative(t *testing.T) {
	empty, err := reconstructAbstract(map[string][]int{})
	if err != nil || empty == nil || *empty != "" {
		t.Fatalf("empty index: %v, %v", empty, err)
	}
	if _, err := reconstructAbstract(map[string][]int{"bad": {-1}}); err == nil {
		t.Fatal("negative position accepted")
	}
}
