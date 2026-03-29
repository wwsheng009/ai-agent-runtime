package commands

import (
	"strings"

	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
)

func (c *chatRuntimeHTTPCapture) Reset() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastSource = ""
	c.lastProvider = ""
	c.lastProtocol = ""
	c.lastModel = ""
	c.lastResponseStatus = 0
	c.lastResponsePreview = ""
	c.lastError = ""
}

func (c *chatRuntimeHTTPCapture) Record(event runtimellm.HTTPDebugEvent) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if value := strings.TrimSpace(event.Source); value != "" {
		c.lastSource = value
	}
	if value := strings.TrimSpace(event.Provider); value != "" {
		c.lastProvider = value
	}
	if value := strings.TrimSpace(event.Protocol); value != "" {
		c.lastProtocol = value
	}
	if value := strings.TrimSpace(event.Model); value != "" {
		c.lastModel = value
	}
	if event.ResponseStatusCode > 0 {
		c.lastResponseStatus = event.ResponseStatusCode
	}
	if value := strings.TrimSpace(event.ResponseBodyPreview); value != "" {
		c.lastResponsePreview = value
	}
	if value := strings.TrimSpace(event.Error); value != "" {
		c.lastError = value
	}
}

func (c *chatRuntimeHTTPCapture) Snapshot() (source, provider, protocol, model string, status int, preview, errText string) {
	if c == nil {
		return "", "", "", "", 0, "", ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastSource, c.lastProvider, c.lastProtocol, c.lastModel, c.lastResponseStatus, c.lastResponsePreview, c.lastError
}
