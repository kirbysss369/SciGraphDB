package httpserver

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/kirbysss369/SciGraphDB/internal/search"
)

type Pinger interface {
	Ping(context.Context) error
}

type VectorSearchFunc func(context.Context, []float64, int) ([]search.Result, error)

func New(pinger Pinger, pingTimeout time.Duration, logger *slog.Logger, exact VectorSearchFunc) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		write(w, http.StatusOK, "ok\n", logger)
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), pingTimeout)
		defer cancel()
		if err := pinger.Ping(ctx); err != nil {
			logger.Warn("database unavailable")
			write(w, http.StatusServiceUnavailable, "unavailable\n", logger)
			return
		}
		write(w, http.StatusOK, "ready\n", logger)
	})
	mux.Handle("POST /api/v1/search/vector", NewVectorSearch(exact, logger))
	return mux
}

func write(w http.ResponseWriter, status int, body string, logger *slog.Logger) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	if _, err := w.Write([]byte(body)); err != nil {
		logger.Warn("write response failed", "error", err)
	}
}
