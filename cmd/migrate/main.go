package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kirbysss369/SciGraphDB/db/migrations"
	"github.com/kirbysss369/SciGraphDB/internal/devconfig"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := run(os.Args[1:]); err != nil {
		logger.Error("migration failed", "error", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) != 1 || (args[0] != "up" && args[0] != "down" && args[0] != "status") {
		return errors.New("usage: go run ./cmd/migrate {up|down|status}")
	}
	if err := devconfig.Load(); err != nil {
		return err
	}
	databaseURL, err := devconfig.DatabaseURL()
	if err != nil {
		return err
	}
	cfg, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		return errors.New("invalid DATABASE_URL")
	}
	cfg.ConnectTimeout = 5 * time.Second
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	connectCtx, cancelConnect := context.WithTimeout(ctx, 5*time.Second)
	conn, err := pgx.ConnectConfig(connectCtx, cfg)
	cancelConnect()
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := conn.Close(closeCtx); err != nil {
			slog.Warn("close database connection failed", "error", err)
		}
	}()

	opCtx, cancelOp := context.WithTimeout(ctx, 2*time.Minute)
	defer cancelOp()
	switch args[0] {
	case "up":
		changed, err := migrations.Up(opCtx, conn)
		if err != nil {
			return err
		}
		for _, m := range changed {
			fmt.Printf("applied %03d_%s\n", m.Version, m.Name)
		}
		if len(changed) == 0 {
			fmt.Println("already up to date")
		}
	case "down":
		changed, err := migrations.Down(opCtx, conn)
		if err != nil {
			return err
		}
		if changed == nil {
			fmt.Println("nothing to roll back")
		} else {
			fmt.Printf("rolled back %03d_%s\n", changed.Version, changed.Name)
		}
	case "status":
		states, err := migrations.Status(opCtx, conn)
		if err != nil {
			return err
		}
		for _, state := range states {
			label := "pending"
			if state.Applied {
				label = "applied"
			}
			fmt.Printf("%03d_%s %s\n", state.Version, state.Name, label)
		}
	}
	return nil
}
