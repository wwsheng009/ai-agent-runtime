package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
)

// ErrNoConfigFiles 表示发现链上没有任何可用的配置文件。
// 调用方据此保留各自的「MCP 未配置」语义，而不是把空配置当成有效配置。
var ErrNoConfigFiles = errors.New("未找到任何 MCP 配置文件")

// SourceFile 描述一个候选配置文件及其来源层级。
//
// 约定：切片按「优先级从低到高」排列——低优先级先加载提供基础项，
// 高优先级后加载并整体覆盖同名 server（§4.5 Step 1 的同名覆盖语义）。
type SourceFile struct {
	Path   string
	Source string // project | user | local | upward | executable | default | explicit
	Exists bool
}

// SourceRef 记录 server 的某个来源文件。
type SourceRef struct {
	Path   string `json:"path" yaml:"path"`
	Source string `json:"source" yaml:"source"`
}

// ServerOrigin 记录某个 server 的胜出来源与被遮蔽（shadowed）的低优先级来源。
type ServerOrigin struct {
	Name   string
	Source string
	Path   string
	// Shadowed 按「低→高」列出被覆盖的同名定义，用于在 list/status 中显式提示，
	// 避免「用户级改了却不生效」这类静默冲突。
	Shadowed []SourceRef
}

// LayeredResult 分层加载结果。
type LayeredResult struct {
	Config  *Config
	Primary SourceFile // 生效的 global 配置来源（最高优先级的已存在文件）
	Files   []SourceFile
	Origins map[string]ServerOrigin
	// Warnings 收集非致命问题（如低优先级文件损坏被跳过），供 CLI/日志展示。
	Warnings []string
}

// Origin 返回某个 server 的来源信息。
func (r *LayeredResult) Origin(name string) (ServerOrigin, bool) {
	if r == nil || r.Origins == nil {
		return ServerOrigin{}, false
	}
	origin, ok := r.Origins[name]
	return origin, ok
}

// LoadEffective 按发现链加载并合并配置。
//
// explicitPath 为显式覆盖（`--config-file` / `aicli.mcp.config_file`）；非空且不是
// 约定路径时退化为「精确加载该文件」，与既有脚本语义保持一致。
func LoadEffective(explicitPath string) (*LayeredResult, error) {
	return LoadLayered(DiscoverSources(explicitPath))
}

// DiscoverSources 把发现链解析为「低→高」的候选来源列表（含存在性）。
func DiscoverSources(explicitPath string) []SourceFile {
	resolution := aiclipaths.ResolveMCPConfigPathDetailed(explicitPath)
	if resolution.Source == "explicit" {
		// 显式覆盖：只认这一个文件，不参与层级合并。
		return []SourceFile{{Path: resolution.Path, Source: "explicit", Exists: fileExists(resolution.Path)}}
	}
	candidates := resolution.Candidates
	sources := make([]SourceFile, 0, len(candidates))
	// resolution.Candidates 为「高→低」，反转后得到「低→高」的加载顺序。
	for index := len(candidates) - 1; index >= 0; index-- {
		candidate := candidates[index]
		if candidate.Source == "explicit" {
			continue
		}
		sources = append(sources, SourceFile{
			Path:   candidate.Path,
			Source: candidate.Source,
			Exists: candidate.Exists,
		})
	}
	return sources
}

// LoadLayered 加载候选文件并执行「同名覆盖」合并。
//
// 规则：
//   - 不存在的候选直接跳过（发现链的正常状态）；
//   - 显式覆盖文件缺失、或最高优先级（primary）文件解析/校验失败 → 返回错误；
//   - 低优先级文件解析失败 → 跳过并记入 Warnings，不让陈旧的用户级文件拖垮整次启动；
//   - global 段取 primary 文件；每个 server 整体取自其胜出的那一层，不做字段级合并，
//     保证「哪一层写的就是哪一层的语义」可预测。
func LoadLayered(files []SourceFile) (*LayeredResult, error) {
	normalized := make([]SourceFile, 0, len(files))
	primaryIndex := -1
	for _, file := range files {
		path := strings.TrimSpace(file.Path)
		if path == "" {
			continue
		}
		file.Path = path
		if !file.Exists {
			file.Exists = fileExists(path)
		}
		normalized = append(normalized, file)
		if file.Exists {
			primaryIndex = len(normalized) - 1
		}
	}
	if primaryIndex < 0 {
		// 显式指定的文件缺失要给出行之有效的错误，而不是笼统的「未配置」。
		for _, file := range normalized {
			if file.Source == "explicit" {
				return nil, fmt.Errorf("读取配置文件失败 %s: %w", file.Path, os.ErrNotExist)
			}
		}
		return nil, ErrNoConfigFiles
	}

	type loadedFile struct {
		source SourceFile
		config *Config
	}
	loaded := make([]loadedFile, 0, len(normalized))
	warnings := make([]string, 0)
	for index, file := range normalized {
		if !file.Exists {
			continue
		}
		cfg, err := loadConfigFile(file.Path)
		if err != nil {
			if index == primaryIndex {
				return nil, err
			}
			warnings = append(warnings, fmt.Sprintf("已跳过 %s（%s）: %v", file.Path, file.Source, err))
			continue
		}
		loaded = append(loaded, loadedFile{source: file, config: cfg})
	}
	if len(loaded) == 0 {
		return nil, ErrNoConfigFiles
	}

	// loaded 已按「低→高」排列，最后一个即为生效的 primary。
	primary := loaded[len(loaded)-1].source
	merged := &Config{
		MCPServers: make(map[string]MCPConfig),
		Global:     loaded[len(loaded)-1].config.Global,
	}
	origins := make(map[string]ServerOrigin)
	for _, entry := range loaded {
		for name, server := range entry.config.MCPServers {
			if previous, exists := origins[name]; exists {
				previous.Shadowed = append(previous.Shadowed, SourceRef{Path: previous.Path, Source: previous.Source})
				previous.Path = entry.source.Path
				previous.Source = entry.source.Source
				origins[name] = previous
			} else {
				origins[name] = ServerOrigin{Name: name, Source: entry.source.Source, Path: entry.source.Path}
			}
			merged.MCPServers[name] = server
		}
	}
	ApplyDefaults(merged)
	if err := ValidateConfig(merged); err != nil {
		return nil, err
	}
	return &LayeredResult{
		Config:   merged,
		Primary:  primary,
		Files:    normalized,
		Origins:  origins,
		Warnings: warnings,
	}, nil
}

// loadConfigFile 读取并解析单个配置文件（与 Loader.Load 共用同一套语义）。
func loadConfigFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败 %s: %w", path, err)
	}
	cfg, err := unmarshalConfig(data, path)
	if err != nil {
		return nil, err
	}
	ApplyDefaults(cfg)
	if err := ValidateConfig(cfg); err != nil {
		return nil, fmt.Errorf("配置验证失败 (%s): %w", path, err)
	}
	return cfg, nil
}

func fileExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
