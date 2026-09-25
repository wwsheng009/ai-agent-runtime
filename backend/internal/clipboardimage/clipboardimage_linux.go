//go:build linux

package clipboardimage

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type linuxBackend struct {
	name string
	args []string
}

// linuxBackends 按 Wayland → X11 的顺序探测可用工具（两者都存在时优先 wl-paste）。
func linuxBackends() []linuxBackend {
	backends := make([]linuxBackend, 0, 2)
	if _, err := exec.LookPath("wl-paste"); err == nil {
		backends = append(backends, linuxBackend{name: "wl-paste", args: []string{"--no-newline", "--type", "image/png"}})
	}
	if _, err := exec.LookPath("xclip"); err == nil {
		backends = append(backends, linuxBackend{name: "xclip", args: []string{"-selection", "clipboard", "-t", "image/png", "-o"}})
	}
	return backends
}

func availability() (bool, string) {
	backends := linuxBackends()
	if len(backends) == 0 {
		return false, "缺少 wl-paste 或 xclip，无法读取剪贴板图片"
	}
	names := make([]string, 0, len(backends))
	for _, backend := range backends {
		names = append(names, backend.name)
	}
	return true, "Linux 剪贴板（" + strings.Join(names, " / ") + " → 临时 PNG）"
}

func readPlatform(ctx context.Context, dir string) (Result, error) {
	backends := linuxBackends()
	if len(backends) == 0 {
		return Result{}, ErrUnsupported
	}
	var lastErr error
	for _, backend := range backends {
		file, err := os.CreateTemp(dir, "aicli-clipboard-*.png")
		if err != nil {
			return Result{}, fmt.Errorf("创建剪贴板临时文件失败: %w", err)
		}
		path := file.Name()
		command := exec.CommandContext(ctx, backend.name, backend.args...)
		command.Stdout = file
		runErr := command.Run()
		_ = file.Close()
		if runErr != nil {
			_ = os.Remove(path)
			lastErr = runErr
			continue
		}
		width, height, err := readPNGDimensions(path)
		if err != nil {
			_ = os.Remove(path)
			lastErr = err
			continue
		}
		return Result{Path: path, Width: width, Height: height, Source: backend.name}, nil
	}
	if lastErr != nil {
		return Result{}, fmt.Errorf("%w（最后一次读取失败: %v）", ErrNoImage, lastErr)
	}
	return Result{}, ErrNoImage
}
