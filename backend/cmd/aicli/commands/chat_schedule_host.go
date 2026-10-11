package commands

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/schedule"
)

// newLocalScheduleService opens the shared schedule store when the schedule
// subsystem is enabled. Disabled or unopenable returns nil, so the schedule_*
// tools stay out of the model tool surface entirely (the broker only advertises
// them when Broker.Schedule is non-nil).
func newLocalScheduleService(cfg *config.RuntimeConfig, eventBus *runtimeevents.Bus) *schedule.Service {
	if cfg == nil || !cfg.Schedule.Enabled {
		return nil
	}
	sc := cfg.Schedule.WithDefaults()
	path := strings.TrimSpace(sc.StorePath)
	if path == "" {
		base := filepath.Dir(agentconfig.DefaultAuthStorePath())
		if base == "" || base == "." {
			return nil
		}
		path = filepath.Join(base, "data", "schedule", "schedule.sqlite")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil
	}
	store, err := schedule.OpenStore(path)
	if err != nil {
		return nil
	}
	service := schedule.NewService(store, schedule.SystemClock)
	if eventBus != nil {
		// schedule.changed（缺口 2）：任务变更提交后发布 runtime 事件，前端
		// 面板据此刷新（publishChatScheduleChanged 归一化事件通道）。
		service.SetNotifier(func() {
			eventBus.Publish(runtimeevents.Event{Type: chatScheduleChangedEventType})
		})
	}
	return service
}

// chatScheduleChangedEventType 是 schedule.changed 的 runtime 事件类型。
// 注册进 events contract（B 通道 live-only）：无载荷失效信号，前端刷新目录；
// 重连后由 catalog 查询恢复权威状态。
const chatScheduleChangedEventType = "schedule.changed"
