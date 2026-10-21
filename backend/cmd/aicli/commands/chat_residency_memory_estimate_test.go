package commands

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// TestEstimateRuntimeMessagesBytesCountsMetadata 锁定 §15.3 归因口径。
//
// 背景（2026-10-21 生产诊断）：某会话 18,677 条消息被旧口径估成 60.4 MB，
// 而 pprof 实测同一批消息在 Go 堆中占 157 MB（encoding/json/v2.makeString
// 122 MB + reflect.mapassign_faststr0 41.5 MB，gc=1 后仍 inuse）。
// 差额全部来自 Metadata：它是 map[string]interface{}，内存表示约为其 JSON
// 字节数的 3~4 倍。该会话 metadata JSON 43.9 MB（tool_metadata 16.6 MB、
// reasoning_details 12.8 MB）。归因若漏掉 metadata，85% 的堆无法解释。
func TestEstimateRuntimeMessagesBytesCountsMetadata(t *testing.T) {
	plain := *runtimetypes.NewAssistantMessage("short answer")

	withMeta := *runtimetypes.NewAssistantMessage("short answer")
	withMeta.Metadata["reasoning_details"] = map[string]interface{}{
		"text": strings.Repeat("reasoning ", 200),
	}
	withMeta.Metadata["tool_metadata"] = map[string]interface{}{
		"stdout": strings.Repeat("x", 4000),
	}

	plainBytes := estimateRuntimeMessagesBytes([]runtimetypes.Message{plain})
	metaBytes := estimateRuntimeMessagesBytes([]runtimetypes.Message{withMeta})

	require.Greater(t, plainBytes, int64(0))
	require.Greater(t, metaBytes, plainBytes*10,
		"带 metadata 的消息必须显著大于纯文本消息：否则 §15.3 无法归因主要内存")
}

// TestEstimateRuntimeMessagesBytesScalesWithMetadataVolume 锁定 metadata 的体积
// 必须线性反映到估算里（避免再次出现"字段在但不算量"的归因空洞）。
func TestEstimateRuntimeMessagesBytesScalesWithMetadataVolume(t *testing.T) {
	build := func(size int) int64 {
		message := *runtimetypes.NewToolMessage("call-1", "ok")
		message.Metadata["tool_metadata"] = map[string]interface{}{
			"stdout": strings.Repeat("y", size),
		}
		return estimateRuntimeMessagesBytes([]runtimetypes.Message{message})
	}

	small := build(1000)
	large := build(100000)

	require.Greater(t, large, small*50,
		"metadata 体积增长 100 倍，估算必须接近线性增长")
}
