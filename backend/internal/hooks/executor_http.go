package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// HTTPExecutor dispatches hook payloads to an HTTP endpoint.
type HTTPExecutor struct {
	Client *http.Client
}

// Execute calls the configured HTTP endpoint and parses a decision.
func (e *HTTPExecutor) Execute(ctx context.Context, hook HookConfig, payload map[string]interface{}) (Decision, error) {
	raw, err := e.ExecuteRaw(ctx, hook, payload)
	if err != nil {
		return Decision{}, err
	}
	if raw.ExitCode != 0 {
		return Decision{}, fmt.Errorf("hook http status %d", raw.ExitCode)
	}
	return parseDecision([]byte(raw.Stdout))
}

// ExecuteRaw calls the endpoint and returns the unparsed response body.
func (e *HTTPExecutor) ExecuteRaw(ctx context.Context, hook HookConfig, payload map[string]interface{}) (RawResult, error) {
	url := strings.TrimSpace(hook.Exec.URL)
	if url == "" {
		return RawResult{}, fmt.Errorf("hook url is empty")
	}
	method := strings.ToUpper(strings.TrimSpace(hook.Exec.Method))
	if method == "" {
		method = http.MethodPost
	}
	timeout := hook.EffectiveTimeout()
	client := e.Client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	// The per-request deadline must not depend on which client was injected.
	// Previously an injected client silently ignored the hook's timeout, so the
	// same handler behaved differently in tests and in the host.
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return RawResult{}, err
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
		return RawResult{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return RawResult{}, err
	}
	// Preserve non-2xx bodies so a Codex-compatible endpoint can express a
	// blocking reason in the body; the caller decides whether the status is
	// a hard failure.
	return RawResult{Stdout: string(data), Stderr: "", ExitCode: httpExitCode(resp.StatusCode)}, nil
}

func httpExitCode(status int) int {
	if status >= 200 && status < 300 {
		return 0
	}
	return status
}
