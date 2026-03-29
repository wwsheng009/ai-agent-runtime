package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

var (
	globalLogger *zap.Logger
	globalSugar  *zap.SugaredLogger
	loggerMu     sync.RWMutex
)

// InitLogger initializes the global logger
func InitLogger(cfg *LogConfig) error {
	logger, sugar, err := buildLogger(cfg)
	if err != nil {
		return err
	}

	loggerMu.Lock()
	oldLogger := globalLogger
	globalLogger = logger
	globalSugar = sugar
	loggerMu.Unlock()

	// 初始化或刷新模块日志管理器
	InitModuleManager(cfg)

	if oldLogger != nil {
		_ = oldLogger.Sync()
	}

	return nil
}

func buildLogger(cfg *LogConfig) (*zap.Logger, *zap.SugaredLogger, error) {
	if cfg == nil {
		return nil, nil, fmt.Errorf("log config is required")
	}

	// 复制一份配置，避免运行时热更新时意外修改外部配置快照。
	effectiveCfg := *cfg

	// Parse log level
	level := parseLogLevel(effectiveCfg.Level)

	// 校验 Output 参数
	if effectiveCfg.EnableConsole {
		// 如果启用了控制台，不需要校验 output
	} else if effectiveCfg.Output != "stdout" && effectiveCfg.Output != "file" {
		return nil, nil, fmt.Errorf("invalid output: %s (must be 'stdout' or 'file')", effectiveCfg.Output)
	}

	// Create encoder config (基础配置)
	baseEncoderConfig := zapcore.EncoderConfig{
		TimeKey:        "timestamp",
		LevelKey:       "level",
		NameKey:        "module", // 模块名显示为独立列
		CallerKey:      "caller", // 调用位置显示为独立列
		FunctionKey:    zapcore.OmitKey,
		MessageKey:     "message",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.SecondsDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
		EncodeName:     zapcore.FullNameEncoder, // 模块名完整显示
	}

	// 为控制台和文件创建不同的 encoder
	var consoleEncoder, fileEncoder zapcore.Encoder

	if effectiveCfg.Format == "json" {
		// JSON 格式 - 控制台和文件都使用基本编码（不带颜色）
		baseEncoderConfig.EncodeLevel = zapcore.LowercaseLevelEncoder
		consoleEncoder = zapcore.NewJSONEncoder(baseEncoderConfig)
		fileEncoder = zapcore.NewJSONEncoder(baseEncoderConfig)
	} else {
		// 文本格式 - 控制台使用颜色
		consoleEncoderConfig := baseEncoderConfig
		consoleEncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
		consoleEncoder = zapcore.NewConsoleEncoder(consoleEncoderConfig)

		// 文件输出使用 JSON 格式（结构更清晰，便于分析）
		fileEncoderConfig := baseEncoderConfig
		fileEncoderConfig.EncodeLevel = zapcore.LowercaseLevelEncoder
		fileEncoder = zapcore.NewJSONEncoder(fileEncoderConfig)
	}

	// Create writer and core for each output
	var cores []zapcore.Core
	var writers []zapcore.WriteSyncer

	// 配置控制台输出
	if effectiveCfg.Output == "stdout" || effectiveCfg.EnableConsole {
		writers = append(writers, zapcore.AddSync(os.Stdout))
	}

	// 配置文件输出
	if effectiveCfg.Output == "file" || effectiveCfg.EnableConsole {
		// 如果输出目标是文件但路径为空，返回错误
		if effectiveCfg.Output == "file" && effectiveCfg.FilePath == "" {
			return nil, nil, fmt.Errorf("file_writer: file_path is required")
		}

		// 启用控制台时，如果文件路径为空，设置默认值
		if effectiveCfg.EnableConsole && effectiveCfg.FilePath == "" {
			effectiveCfg.FilePath = "./logs/gateway.log"
		}

		// Ensure log directory exists
		dir := dirPath(effectiveCfg.FilePath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, nil, fmt.Errorf("failed to create log directory: %w", err)
		}
		writers = append(writers, zapcore.AddSync(&lumberjack.Logger{
			Filename:   effectiveCfg.FilePath,
			MaxSize:    effectiveCfg.MaxSize,
			MaxBackups: effectiveCfg.MaxBackups,
			MaxAge:     effectiveCfg.MaxAge,
			Compress:   effectiveCfg.Compress,
		}))
	}

	if len(writers) == 0 {
		// Default to stdout if no writer configured
		writers = append(writers, zapcore.AddSync(os.Stdout))
	}

	// 为每个 writer 创建独立的 core
	for _, writer := range writers {
		// 如果是控制台输出，使用带颜色的 encoder
		// 否则使用不带颜色的 encoder
		if writer == zapcore.AddSync(os.Stdout) {
			cores = append(cores, zapcore.NewCore(consoleEncoder, writer, level))
		} else {
			cores = append(cores, zapcore.NewCore(fileEncoder, writer, level))
		}
	}

	// 合并所有 cores
	core := zapcore.NewTee(cores...)

	// Create logger
	var opts []zap.Option
	// 始终启用 caller 显示，让文本格式的日志显示代码位置
	opts = append(opts, zap.AddCaller())
	if effectiveCfg.Level == "debug" {
		opts = append(opts, zap.AddStacktrace(zap.ErrorLevel))
	}

	logger := zap.New(core, opts...)
	return logger, logger.Sugar(), nil
}

