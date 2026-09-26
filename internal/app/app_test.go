package app

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestGracefulShutdownWaitsForRequest(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		w.WriteHeader(http.StatusOK)
	})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- serve(ctx, listener, server) }()

	client := &http.Client{Timeout: 3 * time.Second}
	response := make(chan error, 1)
	go func() {
		res, err := client.Get("http://" + listener.Addr().String())
		if err == nil {
			_, err = io.Copy(io.Discard, res.Body)
			if closeErr := res.Body.Close(); err == nil {
				err = closeErr
			}
		}
		response <- err
	}()

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case err := <-served:
		t.Fatalf("server exited before request completed: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-response:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("request did not complete")
	}
	select {
	case err := <-served:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server did not stop")
	}
}
