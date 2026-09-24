package commands

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
)

// chat_profile_lifecycle_ops.go 承载运维类子命令（Batch 13 slice 4 / §23 G4）：
// rename / move / delete / export / edit。
// 公共设施与配置引用改写见 chat_profile_lifecycle.go；创建类见 chat_profile_lifecycle_create.go。
func chatProfileRenameLifecycleText(session *ChatSession, ref, newName string) (string, error) {
	if err := chatProfileLifecycleWriteGuard(session); err != nil {
		return "", err
	}
	newName = strings.TrimSpace(newName)
	if newName == "" {
		return "", fmt.Errorf("用法: /profile rename <ref> <new-name>")
	}
	if err := validateProfileCreateName(newName); err != nil {
		return "", err
	}
	target, err := chatProfileResolveLifecycleTarget(session, ref)
	if err != nil {
		return "", err
	}
	if newName == target.Name {
		return "", fmt.Errorf("新名与旧名相同：%s（无需重命名）", newName)
	}
	newRoot := filepath.Join(filepath.Dir(target.Root), newName)
	if dirExists(newRoot) {
		return "", fmt.Errorf("目标已存在：%s（重命名不覆盖）", newRoot)
	}
	if err := chatProfileMoveTree(target.Root, newRoot); err != nil {
		return "", err
	}
	renamed, err := profilesys.RewriteProfileName(newRoot, newName)
	if err != nil {
		// D34：声明名改写失败就回滚目录名，保持"失败路径状态零改动"。
		if rollbackErr := chatProfileMoveTree(newRoot, target.Root); rollbackErr != nil {
			return "", fmt.Errorf(
				"目录已重命名为 %s，但 profile.yaml 声明名改写失败且回滚失败（请手工处理）：%v / %v",
				newRoot, err, rollbackErr)
		}
		return "", fmt.Errorf("改写 profile.yaml 声明名失败，目录名已回滚: %w", err)
	}
	configLines, err := chatProfileRewriteConfigForRename(session, target, newName, newRoot)
	if err != nil {
		return "", fmt.Errorf("目录已重命名，但配置引用改写失败（请检查 %s）: %w", newRoot, err)
	}

	lines := []string{
		fmt.Sprintf("已重命名 profile: %s → %s", target.Name, newName),
		"  root:  " + newRoot,
	}
	if renamed {
		lines = append(lines, "  profile.yaml: 声明名已同步为 "+newName)
	} else {
		lines = append(lines, "  提示: profile.yaml 未声明 profile.name，仅目录名已改为 "+newName)
	}
	lines = append(lines, chatProfilePostWriteNote(session, newRoot)...)
	lines = append(lines, configLines...)
	if strings.TrimSpace(session.ProfileReference) != "" && strings.TrimSpace(session.ProfileName) == target.Name {
		lines = append(lines, "  警告: 当前会话仍绑定旧引用 "+target.Name+"；用 /profile use "+newName+" 切换")
	}
	lines = append(lines, "下一步: /profile use "+newName)
	return strings.Join(lines, "\n"), nil
}

// chatProfileMoveLifecycleText 执行 `/profile move <ref> --to user|project`（跨层）。
func chatProfileMoveLifecycleText(session *ChatSession, ref, layer string) (string, error) {
	if err := chatProfileLifecycleWriteGuard(session); err != nil {
		return "", err
	}
	layer = strings.TrimSpace(strings.ToLower(layer))
	if layer == "" {
		return "", fmt.Errorf("用法: /profile move <ref> --to user|project")
	}
	target, err := chatProfileResolveLifecycleTarget(session, ref)
	if err != nil {
		return "", err
	}
	if target.Layer == layer {
		return "", fmt.Errorf("同层移动无需 move：%s 已在 %s 层（跨层才需要）", target.Name, layer)
	}
	base, err := chatProfileLayerBase(layer)
	if err != nil {
		return "", err
	}
	newRoot := filepath.Join(base, target.Name)
	if dirExists(newRoot) {
		return "", fmt.Errorf("目标层已存在同名 profile：%s（移动不覆盖）", newRoot)
	}
	if err := chatProfileMoveTree(target.Root, newRoot); err != nil {
		return "", err
	}
	configLines, err := chatProfileRepointConfigAfterMove(session, target, newRoot)
	if err != nil {
		return "", fmt.Errorf("目录已移动，但配置引用改写失败（请检查 %s）: %w", newRoot, err)
	}

	from := firstNonEmptyChatValue(target.Layer, "自定义 root")
	lines := []string{
		fmt.Sprintf("已移动 profile: %s（%s → %s）", target.Name, from, layer),
		"  root:  " + newRoot,
	}
	lines = append(lines, chatProfilePostWriteNote(session, newRoot)...)
	lines = append(lines, configLines...)
	if strings.TrimSpace(session.ProfileReference) != "" && strings.TrimSpace(session.ProfileName) == target.Name {
		lines = append(lines, "  警告: 当前会话仍绑定旧路径 "+target.Root+"；用 /profile use "+target.Name+" 重新解析")
	}
	return strings.Join(lines, "\n"), nil
}

