package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/kirbysss369/SciGraphDB/internal/config"
	"github.com/kirbysss369/SciGraphDB/internal/database"
	"github.com/kirbysss369/SciGraphDB/internal/httpserver"
	"github.com/kirbysss369/SciGraphDB/internal/search"
)

const shutdownTimeout = 10 * time.Second

func Run(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	pool, err := database.New(ctx, cfg.DatabaseURL, cfg.DBConnectTimeout)
	if err != nil {
		return fmt.Errorf("initialize database pool: %w", err)
	}
	defer pool.Close()

	listener, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	server := &http.Server{
		Handler: httpserver.New(pool, cfg.DBPingTimeout, logger, func(ctx context.Context, vector []float64, limit int) ([]search.Result, error) {
			return search.Exact(ctx, pool, vector, limit)
		}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       cfg.HTTPReadTimeout,
		WriteTimeout:      cfg.HTTPWriteTimeout,
		IdleTimeout:       cfg.HTTPIdleTimeout,
	}
	logger.Info("HTTP server listening", "addr", listener.Addr().String())
	return serve(ctx, listener, server)
}

func serve(ctx context.Context, listener net.Listener, server *http.Server) error {
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve(listener) }()

	select {
	case err := <-serveResult:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			if closeErr := server.Close(); closeErr != nil {
				return fmt.Errorf("shutdown: %w (close: %v)", err, closeErr)
			}
			return fmt.Errorf("shutdown: %w", err)
		}
		if err := <-serveResult; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve after shutdown: %w", err)
		}
		return nil
	}
}
