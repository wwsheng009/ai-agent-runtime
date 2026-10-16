package commands

import (
	"fmt"
	"strings"

	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
)

// chat_profile_lifecycle_seed.go 承载 /profile seed（内置 profile 显式补齐）：
// 与启动期自动播种（层空才播、一次性）互补的显式入口——二进制新增内置模板
// （如 full）而用户层已有内容时，用本命令把缺失的模板落盘。语义与 CLI
// `aicli profile seed` 完全同一（profilesys.EnsureBuiltinProfiles 单一实现点）：
// 缺失名整套物化、已有内容零碰、--force 只补缺失文件不改已有文件。
func chatProfileSeedLifecycleText(session *ChatSession, force bool) (string, error) {
	if err := chatProfileLifecycleWriteGuard(session); err != nil {
		return "", err
	}
	written, skipped, err := profilesys.EnsureBuiltinProfiles("", force)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if len(written) == 0 {
		b.WriteString("无缺失内置 profile，未写入任何文件\n")
	} else {
		b.WriteString(fmt.Sprintf("已补齐 %d 个内置 profile（用户层根）:\n", len(written)))
		for _, entry := range written {
			fmt.Fprintf(&b, "  + %s（%d 个文件）\n", entry.Name, len(entry.Files))
		}
	}
	if len(skipped) > 0 {
		fmt.Fprintf(&b, "  已存在未动: %s\n", strings.Join(skipped, ", "))
	}
	if force {
		b.WriteString("  force: 已存在的内置名只补缺失文件，已有文件零改写\n")
	}
	b.WriteString("下一步: /profile use <name>（立即切换）")
	return strings.TrimSpace(b.String()), nil
}