// parseLogLevel parses log level string
func parseLogLevel(level string) zapcore.Level {
	switch level {
	case "debug":
		return zapcore.DebugLevel
	case "info":
		return zapcore.InfoLevel
	case "warn":
		return zapcore.WarnLevel
	case "error":
		return zapcore.ErrorLevel
	default:
		return zapcore.InfoLevel
	}
}

// dirPath returns the directory portion of a path
func dirPath(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			return path[:i]
		}
	}
	return "."
}

// L returns the global zap logger
func L() *zap.Logger {
	loggerMu.RLock()
	if globalLogger != nil {
		logger := globalLogger
		loggerMu.RUnlock()
		return logger
	}
	loggerMu.RUnlock()

	loggerMu.Lock()
	defer loggerMu.Unlock()
	if globalLogger == nil {
		globalLogger, _ = zap.NewProduction()
		globalSugar = globalLogger.Sugar()
	}
	return globalLogger
}

// S returns the global sugared logger
func S() *zap.SugaredLogger {
	loggerMu.RLock()
	if globalSugar != nil {
		sugar := globalSugar
		loggerMu.RUnlock()
		return sugar
	}
	loggerMu.RUnlock()

	loggerMu.Lock()
	defer loggerMu.Unlock()
	if globalSugar == nil {
		if globalLogger == nil {
			globalLogger, _ = zap.NewProduction()
		}
		globalSugar = globalLogger.Sugar()
	}
	return globalSugar
}

// Sync flushes any buffered log entries
func Sync() error {
	loggerMu.RLock()
	logger := globalLogger
	loggerMu.RUnlock()
	if logger != nil {
		return logger.Sync()
	}
	return nil
}

// Named returns a named logger
func Named(name string) *zap.Logger {
	return L().Named(name)
}

// With returns a logger with additional fields
func With(fields ...zapcore.Field) *zap.Logger {
	return L().With(fields...)
}

// getCallerSkip 动态获取需要跳过的调用栈层数
// 通过遍历调用栈，跳过 logger.go 和 zap 层，找到业务代码位置
func getCallerSkip() int {
	const maxDepth = 20
	for skip := 1; skip < maxDepth; skip++ { // 从 skip=1 开始
		_, file, _, ok := runtime.Caller(skip)
		if !ok {
			break
		}

		filename := filepath.Base(file)

		// 找到第一个不是 logger 层或 zap 层的调用
		// 当遇到业务代码时，返回这个 skip - 1（因为 zap 记录时会再 skip 一层）
		if filename != "logger.go" &&
			!strings.Contains(file, "go.uber.org/zap") &&
			!strings.Contains(file, "\\pkg\\logger\\") &&
			!strings.Contains(file, "/pkg/logger/") {
			// 返回的值是 zap.AddCallerSkip 需要的值
			// zap 会从这个调用者位置往上找
			return skip - 1
		}
	}

	return 1 // 默认 skip 1 层
}

