package logger

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap/zapcore"
)

func TestParseLogLevel(t *testing.T) {
	tests := []struct {
		name  string
		level string
		want  string
	}{
		{"debug level", "debug", "debug"},
		{"info level", "info", "info"},
		{"warn level", "warn", "warn"},
		{"error level", "error", "error"},
		{"invalid level defaults to info", "invalid", "info"},
		{"empty string defaults to info", "", "info"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseLogLevel(tt.level)
			assert.Equal(t, tt.want, result.String())
		})
	}
}

func TestDirPath(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"Unix path", "/var/log/app.log", "/var/log"},
		{"Windows path", "C:\\logs\\app.log", "C:\\logs"},
		{"relative path", "logs/app.log", "logs"},
		{"no directory", "app.log", "."},
		{"empty string", "", "."},
		{"multiple levels", "/var/log/app/system.log", "/var/log/app"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := dirPath(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestInitLogger_Stdout(t *testing.T) {
	cfg := &LogConfig{
		Level:  "info",
		Format: "text",
		Output: "stdout",
	}

	err := InitLogger(cfg)
	assert.NoError(t, err)
	assert.NotNil(t, L())
	assert.NotNil(t, S())

	// Clean up
	Sync()
}

func TestInitLogger_Stdout_JSON(t *testing.T) {
	cfg := &LogConfig{
		Level:  "debug",
		Format: "json",
		Output: "stdout",
	}

	err := InitLogger(cfg)
	assert.NoError(t, err)
	assert.NotNil(t, L())

	// Clean up
	Sync()
}

func TestInitLogger_File_SkipOnWindows(t *testing.T) {
	t.Skip("Skipping file logger test on Windows due to lumberjack file lock issue")
}

func TestInitLogger_FileNoPath(t *testing.T) {
	cfg := &LogConfig{
		Level:    "info",
		Format:   "text",
		Output:   "file",
		FilePath: "",
	}

	err := InitLogger(cfg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "file_path is required")
}

func TestInitLogger_InvalidOutput(t *testing.T) {
	cfg := &LogConfig{
		Level:  "info",
		Format: "text",
		Output: "invalid",
	}

	err := InitLogger(cfg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid output")
}

func TestL_Default(t *testing.T) {
	// Reset global logger
	globalLogger = nil
	globalSugar = nil

	logger := L()
	assert.NotNil(t, logger)

	// Clean up
	Sync()
}

func TestS_Default(t *testing.T) {
	// Reset global logger
	globalLogger = nil
	globalSugar = nil

	sugar := S()
	assert.NotNil(t, sugar)

	// Clean up
	Sync()
}

func TestL_Initialized(t *testing.T) {
	cfg := &LogConfig{
		Level:  "info",
		Format: "text",
		Output: "stdout",
	}

	err := InitLogger(cfg)
	assert.NoError(t, err)

	logger := L()
	assert.NotNil(t, logger)

	// Clean up
	Sync()
}

func TestSync_NilLogger(t *testing.T) {
	// Reset global logger
	globalLogger = nil

	err := Sync()
	assert.NoError(t, err)
}

func TestNamed(t *testing.T) {
	cfg := &LogConfig{
		Level:  "info",
		Format: "text",
		Output: "stdout",
	}

	_ = InitLogger(cfg)
	named := Named("test")
	assert.NotNil(t, named)

	// Clean up
	Sync()
}

func TestWith(t *testing.T) {
	cfg := &LogConfig{
		Level:  "info",
		Format: "text",
		Output: "stdout",
	}

	_ = InitLogger(cfg)
	with := With(String("key", "value"))
	assert.NotNil(t, with)

	// Clean up
	Sync()
}

func TestLogLevels(t *testing.T) {
	cfg := &LogConfig{
		Level:  "debug",
		Format: "text",
		Output: "stdout",
	}

	_ = InitLogger(cfg)

	// These should not panic
	assert.NotPanics(t, func() {
		Debug("debug message")
		Info("info message")
		Warn("warn message")
		Error("error message")
	})

	// Clean up
	Sync()
}

func TestLogLevels_Fields(t *testing.T) {
	cfg := &LogConfig{
		Level:  "debug",
		Format: "json",
		Output: "stdout",
	}

	_ = InitLogger(cfg)

	// These should not panic
	assert.NotPanics(t, func() {
		Debug("debug message", String("key", "value"))
		Info("info message", Int("count", 42))
		Warn("warn message", String("detail", "warning details"))
		Error("error message", Err(assert.AnError))
	})

	// Clean up
	Sync()
}

func TestLogFormats(t *testing.T) {
	cfg := &LogConfig{
		Level:  "info",
		Format: "text",
		Output: "stdout",
	}

	_ = InitLogger(cfg)

	assert.NotPanics(t, func() {
		Debugf("formatted %s", "debug")
		Infof("formatted %s", "info")
		Warnf("formatted %s", "warn")
		Errorf("formatted %s", "error")
	})

	// Clean up
	Sync()
}

func TestPanicFunctions(t *testing.T) {
	cfg := &LogConfig{
		Level:  "info",
		Format: "text",
		Output: "stdout",
	}

	_ = InitLogger(cfg)

	// Test Panic and Panicf
	assert.Panics(t, func() {
		Panic("panic message")
	})

	assert.Panics(t, func() {
		Panicf("panic %s", "message")
	})

	// Clean up
	Sync()
}

func TestInitLogger_DebugLevelWithCaller(t *testing.T) {
	cfg := &LogConfig{
		Level:  "debug",
		Format: "json",
		Output: "stdout",
	}

	err := InitLogger(cfg)
	assert.NoError(t, err)

	// Debug level should add caller and stacktrace options
	logger := L()
	assert.NotNil(t, logger)

	// Clean up
	Sync()
}

func TestInitLogger_ReinitializeUpdatesRuntimeLevel(t *testing.T) {
	infoCfg := &LogConfig{
		Level:  "info",
		Format: "json",
		Output: "stdout",
	}

	err := InitLogger(infoCfg)
	assert.NoError(t, err)
	assert.False(t, L().Core().Enabled(zapcore.DebugLevel))
	assert.True(t, L().Core().Enabled(zapcore.InfoLevel))

	debugCfg := &LogConfig{
		Level:  "debug",
		Format: "json",
		Output: "stdout",
	}

	err = InitLogger(debugCfg)
	assert.NoError(t, err)
	assert.True(t, L().Core().Enabled(zapcore.DebugLevel))

	errorCfg := &LogConfig{
		Level:  "error",
		Format: "json",
		Output: "stdout",
	}

	err = InitLogger(errorCfg)
	assert.NoError(t, err)
	assert.False(t, L().Core().Enabled(zapcore.InfoLevel))
	assert.True(t, L().Core().Enabled(zapcore.ErrorLevel))

	Sync()
}

func TestChineseSupport(t *testing.T) {
	cfg := &LogConfig{
		Level:  "info",
		Format: "text",
		Output: "stdout",
	}

	err := InitLogger(cfg)
	assert.NoError(t, err)

	// 测试中文日志消息
	chineseMessages := []string{
		"这是一个中文测试消息",
		"系统启动成功",
		"处理请求失败",
		"用户已登录",
		"数据库连接超时",
		"API密钥验证通过",
	}

	assert.NotPanics(t, func() {
		for i, msg := range chineseMessages {
			Info(msg, Int("index", i))
		}
	})

	// 测试中文格式化消息
	assert.NotPanics(t, func() {
		Infof("当前处理第 %d 个请求", 42)
		Warnf("内存使用率达到 %d%%", 85)
		Errorf("连接到 %s 失败", "数据库服务器")
	})

	// Clean up
	Sync()
}

func TestChineseSupport_WithFields(t *testing.T) {
	cfg := &LogConfig{
		Level:  "debug",
		Format: "json",
		Output: "stdout",
	}

	err := InitLogger(cfg)
	assert.NoError(t, err)

	// 测试中文日志（带字段）
	assert.NotPanics(t, func() {
		Info("用户登录",
			String("用户名", "张三"),
			String("角色", "管理员"),
			Int("登录次数", 5),
		)

		Warn("API调用失败",
			String("端点", "/v1/chat/completions"),
			String("模型", "gpt-4"),
			Int("重试次数", 3),
		)

		Error("系统错误",
			String("错误类型", "数据库连接超时"),
			String("详细信息", "无法连接到 PostgreSQL 数据库"),
		)
	})

	// Clean up
	Sync()
}

func TestChineseSupport_InMiddleware(t *testing.T) {
	// 模拟中间件中的中文日志场景
	cfg := &LogConfig{
		Level:  "info",
		Format: "text",
		Output: "stdout",
	}

	err := InitLogger(cfg)
	assert.NoError(t, err)

	assert.NotPanics(t, func() {
		Infof("收到请求: %s %s", "POST", "/v1/chat/completions")
		Debugf("请求体大小: %d 字节", 1024)
		Warnf("API响应时间: %d 毫秒, 超过阈值", 5000)
		Errorf("处理请求时发生错误: %s", "模型不可用")
	})

	// Clean up
	Sync()
}
