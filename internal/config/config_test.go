package config

import (
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/scigraph")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != "127.0.0.1:8080" || cfg.DBConnectTimeout != 3*time.Second {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadFromLocalPassword(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("POSTGRES_USER", "scigraph")
	t.Setenv("POSTGRES_PASSWORD", "new pass@word")
	t.Setenv("PG_PORT", "55432")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseURL != "postgres://scigraph:new%20pass%40word@127.0.0.1:55432/scigraph?sslmode=disable" {
		t.Fatal("local URL was not built from the PostgreSQL password")
	}
}

func TestLoadInvalid(t *testing.T) {
	tests := []struct {
		name, variable, value string
	}{
		{"missing database URL", "DATABASE_URL", ""},
		{"invalid address", "HTTP_ADDR", "bad-address"},
		{"invalid timeout", "HTTP_READ_TIMEOUT", "yesterday"},
		{"zero timeout", "DB_CONNECT_TIMEOUT", "0s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("DATABASE_URL", "postgres://localhost/scigraph")
			t.Setenv(tt.variable, tt.value)
			if tt.variable == "DATABASE_URL" {
				t.Setenv("POSTGRES_PASSWORD", "")
			}
			if _, err := Load(); err == nil {
				t.Fatal("expected configuration error")
			}
		})
	}
}
