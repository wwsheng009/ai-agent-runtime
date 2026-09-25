//go:build darwin

package clipboardimage

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func availability() (bool, string) {
	if _, err := exec.LookPath("osascript"); err != nil {
		return false, "缺少 osascript，无法读取剪贴板图片"
	}
	return true, "macOS 剪贴板（osascript «class PNGf» → 临时 PNG）"
}

func readPlatform(ctx context.Context, dir string) (Result, error) {
	file, err := os.CreateTemp(dir, "aicli-clipboard-*.png")
	if err != nil {
		return Result{}, fmt.Errorf("创建剪贴板临时文件失败: %w", err)
	}
	path := file.Name()
	_ = file.Close()

	script := fmt.Sprintf(`set pngData to the clipboard as «class PNGf»
set fh to open for access POSIX file %q with write permission
write pngData to fh
close access fh`, path)
	command := exec.CommandContext(ctx, "osascript", "-e", script)
	output, err := command.CombinedOutput()
	if err != nil {
		_ = os.Remove(path)
		message := strings.TrimSpace(string(output))
		if message == "" || strings.Contains(message, "clipboard") {
			return Result{}, ErrNoImage
		}
		return Result{}, fmt.Errorf("osascript 读取剪贴板失败: %v (%s)", err, message)
	}
	width, height, err := readPNGDimensions(path)
	if err != nil {
		_ = os.Remove(path)
		return Result{}, ErrNoImage
	}
	return Result{Path: path, Width: width, Height: height, Source: "macos-clipboard"}, nil
}
