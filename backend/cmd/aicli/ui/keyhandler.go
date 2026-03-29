package ui

import "time"

// KeyHandler 键盘事件处理器
type KeyHandler struct {
	quitChan    chan struct{}
	notifyChan  chan bool  // ESC 键按下通知
	enabled     bool
}

// NewKeyHandler 创建新的键盘事件处理器
func NewKeyHandler() *KeyHandler {
	return &KeyHandler{
		quitChan:   make(chan struct{}),
		notifyChan: make(chan bool, 10),
		enabled:    false,
	}
}

// Stop 停止键盘监听
func (kh *KeyHandler) Stop() {
	if kh.enabled {
		kh.enabled = false
		close(kh.quitChan)
	}
}

// Notify 程序化触发 ESC 键事件（用于测试）
func (kh *KeyHandler) Notify() {
	if kh.enabled {
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
	if !kh.enabled {
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
	return kh.enabled
}

// ManualInterrupt 手动触发中断（用于从代码中模拟 ESC 键）
func (kh *KeyHandler) ManualInterrupt() {
	kh.Notify()
}
