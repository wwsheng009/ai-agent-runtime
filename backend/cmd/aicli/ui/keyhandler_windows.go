//go:build windows

package ui

// Start 启动键盘监听（Windows 系统）
// 不支持 ESC 键监听，只依赖 Ctrl+C (SIGINT)
func (kh *KeyHandler) Start() <-chan bool {
	if kh.enabled {
		return kh.notifyChan
	}

	kh.enabled = true

	return kh.notifyChan
}
