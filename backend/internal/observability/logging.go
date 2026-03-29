package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// LogLevel 日志级别
type LogLevel int

const (
	// LevelDebug 调试级别
	LevelDebug LogLevel = iota
	// LevelInfo 信息级别
	LevelInfo
	// LevelWarn 警告级别
	LevelWarn
	// LevelError 错误级别
	LevelError
)

// String 返回日志级别的字符串表示
func (l LogLevel) String() string {
	switch l {
	case LevelDebug:
		return "DEBUG"
	case LevelInfo:
		return "INFO"
	case LevelWarn:
		return "WARN"
	case LevelError:
		return "ERROR"
	default:
		return "UNKNOWN"
	}
}

// LogEntry 日志条目
type LogEntry struct {
	Timestamp time.Time              `json:"timestamp"`
	Level     LogLevel               `json:"level"`
	Message   string                 `json:"message"`
	Fields    map[string]interface{} `json:"fields,omitempty"`
	TraceID   string                 `json:"traceId,omitempty"`
	SpanID    string                 `json:"spanId,omitempty"`
	Source    string                 `json:"source,omitempty"`
}

// LogWriter 日志写入器接口
type LogWriter interface {
	Write(entry *LogEntry) error
	Flush() error
	Close() error
}

// JSONWriter JSON 格式日志写入器
type JSONWriter struct {
	writer io.Writer
	mu     sync.Mutex
}

// NewJSONWriter 创建 JSON 日志写入器
func NewJSONWriter(w io.Writer) *JSONWriter {
	return &JSONWriter{
		writer: w,
	}
}

// Write 写入日志
func (w *JSONWriter) Write(entry *LogEntry) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}

	_, err = w.writer.Write(append(data, '\n'))
	return err
}

// Flush 刷新缓冲区
func (w *JSONWriter) Flush() error {
	if flusher, ok := w.writer.(interface{ Flush() error }); ok {
		return flusher.Flush()
	}
	return nil
}

// Close 关闭写入器
func (w *JSONWriter) Close() error {
	if closer, ok := w.writer.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

// TextWriter 文本格式日志写入器
type TextWriter struct {
	writer io.Writer
	mu     sync.Mutex
}

// NewTextWriter 创建文本日志写入器
func NewTextWriter(w io.Writer) *TextWriter {
	return &TextWriter{
		writer: w,
	}
}

// Write 写入日志
func (w *TextWriter) Write(entry *LogEntry) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	var fields string
	if len(entry.Fields) > 0 {
		fieldsBytes, _ := json.Marshal(entry.Fields)
		fields = " " + string(fieldsBytes)
	}

	line := fmt.Sprintf("%s %s %s%s\n",
		entry.Timestamp.Format(time.RFC3339),
		entry.Level.String(),
		entry.Message,
		fields,
	)

	_, err := w.writer.Write([]byte(line))
	return err
}

// Flush 刷新缓冲区
func (w *TextWriter) Flush() error {
	if flusher, ok := w.writer.(interface{ Flush() error }); ok {
		return flusher.Flush()
	}
	return nil
}

// Close 关闭写入器
func (w *TextWriter) Close() error {
	if closer, ok := w.writer.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

// Logger 日志记录器
type Logger struct {
	level   LogLevel
	writers []LogWriter
	source  string
	fields  map[string]interface{}
	mu      sync.RWMutex
}

// LoggerConfig 日志配置
type LoggerConfig struct {
	Level   LogLevel
	Writers []LogWriter
	Source  string
	Fields  map[string]interface{}
}

// NewLogger 创建日志记录器
func NewLogger(config *LoggerConfig) *Logger {
	if config == nil {
		config = &LoggerConfig{
			Level:   LevelInfo,
			Writers: []LogWriter{NewJSONWriter(os.Stdout)},
		}
	}

	return &Logger{
		level:   config.Level,
		writers: config.Writers,
		source:  config.Source,
		fields:  config.Fields,
	}
}

// DefaultLogger 默认日志记录器
var DefaultLogger = NewLogger(&LoggerConfig{
	Level:   LevelInfo,
	Writers: []LogWriter{NewJSONWriter(os.Stdout)},
})

// SetLevel 设置日志级别
func (l *Logger) SetLevel(level LogLevel) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.level = level
}

