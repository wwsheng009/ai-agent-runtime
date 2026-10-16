package profile

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// EnsureBuiltinProfiles 是「内置 profile 缺项补齐」的显式入口，与 SeedUserProfiles
// 的"层空才播"一次性纪律互补：
//
//   - SeedUserProfiles：首次初始化场景，层根为空时整套物化（不补齐、不复活已删除项）；
//   - EnsureBuiltinProfiles：版本演进场景——二进制新增内置模板（如 full）后，把
//     **缺失的名字**物化到层根。已有内容（含用户改过的内置名、用户自建名）一律
//     整体不动，因此"用户删除内置 profile 是合法选择"的既有纪律不被破坏：用户
//     删掉它 → 它就从"缺失名单"里消失 → 补齐不会再写它。
//
// force 的作用域刻意收窄：只对"已存在但缺文件"的内置名目录补写缺失文件（例如
// 旧版本落盘的 review 缺新加的 prompt 文件），**绝不改写任何已存在的文件**——
// 同名冲突跳过并在返回值里报告，用户内容永远赢。
//
// root 为空时默认用户层根。返回本次实际写入的目录（去重、排序）。
func EnsureBuiltinProfiles(root string, force bool) ([]BuiltinProfile, []string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		resolved, err := LayerRoot("user")
		if err != nil {
			return nil, nil, err
		}
		root = resolved
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, nil, fmt.Errorf("创建层根目录 %s: %w", root, err)
	}

	names := BuiltinProfileNames()
	written := make([]BuiltinProfile, 0, len(names))
	var skipped []string
	for _, name := range names {
		target := filepath.Join(root, name)
		if _, err := os.Stat(target); err == nil {
			// 已存在：force 只补缺失文件，已有文件零改写。
			if !force {
				skipped = append(skipped, name)
				continue
			}
			files, err := RenderTemplate(name, name, TemplateDefaultAgent)
			if err != nil {
				return written, skipped, fmt.Errorf("渲染内置 profile %s: %w", name, err)
			}
			relPaths := make([]string, 0, len(files))
			for rel := range files {
				relPaths = append(relPaths, rel)
			}
			sort.Strings(relPaths)
			var missing []string
			for _, rel := range relPaths {
				if _, err := os.Stat(filepath.Join(target, filepath.FromSlash(rel))); os.IsNotExist(err) {
					missing = append(missing, rel)
				}
			}
			if len(missing) == 0 {
				skipped = append(skipped, name)
				continue
			}
			missingFiles := make(map[string][]byte, len(missing))
			for _, rel := range missing {
				missingFiles[rel] = files[rel]
			}
			if err := writeRenderedProfileFiles(target, missingFiles, missing); err != nil {
				return written, skipped, fmt.Errorf("补写 %s 缺失文件: %w", name, err)
			}
			written = append(written, BuiltinProfile{Name: name, Root: target, Files: missing})
			continue
		} else if !os.IsNotExist(err) {
			return written, skipped, fmt.Errorf("检查 %s: %w", target, err)
		}
		// 缺失：整套物化（与播种同一落盘纪律）。
		files, err := RenderTemplate(name, name, TemplateDefaultAgent)
		if err != nil {
			return written, skipped, fmt.Errorf("渲染内置 profile %s: %w", name, err)
		}
		relPaths := make([]string, 0, len(files))
		for rel := range files {
			relPaths = append(relPaths, rel)
		}
		sort.Strings(relPaths)
		if err := writeRenderedProfileFiles(target, files, relPaths); err != nil {
			return written, skipped, fmt.Errorf("落盘内置 profile %s: %w", name, err)
		}
		written = append(written, BuiltinProfile{Name: name, Root: target, Files: relPaths})
	}
	sort.Slice(written, func(i, j int) bool { return written[i].Name < written[j].Name })
	sort.Strings(skipped)
	return written, skipped, nil
}
