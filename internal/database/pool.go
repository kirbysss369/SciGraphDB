package database

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func New(ctx context.Context, databaseURL string, connectTimeout time.Duration) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, errors.New("invalid DATABASE_URL")
	}
	cfg.ConnConfig.ConnectTimeout = connectTimeout
	return pgxpool.NewWithConfig(ctx, cfg)
}
