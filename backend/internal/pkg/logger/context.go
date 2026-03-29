package logger

import (
	"context"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Context keys for request-scoped values
type contextKey string

const (
	requestIDKey contextKey = "request_id"
	userIDKey    contextKey = "user_id"
	providerKey  contextKey = "provider"
	modelKey     contextKey = "model"
)

// WithRequestID adds request ID to context
func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDKey, requestID)
}

// WithUserID adds user ID to context
func WithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, userIDKey, userID)
}

// WithProvider adds provider to context
func WithProvider(ctx context.Context, provider string) context.Context {
	return context.WithValue(ctx, providerKey, provider)
}

// WithModel adds model to context
func WithModel(ctx context.Context, model string) context.Context {
	return context.WithValue(ctx, modelKey, model)
}

// GetRequestID gets request ID from context
func GetRequestID(ctx context.Context) string {
	if requestID, ok := ctx.Value(requestIDKey).(string); ok {
		return requestID
	}
	return ""
}

// GetUserID gets user ID from context
func GetUserID(ctx context.Context) string {
	if userID, ok := ctx.Value(userIDKey).(string); ok {
		return userID
	}
	return ""
}

// GetProvider gets provider from context
func GetProvider(ctx context.Context) string {
	if provider, ok := ctx.Value(providerKey).(string); ok {
		return provider
	}
	return ""
}

// GetModel gets model from context
func GetModel(ctx context.Context) string {
	if model, ok := ctx.Value(modelKey).(string); ok {
		return model
	}
	return ""
}

// Ctx returns a logger with fields from context
func Ctx(ctx context.Context) *zap.Logger {
	var fields []zapcore.Field

	if requestID := GetRequestID(ctx); requestID != "" {
		fields = append(fields, RequestID(requestID))
	}

	if userID := GetUserID(ctx); userID != "" {
		fields = append(fields, UserID(userID))
	}

	if provider := GetProvider(ctx); provider != "" {
		fields = append(fields, ProviderID(provider))
	}

	if model := GetModel(ctx); model != "" {
		fields = append(fields, Model(model))
	}

	if len(fields) > 0 {
		return L().With(fields...)
	}

	return L()
}

// CtxDebug logs a message at DebugLevel with context fields
func CtxDebug(ctx context.Context, msg string, fields ...zapcore.Field) {
	Ctx(ctx).Debug(msg, fields...)
}

// CtxInfo logs a message at InfoLevel with context fields
func CtxInfo(ctx context.Context, msg string, fields ...zapcore.Field) {
	Ctx(ctx).Info(msg, fields...)
}

// CtxWarn logs a message at WarnLevel with context fields
func CtxWarn(ctx context.Context, msg string, fields ...zapcore.Field) {
	Ctx(ctx).Warn(msg, fields...)
}

// CtxError logs a message at ErrorLevel with context fields
func CtxError(ctx context.Context, msg string, fields ...zapcore.Field) {
	Ctx(ctx).Error(msg, fields...)
}

// CtxFatal logs a message at FatalLevel with context fields and os.Exit(1)
func CtxFatal(ctx context.Context, msg string, fields ...zapcore.Field) {
	Ctx(ctx).Fatal(msg, fields...)
}

// CtxPanic logs a message at PanicLevel with context fields and panics
func CtxPanic(ctx context.Context, msg string, fields ...zapcore.Field) {
	Ctx(ctx).Panic(msg, fields...)
}
