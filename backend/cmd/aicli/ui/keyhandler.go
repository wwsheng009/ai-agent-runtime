package ui

import (
	"sync/atomic"
	"time"
)

// KeyHandler 键盘事件处理器
type KeyHandler struct {
	quitChan   chan struct{}
	notifyChan chan bool // ESC 键按下通知
	doneChan   chan struct{}
	pollState  chan struct{}
	enabled    atomic.Bool
	started    atomic.Bool
	armed      atomic.Bool
	suspended  atomic.Bool
}

// NewKeyHandler 创建新的键盘事件处理器
func NewKeyHandler() *KeyHandler {
	return &KeyHandler{
		quitChan:   make(chan struct{}),
		notifyChan: make(chan bool, 10),
		doneChan:   make(chan struct{}),
		pollState:  make(chan struct{}, 1),
	}
}

// Stop 停止键盘监听
func (kh *KeyHandler) Stop() {
	if kh == nil || !kh.enabled.Swap(false) {
		return
	}
	kh.armed.Store(false)
	close(kh.quitChan)
	<-kh.doneChan
}

// Arm enables physical-key polling while a turn-scoped ESC consumer exists.
// Keeping the handler disarmed during normal composer reads prevents two
// components from consuming the same session-local stdin stream.
func (kh *KeyHandler) Arm() {
	if kh != nil && kh.enabled.Load() && !kh.armed.Swap(true) {
		kh.signalPollStateChange()
	}
}

// Disarm stops physical-key polling without stopping the handler goroutine.
func (kh *KeyHandler) Disarm() {
	if kh != nil && kh.armed.Swap(false) {
		kh.signalPollStateChange()
	}
}

// Suspend temporarily disables physical-key polling while another component
// owns the session's stdin (for example, the busy composer capture).
func (kh *KeyHandler) Suspend() {
	if kh != nil && !kh.suspended.Swap(true) {
		kh.signalPollStateChange()
	}
}

// Resume re-enables physical-key polling after the stdin owner is released.
func (kh *KeyHandler) Resume() {
	if kh != nil && kh.suspended.Swap(false) {
		kh.signalPollStateChange()
	}
}

func (kh *KeyHandler) signalPollStateChange() {
	select {
	case kh.pollState <- struct{}{}:
	default:
	}
}

// Notify 程序化触发 ESC 键事件（用于测试）
func (kh *KeyHandler) Notify() {
	if kh != nil && kh.enabled.Load() && kh.armed.Load() && !kh.suspended.Load() {
		select {
		case kh.notifyChan <- true:
		default:
		}
	}
}

// GetESCChannel 获取 ESC 键事件通道
func (kh *KeyHandler) GetESCChannel() <-chan bool {
	return kh.notifyChan
}

// WaitForESC 等待 ESC 键按下（带超时）
// 返回 true 表示检测到 ESC 键，false 表示超时
func (kh *KeyHandler) WaitForESC(timeout time.Duration) bool {
	if kh == nil || !kh.enabled.Load() {
		return false
	}

	select {
	case <-kh.notifyChan:
		return true
	case <-time.After(timeout):
		return false
	}
}

// IsEnabled 检查键盘监听是否启用
func (kh *KeyHandler) IsEnabled() bool {
	return kh != nil && kh.enabled.Load()
}

// ManualInterrupt 手动触发中断（用于从代码中模拟 ESC 键）
func (kh *KeyHandler) ManualInterrupt() {
	kh.Notify()
}
