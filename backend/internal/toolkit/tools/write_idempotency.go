package tools

import (
	"strings"

	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/mutatecore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

func writeContentRevision(exists bool, content string) string {
	return mutatecore.Revision(exists, content)
}

func writePreconditionFailure(message, actual, expected string) *toolkit.ToolResult {
	err := runtimeerrors.New(runtimeerrors.ErrWritePrecondition, strings.TrimSpace(message)).
		WithContext("actual_revision", strings.TrimSpace(actual)).
		WithContext("expected_revision", strings.TrimSpace(expected))
	return &toolkit.ToolResult{
		Success:    false,
		OutputKind: toolresult.KindText,
		Error:      err,
		Metadata: map[string]interface{}{
			"error_code":        string(runtimeerrors.ErrWritePrecondition),
			"actual_revision":   strings.TrimSpace(actual),
			"expected_revision": strings.TrimSpace(expected),
			"retryable":         true,
		},
	}
}
