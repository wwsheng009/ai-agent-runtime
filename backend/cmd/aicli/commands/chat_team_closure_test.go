package commands

import (
	"strings"
	"testing"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

func TestChatSlashHelpIncludesTeamWorkflowContract(t *testing.T) {
	help := strings.Join(buildChatSlashHelpLines(), "\n")
	for _, marker := range []string{
		"Team 协作工具（由对话中的模型调用）:",
		"team_spawn",
		"team_wait",
		"next_cursor",
		"timeout 只结束观察窗口，不把 Team 标为失败",
		"team_task_spec/context/report/block、team_send、team_mailbox",
		"普通 subagent 不可使用这些 Team-only 工具",
		"只有 summary_state=ready 才展示最终摘要",
	} {
		if !strings.Contains(help, marker) {
			t.Fatalf("/help is missing Team workflow marker %q:\n%s", marker, help)
		}
	}
}

func TestRenderTeamSummaryFailureShowsRequestIDOrExplicitUnavailable(t *testing.T) {
	tests := []struct {
		name      string
		requestID string
		want      string
	}{
		{
			name: "local failure without http request",
			want: strings.Join([]string{
				"[team summary] failed team-1",
				"  request_id: unavailable",
				"  错误: summary failed",
			}, "\n"),
		},
		{
			name:      "backend supplied request",
			requestID: "req-summary-1",
			want: strings.Join([]string{
				"[team summary] failed team-1",
				"  request_id: req-summary-1",
				"  错误: summary failed",
			}, "\n"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := map[string]interface{}{
				"team_id": "team-1",
				"error":   "summary failed",
			}
			if tt.requestID != "" {
				payload["request_id"] = tt.requestID
			}
			got := renderChatRuntimeEvent(runtimeevents.Event{
				Type:    "team.summary.failed",
				Payload: payload,
			})
			if got != tt.want {
				t.Fatalf("unexpected Team summary failure render:\nwant:\n%s\ngot:\n%s", tt.want, got)
			}
		})
	}
}
