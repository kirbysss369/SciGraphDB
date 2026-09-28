package httpserver

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/kirbysss369/SciGraphDB/internal/search"
)

const maxVectorBody = 64 << 10

func NewVectorSearch(exact VectorSearchFunc, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxVectorBody)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		var request struct {
			Embedding []float64 `json:"embedding"`
			Limit     *int      `json:"limit"`
		}
		if err := decoder.Decode(&request); err != nil {
			http.Error(w, "invalid JSON request", http.StatusBadRequest)
			return
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			http.Error(w, "expected one JSON object", http.StatusBadRequest)
			return
		}
		limit := search.DefaultLimit
		if request.Limit != nil {
			limit = *request.Limit
		}
		if err := search.ValidateLimit(limit); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if _, err := search.VectorLiteral(request.Embedding); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		results, err := exact(ctx, request.Embedding, limit)
		if err != nil {
			logger.Warn("vector search unavailable", "error", err)
			http.Error(w, "search unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(struct {
			Results []search.Result `json:"results"`
		}{Results: results}); err != nil {
			logger.Warn("write vector search response failed", "error", err)
		}
	})
}
