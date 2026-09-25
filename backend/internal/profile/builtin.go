package profile

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// 本文件负责「内置 profile 的首次初始化落盘」：
//
// 与 config starter（agentconfig.EnsureStarterConfigFile）、用户 presets
// （agentconfig.EnsureUserPresetsFile）属同一类动作——把随二进制嵌入的默认内容，
// 在**用户层根**还空着的时候物化到 `<home>/.aicli` 下。三条纪律：
//
//  1. 内容只有一个来源：内置模板（templates/）。不为"内置 profile"另写一份内容，
//     否则模板与内置 profile 会立刻分叉——占位符替换、agent 目录改名、样例一致性
//     测试（consistency_test.go）都建立在模板这一处上。内置 profile 即"模板渲染产物
//     落到 user 层"。
//  2. 只在用户层根**还没有任何 profile** 时播种。层里已有内容（用户自建、上一次播种的
//     产物、或用户删除后剩下的任意一份）一律整体不动：不补齐、不覆盖、不复活已删除的
//     内置项。删除内置 profile 是用户的合法选择。
//  3. 不写 config：内置 profile 只是"可用"，不等于"生效"。default_profile /
//     profiles.items / 项目绑定一律不变，避免首次启动就悄悄改变既有会话行为。

// BuiltinProfileNames 返回首次初始化会落盘的内置 profile 名单（确定性顺序）。
//
// 名单与内置模板同源：模板名即 profile 名，增删内置 profile 只需要动 templates/。
// 「模板名必须是合法 profile 名」由测试钉住（TestBuiltinProfileNamesAreValidNames），
// 不在运行期静默过滤——否则会出现"文件落了盘、名字却选不中"的隐身 profile。
func BuiltinProfileNames() []string {
	return TemplateNames()
}

// BuiltinProfile 描述一份已落盘的内置 profile。
type BuiltinProfile struct {
	Name  string   // profile 名（= 目录名 = 引用名）
	Root  string   // profile 根目录
	Files []string // profile 相对路径（slash 分隔，确定性顺序）
}

// SeedUserProfiles 在用户层根（`<home>/.aicli/profiles`）还空着时，把内置 profile
// 物化到该层，返回被播种的条目（按名升序）。
//
// 层里已有内容时返回空切片且不写任何文件。home 不可定位时静默跳过（与
// EnsureUserPresetsFile 同口径：环境缺 home 不该让命令整体失败），因此调用方只在
// len(seeded) > 0 时提示。
func SeedUserProfiles() ([]BuiltinProfile, error) {
	root, err := LayerRoot("user")
	if err != nil || strings.TrimSpace(root) == "" {
		return nil, nil
	}
	return seedBuiltinProfilesAt(root)
}

// seedBuiltinProfilesAt 是播种的单一实现：层根由调用方给出，便于测试与将来复用
// 到其它层（服务端会话工作区的 project 层目前**不**播种：内置内容进用户层一次即可）。
func seedBuiltinProfilesAt(root string) ([]BuiltinProfile, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("内置 profile 播种需要层根目录")
	}
	if layerHasProfiles(root) {
		return nil, nil
	}

	names := BuiltinProfileNames()
	// 两阶段：先把全部模板渲染进内存，再落盘。渲染期失败（模板损坏、名字非法）
	// 不会留下半个 profile 集；写盘期失败则回滚本次创建的目录（见下）。
	rendered := make([]BuiltinProfile, 0, len(names))
	contents := make(map[string]map[string][]byte, len(names))
	for _, name := range names {
		files, err := RenderTemplate(name, name, TemplateDefaultAgent)
		if err != nil {
			return nil, fmt.Errorf("渲染内置 profile %s: %w", name, err)
		}
		relPaths := make([]string, 0, len(files))
		for rel := range files {
			relPaths = append(relPaths, rel)
		}
		sort.Strings(relPaths)
		rendered = append(rendered, BuiltinProfile{
			Name:  name,
			Root:  filepath.Join(root, name),
			Files: relPaths,
		})
		contents[name] = files
	}

	written := make([]string, 0, len(rendered))
	for _, entry := range rendered {
		if err := writeRenderedProfileFiles(entry.Root, contents[entry.Name], entry.Files); err != nil {
			rollbackSeededProfiles(written)
			return nil, fmt.Errorf("落盘内置 profile %s: %w", entry.Name, err)
		}
		written = append(written, entry.Root)
	}
	return rendered, nil
}

// rollbackSeededProfiles 尽力删除本次播种创建的目录。
//
// 只在"调用前层根为空"的前提下被调用（见 seedBuiltinProfilesAt），因此删除的对象
// 必然是本次新建的目录，不会碰到用户内容。失败不掩盖原始错误（返回值只用于日志）。
func rollbackSeededProfiles(roots []string) {
	for _, root := range roots {
		_ = os.RemoveAll(root)
	}
}

// writeRenderedProfileFiles 与 CLI `profile create` 的落盘纪律保持一致
// （MkdirAll 0755 + 文件 0644），使"内置播种出来的 profile"与"手工 create 出来的
// profile"在权限与目录布局上无差异——否则两者会在同一层里表现出不同的可写性。
func writeRenderedProfileFiles(root string, files map[string][]byte, relPaths []string) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("创建 profile 目录 %s: %w", root, err)
	}
	for _, rel := range relPaths {
		target := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("创建 %s 的目录: %w", rel, err)
		}
		if err := os.WriteFile(target, files[rel], 0o644); err != nil {
			return fmt.Errorf("写入 %s: %w", rel, err)
		}
	}
	return nil
}

// layerHasProfiles 判断层根下是否已存在任何 profile。
//
// 判据与 LayerProfiles / CLI `profile list` 完全相同（只认含 profile.yaml 的目录）：
// 发现口径必须只有一处，否则会出现"播种看见有、列表却说没有"这类分叉。
func layerHasProfiles(base string) bool {
	entries, err := os.ReadDir(base)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if hasProfileFile(filepath.Join(base, entry.Name())) {
			return true
		}
	}
	return false
}
