package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
)

// chat_profile_lifecycle_create.go 承载创建类子命令（Batch 13 slice 4 / §23 G4）：
// create（模板渲染落盘）与 duplicate（深拷贝到目标层）。
// 公共设施与配置改写见 chat_profile_lifecycle.go；运维类见 chat_profile_lifecycle_ops.go。
// 纪律：目标目录若是本次新建，失败即清理；预先存在的目录绝不自动删除。
func chatProfileCreateLifecycleText(session *ChatSession, name, template, layer string, force bool) (string, error) {
	if err := chatProfileLifecycleWriteGuard(session); err != nil {
		return "", err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("用法: /profile create <name> [--template coding|review|minimal|docs] [--to user|project]")
	}
	if err := validateProfileCreateName(name); err != nil {
		return "", err
	}
	template = strings.TrimSpace(template)
	if template == "" {
		template = "coding"
	}
	if !chatProfileTemplateKnown(template) {
		return "", fmt.Errorf("未知模板 %q（可用: %s）", template, strings.Join(profilesys.TemplateNames(), "|"))
	}
	layer = strings.TrimSpace(strings.ToLower(layer))
	if layer == "" {
		layer = chatProfileLayerUser
	}
	base, err := chatProfileLayerBase(layer)
	if err != nil {
		return "", err
	}
	root := filepath.Join(base, name)
	preExisting := dirExists(root)
	if err := ensureProfileCreateTarget(root, force); err != nil {
		return "", err
	}
	files, err := profilesys.RenderTemplate(template, name, "")
	if err != nil {
		return "", err
	}
	relPaths := sortedProfileTemplatePaths(files)
	if err := writeProfileTemplateFiles(root, files, relPaths); err != nil {
		if !preExisting {
			_ = os.RemoveAll(root)
		}
		return "", err
	}

	lines := []string{
		fmt.Sprintf("已创建 profile: %s（template: %s，layer: %s）", name, template, layer),
		"  root:  " + root,
		fmt.Sprintf("  文件（%d）:", len(relPaths)),
	}
	lines = append(lines, chatProfileFormatPathList(relPaths, "    ")...)
	lines = append(lines, chatProfilePostWriteNote(session, root)...)
	lines = append(lines,
		"下一步: /profile use "+name+"（立即切换）或 /profile edit "+name+"（继续编辑）")
	return strings.Join(lines, "\n"), nil
}

// chatProfileDuplicateLifecycleText 执行 `/profile duplicate <ref> <name> [--to user|project]`。
// 复制复用 transfer.go 的收集/物化核心（同一份路径清洗与 tmp/符号链接纪律），不另写拷贝器。
func chatProfileDuplicateLifecycleText(session *ChatSession, ref, name, layer string) (string, error) {
	if err := chatProfileLifecycleWriteGuard(session); err != nil {
		return "", err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("用法: /profile duplicate <ref> <name> [--to user|project]")
	}
	if err := validateProfileCreateName(name); err != nil {
		return "", err
	}
	source, err := chatProfileResolveLifecycleTarget(session, ref)
	if err != nil {
		return "", err
	}
	layer = strings.TrimSpace(strings.ToLower(layer))
	if layer == "" {
		layer = firstNonEmptyChatValue(source.Layer, chatProfileLayerUser)
	}
	base, err := chatProfileLayerBase(layer)
	if err != nil {
		return "", err
	}
	root := filepath.Join(base, name)
	if dirExists(root) {
		return "", fmt.Errorf("目标已存在：%s（复制不覆盖，请换名或用 /profile delete 先清理）", root)
	}
	files, err := profilesys.CollectBundleFiles(source.Root)
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return "", fmt.Errorf("源 profile 无可复制文件：%s", source.Root)
	}
	written, err := profilesys.ExtractBundle(root, files)
	if err != nil {
		_ = os.RemoveAll(root)
		return "", err
	}

	lines := []string{
		fmt.Sprintf("已复制 profile: %s → %s（layer: %s）", source.Name, name, layer),
		"  root:  " + root,
		fmt.Sprintf("  文件（%d）:", len(written)),
	}
	lines = append(lines, chatProfileFormatPathList(written, "    ")...)
	if !strings.EqualFold(source.Name, name) {
		lines = append(lines, fmt.Sprintf(
			"  提示: 副本 profile.yaml 仍声明 name: %s（复制是逐字节深拷贝，不改写声明名）；如需改名用 /profile rename %s <new-name>",
			source.Name, name))
	}
	lines = append(lines, chatProfilePostWriteNote(session, root)...)
	lines = append(lines, "下一步: /profile use "+name+"（立即切换）")
	return strings.Join(lines, "\n"), nil
}
