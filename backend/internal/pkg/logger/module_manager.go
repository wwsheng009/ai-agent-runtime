package logger

import (
	"fmt"
	"sync"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// ModuleManager 模块日志管理器
// 管理各个模块的独立 logger 实例
type ModuleManager struct {
	mu      sync.RWMutex
	loggers map[Module]*zap.Logger
	configs map[string]ModuleLogConfig
	baseCfg *LogConfig
}

var (
	moduleManager *ModuleManager
	once          sync.Once
)

// InitModuleManager 初始化模块日志管理器
func InitModuleManager(cfg *LogConfig) *ModuleManager {
	once.Do(func() {
		moduleManager = &ModuleManager{
			loggers: make(map[Module]*zap.Logger),
			configs: make(map[string]ModuleLogConfig),
			baseCfg: &LogConfig{},
		}
	})
	moduleManager.Reload(cfg)
	return moduleManager
}

// GetModuleManager 获取模块日志管理器实例
func GetModuleManager() *ModuleManager {
	if moduleManager == nil {
		moduleManager = &ModuleManager{
			loggers: make(map[Module]*zap.Logger),
			configs: make(map[string]ModuleLogConfig),
			baseCfg: &LogConfig{},
		}
	}
	return moduleManager
}

// Reload 用最新配置刷新模块日志管理器的基线配置和模块级覆盖。
func (m *ModuleManager) Reload(cfg *LogConfig) {
	if m == nil {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	baseCfg := &LogConfig{}
	if cfg != nil {
		cloned := *cfg
		baseCfg = &cloned
	}
	m.baseCfg = baseCfg

	m.configs = make(map[string]ModuleLogConfig)
	if cfg != nil && cfg.Modules != nil {
		for k, v := range cfg.Modules {
			m.configs[k] = v
		}
	}

	for module, existing := range m.loggers {
		newLogger := m.createModuleLogger(module)
		m.loggers[module] = newLogger
		if existing != nil {
			_ = existing.Sync()
		}
	}
}

// Get 获取指定模块的 logger
// 如果模块没有独立配置，返回全局 logger 的命名副本
func (m *ModuleManager) Get(module Module) *zap.Logger {
	m.mu.RLock()
	if logger, ok := m.loggers[module]; ok {
		m.mu.RUnlock()
		return logger
	}
	m.mu.RUnlock()

	m.mu.Lock()
	defer m.mu.Unlock()

	if logger, ok := m.loggers[module]; ok {
		return logger
	}

	logger := m.createModuleLogger(module)
	m.loggers[module] = logger
	return logger
}

// GetSugar 获取指定模块的 SugaredLogger
func (m *ModuleManager) GetSugar(module Module) *zap.SugaredLogger {
	return m.Get(module).Sugar()
}

// createModuleLogger 创建模块专用 logger
// 注意：zap 的 IncreaseLevel 只能提高级别（如 info -> warn），不能降低（如 info -> debug）
// 如果需要更低的级别，应该设置全局级别为 debug，然后各模块提高级别
func (m *ModuleManager) createModuleLogger(module Module) *zap.Logger {
	moduleCfg, hasCustomCfg := m.configs[string(module)]

	// 添加 CallerSkip 以跳过包装函数，使调用者位置正确
	opts := []zap.Option{zap.AddCallerSkip(1)}

	// 没有自定义配置，使用全局 logger 的命名副本
	if !hasCustomCfg || moduleCfg.Level == "" {
		return L().Named(string(module)).WithOptions(opts...)
	}

	level := parseLogLevel(moduleCfg.Level)
	baseLevel := parseLogLevel(m.baseCfg.Level)

	// 如果级别相同，直接返回命名 logger
	if level == baseLevel {
		return L().Named(string(module)).WithOptions(opts...)
	}

	// IncreaseLevel 只能提高级别，不能降低
	// 如果尝试降低级别，忽略并使用全局 logger
	if level < baseLevel {
		// debug < info < warn < error
		// 如果模块配置的级别比全局低，记录警告并使用全局级别
		return L().Named(string(module)).WithOptions(opts...)
	}

	// 级别更高时（如 info -> warn），可以成功应用
	return L().Named(string(module)).WithOptions(append(opts, zap.IncreaseLevel(level))...)
}

// SetModuleLevel 设置模块日志级别
func (m *ModuleManager) SetModuleLevel(module Module, level string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	zapLevel := parseLogLevel(level)
	if zapLevel == zapcore.InfoLevel && level != "info" {
		return fmt.Errorf("invalid log level: %s", level)
	}

	m.configs[string(module)] = ModuleLogConfig{Level: level}

	if logger, ok := m.loggers[module]; ok {
		newLogger := L().Named(string(module))
		m.loggers[module] = newLogger
		_ = logger.Sync()
	}

	return nil
}

// RegisterModule 注册模块并创建专用 logger
func (m *ModuleManager) RegisterModule(module Module, cfg *ModuleLogConfig) *zap.Logger {
	m.mu.Lock()
	defer m.mu.Unlock()

	if cfg != nil {
		m.configs[string(module)] = *cfg
	}

	logger := m.createModuleLogger(module)
	m.loggers[module] = logger
	return logger
}

// ListModules 列出所有已注册的模块
func (m *ModuleManager) ListModules() []Module {
	m.mu.RLock()
	defer m.mu.RUnlock()

	modules := make([]Module, 0, len(m.loggers))
	for module := range m.loggers {
		modules = append(modules, module)
	}
	return modules
}

// GetModuleConfig 获取模块配置
func (m *ModuleManager) GetModuleConfig(module Module) (ModuleLogConfig, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	cfg, ok := m.configs[string(module)]
	return cfg, ok
}

// SyncAll 同步所有模块 logger
func (m *ModuleManager) SyncAll() error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var lastErr error
	for _, logger := range m.loggers {
		if err := logger.Sync(); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

// M 是 GetModuleManager().Get() 的快捷方式
func M(module Module) *zap.Logger {
	return L().Named(string(module)).WithOptions(zap.AddCallerSkip(1))
}

// MS 是 GetModuleManager().GetSugar() 的快捷方式
func MS(module Module) *zap.SugaredLogger {
	return L().Named(string(module)).WithOptions(zap.AddCallerSkip(1)).Sugar()
}

// 预定义的模块 logger 快捷方法
// 返回 UnifiedLogger，内部使用全局 logger 穿透功能

// Gateway 返回网关模块 logger
func Gateway() UnifiedLogger { return newUnifiedLogger(string(ModuleGateway)) }

// Proxy 返回代理模块 logger
func Proxy() UnifiedLogger { return newUnifiedLogger(string(ModuleProxy)) }

// Transformer 返回转换模块 logger
func Transformer() UnifiedLogger { return newUnifiedLogger(string(ModuleTransformer)) }

// Provider 返回供应商模块 logger
func Provider() UnifiedLogger { return newUnifiedLogger(string(ModuleProvider)) }

// Auth 返回认证模块 logger
func Auth() UnifiedLogger { return newUnifiedLogger(string(ModuleAuth)) }

// RateLimit 返回限流模块 logger
func RateLimit() UnifiedLogger { return newUnifiedLogger(string(ModuleRateLimit)) }

// Tracing 返回追踪模块 logger
func Tracing() UnifiedLogger { return newUnifiedLogger(string(ModuleTracing)) }

// Database 返回数据库模块 logger
func Database() UnifiedLogger { return newUnifiedLogger(string(ModuleDatabase)) }

// ConfigModule 返回配置模块 logger
func ConfigModule() UnifiedLogger { return newUnifiedLogger(string(ModuleConfig)) }

// Router 返回路由模块 logger
func Router() UnifiedLogger { return newUnifiedLogger(string(ModuleRouter)) }

// Middleware 返回中间件模块 logger
func Middleware() UnifiedLogger { return newUnifiedLogger(string(ModuleMiddleware)) }

// Handler 返回处理器模块 logger
func Handler() UnifiedLogger { return newUnifiedLogger(string(ModuleHandler)) }

// Admin 返回管理模块 logger
func Admin() UnifiedLogger { return newUnifiedLogger(string(ModuleAdmin)) }

// MCP 返回 MCP 模块 logger
func MCP() UnifiedLogger { return newUnifiedLogger(string(ModuleMCP)) }

// Vendor 返回供应商适配模块 logger
func Vendor() UnifiedLogger { return newUnifiedLogger(string(ModuleVendor)) }

// Metrics 返回指标收集模块 logger
func Metrics() UnifiedLogger { return newUnifiedLogger(string(ModuleMetrics)) }

// Truncation 返回上下文截断模块 logger
func Truncation() UnifiedLogger { return newUnifiedLogger(string(ModuleTruncation)) }

// Replay 返回请求重放模块 logger
func Replay() UnifiedLogger { return newUnifiedLogger(string(ModuleReplay)) }

// Discovery 返回 API 发现模块 logger
func Discovery() UnifiedLogger { return newUnifiedLogger(string(ModuleDiscovery)) }

// Capability 返回能力追踪模块 logger
func Capability() UnifiedLogger { return newUnifiedLogger(string(ModuleCapability)) }

// TaskQueue 返回任务队列模块 logger
func TaskQueue() UnifiedLogger { return newUnifiedLogger(string(ModuleTaskQueue)) }

// Pprof 返回 pprof 性能分析模块 logger
func Pprof() UnifiedLogger { return newUnifiedLogger(string(ModulePprof)) }

// Pipeline 返回 Pipeline 模块 logger
func Pipeline() UnifiedLogger { return newUnifiedLogger(string(ModulePipeline)) }

// UnifiedLogger 统一的日志接口，内部使用全局 logger 穿透功能
type UnifiedLogger interface {
	Debug(msg string, fields ...zapcore.Field)
	Info(msg string, fields ...zapcore.Field)
	Warn(msg string, fields ...zapcore.Field)
	Error(msg string, fields ...zapcore.Field)
	With(fields ...zapcore.Field) UnifiedLogger
	Named(name string) UnifiedLogger
	Sugar() *zap.SugaredLogger
	Zap() *zap.Logger
}

// unifiedLogger 实现，内部调用全局 logger 穿透函数
type unifiedLogger struct {
	moduleName string
}

// newUnifiedLogger 创建统一 logger
func newUnifiedLogger(moduleName string) UnifiedLogger {
	return &unifiedLogger{moduleName: moduleName}
}

func (l *unifiedLogger) Debug(msg string, fields ...zapcore.Field) {
	newFields := make([]zapcore.Field, len(fields))
	copy(newFields, fields)
	L().Named(l.moduleName).WithOptions(zap.AddCallerSkip(getCallerSkip())).Debug(msg, newFields...)
}

func (l *unifiedLogger) Info(msg string, fields ...zapcore.Field) {
	newFields := make([]zapcore.Field, len(fields))
	copy(newFields, fields)
	L().Named(l.moduleName).WithOptions(zap.AddCallerSkip(getCallerSkip())).Info(msg, newFields...)
}

func (l *unifiedLogger) Warn(msg string, fields ...zapcore.Field) {
	newFields := make([]zapcore.Field, len(fields))
	copy(newFields, fields)
	L().Named(l.moduleName).WithOptions(zap.AddCallerSkip(getCallerSkip())).Warn(msg, newFields...)
}

func (l *unifiedLogger) Error(msg string, fields ...zapcore.Field) {
	newFields := make([]zapcore.Field, len(fields))
	copy(newFields, fields)
	L().Named(l.moduleName).WithOptions(zap.AddCallerSkip(getCallerSkip())).Error(msg, newFields...)
}

func (l *unifiedLogger) With(fields ...zapcore.Field) UnifiedLogger {
	return &unifiedLogger{moduleName: l.moduleName + ":with"}
}

func (l *unifiedLogger) Named(name string) UnifiedLogger {
	return &unifiedLogger{moduleName: l.moduleName + "/" + name}
}

// Sugar 返回 zap.SugaredLogger（用于兼容性）
func (l *unifiedLogger) Sugar() *zap.SugaredLogger {
	return L().Named(l.moduleName).Sugar()
}

// Zap 返回原始的 *zap.Logger（用于需要 *zap.Logger 的场景）
func (l *unifiedLogger) Zap() *zap.Logger {
	return L().Named(l.moduleName)
}
