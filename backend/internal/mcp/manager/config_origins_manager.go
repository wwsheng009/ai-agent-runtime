//go:build !win7compat

package manager

import (
	"fmt"

	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
)

// LoadConfigEffective 按 MCP 配置发现链分层加载（低优先级提供基础项，高优先级同名整体覆盖）。
//
// explicitPath 为显式覆盖（`--config-file` / `aicli.mcp.config_file`）；非空且不是
// 约定路径时退化为「精确加载该文件」，与既有脚本语义一致。
func (m *manager) LoadConfigEffective(explicitPath string) error {
	if m == nil {
		return fmt.Errorf("manager 为空")
	}
	result, err := config.LoadEffective(explicitPath)
	if err != nil {
		return fmt.Errorf("加载配置失败: %w", err)
	}
	// 运行时入口才展开环境变量，与 LoadConfig 同一约定。
	config.ExpandEnv(result.Config)

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started {
		return fmt.Errorf("管理器已经启动")
	}
	m.cfg = result.Config
	m.loader = nil
	m.layered = true
	m.explicitPath = explicitPath
	m.origins = result.Origins
	m.originWarnings = append([]string(nil), result.Warnings...)
	return nil
}

// MCPConfigOrigins 返回各 server 的配置来源快照（实现 ConfigOriginReporter）。
func (m *manager) MCPConfigOrigins() map[string]config.ServerOrigin {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.origins) == 0 {
		return nil
	}
	out := make(map[string]config.ServerOrigin, len(m.origins))
	for name, origin := range m.origins {
		origin.Shadowed = append([]config.SourceRef(nil), origin.Shadowed...)
		out[name] = origin
	}
	return out
}

// MCPConfigWarnings 返回分层加载的非致命告警（实现 ConfigOriginReporter）。
func (m *manager) MCPConfigWarnings() []string {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]string(nil), m.originWarnings...)
}

// recordSingleFileOrigins 为单文件加载记录来源，保证 list/status 始终能显示配置出处。
// 调用方需持有 m.mu 写锁。
func (m *manager) recordSingleFileOrigins(cfg *config.Config, path string) {
	origins := make(map[string]config.ServerOrigin)
	if cfg != nil {
		for name := range cfg.MCPServers {
			origins[name] = config.ServerOrigin{Name: name, Source: "file", Path: path}
		}
	}
	m.origins = origins
	m.originWarnings = nil
}
