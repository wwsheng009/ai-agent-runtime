package logger

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestModule_String(t *testing.T) {
	tests := []struct {
		module   Module
		expected string
	}{
		{ModuleGateway, "gateway"},
		{ModuleProxy, "proxy"},
		{ModuleTransformer, "transformer"},
		{ModuleProvider, "provider"},
		{ModuleAuth, "auth"},
		{ModuleRateLimit, "ratelimit"},
		{ModuleTracing, "tracing"},
		{ModuleDatabase, "database"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			assert.Equal(t, tt.expected, string(tt.module))
		})
	}
}

func TestGetModuleInfo(t *testing.T) {
	info := GetModuleInfo(ModuleGateway)
	assert.Equal(t, ModuleGateway, info.Name)
	assert.Equal(t, "网关核心模块", info.Description)
	assert.Equal(t, "core", info.Category)

	unknownInfo := GetModuleInfo(Module("unknown"))
	assert.Equal(t, Module("unknown"), unknownInfo.Name)
	assert.Equal(t, "未知模块", unknownInfo.Description)
}

func TestAllModules(t *testing.T) {
	modules := AllModules()
	assert.NotEmpty(t, modules)
	assert.Contains(t, modules, ModuleGateway)
	assert.Contains(t, modules, ModuleProxy)
	assert.Contains(t, modules, ModuleTransformer)
}

func TestModuleManager_Get(t *testing.T) {
	cfg := &LogConfig{
		Level:  "info",
		Format: "json",
		Output: "stdout",
	}

	err := InitLogger(cfg)
	assert.NoError(t, err)

	mgr := InitModuleManager(cfg)
	assert.NotNil(t, mgr)

	logger := mgr.Get(ModuleGateway)
	assert.NotNil(t, logger)

	sugar := mgr.GetSugar(ModuleGateway)
	assert.NotNil(t, sugar)
}

func TestModuleManager_Get_Cached(t *testing.T) {
	cfg := &LogConfig{
		Level:  "info",
		Format: "json",
		Output: "stdout",
	}

	_ = InitLogger(cfg)
	mgr := GetModuleManager()

	logger1 := mgr.Get(ModuleProxy)
	logger2 := mgr.Get(ModuleProxy)

	assert.Equal(t, logger1, logger2, "should return cached logger")
}

func TestModuleManager_SetModuleLevel(t *testing.T) {
	cfg := &LogConfig{
		Level:  "info",
		Format: "json",
		Output: "stdout",
	}

	_ = InitLogger(cfg)
	mgr := GetModuleManager()

	err := mgr.SetModuleLevel(ModuleTransformer, "debug")
	assert.NoError(t, err)

	moduleCfg, ok := mgr.GetModuleConfig(ModuleTransformer)
	assert.True(t, ok)
	assert.Equal(t, "debug", moduleCfg.Level)
}

func TestModuleManager_SetModuleLevel_Invalid(t *testing.T) {
	cfg := &LogConfig{
		Level:  "info",
		Format: "json",
		Output: "stdout",
	}

	_ = InitLogger(cfg)
	mgr := GetModuleManager()

	err := mgr.SetModuleLevel(ModuleGateway, "invalid")
	assert.Error(t, err)
}

func TestModuleManager_RegisterModule(t *testing.T) {
	cfg := &LogConfig{
		Level:  "info",
		Format: "json",
		Output: "stdout",
	}

	_ = InitLogger(cfg)
	mgr := GetModuleManager()

	moduleCfg := &ModuleLogConfig{
		Level: "debug",
	}

	logger := mgr.RegisterModule(ModuleAuth, moduleCfg)
	assert.NotNil(t, logger)

	storedCfg, ok := mgr.GetModuleConfig(ModuleAuth)
	assert.True(t, ok)
	assert.Equal(t, "debug", storedCfg.Level)
}

