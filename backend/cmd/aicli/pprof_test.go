package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestStartPprofServerRandomPort(t *testing.T) {
	handle, err := startPprofServer("")
	if err != nil {
		t.Fatalf("startPprofServer() error = %v", err)
	}
	defer handle.Close()

	if handle.Addr() == "" {
		t.Fatal("Addr() returned empty string")
	}
	if strings.HasPrefix(handle.Addr(), "127.0.0.1:") == false {
		t.Fatalf("Addr() = %q, want 127.0.0.1:<random-port>", handle.Addr())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, handle.URL(), nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext() error = %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s error = %v", handle.URL(), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200", handle.URL(), resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		t.Fatalf("read body error = %v", err)
	}
	if len(body) == 0 {
		t.Fatalf("GET %s returned empty body", handle.URL())
	}
}

func TestStartPprofServerEndpoints(t *testing.T) {
	handle, err := startPprofServer("127.0.0.1:0")
	if err != nil {
		t.Fatalf("startPprofServer() error = %v", err)
	}
	defer handle.Close()

	endpoints := []string{
		"/debug/pprof/heap",
		"/debug/pprof/goroutine",
		"/debug/pprof/allocs",
		"/debug/pprof/cmdline",
	}
	for _, path := range endpoints {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+handle.Addr()+path, nil)
		if err != nil {
			cancel()
			t.Fatalf("NewRequestWithContext(%s) error = %v", path, err)
		}
		resp, err := http.DefaultClient.Do(req)
		cancel()
		if err != nil {
			t.Fatalf("GET %s error = %v", path, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200", path, resp.StatusCode)
		}
	}
}

func TestStartPprofServerExplicitAddr(t *testing.T) {
	handle, err := startPprofServer("127.0.0.1:0")
	if err != nil {
		t.Fatalf("startPprofServer() error = %v", err)
	}
	defer handle.Close()

	if !strings.HasPrefix(handle.Addr(), "127.0.0.1:") {
		t.Fatalf("Addr() = %q, want loopback prefix", handle.Addr())
	}
	if !strings.HasPrefix(handle.URL(), "http://127.0.0.1:") {
		t.Fatalf("URL() = %q, want http://127.0.0.1: prefix", handle.URL())
	}
}
