package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/kirbysss369/SciGraphDB/internal/devconfig"
)

type Config struct {
	HTTPAddr         string
	DatabaseURL      string
	HTTPReadTimeout  time.Duration
	HTTPWriteTimeout time.Duration
	HTTPIdleTimeout  time.Duration
	DBConnectTimeout time.Duration
	DBPingTimeout    time.Duration
}

func Load() (Config, error) {
	if err := devconfig.Load(); err != nil {
		return Config{}, err
	}
	databaseURL, err := devconfig.DatabaseURL()
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		HTTPAddr:    envOrDefault("HTTP_ADDR", "127.0.0.1:8080"),
		DatabaseURL: databaseURL,
	}
	if err := validateAddr(cfg.HTTPAddr); err != nil {
		return Config{}, err
	}

	for _, item := range []struct {
		name         string
		defaultValue string
		dest         *time.Duration
	}{
		{"HTTP_READ_TIMEOUT", "5s", &cfg.HTTPReadTimeout},
		{"HTTP_WRITE_TIMEOUT", "10s", &cfg.HTTPWriteTimeout},
		{"HTTP_IDLE_TIMEOUT", "60s", &cfg.HTTPIdleTimeout},
		{"DB_CONNECT_TIMEOUT", "3s", &cfg.DBConnectTimeout},
		{"DB_PING_TIMEOUT", "3s", &cfg.DBPingTimeout},
	} {
		*item.dest, err = time.ParseDuration(envOrDefault(item.name, item.defaultValue))
		if err != nil || *item.dest <= 0 {
			return Config{}, fmt.Errorf("%s must be a positive duration", item.name)
		}
	}
	return cfg, nil
}

func envOrDefault(name, fallback string) string {
	if value, ok := os.LookupEnv(name); ok {
		return value
	}
	return fallback
}

func validateAddr(addr string) error {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return errors.New("HTTP_ADDR must be in host:port form")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > 65535 {
		return errors.New("HTTP_ADDR has an invalid port")
	}
	return nil
}
