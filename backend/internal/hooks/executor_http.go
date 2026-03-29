package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// HTTPExecutor dispatches hook payloads to an HTTP endpoint.
type HTTPExecutor struct {
	Client *http.Client
}

// Execute calls the configured HTTP endpoint and parses a decision.
func (e *HTTPExecutor) Execute(ctx context.Context, hook HookConfig, payload map[string]interface{}) (Decision, error) {
	url := strings.TrimSpace(hook.Exec.URL)
	if url == "" {
		return Decision{}, fmt.Errorf("hook url is empty")
	}
	method := strings.ToUpper(strings.TrimSpace(hook.Exec.Method))
	if method == "" {
		method = http.MethodPost
	}
	timeout := hook.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	client := e.Client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return Decision{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range hook.Exec.Headers {
		if strings.TrimSpace(key) == "" {
			continue
		}
		req.Header.Set(key, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		return Decision{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return Decision{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Decision{}, fmt.Errorf("hook http status %d", resp.StatusCode)
	}
	return parseDecision(data)
}