// chatProfileDeleteLifecycleText 执行 `/profile delete <ref> [--force]`。
//
// 引用检查范围（与 API 端点的差异已在报告里明示）：default_profile（阻止，`--force`
// 同时清空）、配置注册项（删除后一并清理）、当前会话绑定（警告）。跨 profile 的 agent
// 引用在当前 schema 无该字段（与 API 同一结论：恒空，不做猜测式扫描）。
func chatProfileDeleteLifecycleText(session *ChatSession, ref string, force bool) (string, error) {
	if err := chatProfileLifecycleWriteGuard(session); err != nil {
		return "", err
	}
	target, err := chatProfileResolveLifecycleTarget(session, ref)
	if err != nil {
		return "", err
	}
	if !profileRootHasProfileYAML(target.Root) {
		return "", fmt.Errorf("目标目录不含 profile.yaml，拒绝删除（防误删）：%s", target.Root)
	}
	cfg := session.Config
	defaultName := ""
	if cfg != nil && cfg.Profiles != nil {
		defaultName = strings.TrimSpace(cfg.Profiles.DefaultProfile)
	}
	isDefault := defaultName != "" && defaultName == target.Name
	if isDefault && !force {
		return "", fmt.Errorf(
			"profile %s 被 config.profiles.default_profile 引用：删除会留下悬空默认；确认后追加 --force（同时清空 default）",
			target.Name)
	}
	files, err := profilesys.CollectBundleFiles(target.Root)
	if err != nil {
		return "", err
	}
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	sort.Strings(paths)

	if err := os.RemoveAll(target.Root); err != nil {
		return "", fmt.Errorf("删除失败: %w", err)
	}
	if dirExists(target.Root) {
		return "", fmt.Errorf("删除未完成，目录仍存在：%s", target.Root)
	}
	configLines, err := chatProfileCleanupConfigAfterDelete(session, target, isDefault && force)
	if err != nil {
		return "", fmt.Errorf("目录已删除，但配置清理失败: %w", err)
	}

	lines := []string{
		fmt.Sprintf("已删除 profile: %s%s（layer: %s）", target.Name, chatProfileRefNote(ref, target.Name),
			firstNonEmptyChatValue(target.Layer, "自定义 root")),
		"  root:  " + target.Root,
		fmt.Sprintf("  删除文件（%d）:", len(paths)),
	}
	lines = append(lines, chatProfileFormatPathList(paths, "    ")...)
	lines = append(lines, configLines...)
	if isDefault && force {
		lines = append(lines, "  说明: default 已清空，新会话回到无 profile 基线")
	}
	if strings.TrimSpace(session.ProfileReference) != "" && strings.TrimSpace(session.ProfileName) == target.Name {
		lines = append(lines, "  警告: 当前会话仍绑定该 profile（本会话继续可用）；resume 时按 R18 降级（警告 + 会话不崩）")
	}
	lines = append(lines, "说明: 活跃会话的全库扫描属 API 端点（/references）；TUI 只报告当前会话绑定")
	return strings.Join(lines, "\n"), nil
}

