package config

// CloneWithDefaults 深拷贝配置并补齐默认值。
//
// 供「内存配置」入口（例如 ACP 会话级 MCP manager）复用文件加载的默认值语义：
// 调用方传入的 map 与切片不会被后续 Start/Reload 修改，避免调用方持有的
// 快照被 manager 内部状态污染。
func CloneWithDefaults(cfg *Config) *Config {
	if cfg == nil {
		return nil
	}
	out := &Config{Global: cfg.Global}
	if cfg.MCPServers != nil {
		out.MCPServers = make(map[string]MCPConfig, len(cfg.MCPServers))
		for name, srv := range cfg.MCPServers {
			out.MCPServers[name] = cloneMCPConfig(srv)
		}
	}
	ApplyDefaults(out)
	return out
}

func cloneMCPConfig(src MCPConfig) MCPConfig {
	out := src
	out.Args = append([]string(nil), src.Args...)
	if src.Env != nil {
		out.Env = make(map[string]string, len(src.Env))
		for k, v := range src.Env {
			out.Env[k] = v
		}
	}
	if src.Headers != nil {
		out.Headers = make(map[string]string, len(src.Headers))
		for k, v := range src.Headers {
			out.Headers[k] = v
		}
	}
	if src.Tools != nil {
		out.Tools = make(map[string]MCPToolConfig, len(src.Tools))
		for name, tool := range src.Tools {
			cloned := tool
			if tool.Enabled != nil {
				enabled := *tool.Enabled
				cloned.Enabled = &enabled
			}
			out.Tools[name] = cloned
		}
	}
	if src.HealthCheck != nil {
		health := *src.HealthCheck
		health.Tools = append([]string(nil), src.HealthCheck.Tools...)
		health.Resources = append([]string(nil), src.HealthCheck.Resources...)
		if src.HealthCheck.ToolArgs != nil {
			health.ToolArgs = make(map[string]map[string]interface{}, len(src.HealthCheck.ToolArgs))
			for tool, args := range src.HealthCheck.ToolArgs {
				clonedArgs := make(map[string]interface{}, len(args))
				for k, v := range args {
					clonedArgs[k] = v
				}
				health.ToolArgs[tool] = clonedArgs
			}
		}
		out.HealthCheck = &health
	}
	return out
}
