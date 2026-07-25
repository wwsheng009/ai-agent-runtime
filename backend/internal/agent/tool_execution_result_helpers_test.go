package agent

import (
	"fmt"
	"strings"
	"testing"

	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
)

func TestClassifyGenericToolExecutionError_SpawnDepthLimit(t *testing.T) {
	err := fmt.Errorf("[SPAWN_DEPTH_LIMIT] agent spawn depth limit reached before child creation")
	classified := classifyGenericToolExecutionError(err)
	if classified == nil {
		t.Fatal("expected classified runtime error")
	}
	if classified.Code != runtimeerrors.ErrAgentSpawnDepthLimit {
		t.Fatalf("code=%s want SPAWN_DEPTH_LIMIT", classified.Code)
	}

	// Typed RuntimeError path must preserve the structured code.
	typed := runtimeerrors.Newf(runtimeerrors.ErrAgentSpawnDepthLimit, "depth limit")
	result := &toolExecutionResult{}
	metadata := map[string]interface{}{}
	recordToolExecutionOutcome(result, metadata, nil, nil, typed)
	if code, _ := metadata["error_code"].(string); code != string(runtimeerrors.ErrAgentSpawnDepthLimit) {
		t.Fatalf("metadata error_code=%v want SPAWN_DEPTH_LIMIT meta=%#v", metadata["error_code"], metadata)
	}
	if !strings.Contains(result.Error, "SPAWN_DEPTH_LIMIT") {
		t.Fatalf("result error should include SPAWN_DEPTH_LIMIT, got %q", result.Error)
	}
}
