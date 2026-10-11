package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func getConsole(path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	consoleHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestConsoleServesIndex(t *testing.T) {
	rec := getConsole("/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("content type = %q, want text/html", ct)
	}
	if !strings.Contains(rec.Body.String(), "aicli-gateway") {
		t.Fatalf("index body missing title: %s", rec.Body.String())
	}
}

func TestConsoleServesAssets(t *testing.T) {
	for _, path := range []string{"/style.css", "/app.js"} {
		rec := getConsole(path)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", path, rec.Code)
		}
		if rec.Body.Len() == 0 {
			t.Fatalf("%s served an empty body", path)
		}
	}
}

func TestConsoleFallsBackToIndexForUnknownRoutes(t *testing.T) {
	rec := getConsole("/some/spa/route")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "aicli-gateway") {
		t.Fatalf("fallback did not serve index: %s", rec.Body.String())
	}
}

func TestConsoleDoesNotShadowReservedPrefixes(t *testing.T) {
	for _, path := range []string{"/api/unknown", "/gw/agent", "/n/node-x/foo"} {
		rec := getConsole(path)
		if rec.Code != http.StatusNotFound {
			body, _ := io.ReadAll(rec.Body)
			t.Fatalf("%s status = %d, want 404 (body=%s)", path, rec.Code, body)
		}
	}
}

func TestConsoleRejectsTraversal(t *testing.T) {
	rec := getConsole("/../../etc/passwd")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}
