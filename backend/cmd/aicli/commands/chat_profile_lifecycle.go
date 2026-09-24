package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
)

// chat_profile_lifecycle.go 实现 TUI `/profile` 的生命周期子命令（设计文档 §23 G4 / D27）：
// create / duplicate / rename / move / delete / export / edit。
//
// 纪律（延续 §17.2 与 D27）：
//   - 解析不了就报错：不猜目标、不自动改名、不静默降级；
//   - 写盘失败路径状态零改动：目标目录若是本次新建，失败即清理；预先存在的目录绝不
//     自动删除（宁可留下可诊断的半成品，也不误删用户文件）；
//   - 复杂编辑仍在前端：TUI 只提供最小闭环 + 路径指引（D27）；
//   - 所有名称/层校验复用 internal/profile 与 CLI 的既有实现，不引入第二套方言。
//
// save-as（D24 差分固化）需要"会话实际生效面 vs 基线"的差分核心，属 Batch 13 后续 slice；
// dispatch 层对 save-as 保持显式拒绝，不提供半成品实现。

const (
	chatProfileLayerUser    = "user"
	chatProfileLayerProject = "project"
)

// chatProfileLifecycleTarget 是一个已解析的生命周期操作目标。
type chatProfileLifecycleTarget struct {
	Ref   string // 解析后的引用（路径或名字）
	Name  string // 解析后的 profile 名（= 目录名）
	Root  string // profile 根目录（绝对路径）
	Layer string // user | project | ""（不在标准层内，如自定义 root）
}

// chatProfileLifecycleWriteGuard 统一挡住"写了也不生效"的子会话（M16/INV-A3）。
func chatProfileLifecycleWriteGuard(session *ChatSession) error {
	if session == nil {
		return errChatProfileNoSession
	}
	if chatRoutingSessionIsChildAgent(session) {
		return errChatProfileChildSessionReadOnly
	}
	return nil
}

// chatProfileResolveLifecycleTarget 把一个引用解析为可写目标；ref 为空时回落到
// 当前会话绑定（其次配置默认），与 `/profile status` 的口径一致。
func chatProfileResolveLifecycleTarget(session *ChatSession, ref string) (chatProfileLifecycleTarget, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		ref = chatProfileDefaultRefForSession(session)
	}
	if ref == "" {
		return chatProfileLifecycleTarget{}, fmt.Errorf(
			"缺少 profile 引用：当前会话未绑定且未配置默认 profile；用法 /profile <子命令> <ref>")
	}
	state, err := resolveChatProfilePreviewState(session, ref)
	if err != nil {
		return chatProfileLifecycleTarget{}, err
	}
	root := strings.TrimSpace(state.Resolved.ProfileRoot)
	if root == "" {
		return chatProfileLifecycleTarget{}, fmt.Errorf("profile %s 解析结果缺少 root，无法定位目录", ref)
	}
	name := strings.TrimSpace(state.Resolved.ProfileName)
	if name == "" {
		name = filepath.Base(root)
	}
	return chatProfileLifecycleTarget{
		Ref:   firstNonEmptyChatValue(strings.TrimSpace(state.Reference), ref),
		Name:  name,
		Root:  root,
		Layer: chatProfileLayerForRoot(root),
	}, nil
}

// chatProfileDefaultRefForSession 返回"没有显式 ref 时"的回落引用：会话绑定优先，
// 其次配置默认（与 D30/A5 的优先级口径一致）。
func chatProfileDefaultRefForSession(session *ChatSession) string {
	if session == nil {
		return ""
	}
	if ref := strings.TrimSpace(session.ProfileReference); ref != "" {
		return ref
	}
	if session.Config != nil && session.Config.Profiles != nil {
		return strings.TrimSpace(session.Config.Profiles.DefaultProfile)
	}
	return ""
}

// chatProfileLayerForRoot 判定 root 属于哪个标准层（用于同层/跨层判断与报告）。
func chatProfileLayerForRoot(root string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		return ""
	}
	for _, layer := range []string{chatProfileLayerUser, chatProfileLayerProject} {
		base, err := profilesys.LayerRoot(layer)
		if err != nil || strings.TrimSpace(base) == "" {
			continue
		}
		if chatProfilePathWithin(base, root) {
			return layer
		}
	}
	return ""
}

