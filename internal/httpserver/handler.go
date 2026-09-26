package httpserver

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

type Pinger interface {
	Ping(context.Context) error
}

func New(pinger Pinger, pingTimeout time.Duration, logger *slog.Logger) http.Handler {
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
	return mux
}

func write(w http.ResponseWriter, status int, body string, logger *slog.Logger) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	if _, err := w.Write([]byte(body)); err != nil {
		logger.Warn("write response failed", "error", err)
	}
}
