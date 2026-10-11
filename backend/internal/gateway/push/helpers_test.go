package push

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// newServer starts a test HTTP server invoking handler for every request.
func newServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(handler)
}

// newGoneServer starts a test HTTP server that always reports 410 Gone.
func newGoneServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGone)
	}))
}
