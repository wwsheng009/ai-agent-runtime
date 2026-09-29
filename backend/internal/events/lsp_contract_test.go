package events

import "testing"

// TestLSPEventChannelsPinBaselineStorage 钉死 LSP 事件的通道表态：
//   - lsp.request.finished 必须挂 A 通道（session store）——它是离线基线
//     （scripts/analyze-lsp-baseline.py、/lsp baseline、页签 F）复算覆盖率/
//     命中率/降级率/等待 P50·P95 的唯一事实源；
//   - 池生命周期与诊断发布保持 live-only，约束事件体积。
//
// 方案：docs/plan/lsp-observability-and-analysis-plan-20260929.md §3/§4.2。
func TestLSPEventChannelsPinBaselineStorage(t *testing.T) {
	if !IsPersistedEventType(EventLSPRequestFinished) {
		t.Fatalf("%s 必须落盘（session store）：基线无法从 live-only 事件复算", EventLSPRequestFinished)
	}
	for _, eventType := range []string{EventLSPServerState, EventLSPDiagnosticsUpdated} {
		if IsPersistedEventType(eventType) {
			t.Fatalf("%s 必须保持 live-only（体积约束）", eventType)
		}
	}
}
