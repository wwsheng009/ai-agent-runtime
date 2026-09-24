package profile

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LayerRoot 返回层根目录（G1/G2 只允许 user 与 project 两层）。
//
// 规则本体只此一份：API（internal/api/skills 的 profile 写端点）与 CLI
// （aicli profile create/import/move）共用。两个入口各写一套"层根在哪"会立刻
// 分叉——一个写 <home>/.aicli/profiles、另一个写 ./profiles，而冲突检查、
// 引用重定向都建立在这个路径上。
func LayerRoot(layer string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(layer)) {
	case "user":
		home, err := os.UserHomeDir()
		if err != nil || strings.TrimSpace(home) == "" {
			return "", fmt.Errorf("无法定位用户配置目录：%v", err)
		}
		return filepath.Join(home, ".aicli", "profiles"), nil
	case "project":
		cwd, err := os.Getwd()
		if err != nil || strings.TrimSpace(cwd) == "" {
			return "", fmt.Errorf("无法定位当前工作目录：%v", err)
		}
		return filepath.Join(cwd, ".aicli", "profiles"), nil
	default:
		return "", fmt.Errorf("layer 只支持 user / project：%q", layer)
	}
}
