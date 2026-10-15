package commands

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

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

// processScheduleService 是"本进程当前 local runtime host 的调度服务"注册位（S2b）。
//
// 为什么需要它：能力面（含 runtime host）在首帧之后**异步装载**；web/mesh 处理器
// 经 chatWebSession() 拿到的 ChatSession 可能是装载前的早期快照（其
// LocalRuntimeHost 恒为 nil），而真正挂载了 Schedule 的是宿主对象。账本解析
// 不能依赖 session 指针同一性，所以在宿主挂载点做进程级注册（2026-10-15 实机
// E2E 抓到：L2 接收侧因 local-runtime-host-nil 静默退化为"无去重投递"）。
var (
	processScheduleMu      sync.RWMutex
	processScheduleService *schedule.Service
)

// registerProcessScheduleService 在 local runtime host 挂载/卸载时调用（nil = 清除）。
func registerProcessScheduleService(service *schedule.Service) {
	processScheduleMu.Lock()
	processScheduleService = service
	processScheduleMu.Unlock()
}

// processScheduleLedger 返回进程级交付账本（未启用 schedule → nil）。
func processScheduleLedger() schedule.OccurrenceLedger {
	processScheduleMu.RLock()
	service := processScheduleService
	processScheduleMu.RUnlock()
	if service == nil {
		return nil
	}
	ledger, _ := service.Store().(schedule.OccurrenceLedger)
	return ledger
}
