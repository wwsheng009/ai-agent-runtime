//go:build !win7compat

package auth

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// defaultOpenBrowser 尽力打开系统浏览器；失败时返回错误，由调用方打印 URL 兜底。
//
// 不等待浏览器退出（Start 而非 Run），避免阻塞授权等待循环。
func defaultOpenBrowser(target string) error {
	target = strings.TrimSpace(target)
	if target == "" {
		return fmt.Errorf("授权 URL 为空")
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		// rundll32 在 Windows 7+ 均可用，且不会像 `cmd /c start` 那样受引号影响。
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	case "darwin":
		cmd = exec.Command("open", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
