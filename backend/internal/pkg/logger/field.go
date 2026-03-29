package logger

import "go.uber.org/zap"
import "go.uber.org/zap/zapcore"

// Common field constructors for structured logging

// String creates a string field
func String(key, val string) zapcore.Field {
	return zapcore.Field{Key: key, Type: zapcore.StringType, String: val}
}

// Int creates an int field
func Int(key string, val int) zapcore.Field {
	return zapcore.Field{Key: key, Type: zapcore.Int64Type, Integer: int64(val)}
}

// Int8 creates an int8 field
func Int8(key string, val int8) zapcore.Field {
	return zapcore.Field{Key: key, Type: zapcore.Int32Type, Integer: int64(val)}
}

// Int16 creates an int16 field
func Int16(key string, val int16) zapcore.Field {
	return zapcore.Field{Key: key, Type: zapcore.Int32Type, Integer: int64(val)}
}

// Int32 creates an int32 field
func Int32(key string, val int32) zapcore.Field {
	return zapcore.Field{Key: key, Type: zapcore.Int32Type, Integer: int64(val)}
}

// Int64 creates an int64 field
func Int64(key string, val int64) zapcore.Field {
	return zapcore.Field{Key: key, Type: zapcore.Int64Type, Integer: val}
}

// Uint creates a uint field
func Uint(key string, val uint) zapcore.Field {
	return zapcore.Field{Key: key, Type: zapcore.Uint64Type, Integer: int64(val)}
}

// Uint8 creates a uint8 field
func Uint8(key string, val uint8) zapcore.Field {
	return zapcore.Field{Key: key, Type: zapcore.Uint32Type, Integer: int64(val)}
}

// Uint16 creates a uint16 field
func Uint16(key string, val uint16) zapcore.Field {
	return zapcore.Field{Key: key, Type: zapcore.Uint32Type, Integer: int64(val)}
}

// Uint32 creates a uint32 field
func Uint32(key string, val uint32) zapcore.Field {
	return zapcore.Field{Key: key, Type: zapcore.Uint32Type, Integer: int64(val)}
}

// Uint64 creates a uint64 field
func Uint64(key string, val uint64) zapcore.Field {
	return zapcore.Field{Key: key, Type: zapcore.Uint64Type, Integer: int64(val)}
}

// Float32 creates a float32 field
func Float32(key string, val float32) zapcore.Field {
	return zapcore.Field{Key: key, Type: zapcore.Float64Type, Interface: float64(val)}
}

// Float64 creates a float64 field
func Float64(key string, val float64) zapcore.Field {
	return zapcore.Field{Key: key, Type: zapcore.Float64Type, Interface: val}
}

// Bool creates a bool field
func Bool(key string, val bool) zapcore.Field {
	i := int64(0)
	if val {
		i = 1
	}
	return zapcore.Field{Key: key, Type: zapcore.BoolType, Integer: i}
}

// Any creates a field that can hold any value
func Any(key string, val interface{}) zapcore.Field {
	return zap.Any(key, val)
}

// Err creates an error field
func Err(err error) zapcore.Field {
	return zap.NamedError("error", err)
}

// Duration creates a duration field in milliseconds
func Duration(key string, val int64) zapcore.Field {
	return zapcore.Field{Key: key, Type: zapcore.DurationType, Integer: val}
}

// Namespace creates a namespace field
func Namespace(key string) zapcore.Field {
	return zap.Namespace(key)
}

// RequestID creates a request ID field
func RequestID(id string) zapcore.Field {
	return String("request_id", id)
}

// UserID creates a user ID field
func UserID(id string) zapcore.Field {
	return String("user_id", id)
}

// ProviderID creates a provider ID field
func ProviderID(id string) zapcore.Field {
	return String("provider_id", id)
}

// Model creates a model name field
func Model(model string) zapcore.Field {
	return String("model", model)
}

// Method creates an HTTP method field
func Method(method string) zapcore.Field {
	return String("method", method)
}

// URL creates a URL field
func URL(url string) zapcore.Field {
	return String("url", url)
}

// Status creates an HTTP status field
func Status(status int) zapcore.Field {
	return zap.Int("status", status)
}

// Latency creates a latency field in milliseconds
func Latency(lat int64) zapcore.Field {
	return Int64("latency_ms", lat)
}

// Tokens creates a tokens field
func Tokens(val int) zapcore.Field {
	return Int("tokens", val)
}

// Cost creates a cost field
func Cost(val float64) zapcore.Field {
	return Float64("cost", val)
}

// Caller creates a caller field
func Caller(skip int) zapcore.Field {
	return zap.Skip()
}
