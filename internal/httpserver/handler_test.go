package httpserver

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type fakePinger struct {
	err   error
	calls int
}

func (f *fakePinger) Ping(context.Context) error {
	f.calls++
	return f.err
}

func TestHealthAndReadiness(t *testing.T) {
	pinger := &fakePinger{}
	handler := New(pinger, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))

	for _, tt := range []struct {
		path   string
		status int
	}{
		{"/healthz", http.StatusOK},
		{"/readyz", http.StatusOK},
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, tt.path, nil))
		if recorder.Code != tt.status {
			t.Errorf("%s: got %d, want %d", tt.path, recorder.Code, tt.status)
		}
	}
	if pinger.calls != 1 {
		t.Fatalf("expected one database ping, got %d", pinger.calls)
	}

	pinger.err = errors.New("offline")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503", recorder.Code)
	}
}

func TestReadinessHasDeadline(t *testing.T) {
	pinger := pingerFunc(func(ctx context.Context) error {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("ping context has no deadline")
		}
		return nil
	})
	handler := New(pinger, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", recorder.Code)
	}
}

type pingerFunc func(context.Context) error

func (fn pingerFunc) Ping(ctx context.Context) error { return fn(ctx) }
