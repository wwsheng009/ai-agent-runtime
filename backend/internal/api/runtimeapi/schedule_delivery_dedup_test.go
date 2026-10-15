package runtimeapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/schedule"
)

// TestSubmitRuntimeCommand_LedgerHitReturnsDuplicate 钉住 S2（§12.1）的服务端
// 去重：同一 occurrence 已送达时，带 occurrences 的 submit_prompt 直接回
// 200 duplicate——不建 actor、不重复入会话（hub 不可用时依然成立：重放恒幂等）。
func TestSubmitRuntimeCommand_LedgerHitReturnsDuplicate(t *testing.T) {
	handler, router := newScheduleTestHandler(t)
	ledger := handler.scheduleOccurrenceLedger()
	require.NotNil(t, ledger)

	key := schedule.OccurrenceKey("schedule-s2-test", time.Now().UTC())
	require.NoError(t, ledger.MarkOccurrencesDelivered([]string{key}, time.Now()))

	body := fmt.Sprintf(`{"type":"submit_prompt","prompt":"ping","occurrences":[%q]}`, key)
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/sess-ledger/runtime/commands", strings.NewReader(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"duplicate":true`)
}

// TestSubmitRuntimeCommand_FreshOccurrenceNotDeduplicated 反向断言：未落账的
// occurrence 不会被误判为重复——按原路径处理（测试环境无 hub/session manager
// → 5xx），且失败路径绝不落账。
func TestSubmitRuntimeCommand_FreshOccurrenceNotDeduplicated(t *testing.T) {
	handler, router := newScheduleTestHandler(t)
	ledger := handler.scheduleOccurrenceLedger()
	require.NotNil(t, ledger)

	key := schedule.OccurrenceKey("schedule-s2-fresh", time.Now().UTC())
	body := fmt.Sprintf(`{"type":"submit_prompt","prompt":"ping","occurrences":[%q]}`, key)
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/sess-ledger/runtime/commands", strings.NewReader(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.NotEqual(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotContains(t, rec.Body.String(), `"duplicate":true`)
	delivered, err := ledger.AllOccurrencesDelivered([]string{key})
	require.NoError(t, err)
	require.False(t, delivered, "failed submit must not be marked delivered")
}
