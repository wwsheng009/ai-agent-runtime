package usageanalytics

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	cacheanalytics "github.com/wwsheng009/ai-agent-runtime/internal/cacheanalytics"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// TestCacheSourceCarriesReasoningEffortFromStartedPayload 锁定统一用量库
// （usage_requests.record_json）同样保留 requested/effective reasoning effort：
// 缓存页优先读用量库时，不能因数据源切换丢失该展示字段。
func TestCacheSourceCarriesReasoningEffortFromStartedPayload(t *testing.T) {
	now := time.Date(2026, 11, 22, 10, 20, 0, 0, time.UTC)
	service, bus := newTestService(t, nil, now)
	bus.Publish(runtimeevents.Event{
		Type:      EventLLMRequestStarted,
		SessionID: "s-effort",
		Timestamp: now,
		Payload: map[string]interface{}{
			"llm_request_id":             "req-effort",
			"trace_id":                   "trace-effort",
			"turn_id":                    "turn-effort",
			"step":                       1,
			"provider":                   "mdkj",
			"model":                      "gpt-6.1-terra",
			"requested_reasoning_effort": "max",
			"reasoning_effort":           "max",
		},
	})
	publishRequestFinished(bus, "s-effort", "req-effort", usagePayload(10, 1, 11, 0))

	source := NewCacheSource(service.Store(), nil, true)
	require.NotNil(t, source)
	page, err := source.Requests("s-effort", cacheanalytics.RequestQuery{Limit: 10})
	require.NoError(t, err)
	require.Len(t, page.Requests, 1)
	require.Equal(t, "max", page.Requests[0].RequestedReasoningEffort)
	require.Equal(t, "max", page.Requests[0].ReasoningEffort)
}