// chatProfilePathWithin 判断 child 是否位于 parent 之内（路径前缀比较，不解析符号链接）。
func chatProfilePathWithin(parent, child string) bool {
	parent = strings.TrimSpace(parent)
	child = strings.TrimSpace(child)
	if parent == "" || child == "" {
		return false
	}
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// chatProfileLayerBase 返回目标层的层根（user/project），未知层报错。
func chatProfileLayerBase(layer string) (string, error) {
	layer = strings.TrimSpace(strings.ToLower(layer))
	switch layer {
	case chatProfileLayerUser, chatProfileLayerProject:
	default:
		return "", fmt.Errorf("未知层 %q（可用 user|project）", layer)
	}
	base, err := profilesys.LayerRoot(layer)
	if err != nil {
		return "", err
	}
	base = strings.TrimSpace(base)
	if base == "" {
		return "", fmt.Errorf("无法解析 %s 层根目录", layer)
	}
	return base, nil
}

// chatProfilePostWriteNote 写盘后跑同一 validate 并把结果渲染为报告行（D28 纪律：
// 校验结论必须来自同一实现，不在命令层自造判断）。校验不通过只报告、不自动回滚：
// 文件是用户显式要求生成的，回滚会丢掉可修复的内容。
func chatProfilePostWriteNote(session *ChatSession, root string) []string {
	if session == nil || session.Config == nil {
		return nil
	}
	result, err := runProfileValidateCommand(session.Config, root, "")
	if err != nil {
		return []string{"  校验: 无法解析（" + err.Error() + "）；用 /profile validate " + filepath.Base(root) + " 复查"}
	}
	if result.Valid {
		return []string{fmt.Sprintf("  校验: 通过（warning %d 条）", result.WarningCount)}
	}
	lines := []string{fmt.Sprintf(
		"  校验: 未通过（error %d / warning %d）——文件已保留，可用 /profile edit %s 修复后重跑 /profile validate",
		result.ErrorCount, result.WarningCount, filepath.Base(root))}
	if first := firstProfileValidateError(result); first != "" {
		lines = append(lines, "    "+first)
	}
	return lines
}

// chatProfileFormatPathList 把相对路径清单渲染为缩进行（报告与删除确认共用）。
func chatProfileFormatPathList(paths []string, indent string) []string {
	lines := make([]string, 0, len(paths))
	for _, path := range paths {
		lines = append(lines, indent+path)
	}
	return lines
}

// chatProfileRefNote 在"用户给的 ref ≠ 声明名"时补一个显式标注（D34）：
// 声明名是权威（导出包按它校验），但用户看到的名字也要能对上，否则会以为
// 操作的是另一个 profile。路径形态的 ref 不标注（太长且无信息量）。
func chatProfileRefNote(ref, declared string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.EqualFold(ref, declared) {
		return ""
	}
	if strings.ContainsAny(ref, `/\`) {
		return ""
	}
	return "（ref: " + ref + "）"
}

// chatProfileTemplateKnown 判定模板名是否在 FR-2 内置模板内。
func chatProfileTemplateKnown(name string) bool {
	name = strings.TrimSpace(name)
	for _, candidate := range profilesys.TemplateNames() {
		if candidate == name {
			return true
		}
	}
	return false
}

// 以下为共用小工具与配置引用改写：被 create/duplicate/rename/move/delete 共用。
// 文件分工：create/duplicate → chat_profile_lifecycle_create.go；
// rename/move/delete/export/edit → chat_profile_lifecycle_ops.go。
var (
	errChatProfileNoSession            = fmt.Errorf("当前没有活动会话")
	errChatProfileChildSessionReadOnly = fmt.Errorf("%s", chatRoutingChildSessionReadOnlyNote)
)

// dirExists / fileExists 是本文件的小工具：只做存在性判断，不引入新的解析路径。
func dirExists(path string) bool {
	info, err := os.Stat(strings.TrimSpace(path))
	return err == nil && info.IsDir()
}

func fileExists(path string) bool {
	info, err := os.Stat(strings.TrimSpace(path))
	return err == nil && !info.IsDir()
}

// chatProfileSamePath 只比较规范化后的路径字符串（不做 EvalSymlinks，避免为一次
// 引用比较引入新的 IO 语义）。
func chatProfileSamePath(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// chatProfileConfigItemNames 返回配置里 root 指向给定目录的注册项名（升序）。
func chatProfileConfigItemNames(cfg *agentconfig.Config, root string) []string {
	if cfg == nil || cfg.Profiles == nil || len(cfg.Profiles.Items) == 0 {
		return nil
	}
	names := make([]string, 0, len(cfg.Profiles.Items))
	for name, item := range cfg.Profiles.Items {
		if chatProfileSamePath(item.Root, root) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// chatProfileMoveTree 移动/重命名 profile 目录：优先原子 os.Rename；跨卷失败时回退为
// "收集 → 物化 → 删源"（复用 transfer.go 的同一套路径纪律），回退失败即回滚目标。
func chatProfileMoveTree(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	files, err := profilesys.CollectBundleFiles(src)
	if err != nil {
		return fmt.Errorf("移动 %s → %s 失败（且无法收集源文件）: %w", src, dst, err)
	}
	if _, err := profilesys.ExtractBundle(dst, files); err != nil {
		_ = os.RemoveAll(dst)
		return fmt.Errorf("移动 %s → %s 失败（跨卷回退未完成）: %w", src, dst, err)
	}
	if err := os.RemoveAll(src); err != nil {
		_ = os.RemoveAll(dst)
		return fmt.Errorf("移动 %s → %s 失败（源目录清理未完成，已回滚目标）: %w", src, dst, err)
	}
	return nil
}

// chatProfileRewriteConfigForRename 按 API 同一语义改写配置引用：default_profile 改名、
// 旧注册项删除、指向同一 root 的其它注册项重指新路径（D25）。
func chatProfileRewriteConfigForRename(session *ChatSession, target chatProfileLifecycleTarget, newName, newRoot string) ([]string, error) {
	cfg := session.Config
	if cfg == nil || cfg.Profiles == nil {
		return nil, nil
	}
	isDefault := strings.TrimSpace(cfg.Profiles.DefaultProfile) == target.Name
	items := chatProfileConfigItemNames(cfg, target.Root)
	if !isDefault && len(items) == 0 {
		return []string{"  config: 该 profile 未注册（按 profiles.root/<name> 解析），无需改写引用"}, nil
	}
	configPath, err := ensureWritableAICLIConfigPath(cfg, "")
	if err != nil {
		return nil, err
	}
	update := agentconfig.ProfilesConfigUpdate{ItemName: newName, ItemRoot: newRoot}
	if isDefault {
		update.DefaultProfile = newName
	}
	if err := agentconfig.UpdateProfilesConfig(configPath, update); err != nil {
		return nil, err
	}
	lines := []string{"  config: " + configPath}
	if isDefault {
		lines = append(lines, "  config: profiles.default_profile → "+newName)
	}
	for _, name := range items {
		if name == target.Name {
			if err := agentconfig.RemoveProfilesConfigItem(configPath, name); err != nil {
				return lines, err
			}
			lines = append(lines, "  config: 已移除旧注册项 profiles.items."+name)
			continue
		}
		if err := agentconfig.UpdateProfilesConfig(configPath, agentconfig.ProfilesConfigUpdate{ItemName: name, ItemRoot: newRoot}); err != nil {
			return lines, err
		}
		lines = append(lines, "  config: profiles.items."+name+".root 已指向新路径")
	}
	return lines, nil
}

// chatProfileRepointConfigAfterMove 层级移动后的配置改写：同名（ref 不变）只改 root。
func chatProfileRepointConfigAfterMove(session *ChatSession, target chatProfileLifecycleTarget, newRoot string) ([]string, error) {
	cfg := session.Config
	if cfg == nil || cfg.Profiles == nil {
		return nil, nil
	}
	items := chatProfileConfigItemNames(cfg, target.Root)
	if len(items) == 0 {
		return []string{"  config: 该 profile 未注册（按 profiles.root/<name> 解析），移动后请确认解析目标层"}, nil
	}
	configPath, err := ensureWritableAICLIConfigPath(cfg, "")
	if err != nil {
		return nil, err
	}
	lines := []string{"  config: " + configPath}
	for _, name := range items {
		if err := agentconfig.UpdateProfilesConfig(configPath, agentconfig.ProfilesConfigUpdate{ItemName: name, ItemRoot: newRoot}); err != nil {
			return lines, err
		}
		lines = append(lines, "  config: profiles.items."+name+".root 已指向新路径")
	}
	return lines, nil
}

// chatProfileCleanupConfigAfterDelete 删除后的配置清理：`--force` 清空 default（D25），
// 并移除指向该目录的注册项（悬空 items 会让解析器指向不存在的路径）。
func chatProfileCleanupConfigAfterDelete(session *ChatSession, target chatProfileLifecycleTarget, clearDefault bool) ([]string, error) {
	cfg := session.Config
	if cfg == nil || cfg.Profiles == nil {
		return nil, nil
	}
	items := chatProfileConfigItemNames(cfg, target.Root)
	if !clearDefault && len(items) == 0 {
		return nil, nil
	}
	configPath, err := ensureWritableAICLIConfigPath(cfg, "")
	if err != nil {
		return nil, err
	}
	lines := []string{"  config: " + configPath}
	if clearDefault {
		if err := agentconfig.UpdateProfilesConfig(configPath, agentconfig.ProfilesConfigUpdate{ClearDefaultProfile: true}); err != nil {
			return lines, err
		}
		lines = append(lines, "  config: profiles.default_profile 已清空（--force）")
	}
	for _, name := range items {
		if err := agentconfig.RemoveProfilesConfigItem(configPath, name); err != nil {
			return lines, err
		}
		lines = append(lines, "  config: 已移除注册项 profiles.items."+name)
	}
	return lines, nil
}

// chatProfileRenameLifecycleText 执行 `/profile rename <ref> <new-name>`（同层）。
