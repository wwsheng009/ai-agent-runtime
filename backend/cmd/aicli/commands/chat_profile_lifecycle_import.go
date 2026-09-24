package commands

import (
	"fmt"
	"strings"
)

// chat_profile_lifecycle_import.go 承载 TUI `/profile import`（Batch 13 slice 7 / §23 G5 / D28）：
// 把 profile 目录或 zip 包导入到 user/project 层，补齐生命周期面的最后一个缺口
// （export 早已接线，此前 import 只提示 CLI 命令）。
//
// 执行核心复用 CLI 侧的 runProfileImportCommand——同一段"读包 → 临时目录物化 →
// 同一个 validate → 原子落位"，命令层不复制任何导入逻辑（与 export/duplicate 同一纪律）：
//   - D28-1：validate 不通过即拒绝（失败时目标层根不留 profile 目录）；
//   - D28-2：绝不自动激活：不写 default、不切换当前会话；
//   - D28-3：打印将写入的路径清单（与导出/删除同一投影）；
//   - D28-4：同名目标冲突即拒绝，不覆盖。
//
// 守卫：真实导入沿用 chatProfileLifecycleWriteGuard（子会话"写了也不生效"，M16/INV-A3）；
// --dry-run 是只读预演（连层根都不创建），因此不挡子会话。
func chatProfileImportLifecycleText(session *ChatSession, path, name, layer string, dryRun bool) (string, error) {
	if session == nil {
		return "", errChatProfileNoSession
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("用法: /profile import <包路径|目录> [--to user|project] [--name <名字>] [--dry-run]")
	}
	if !dryRun {
		if err := chatProfileLifecycleWriteGuard(session); err != nil {
			return "", err
		}
	}
	if session.Config == nil {
		return "", fmt.Errorf("当前会话缺少配置上下文，无法执行导入校验（D28-1 要求与 CLI/API 同一个 validate）")
	}
	result, err := runProfileImportCommand(session.Config, profileImportOptions{
		Path:   path,
		Layer:  strings.TrimSpace(layer),
		Name:   strings.TrimSpace(name),
		DryRun: dryRun,
	})
	if err != nil {
		return "", err
	}

	lines := make([]string, 0, 8+len(result.Files))
	if result.Imported {
		lines = append(lines, fmt.Sprintf("已导入 profile: %s（%s 层）", result.Name, result.Layer))
	} else {
		lines = append(lines, fmt.Sprintf("将导入 profile（--dry-run，未写盘）: %s（%s 层）", result.Name, result.Layer))
	}
	lines = append(lines,
		"  源:   "+result.Source,
		"  root: "+result.Root,
		fmt.Sprintf("  文件（%d）:", result.FileCount),
	)
	lines = append(lines, chatProfileFormatPathList(result.Files, "    ")...)
	if result.Imported {
		// D28 纪律：校验结论来自同一实现（不在命令层自造判断）。
		lines = append(lines, chatProfilePostWriteNote(session, result.Root)...)
		lines = append(lines, "  提示: 已导入但未激活（D28）：设为默认或会话内 /profile use 都是独立动作")
		lines = append(lines, "下一步: /profile use "+result.Name+"（立即切换）或 /profile show "+result.Name+"（只读预览）")
	} else {
		lines = append(lines, "  说明: --dry-run 只预演（未写盘、未创建层根）；去掉 --dry-run 执行导入")
	}
	return strings.Join(lines, "\n"), nil
}
