package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strings"

	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const writeRevisionAbsent = "absent"

func validWritePrecondition(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || value == writeRevisionAbsent {
		return true
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func writeContentRevision(exists bool, content string) string {
	if !exists {
		return writeRevisionAbsent
	}
	digest := sha256.Sum256([]byte(content))
	return hex.EncodeToString(digest[:])
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

func idempotentWriteResult(path string, size int64, revision, expected string) *toolkit.ToolResult {
	return &toolkit.ToolResult{
		Success:    true,
		OutputKind: toolresult.KindText,
		Content:    fmt.Sprintf("文件内容已与请求一致，按幂等回放处理: %s\n文件大小: %d 字节", path, size),
		Metadata: map[string]interface{}{
			"file_path":         path,
			"action":            "unchanged",
			"old_existed":       true,
			"old_size":          size,
			"new_size":          size,
			"size_changed":      int64(0),
			"patch":             "",
			"mutated_paths":     []string{},
			"old_sha256":        revision,
			"new_sha256":        revision,
			"expected_sha256":   strings.TrimSpace(expected),
			"idempotent_replay": true,
			"retry_class":       runtimetypes.ToolRetryClassIdempotencyKeyRequired,
		},
	}
}

func optionalNonNegativeInt64(params map[string]interface{}, key string) (int64, bool, bool) {
	value, exists := params[key]
	if !exists || value == nil {
		return 0, false, true
	}
	var parsed int64
	switch typed := value.(type) {
	case int:
		parsed = int64(typed)
	case int32:
		parsed = int64(typed)
	case int64:
		parsed = typed
	case float32:
		converted := float64(typed)
		if converted != math.Trunc(converted) || converted > math.MaxInt64 {
			return 0, true, false
		}
		parsed = int64(converted)
	case float64:
		if typed != math.Trunc(typed) || typed > math.MaxInt64 {
			return 0, true, false
		}
		parsed = int64(typed)
	default:
		return 0, true, false
	}
	return parsed, true, parsed >= 0
}

func expectedOffsetMetadata(offset int64, present bool) interface{} {
	if !present {
		return nil
	}
	return offset
}

func idempotentAppendWriteResult(path string, size int64, content string, expectedOffset int64, hasExpectedOffset, truncated bool) *toolkit.ToolResult {
	action := "append_replay"
	if truncated {
		action = "truncate_replay"
	}
	return &toolkit.ToolResult{
		Success:    true,
		OutputKind: toolresult.KindText,
		Content:    fmt.Sprintf("写入块已存在，按幂等回放处理: %s\n文件当前大小: %d 字节", path, size),
		Metadata: map[string]interface{}{
			"file_path":         path,
			"action":            action,
			"bytes_written":     int64(0),
			"size_before":       size,
			"size_after":        size,
			"truncate_first":    truncated,
			"appended":          !truncated,
			"patch":             "",
			"mutated_paths":     []string{},
			"transport_backend": "local_filetransport",
			"expected_offset":   expectedOffsetMetadata(expectedOffset, hasExpectedOffset),
			"chunk_sha256":      writeContentRevision(true, content),
			"idempotent_replay": true,
			"retry_class":       runtimetypes.ToolRetryClassIdempotencyKeyRequired,
		},
	}
}
