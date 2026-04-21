package commands

import (
	"fmt"
	"path/filepath"
	"strings"

	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
)

type chatRuntimeHTTPCaptureSnapshot struct {
	Source               string
	Provider             string
	Protocol             string
	Model                string
	ResponseStatus       int
	ResponsePreview      string
	ErrorText            string
	ArtifactDir          string
	RequestArtifactPath  string
	ResponseArtifactPath string
}

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
	c.lastRequestArtifact = ""
	c.lastResponseArtifact = ""
	c.pendingArtifactSeq = 0
}

func (c *chatRuntimeHTTPCapture) SetArtifactDir(dir string) {
	if c == nil {
		return
	}
	dir = strings.TrimSpace(dir)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.artifactDir == dir {
		return
	}
	c.artifactDir = dir
	c.lastRequestArtifact = ""
	c.lastResponseArtifact = ""
	c.artifactCounter = 0
	c.pendingArtifactSeq = 0
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

func (c *chatRuntimeHTTPCapture) NextArtifactPath(phase, source string) (string, int) {
	if c == nil {
		return "", 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if strings.TrimSpace(c.artifactDir) == "" {
		return "", 0
	}

	phase = sanitizeRuntimeHTTPArtifactToken(firstNonEmptyChatValue(strings.TrimSpace(phase), "event"))
	source = sanitizeRuntimeHTTPArtifactToken(firstNonEmptyChatValue(strings.TrimSpace(source), "runtime"))

	sequence := 0
	switch strings.ToLower(strings.TrimSpace(phase)) {
	case "request":
		c.artifactCounter++
		sequence = c.artifactCounter
		c.pendingArtifactSeq = sequence
	case "response":
		if c.pendingArtifactSeq > 0 {
			sequence = c.pendingArtifactSeq
			c.pendingArtifactSeq = 0
		} else {
			c.artifactCounter++
			sequence = c.artifactCounter
		}
	default:
		c.artifactCounter++
		sequence = c.artifactCounter
	}

	filename := fmt.Sprintf("%03d_%s_%s.json", sequence, phase, source)
	return filepath.Join(c.artifactDir, filename), sequence
}

func (c *chatRuntimeHTTPCapture) RecordArtifactPath(phase, path string) {
	if c == nil {
		return
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	switch strings.ToLower(strings.TrimSpace(phase)) {
	case "request":
		c.lastRequestArtifact = path
	case "response":
		c.lastResponseArtifact = path
	}
}

func (c *chatRuntimeHTTPCapture) Snapshot() chatRuntimeHTTPCaptureSnapshot {
	if c == nil {
		return chatRuntimeHTTPCaptureSnapshot{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return chatRuntimeHTTPCaptureSnapshot{
		Source:               c.lastSource,
		Provider:             c.lastProvider,
		Protocol:             c.lastProtocol,
		Model:                c.lastModel,
		ResponseStatus:       c.lastResponseStatus,
		ResponsePreview:      c.lastResponsePreview,
		ErrorText:            c.lastError,
		ArtifactDir:          c.artifactDir,
		RequestArtifactPath:  c.lastRequestArtifact,
		ResponseArtifactPath: c.lastResponseArtifact,
	}
}

func sanitizeRuntimeHTTPArtifactToken(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "runtime"
	}
	replacer := strings.NewReplacer(
		"\\", "_",
		"/", "_",
		":", "_",
		"*", "_",
		"?", "_",
		"\"", "_",
		"<", "_",
		">", "_",
		"|", "_",
		" ", "_",
	)
	value = replacer.Replace(value)
	if value == "" {
		return "runtime"
	}
	return value
}