func TestModuleManager_ListModules(t *testing.T) {
	cfg := &LogConfig{
		Level:  "info",
		Format: "json",
		Output: "stdout",
	}

	_ = InitLogger(cfg)
	mgr := GetModuleManager()

	_ = mgr.Get(ModuleGateway)
	_ = mgr.Get(ModuleProxy)
	_ = mgr.Get(ModuleTransformer)

	modules := mgr.ListModules()
	assert.GreaterOrEqual(t, len(modules), 3)
}

func TestM_Shortcut(t *testing.T) {
	cfg := &LogConfig{
		Level:  "info",
		Format: "json",
		Output: "stdout",
	}

	_ = InitLogger(cfg)

	logger := M(ModuleGateway)
	assert.NotNil(t, logger)
}

func TestMS_Shortcut(t *testing.T) {
	cfg := &LogConfig{
		Level:  "info",
		Format: "json",
		Output: "stdout",
	}

	_ = InitLogger(cfg)

	sugar := MS(ModuleGateway)
	assert.NotNil(t, sugar)
}

func TestModuleLoggerHelpers(t *testing.T) {
	cfg := &LogConfig{
		Level:  "info",
		Format: "json",
		Output: "stdout",
	}

	_ = InitLogger(cfg)

	assert.NotNil(t, Gateway())
	assert.NotNil(t, Proxy())
	assert.NotNil(t, Transformer())
	assert.NotNil(t, Provider())
	assert.NotNil(t, Auth())
	assert.NotNil(t, RateLimit())
	assert.NotNil(t, Tracing())
	assert.NotNil(t, Database())
	assert.NotNil(t, ConfigModule())
	assert.NotNil(t, Router())
	assert.NotNil(t, Middleware())
	assert.NotNil(t, Handler())
	assert.NotNil(t, Admin())
	assert.NotNil(t, MCP())
	assert.NotNil(t, Vendor())
}

func TestModuleManager_WithCustomModuleConfig(t *testing.T) {
	// 重置全局状态
	once = sync.Once{}
	moduleManager = nil
	globalLogger = nil
	globalSugar = nil

	cfg := &LogConfig{
		Level:  "info",
		Format: "json",
		Output: "stdout",
		Modules: map[string]ModuleLogConfig{
			"transformer": {Level: "warn"}, // 使用更高的级别，因为 IncreaseLevel 只能提高
			"proxy":       {Level: "error"},
		},
	}

	_ = InitLogger(cfg)
	mgr := GetModuleManager()

	transformerCfg, ok := mgr.GetModuleConfig(ModuleTransformer)
	assert.True(t, ok)
	assert.Equal(t, "warn", transformerCfg.Level)

	proxyCfg, ok := mgr.GetModuleConfig(ModuleProxy)
	assert.True(t, ok)
	assert.Equal(t, "error", proxyCfg.Level)
}

func TestModuleLogger_Logging(t *testing.T) {
	cfg := &LogConfig{
		Level:  "debug",
		Format: "json",
		Output: "stdout",
	}

	_ = InitLogger(cfg)

	assert.NotPanics(t, func() {
		Gateway().Info("gateway message")
		Proxy().Debug("proxy debug message")
		Transformer().Warn("transformer warning")
		Provider().Error("provider error")
	})
}

func TestModuleLogger_WithFields(t *testing.T) {
	cfg := &LogConfig{
		Level:  "debug",
		Format: "json",
		Output: "stdout",
	}

	_ = InitLogger(cfg)

	assert.NotPanics(t, func() {
		Gateway().Info("request received",
			String("method", "POST"),
			String("path", "/v1/chat"),
		)

		Transformer().Debug("transforming request",
			String("source", "openai"),
			String("target", "anthropic"),
		)

		Provider().Warn("provider slow response",
			String("provider", "openai"),
			Int("latency_ms", 5000),
		)
	})
}

func TestSyncAll(t *testing.T) {
	cfg := &LogConfig{
		Level:  "info",
		Format: "json",
		Output: "stdout",
	}

	_ = InitLogger(cfg)
	mgr := GetModuleManager()

	_ = mgr.Get(ModuleGateway)
	_ = mgr.Get(ModuleProxy)

	err := mgr.SyncAll()
	assert.NoError(t, err)
}
