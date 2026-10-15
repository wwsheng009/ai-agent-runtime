// 主动消息频控账本（本地预检，best-effort）。
//
// QQ 对「主动消息」（不带 msg_id 的消息）有平台级频控（默认每秒 10 条总量 +
// 每会话每日 1000 条），超出后平台直接报错。本账本在发送前预检并给出可操作的
// 本地错误，避免无谓请求与难懂的平台错误；被动回复（带 msg_id）与输入状态
// 不计入（输入状态走独立消息类型，不计主动消息配额）。
//
// 口径与边界：
//   - 进程内计数：重启归零、多实例各自计数——平台仍兜底，本账本只是提前量；
//   - Settings 可调：`proactive_qps` / `proactive_daily_limit`（留空=默认；
//     填 0 = 不限制）。
package qqbot

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

const (
	// defaultProactiveQPS 是主动消息的默认每秒上限（官方 10 qps）。
	defaultProactiveQPS = 10
	// defaultProactiveDailyLimit 是每会话每日主动消息默认上限（官方 1000 条/日）。
	defaultProactiveDailyLimit = 1000
	// proactiveQPSWindow / proactiveDailyWindow 是两个计数窗口。
	proactiveQPSWindow   = time.Second
	proactiveDailyWindow = 24 * time.Hour
)

// proactiveQuotaError 是本机频控预检失败（可直接记日志/回给用户）。
type proactiveQuotaError struct {
	scope string // qps | daily
	limit int
	chat  string
}

func (e *proactiveQuotaError) Error() string {
	if e == nil {
		return ""
	}
	switch e.scope {
	case "qps":
		return fmt.Sprintf("qqbot: 主动消息触发本机频控（每秒上限 %d 条）：请稍后重试", e.limit)
	default:
		return fmt.Sprintf("qqbot: 会话 %s 今日主动消息已达本机上限（%d 条，平台同限）：请明日再试，或等待用户主动发起", e.chat, e.limit)
	}
}

// proactiveLedger 是进程内主动消息计数（qps 窗口 + 每会话日窗口）。
type proactiveLedger struct {
	mu          sync.Mutex
	windowStart time.Time
	windowCount int
	daily       map[string]proactiveDailyCounter
}

type proactiveDailyCounter struct {
	dayStart time.Time
	count    int
}

// allow 预检一次主动消息；超限返回 *proactiveQuotaError。qps/dailyLimit <= 0
// 表示该项不限制。
func (l *proactiveLedger) allow(chat string, now time.Time, qps, dailyLimit int) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	if qps > 0 {
		if l.windowStart.IsZero() || now.Sub(l.windowStart) >= proactiveQPSWindow {
			l.windowStart = now
			l.windowCount = 0
		}
		if l.windowCount >= qps {
			return &proactiveQuotaError{scope: "qps", limit: qps, chat: chat}
		}
		l.windowCount++
	}

	if dailyLimit > 0 {
		if l.daily == nil {
			l.daily = make(map[string]proactiveDailyCounter)
		}
		entry, exists := l.daily[chat]
		if !exists && len(l.daily) >= maxTrackedChats {
			// 防御性上限：极端多会话场景下丢弃整表重新计数。
			l.daily = make(map[string]proactiveDailyCounter)
		}
		if entry.dayStart.IsZero() || now.Sub(entry.dayStart) >= proactiveDailyWindow {
			entry = proactiveDailyCounter{dayStart: now}
		}
		if entry.count >= dailyLimit {
			l.daily[chat] = entry
			return &proactiveQuotaError{scope: "daily", limit: dailyLimit, chat: chat}
		}
		entry.count++
		l.daily[chat] = entry
	}
	return nil
}

// proactiveQPS / proactiveDailyLimit 读取连接器配置（留空/非法回退默认）。
func (p *Provider) proactiveQPS(cfg connectors.Config) int {
	return proactiveSettingInt(cfg, "proactive_qps", defaultProactiveQPS)
}

func (p *Provider) proactiveDailyLimit(cfg connectors.Config) int {
	return proactiveSettingInt(cfg, "proactive_daily_limit", defaultProactiveDailyLimit)
}

func proactiveSettingInt(cfg connectors.Config, key string, fallback int) int {
	raw := strings.TrimSpace(cfg.Setting(key, ""))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return fallback
	}
	return value
}

// guardProactive 在发送一条主动消息前预检（被动回复不调用）。
func (p *Provider) guardProactive(cfg connectors.Config, chat string) error {
	return p.proactive.allow(chat, p.now(), p.proactiveQPS(cfg), p.proactiveDailyLimit(cfg))
}