// GetLevel 获取日志级别
func (l *Logger) GetLevel() LogLevel {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.level
}

// AddWriter 添加日志写入器
func (l *Logger) AddWriter(writer LogWriter) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.writers = append(l.writers, writer)
}

// SetField 设置全局字段
func (l *Logger) SetField(key string, value interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fields == nil {
		l.fields = make(map[string]interface{})
	}
	l.fields[key] = value
}

// SetFields 批量设置字段
func (l *Logger) SetFields(fields map[string]interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fields == nil {
		l.fields = make(map[string]interface{})
	}
	for k, v := range fields {
		l.fields[k] = v
	}
}

// WithField 返回带新字段的 Logger
func (l *Logger) WithField(key string, value interface{}) *Logger {
	l.mu.RLock()
	newFields := make(map[string]interface{})
	for k, v := range l.fields {
		newFields[k] = v
	}
	newFields[key] = value
	l.mu.RUnlock()

	return &Logger{
		level:   l.level,
		writers: l.writers,
		source:  l.source,
		fields:  newFields,
	}
}

// WithFields 返回带新字段的 Logger
func (l *Logger) WithFields(fields map[string]interface{}) *Logger {
	l.mu.RLock()
	newFields := make(map[string]interface{})
	for k, v := range l.fields {
		newFields[k] = v
	}
	for k, v := range fields {
		newFields[k] = v
	}
	l.mu.RUnlock()

	return &Logger{
		level:   l.level,
		writers: l.writers,
		source:  l.source,
		fields:  newFields,
	}
}

// WithSource 返回带源的 Logger
func (l *Logger) WithSource(source string) *Logger {
	return &Logger{
		level:   l.level,
		writers: l.writers,
		source:  source,
		fields:  l.fields,
	}
}

// log 内部日志方法
func (l *Logger) log(level LogLevel, message string, fields map[string]interface{}, traceCtx *TraceContext) {
	if level < l.level {
		return
	}

	entry := &LogEntry{
		Timestamp: time.Now(),
		Level:     level,
		Message:   message,
		Source:    l.source,
		Fields:    l.fields,
	}

	// 合并字段
	if fields != nil {
		if entry.Fields == nil {
			entry.Fields = make(map[string]interface{})
		}
		for k, v := range fields {
			entry.Fields[k] = v
		}
	}

	// 添加追踪信息
	if traceCtx != nil {
		entry.TraceID = traceCtx.TraceID
		entry.SpanID = traceCtx.SpanID
	}

	// 写入所有 writers
	for _, writer := range l.writers {
		_ = writer.Write(entry)
	}
}

// Debug 记录调试日志
func (l *Logger) Debug(message string) {
	l.log(LevelDebug, message, nil, nil)
}

// DebugWithFields 记录带字段的调试日志
func (l *Logger) DebugWithFields(message string, fields map[string]interface{}) {
	l.log(LevelDebug, message, fields, nil)
}

// DebugWithContext 记录带追踪上下文的调试日志
func (l *Logger) DebugWithContext(ctx context.Context, message string) {
	traceCtx := FromContext(ctx)
	l.log(LevelDebug, message, nil, traceCtx)
}

// Info 记录信息日志
func (l *Logger) Info(message string) {
	l.log(LevelInfo, message, nil, nil)
}

// InfoWithFields 记录带字段的信息日志
func (l *Logger) InfoWithFields(message string, fields map[string]interface{}) {
	l.log(LevelInfo, message, fields, nil)
}

