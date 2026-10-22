//go:build !win7compat

package transport

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestResponseLimitRoundTripperRejectsOversizedBody(t *testing.T) {
	inner := roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(strings.Repeat("x", 11))),
			Header:     make(http.Header),
		}, nil
	})
	client := &http.Client{Transport: responseLimitRoundTripper{base: inner, limit: 10}}
	response, err := client.Get("http://example.invalid/mcp")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if _, err := io.ReadAll(response.Body); err == nil {
		t.Fatal("expected oversized response error")
	}
}

func TestResponseLimitAllowsExactLimit(t *testing.T) {
	inner := roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(strings.Repeat("x", 10))),
			Header:     make(http.Header),
		}, nil
	})
	client := &http.Client{Transport: responseLimitRoundTripper{base: inner, limit: 10}}
	response, err := client.Get("http://example.invalid/mcp")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 10 {
		t.Fatalf("read %d bytes, want 10", len(data))
	}
}
