package connectors

import (
	"errors"
	"strings"
	"sync"
	"time"
)

// ErrServiceNotConfigured 表示 Service 缺少 Store/Backend。
var ErrServiceNotConfigured = errors.New("connectors: service is not configured (Store/Backend required)")

// deltaThrottle 合并流式片段：仅在文本变化且距上次发送 ≥ minInterval 时放行
// （docs/bots/05-review-and-hardening.md §5.3：编辑间隔 ≥1s 且内容变化才编辑）。
type deltaThrottle struct {
	mu          sync.Mutex
	lastText    string
	lastEmitAt  time.Time
	minInterval time.Duration
	now         func() time.Time
}

func newDeltaThrottle(minInterval time.Duration) *deltaThrottle {
	if minInterval <= 0 {
		minInterval = 800 * time.Millisecond
	}
	return &deltaThrottle{minInterval: minInterval, now: time.Now}
}

// shouldEmit 报告该文本是否应触发一次编辑；首帧立即放行。
func (t *deltaThrottle) shouldEmit(text string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	text = strings.TrimSpace(text)
	if text == "" || text == t.lastText {
		return false
	}
	now := t.now()
	if t.lastEmitAt.IsZero() || now.Sub(t.lastEmitAt) >= t.minInterval {
		t.lastText = text
		t.lastEmitAt = now
		return true
	}
	return false
}
