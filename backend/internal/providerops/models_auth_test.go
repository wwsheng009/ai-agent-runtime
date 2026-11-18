package providerops

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func TestAPIKeySelectsOneNonEmptyKeyFromPools(t *testing.T) {
	tests := []struct {
		name     string
		provider config.Provider
		allowed  map[string]bool
	}{
		{
			name:     "comma separated inline pool",
			provider: config.Provider{APIKey: " key-a, , key-b "},
			allowed:  map[string]bool{"key-a": true, "key-b": true},
		},
		{
			name:     "api_keys with empty entries",
			provider: config.Provider{APIKeys: []string{"", "key-a", "  "}},
			allowed:  map[string]bool{"key-a": true},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for i := 0; i < 100; i++ {
				if got := APIKey(tc.provider); !tc.allowed[got] {
					t.Fatalf("APIKey() = %q; want one of %v", got, tc.allowed)
				}
			}
		})
	}
}

func TestFetchModelsDoesNotSendCommaSeparatedKeyPoolAsOneCredential(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "Bearer key-a", "Bearer key-b":
			_, _ = w.Write([]byte(`{"data":[{"id":"model-a"}]}`))
		default:
			http.Error(w, `{"error":{"message":"invalid api key"}}`, http.StatusUnauthorized)
		}
	}))
	defer server.Close()

	for i := 0; i < 20; i++ {
		_, err := FetchModels(nil, FetchModelsRequest{
			Provider: config.Provider{
				Protocol: "openai",
				BaseURL:  server.URL,
				APIKey:   "key-a,key-b",
			},
			LoginProtocol: "openai",
		})
		if err != nil {
			t.Fatalf("FetchModels with a key pool failed on attempt %d: %v", i+1, err)
		}
	}
}

func TestFetchModelsHonorsRequestTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	started := time.Now()
	_, err := FetchModels(context.Background(), FetchModelsRequest{
		Provider: config.Provider{
			Protocol: "openai",
			BaseURL:  server.URL,
			APIKey:   "key-a",
		},
		LoginProtocol: "openai",
		Timeout:       40 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("FetchModels unexpectedly succeeded against a server that never responds")
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("FetchModels took %s despite a 40ms timeout", elapsed)
	}
}
