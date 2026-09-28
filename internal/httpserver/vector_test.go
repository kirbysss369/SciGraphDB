package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kirbysss369/SciGraphDB/internal/search"
)

func TestVectorSearchRequestAndResponse(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	vector := make([]float64, search.Dimension)
	vector[0] = 1
	var calls int
	handler := NewVectorSearch(func(ctx context.Context, got []float64, limit int) ([]search.Result, error) {
		calls++
		if got[0] != 1 || limit != search.DefaultLimit {
			t.Fatalf("query values: first=%v limit=%d", got[0], limit)
		}
		return []search.Result{{ID: 7, OpenAlexID: "W7", Title: "Example", Distance: 0.25}}, nil
	}, logger)
	request, _ := json.Marshal(map[string]any{"embedding": vector})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/search/vector", bytes.NewReader(request)))
	if recorder.Code != http.StatusOK || calls != 1 || !strings.Contains(recorder.Body.String(), `"distance":0.25`) {
		t.Fatalf("status=%d calls=%d body=%s", recorder.Code, calls, recorder.Body.String())
	}
	var response struct{ Results []search.Result `json:"results"` }
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || len(response.Results) != 1 || response.Results[0].ID != 7 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestVectorSearchRejectsInvalidRequests(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := NewVectorSearch(func(context.Context, []float64, int) ([]search.Result, error) {
		t.Fatal("invalid request reached database")
		return nil, nil
	}, logger)
	vector := make([]float64, search.Dimension)
	vector[0] = 1
	base, _ := json.Marshal(map[string]any{"embedding": vector})
	for _, body := range []string{
		`{`, `null`, `{}`, `{"embedding":[1]}`, `{"embedding":null}`,
		`{"embedding":[1e400]}`, `{"embedding":[1],"unknown":true}`,
		`{"embedding":[1],"limit":0}`, `{"embedding":[1],"limit":101}`,
		string(base) + ` {}`, strings.Repeat(" ", maxVectorBody+1) + string(base),
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/search/vector", strings.NewReader(body)))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("input length %d: status=%d", len(body), recorder.Code)
		}
	}
}

func TestVectorSearchUnavailable(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := NewVectorSearch(func(context.Context, []float64, int) ([]search.Result, error) {
		return nil, errors.New("offline")
	}, logger)
	vector := make([]float64, search.Dimension)
	vector[0] = 1
	body, _ := json.Marshal(map[string]any{"embedding": vector, "limit": 2})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/search/vector", bytes.NewReader(body)))
	if recorder.Code != http.StatusServiceUnavailable || strings.Contains(recorder.Body.String(), "offline") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
