package executor

// CloneSandboxConfig returns a defensive copy of a sandbox config.
func CloneSandboxConfig(cfg SandboxConfig) SandboxConfig {
	return SandboxConfig{
		Enabled:          cfg.Enabled,
		MaxExecutionTime: cfg.MaxExecutionTime,
		AllowedPaths:     cloneStrings(cfg.AllowedPaths),
		DeniedPaths:      cloneStrings(cfg.DeniedPaths),
		ReadOnlyPaths:    cloneStrings(cfg.ReadOnlyPaths),
		AllowedCommands:  cloneStrings(cfg.AllowedCommands),
		DeniedCommands:   cloneStrings(cfg.DeniedCommands),
		EnvWhitelist:     cloneStrings(cfg.EnvWhitelist),
		AllowedHosts:     cloneStrings(cfg.AllowedHosts),
		DeniedHosts:      cloneStrings(cfg.DeniedHosts),
	}
}

// OverlaySandboxConfig overlays high-priority sandbox values onto a base config.
func OverlaySandboxConfig(base *SandboxConfig, override SandboxConfig) {
	if base == nil {
		return
	}
	if override.Enabled || SandboxConfigActive(override) {
		base.Enabled = true
	}
	if override.MaxExecutionTime > 0 {
		base.MaxExecutionTime = override.MaxExecutionTime
	}
	if len(override.AllowedPaths) > 0 {
		base.AllowedPaths = cloneStrings(override.AllowedPaths)
	}
	if len(override.DeniedPaths) > 0 {
		base.DeniedPaths = cloneStrings(override.DeniedPaths)
	}
	if len(override.ReadOnlyPaths) > 0 {
		base.ReadOnlyPaths = cloneStrings(override.ReadOnlyPaths)
	}
	if len(override.AllowedCommands) > 0 {
		base.AllowedCommands = cloneStrings(override.AllowedCommands)
	}
	if len(override.DeniedCommands) > 0 {
		base.DeniedCommands = cloneStrings(override.DeniedCommands)
	}
	if len(override.EnvWhitelist) > 0 {
		base.EnvWhitelist = cloneStrings(override.EnvWhitelist)
	}
	if len(override.AllowedHosts) > 0 {
		base.AllowedHosts = cloneStrings(override.AllowedHosts)
	}
	if len(override.DeniedHosts) > 0 {
		base.DeniedHosts = cloneStrings(override.DeniedHosts)
	}
}

// SandboxConfigActive reports whether a sandbox config contains effective restrictions.
func SandboxConfigActive(cfg SandboxConfig) bool {
	return cfg.Enabled ||
		cfg.MaxExecutionTime > 0 ||
		len(cfg.AllowedPaths) > 0 ||
		len(cfg.DeniedPaths) > 0 ||
		len(cfg.ReadOnlyPaths) > 0 ||
		len(cfg.AllowedCommands) > 0 ||
		len(cfg.DeniedCommands) > 0 ||
		len(cfg.EnvWhitelist) > 0 ||
		len(cfg.AllowedHosts) > 0 ||
		len(cfg.DeniedHosts) > 0
}
