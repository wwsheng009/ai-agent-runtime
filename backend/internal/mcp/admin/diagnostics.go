package admin

import (
	"os"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
)

// ConfigCandidate 描述一个候选 MCP 配置位置及其当前状态（观测用途）。
type ConfigCandidate struct {
	Path   string `json:"path"`
	Source string `json:"source"`
	Exists bool   `json:"exists"`
}

// ConfigDiagnostics 描述管理服务实际读写的 MCP 配置文件、解析来源与候选清单。
// 文件存在性 / 大小 / 修改时间在每次读取时实时刷新，避免陈旧快照。
type ConfigDiagnostics struct {
	Path          string            `json:"path"`
	Source        string            `json:"source,omitempty"`
	Exists        bool              `json:"exists"`
	SizeBytes     int64             `json:"size_bytes,omitempty"`
	ModTime       string            `json:"mod_time,omitempty"`
	ManagerLoaded bool              `json:"manager_loaded"`
	Candidates    []ConfigCandidate `json:"candidates,omitempty"`
}

// DiagnosticsProvider 由支持配置诊断信息的管理服务实现；调用方可据此做可选增强，
// 不实现也不影响既有行为。
type DiagnosticsProvider interface {
	ConfigDiagnostics() *ConfigDiagnostics
}

// ListSummary 汇总当前 MCP 面（管理页 / 诊断消费）。
type ListSummary struct {
	Total     int `json:"total"`
	Enabled   int `json:"enabled"`
	Disabled  int `json:"disabled"`
	Connected int `json:"connected"`
	Tools     int `json:"tools"`
}

// Summarize 汇总列表项：启用/停用按配置判定，连接数与工具数来自运行时状态。
func Summarize(items []Item) ListSummary {
	summary := ListSummary{Total: len(items)}
	for _, item := range items {
		if item.Config.IsEnabled() {
			summary.Enabled++
		} else {
			summary.Disabled++
		}
		if item.Status != nil {
			if item.Status.Connected {
				summary.Connected++
			}
			summary.Tools += item.Status.ToolCount
		}
	}
	return summary
}

// ConfigDiagnosticsFromResolution 把路径解析结果转换为诊断信息。
func ConfigDiagnosticsFromResolution(resolution aiclipaths.MCPConfigResolution) ConfigDiagnostics {
	diagnostics := ConfigDiagnostics{Path: resolution.Path, Source: resolution.Source}
	for _, candidate := range resolution.Candidates {
		diagnostics.Candidates = append(diagnostics.Candidates, ConfigCandidate{
			Path:   candidate.Path,
			Source: candidate.Source,
			Exists: candidate.Exists,
		})
	}
	return diagnostics
}

// WithConfigDiagnostics 注入解析来源与候选清单；路径始终以 Service 的 configPath 为准。
func WithConfigDiagnostics(diagnostics ConfigDiagnostics) Option {
	return func(s *Service) {
		if s == nil {
			return
		}
		copied := diagnostics
		copied.Candidates = append([]ConfigCandidate(nil), diagnostics.Candidates...)
		s.diagnostics = &copied
	}
}

// ConfigDiagnostics 返回当前配置诊断（文件属性实时刷新）。
func (s *Service) ConfigDiagnostics() *ConfigDiagnostics {
	if s == nil || strings.TrimSpace(s.configPath) == "" {
		return nil
	}
	diagnostics := ConfigDiagnostics{Path: s.configPath}
	if s.diagnostics != nil {
		diagnostics = *s.diagnostics
		diagnostics.Path = s.configPath
		diagnostics.Candidates = append([]ConfigCandidate(nil), s.diagnostics.Candidates...)
	}

	s.mu.Lock()
	diagnostics.ManagerLoaded = s.manager != nil
	s.mu.Unlock()

	if info, err := os.Stat(s.configPath); err == nil && !info.IsDir() {
		diagnostics.Exists = true
		diagnostics.SizeBytes = info.Size()
		diagnostics.ModTime = info.ModTime().UTC().Format(time.RFC3339)
	} else {
		diagnostics.Exists = false
		diagnostics.SizeBytes = 0
		diagnostics.ModTime = ""
	}
	return &diagnostics
}