// Debug logs a message at DebugLevel
func Debug(msg string, fields ...zapcore.Field) {
	L().WithOptions(zap.AddCallerSkip(getCallerSkip())).Debug(msg, fields...)
}

// Info logs a message at InfoLevel
func Info(msg string, fields ...zapcore.Field) {
	L().WithOptions(zap.AddCallerSkip(getCallerSkip())).Info(msg, fields...)
}

// Warn logs a message at WarnLevel
func Warn(msg string, fields ...zapcore.Field) {
	L().WithOptions(zap.AddCallerSkip(getCallerSkip())).Warn(msg, fields...)
}

// Error logs a message at ErrorLevel
func Error(msg string, fields ...zapcore.Field) {
	L().WithOptions(zap.AddCallerSkip(getCallerSkip())).Error(msg, fields...)
}

// Fatal logs a message at FatalLevel and os.Exit(1)
func Fatal(msg string, fields ...zapcore.Field) {
	L().WithOptions(zap.AddCallerSkip(getCallerSkip())).Fatal(msg, fields...)
}

// Panic logs a message at PanicLevel and panics
func Panic(msg string, fields ...zapcore.Field) {
	L().WithOptions(zap.AddCallerSkip(getCallerSkip())).Panic(msg, fields...)
}

// Debugf logs a formatted message at DebugLevel
func Debugf(format string, args ...interface{}) {
	L().WithOptions(zap.AddCallerSkip(getCallerSkip())).Sugar().Debugf(format, args...)
}

// Infof logs a formatted message at InfoLevel
func Infof(format string, args ...interface{}) {
	L().WithOptions(zap.AddCallerSkip(getCallerSkip())).Sugar().Infof(format, args...)
}

// Warnf logs a formatted message at WarnLevel
func Warnf(format string, args ...interface{}) {
	L().WithOptions(zap.AddCallerSkip(getCallerSkip())).Sugar().Warnf(format, args...)
}

// Errorf logs a formatted message at ErrorLevel
func Errorf(format string, args ...interface{}) {
	L().WithOptions(zap.AddCallerSkip(getCallerSkip())).Sugar().Errorf(format, args...)
}

// Fatalf logs a formatted message at FatalLevel and os.Exit(1)
func Fatalf(format string, args ...interface{}) {
	L().WithOptions(zap.AddCallerSkip(getCallerSkip())).Sugar().Fatalf(format, args...)
}

// Panicf logs a formatted message at PanicLevel and panics
func Panicf(format string, args ...interface{}) {
	L().WithOptions(zap.AddCallerSkip(getCallerSkip())).Sugar().Panicf(format, args...)
}

// getCallerInfo 动态获取真实的调用者位置
// 通过遍历调用栈，跳过 logger.go 和 zap 层，找到业务代码位置
func getCallerInfo() zapcore.Field {
	const maxDepth = 20
	for skip := 2; skip < maxDepth; skip++ {
		pc, file, line, ok := runtime.Caller(skip)
		if !ok {
			break
		}

		filename := filepath.Base(file)

		// 跳过 logger 层和日志库层
		if filename == "logger.go" ||
			strings.Contains(file, "go.uber.org/zap") ||
			strings.Contains(file, "\\pkg\\logger\\") ||
			strings.Contains(file, "/pkg/logger/") {
			continue
		}

		_ = runtime.FuncForPC(pc)

		caller := zap.String("caller", shortenCaller(file, line))
		return caller
	}

	return zap.String("caller", "unknown")
}

// shortenCaller 缩短调用者路径，与 zap.ShortCallerEncoder 格式一致
func shortenCaller(file string, line int) string {
	parts := strings.Split(file, "/")
	if len(parts) >= 2 {
		file = strings.Join(parts[len(parts)-2:], "/")
	} else {
		parts = strings.Split(file, "\\")
		if len(parts) >= 2 {
			file = strings.Join(parts[len(parts)-2:], "\\")
		}
	}
	return fmt.Sprintf("%s:%d", file, line)
}