// chatProfileExportLifecycleText 执行 `/profile export [<ref>] [--out <file|dir>]`（G5）。
// 只读操作，复用 transfer.go 的收集/打包核心与 CLI 的输出路径规则。
func chatProfileExportLifecycleText(session *ChatSession, ref, out string) (string, error) {
	if session == nil {
		return "", errChatProfileNoSession
	}
	target, err := chatProfileResolveLifecycleTarget(session, ref)
	if err != nil {
		return "", err
	}
	files, err := profilesys.CollectBundleFiles(target.Root)
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return "", fmt.Errorf("profile 无可导出文件：%s", target.Root)
	}
	outPath, err := filepath.Abs(resolveProfileExportOutputPath(out, target.Name))
	if err != nil {
		return "", fmt.Errorf("解析导出路径失败: %w", err)
	}
	file, err := os.Create(outPath)
	if err != nil {
		return "", fmt.Errorf("创建导出文件失败: %w", err)
	}
	writeErr := profilesys.WriteBundleZip(file, files)
	closeErr := file.Close()
	if writeErr != nil {
		_ = os.Remove(outPath)
		return "", fmt.Errorf("写入 zip 失败: %w", writeErr)
	}
	if closeErr != nil {
		_ = os.Remove(outPath)
		return "", fmt.Errorf("关闭导出文件失败: %w", closeErr)
	}
	total := 0
	for _, item := range files {
		total += len(item.Data)
	}

	lines := []string{
		fmt.Sprintf("已导出 profile: %s%s（%d 个文件，%d 字节）", target.Name, chatProfileRefNote(ref, target.Name),
			len(files), total),
		"  包:    " + outPath,
		"  根:    " + target.Root,
	}
	lines = append(lines, chatProfileFormatPathList(profileBundlePathList(files), "    ")...)
	lines = append(lines, "导入: aicli profile import <包路径> [--to user|project]（导入绝不自动激活）")
	return strings.Join(lines, "\n"), nil
}

// chatProfileEditLifecycleText 执行 `/profile edit [<ref>] [--open]`。
//
// D27 要求"打印 profile.yaml 路径并尝试 $EDITOR"。TUI 的输入循环占用终端，直接拉起
// 编辑器会与行编辑抢 stdin，因此默认只给路径 + 可复制命令；显式 `--open` 才真正
// 挂起并运行 $EDITOR（等价于"尝试"），失败时如实报错而不是假装成功。
func chatProfileEditLifecycleText(session *ChatSession, ref string, open bool) (string, error) {
	if session == nil {
		return "", errChatProfileNoSession
	}
	target, err := chatProfileResolveLifecycleTarget(session, ref)
	if err != nil {
		return "", err
	}
	if !profileRootHasProfileYAML(target.Root) {
		return "", fmt.Errorf("profile 目录缺少 profile.yaml：%s", target.Root)
	}
	yamlPath := profileYAMLPath(target.Root)
	editor := strings.TrimSpace(firstNonEmptyChatValue(os.Getenv("VISUAL"), os.Getenv("EDITOR")))

	lines := []string{
		fmt.Sprintf("profile: %s（root: %s）", target.Name, target.Root),
		"  主文件: " + yamlPath,
	}
	if files, err := profilesys.CollectBundleFiles(target.Root); err == nil && len(files) > 0 {
		paths := profileBundlePathList(files)
		lines = append(lines, fmt.Sprintf("  文件（%d）:", len(paths)))
		lines = append(lines, chatProfileFormatPathList(paths, "    ")...)
	}
	if open {
		if editor == "" {
			return "", fmt.Errorf("--open 需要 $EDITOR 或 $VISUAL；也可以手工编辑：%s", yamlPath)
		}
		fields := strings.Fields(editor)
		command := exec.Command(fields[0], append(fields[1:], yamlPath)...)
		command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := command.Run(); err != nil {
			return "", fmt.Errorf("编辑器退出异常（%s）: %w", editor, err)
		}
		lines = append(lines, "  编辑器已退出；若编辑的是当前绑定 profile，用 /profile reload 重新解析")
		return strings.Join(lines, "\n"), nil
	}
	if editor == "" {
		lines = append(lines, "  未检测到 $EDITOR/$VISUAL：请手工编辑上面的文件，然后 /profile reload")
	} else {
		lines = append(lines, "  在终端执行: "+editor+" \""+yamlPath+"\"（或 /profile edit "+target.Name+" --open 直接拉起）")
		lines = append(lines, "  编辑后用 /profile reload 重新解析")
	}
	lines = append(lines, "说明: 复杂编辑（工具面/overrides 表单）仍在前端 Profiles 页（D27）")
	return strings.Join(lines, "\n"), nil
}