// InfoWithContext 记录带追踪上下文的信息日志
func (l *Logger) InfoWithContext(ctx context.Context, message string) {
	traceCtx := FromContext(ctx)
	l.log(LevelInfo, message, nil, traceCtx)
}

// Warn 记录警告日志
func (l *Logger) Warn(message string) {
	l.log(LevelWarn, message, nil, nil)
}

// WarnWithFields 记录带字段的警告日志
func (l *Logger) WarnWithFields(message string, fields map[string]interface{}) {
	l.log(LevelWarn, message, fields, nil)
}

// WarnWithContext 记录带追踪上下文的警告日志
func (l *Logger) WarnWithContext(ctx context.Context, message string) {
	traceCtx := FromContext(ctx)
	l.log(LevelWarn, message, nil, traceCtx)
}

// Error 记录错误日志
func (l *Logger) Error(message string) {
	l.log(LevelError, message, nil, nil)
}

// ErrorWithFields 记录带字段的错误日志
func (l *Logger) ErrorWithFields(message string, fields map[string]interface{}) {
	l.log(LevelError, message, fields, nil)
}

// ErrorWithContext 记录带追踪上下文的错误日志
func (l *Logger) ErrorWithContext(ctx context.Context, message string) {
	traceCtx := FromContext(ctx)
	l.log(LevelError, message, nil, traceCtx)
}

// Errorf 记录格式化错误日志
func (l *Logger) Errorf(format string, args ...interface{}) {
	message := fmt.Sprintf(format, args...)
	l.log(LevelError, message, nil, nil)
}

// Flush 刷新所有缓冲区
func (l *Logger) Flush() error {
	var lastErr error
	for _, writer := range l.writers {
		if err := writer.Flush(); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

// Close 关闭所有写入器
func (l *Logger) Close() error {
	var lastErr error
	for _, writer := range l.writers {
		if err := writer.Close(); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

// 全局日志辅助函数

// Debug 记录调试日志（使用默认 Logger）
func Debug(message string) {
	DefaultLogger.Debug(message)
}

// Info 记录信息日志（使用默认 Logger）
func Info(message string) {
	DefaultLogger.Info(message)
}

// Warn 记录警告日志（使用默认 Logger）
func Warn(message string) {
	DefaultLogger.Warn(message)
}

// Error 记录错误日志（使用默认 Logger）
func Error(message string) {
	DefaultLogger.Error(message)
}

// DebugWithField 记录带字段的调试日志
func DebugWithField(key string, value interface{}, message string) {
	DefaultLogger.WithField(key, value).Debug(message)
}

// InfoWithField 记录带字段的信息日志
func InfoWithField(key string, value interface{}, message string) {
	DefaultLogger.WithField(key, value).Info(message)
}

// WarnWithField 记录带字段的警告日志
func WarnWithField(key string, value interface{}, message string) {
	DefaultLogger.WithField(key, value).Warn(message)
}

// ErrorWithField 记录带字段的错误日志
func ErrorWithField(key string, value interface{}, message string) {
	DefaultLogger.WithField(key, value).Error(message)
}

// DebugWithFields 记录带多个字段的调试日志
func DebugWithFields(fields map[string]interface{}, message string) {
	DefaultLogger.DebugWithFields(message, fields)
}

// InfoWithFields 记录带多个字段的信息日志
func InfoWithFields(fields map[string]interface{}, message string) {
	DefaultLogger.InfoWithFields(message, fields)
}

// WarnWithFields 记录带多个字段的警告日志
func WarnWithFields(fields map[string]interface{}, message string) {
	DefaultLogger.WarnWithFields(message, fields)
}

// ErrorWithFields 记录带多个字段的错误日志
func ErrorWithFields(fields map[string]interface{}, message string) {
	DefaultLogger.ErrorWithFields(message, fields)
}

// WithSource 返回带源的 Logger
func WithSource(source string) *Logger {
	return DefaultLogger.WithSource(source)
}
