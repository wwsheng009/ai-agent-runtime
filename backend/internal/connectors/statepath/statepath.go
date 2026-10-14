// Package statepath 定义 connectors 共享状态文件的唯一路径解析（docs/bots/
// 09-multi-instance-binding.md D1）。
//
// 背景：runtime-server 与 aicli 此前各自按"configPath 所在目录"推导 state.json，
// 分层合并下两端解析到不同分层（项目层 vs 用户层），导致两份 state 并存、
// 绑定/归属互不可见（2026-10-13 实测事故）。
//
// 定案：state 路径锚定**用户级** `~/.aicli/data/connectors/state.json`，由本包
// 唯一函数给出；两宿主禁止任何 configPath 相对推导。唯一显式覆盖口是环境变量
// AICLI_CONNECTORS_STATE（测试/容器挂载用）。
package statepath

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// EnvOverride 是显式覆盖 state 路径的环境变量（仅测试/容器挂载场景使用）。
const EnvOverride = "AICLI_CONNECTORS_STATE"

// FileName 是共享状态文件名（与既有 state.json 兼容，迁移零转换）。
const FileName = "state.json"

// CanonicalStatePath 返回 connectors 共享状态的唯一路径：
//
//	$AICLI_CONNECTORS_STATE                    （显式覆盖，优先）
//	<用户主目录>/.aicli/data/connectors/state.json
//
// 用户主目录取 os.UserHomeDir()（与 aicli 分层栈兜底一致）。两宿主必须经由
// 本函数取路径；禁止任何 configPath 相对推导。
func CanonicalStatePath() (string, error) {
	if override := strings.TrimSpace(os.Getenv(EnvOverride)); override != "" {
		return filepath.Clean(override), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", errors.Join(errors.New("connectors: resolve user home for state path"), err)
	}
	return filepath.Join(home, ".aicli", "data", "connectors", FileName), nil
}

// LegacyCandidate 是历史版本可能写过的 state 路径（迁移扫描用）。
type LegacyCandidate struct {
	// Path 是候选 state 文件路径。
	Path string
	// Source 描述候选来源（迁移日志用）。
	Source string
}

// LegacyCandidates 基于给定的历史 configPath 枚举旧推导结果（去重、去规范位）。
// 候选**只读不写**，仅用于 storejson 打开时的一次性并集迁移。
func LegacyCandidates(configPaths ...string) []LegacyCandidate {
	seen := map[string]struct{}{}
	out := make([]LegacyCandidate, 0, len(configPaths)+1)

	add := func(path, source string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		path = filepath.Clean(path)
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		out = append(out, LegacyCandidate{Path: path, Source: source})
	}

	// 项目级 .aicli（旧 runtime-server 在 backend/ cwd 下的实际落点）。
	if cwd, err := os.Getwd(); err == nil {
		add(filepath.Join(cwd, ".aicli", "data", "connectors", FileName), "cwd .aicli")
	}

	for _, configPath := range configPaths {
		dir := filepath.Dir(strings.TrimSpace(configPath))
		if dir == "" || dir == "." {
			continue
		}
		add(filepath.Join(dir, "data", "connectors", FileName), "configPath sibling")
	}

	// 规范路径不算候选（自己迁自己没有意义）。
	if canonical, err := CanonicalStatePath(); err == nil {
		canonical = filepath.Clean(canonical)
		filtered := out[:0]
		for _, candidate := range out {
			if candidate.Path != canonical {
				filtered = append(filtered, candidate)
			}
		}
		out = filtered
	}
	return out
}
