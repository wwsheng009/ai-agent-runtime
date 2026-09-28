package agentconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
	runtimelsp "github.com/wwsheng009/ai-agent-runtime/internal/lsp"
)

// 工作区层 runtime.yaml 的自动写入（项目类型扫描 → LSP 启用）。
//
// 约定：runtime.yaml 的工作区层是 <workspace>/.aicli/runtime.yaml（与
// aiclipaths.ResolveRuntimeConfigBootstrapPath 的项目层一致）。本文件只负责
// 「把检测结果持久化进该层」，不做任何检测；检测在 internal/projectscan。
//
// 两条硬约束：
//  1. 显式决定优先：目标文件已显式声明 lsp.enabled（无论 true/false）时一律
//     不修改——自动检测只补「没配置过」的场景，绝不覆盖用户的选择；
//  2. 最小改写：只在键缺失时补齐 lsp.enabled / lsp.servers，文件其余内容
//     （其它键、注释、格式）保持原样，写入走配置写事务（文件锁 + 原子落盘）。

// errWorkspaceRuntimeLSPExplicit 表示工作区层已经显式配置了 lsp.enabled。
// 调用方按「未修改」处理，不视为错误。
var errWorkspaceRuntimeLSPExplicit = errors.New("workspace runtime config already sets lsp.enabled")

// WorkspaceRuntimeConfigPath 返回工作区层 runtime.yaml 的约定路径
// （<workspaceRoot>/.aicli/runtime.yaml）；workspaceRoot 为空时返回空串。
func WorkspaceRuntimeConfigPath(workspaceRoot string) string {
	root := strings.TrimSpace(workspaceRoot)
	if root == "" {
		return ""
	}
	return filepath.Join(root, ".aicli", aiclipaths.DefaultRuntimeConfigFileName)
}

// RuntimeConfigExplicitKey 报告 path 指向的 runtime.yaml 是否显式声明了嵌套键
// （keys 为从根到叶的键路径，例如 "lsp", "enabled"）。文件不存在、无法解析或
// 键缺失都返回 false——调用方据此区分「未配置」与「显式关闭」。
func RuntimeConfigExplicitKey(path string, keys ...string) bool {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" || len(keys) == 0 {
		return false
	}
	raw, err := os.ReadFile(trimmed)
	if err != nil {
		return false
	}
	var document map[string]interface{}
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return false
	}
	var current interface{} = document
	for _, key := range keys {
		mapping, ok := current.(map[string]interface{})
		if !ok {
			return false
		}
		value, exists := mapping[key]
		if !exists {
			return false
		}
		current = value
	}
	return true
}

// RuntimeConfigExplicitKeyInLayers 报告 runtime.yaml 层栈中任一「存在的层」是否
// 显式声明了嵌套键（keys 为从根到叶的键路径，例如 "lsp", "enabled"）。
//
// 自动检测据此尊重任意层的显式决定：只有所有层都未配置该键时才补检测结果，
// 避免「工作区层覆盖用户层的显式关闭」这类越权。
func RuntimeConfigExplicitKeyInLayers(keys ...string) bool {
	if len(keys) == 0 {
		return false
	}
	for _, layer := range RuntimeConfigLayerStack() {
		if !layer.Present {
			continue
		}
		if RuntimeConfigExplicitKey(layer.Path, keys...) {
			return true
		}
	}
	return false
}

// EnsureWorkspaceRuntimeLSP 把项目扫描检测到的语言服务器持久化到工作区层
// <workspaceRoot>/.aicli/runtime.yaml：
//
//   - 文件不存在时创建，写入 lsp.enabled: true 与检测到的 lsp.servers；
//   - 文件已存在时保留其余键与注释，仅补齐缺失的键；
//   - 已显式声明 lsp.enabled 时不做任何修改（显式决定优先）；
//   - servers 为空时不做任何事。
//
// 返回目标文件路径与是否发生修改。
func EnsureWorkspaceRuntimeLSP(workspaceRoot string, servers []runtimelsp.ServerSpec) (string, bool, error) {
	path := WorkspaceRuntimeConfigPath(workspaceRoot)
	if path == "" || len(servers) == 0 {
		return path, false, nil
	}
	err := updateWorkspaceRuntimeDocument(path, func(root *yaml.Node) error {
		lspNode := currentMappingValue(root, "lsp")
		switch {
		case lspNode == nil:
			lspNode = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			upsertYAMLMappingValue(root, "lsp", lspNode)
		case lspNode.Kind != yaml.MappingNode:
			// lsp 段不是映射（例如 lsp: false / lsp: null）：不猜测语义，保持原样。
			return errWorkspaceRuntimeLSPExplicit
		}
		if currentMappingValue(lspNode, "enabled") != nil {
			return errWorkspaceRuntimeLSPExplicit
		}
		upsertYAMLMappingValue(lspNode, "enabled", boolYAMLNode(true))
		if currentMappingValue(lspNode, "servers") == nil {
			serversNode, err := marshalYAMLNode(servers)
			if err != nil {
				return err
			}
			upsertYAMLMappingValue(lspNode, "servers", serversNode)
		}
		return nil
	})
	if errors.Is(err, errWorkspaceRuntimeLSPExplicit) {
		return path, false, nil
	}
	if err != nil {
		return path, false, err
	}
	return path, true, nil
}

// updateWorkspaceRuntimeDocument 是工作区层 runtime.yaml 的写事务：文件锁内
// 「读 → 解析 → mutate → 编码 → 原子写」，文件缺失时从空 mapping 起步创建。
// 与 updateConfigFileDocument 的差别只有一处：缺失文件不是错误，而是新建。
func updateWorkspaceRuntimeDocument(path string, mutate func(root *yaml.Node) error) error {
	unlock := LockConfigFileWrite(path)
	defer unlock()

	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
	case os.IsNotExist(err):
		raw = nil
	default:
		return fmt.Errorf("read config file %s: %w", path, err)
	}
	document, err := parseYAMLDocument(raw)
	if err != nil {
		return err
	}
	root, err := ensureYAMLRootMapping(document)
	if err != nil {
		return err
	}
	if err := mutate(root); err != nil {
		return err
	}
	out, err := encodeYAMLDocument(document)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, out)
}
