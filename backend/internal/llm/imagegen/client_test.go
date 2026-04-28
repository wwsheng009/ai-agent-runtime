package imagegen

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func TestClientGenerate_SendsExpectedRequestAndParsesResponse(t *testing.T) {
	var requestCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)

		if r.Method != http.MethodPost {
			t.Errorf("expected method POST, got %s", r.Method)
		}
		if r.URL.Path != "/v1/images/generations" {
			t.Errorf("expected path /v1/images/generations, got %s", r.URL.Path)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("expected Content-Type application/json, got %q", got)
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("expected Accept application/json, got %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("expected Authorization bearer token, got %q", got)
		}
		if got := r.Header.Get("X-Custom"); got != "custom-value" {
			t.Errorf("expected X-Custom header to be forwarded, got %q", got)
		}

		var got GenerateRequest
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request body: %v", err)
			return
		}
		if got.Model != "gpt-image-2" {
			t.Errorf("expected model gpt-image-2, got %q", got.Model)
		}
		if got.Prompt != "a cat on a window sill" {
			t.Errorf("expected trimmed prompt, got %q", got.Prompt)
		}
		if got.N != 2 {
			t.Errorf("expected n=2, got %d", got.N)
		}
		if got.Size != "1024x1024" {
			t.Errorf("expected size 1024x1024, got %q", got.Size)
		}
		if got.Quality != "medium" {
			t.Errorf("expected quality medium, got %q", got.Quality)
		}
		if got.Background != "auto" {
			t.Errorf("expected background auto, got %q", got.Background)
		}
		if got.OutputFormat != "jpeg" {
			t.Errorf("expected output_format jpeg, got %q", got.OutputFormat)
		}
		if got.OutputCompression == nil {
			t.Errorf("expected output_compression to be present")
		} else if *got.OutputCompression != 75 {
			t.Errorf("expected output_compression 75, got %d", *got.OutputCompression)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":123,"data":[{"b64_json":"aGVsbG8=","revised_prompt":"a cat"}]}`))
	}))
	defer server.Close()

	provider := agentconfig.Provider{
		BaseURL: server.URL,
		APIKey:  "test-key",
		Headers: map[string]string{
			"X-Custom": "custom-value",
		},
	}
	client := NewClient(provider, time.Second, nil)
	client.httpClient = server.Client()

	compression := 75
	resp, err := client.Generate(context.Background(), &GenerateRequest{
		Model:             " gpt-image-2 ",
		Prompt:            "  a cat on a window sill  ",
		N:                 2,
		Size:              "1024x1024",
		Quality:           "Medium",
		Background:        "Auto",
		OutputFormat:      "jpg",
		OutputCompression: &compression,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Len(t, resp.Data, 1)
	require.Equal(t, "aGVsbG8=", resp.Data[0].B64JSON)
	require.Equal(t, "a cat", resp.Data[0].RevisedPrompt)
	require.Equal(t, int32(1), atomic.LoadInt32(&requestCount))
}

func TestClientGenerate_DoesNotRetryOn400(t *testing.T) {
	var requestCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad request","type":"invalid_request_error","code":"bad"}}`))
	}))
	defer server.Close()

	client := NewClient(agentconfig.Provider{BaseURL: server.URL, APIKey: "test-key"}, time.Second, nil)
	client.httpClient = server.Client()

	_, err := client.Generate(context.Background(), &GenerateRequest{
		Model:   "gpt-image-2",
		Prompt:  "test",
		Size:    "1024x1024",
		Quality: "medium",
	})
	require.Error(t, err)

	var apiErr *APIError
	require.True(t, errors.As(err, &apiErr))
	require.Equal(t, http.StatusBadRequest, apiErr.HTTPStatusCode())
	require.Equal(t, int32(1), atomic.LoadInt32(&requestCount))
}

func TestClientGenerate_RetriesOn429WithRetryAfterHeader(t *testing.T) {
	var requestCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&requestCount, 1)
		if count == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After-Ms", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"slow down","type":"rate_limit_error","code":"rate_limit"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":123,"data":[{"b64_json":"aGVsbG8="}]}`))
	}))
	defer server.Close()

	client := NewClient(agentconfig.Provider{BaseURL: server.URL, APIKey: "test-key"}, time.Second, nil)
	client.httpClient = server.Client()

	resp, err := client.Generate(context.Background(), &GenerateRequest{
		Model:   "gpt-image-2",
		Prompt:  "retry",
		Size:    "1024x1024",
		Quality: "medium",
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Len(t, resp.Data, 1)
	require.Equal(t, int32(2), atomic.LoadInt32(&requestCount))
}

func TestClientGenerate_RetriesOnServerError(t *testing.T) {
	var requestCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&requestCount, 1)
		if count == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After-Ms", "1")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"server exploded","type":"server_error","code":"server_error"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":123,"data":[{"b64_json":"aGVsbG8="}]}`))
	}))
	defer server.Close()

	client := NewClient(agentconfig.Provider{BaseURL: server.URL, APIKey: "test-key"}, time.Second, nil)
	client.httpClient = server.Client()

	resp, err := client.Generate(context.Background(), &GenerateRequest{
		Model:   "gpt-image-2",
		Prompt:  "retry",
		Size:    "1024x1024",
		Quality: "medium",
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Len(t, resp.Data, 1)
	require.Equal(t, int32(2), atomic.LoadInt32(&requestCount))
}

func TestClientGenerate_RetriesOnTransientTransportError(t *testing.T) {
	var requestCount int32
	client := NewClient(agentconfig.Provider{BaseURL: "https://example.invalid", APIKey: "test-key"}, time.Second, nil)
	client.httpClient = &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			count := atomic.AddInt32(&requestCount, 1)
			if count == 1 {
				return nil, transientNetError{message: "temporary network failure"}
			}
			body := io.NopCloser(strings.NewReader(`{"created":123,"data":[{"b64_json":"aGVsbG8="}]}`))
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     make(http.Header),
				Body:       body,
				Request:    req,
			}, nil
		}),
	}

	resp, err := client.Generate(context.Background(), &GenerateRequest{
		Model:   "gpt-image-2",
		Prompt:  "retry",
		Size:    "1024x1024",
		Quality: "medium",
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Len(t, resp.Data, 1)
	require.Equal(t, int32(2), atomic.LoadInt32(&requestCount))
}

func TestParseImageGenAPIError_ExtractsRetryAfterDelay(t *testing.T) {
	err := parseImageGenAPIError(http.StatusTooManyRequests, []byte(`{"error":{"message":"slow down","type":"rate_limit_error","code":"rate_limit"},"retry_after_ms":125}`), nil)
	require.Error(t, err)

	var apiErr *APIError
	require.True(t, errors.As(err, &apiErr))
	require.Equal(t, http.StatusTooManyRequests, apiErr.HTTPStatusCode())
	require.Equal(t, 125*time.Millisecond, apiErr.RetryAfterDelay())
	require.Equal(t, "slow down", apiErr.Message)
	require.Equal(t, "rate_limit_error", apiErr.Type)
	require.Equal(t, "rate_limit", apiErr.Code)
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type transientNetError struct {
	message string
}

func (e transientNetError) Error() string   { return e.message }
func (e transientNetError) Timeout() bool   { return false }
func (e transientNetError) Temporary() bool { return true }

func (e transientNetError) RetryAfterDelay() time.Duration { return 1 * time.Millisecond }
func (e transientNetError) Unwrap() error                  { return nil }

var _ net.Error = transientNetError{}
