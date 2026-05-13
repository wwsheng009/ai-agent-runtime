package aiclitools

import (
	"context"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

type ExposurePath string

const (
	ExposureShared        ExposurePath = "shared"
	ExposureActor         ExposurePath = "actor"
	ExposureRuntimeServer ExposurePath = "runtime_server"
)

type ToolResult struct {
	Output   interface{}
	Metadata map[string]interface{}
}

type ToolSessionContext interface {
	SessionID() string
	RuntimeSession() *runtimechat.Session
	SessionStorage() runtimechat.SessionStorage
	RefreshRuntimeSession(ctx context.Context, updated *runtimechat.Session) error
	ExecutorPath() ExposurePath
}

type Capability struct {
	Name        string
	Description string
	Parameters  map[string]interface{}
	Metadata    map[string]interface{}
	Exposure    []ExposurePath
	Execute     func(ctx context.Context, session ToolSessionContext, args map[string]interface{}) (ToolResult, error)
}

func (c Capability) SupportsPath(path ExposurePath) bool {
	if len(c.Exposure) == 0 {
		return false
	}
	for _, candidate := range c.Exposure {
		if candidate == path {
			return true
		}
	}
	return false
}
