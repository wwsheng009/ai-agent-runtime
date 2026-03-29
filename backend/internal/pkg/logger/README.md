# Logger Module

This module provides structured logging capabilities for the AI Gateway using Zap.

## Features

- **Structured Logging**: JSON and text formats with customizable fields
- **Context Awareness**: Automatic correlation of request-scoped fields (request_id, user_id, etc.)
- **Log Rotation**: Automatic log file rotation with lumberjack
- **Multiple Outputs**: Support for stdout and file outputs
- **Configurable Levels**: Debug, Info, Warn, Error levels
- **Performance**: Zero-allocation logging for hot paths

## Usage

### Initialization

```go
import (
    "github.com/ai-gateway/gateway/internal/config"
    "github.com/ai-gateway/gateway/internal/pkg/logger"
)

// Initialize logger with config
cfg := &config.LogConfig{
    Level:      "info",
    Format:     "json",
    Output:     "stdout",
    FilePath:   "./logs/gateway.log",
    MaxSize:    100,
    MaxBackups: 3,
    MaxAge:     7,
    Compress:   true,
}

if err := logger.InitLogger(cfg); err != nil {
    log.Fatal(err)
}

// Flush logs before exit
defer logger.Sync()
```

### Basic Logging

```go
// Using global logger
logger.Info("Server started",
    logger.String("host", "0.0.0.0"),
    logger.Int("port", 8080),
)

logger.Error("Failed to connect to database",
    logger.Err(err),
    logger.String("dsn", dsn),
)

// Using typed constructors
logger.Debug("Processing request")
logger.Infof("User %s logged in", userID)
logger.Warnf("Request took too long: %dms", latency)
logger.Errorf("Request failed: %v", err)
```

### Context-Aware Logging

```go
import (
    "context"
    "github.com/ai-gateway/gateway/internal/pkg/logger"
)

// Add request-scoped values to context
ctx = logger.WithRequestID(ctx, "req-123")
ctx = logger.WithUserID(ctx, "user-456")
ctx = logger.WithProvider(ctx, "openai")
ctx = logger.WithModel(ctx, "gpt-4")

// Log with context
logger.CtxInfo(ctx, "Processing request")

// Request ID, user ID, provider, and model will be automatically included
// Output:
// {"level":"info","timestamp":"2024-01-01T00:00:00Z","message":"Processing request","request_id":"req-123","user_id":"user-456","provider":"openai","model":"gpt-4"}
```

### Named Loggers

```go
// Create named logger for subsystems
providerLogger := logger.Named("provider")
dbLogger := logger.Named("database")
cacheLogger := logger.Named("cache")

providerLogger.Info("Provider initialized",
    logger.String("name", "openai"),
    logger.Bool("enabled", true),
)
```

### Custom Fields

```go
// Use provided field constructors
logger.Info("API call",
    logger.Method("POST"),
    logger.URL("/v1/chat/completions"),
    logger.Model("gpt-4"),
    logger.Tokens(150),
    logger.Cost(0.003),
    logger.Latency(234),
)

// Or use Any() for custom values
logger.Info("Custom data",
    logger.Any("metadata", map[string]interface{}{
        "key1": "value1",
        "key2": 123,
    }),
)
```

### Log Levels

```go
logger.Debug("Detailed debugging info")
logger.Info("General information")
logger.Warn("Warning messages")
logger.Error("Error messages")
logger.Fatal("Fatal error, calls os.Exit(1)")
logger.Panic("Panic, calls panic()")
```

## Configuration Options

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| Level | string | info | Log level: debug, info, warn, error |
| Format | string | json | Log format: json, text |
| Output | string | stdout | Output target: stdout, file |
| FilePath | string | ./logs/gateway.log | Log file path (when output=file) |
| MaxSize | int | 100 | Max log file size in MB |
| MaxBackups | int | 3 | Max number of old log files |
| MaxAge | int | 7 | Max age of old log files in days |
| Compress | bool | true | Compress old log files with gzip |

## Environment Variables

Use environment variables to override logging configuration:

```bash
# Set log level
export LOG__LEVEL=debug

# Set output to file
export LOG__OUTPUT=file
export LOG__FILE_PATH=/var/log/gateway/gateway.log
```

## Context Keys

The following context keys are automatically extracted when using `Ctx*` logging functions:

- `request_id`: Request identifier
- `user_id`: User identifier
- `provider`: AI provider name
- `model`: AI model name

## Best Practices

1. **Use Structured Fields**: Always use field constructors for structured data
   ```go
   logger.Info("User login", logger.String("user_id", userID)) // ✅
   logger.Info("User login", "user_id", userID)               // ❌
   ```

2. **Use Context**: Pass context through your call chain for automatic correlation
   ```go
   func HandleRequest(ctx context.Context) {
       ctx = logger.WithRequestID(ctx, generateRequestID())
       logger.CtxInfo(ctx, "Handling request")
   }
   ```

3. **Choose Appropriate Levels**:
   - Debug: Detailed diagnostics for troubleshooting
   - Info: Normal operation, significant events
   - Warn: Potentially harmful situations that don't prevent operation
   - Error: Error events that might still allow the application to continue
   - Fatal: Severe errors that will lead to application termination

4. **Avoid Expensive Logging**: Guard expensive logging with level checks
   ```go
   if logger.L().Core().Enabled(zapcore.DebugLevel) {
       logger.Debug("Expensive data", logger.Any("complex", computeExpensiveData()))
   }
   ```

5. **Use Named Loggers**: Create named loggers for different subsystems
   ```go
   var dbLogger = logger.Named("database")
   var apiLogger = logger.Named("api")
   var cacheLogger = logger.Named("cache")
   ```

6. **Always Sync**: Call `defer logger.Sync()` in your main function
   ```go
   func main() {
       logger.InitLogger(cfg)
       defer logger.Sync()
       // ... application code
   }
   ```

## Example Output

### JSON Format
```json
{
  "level": "info",
  "timestamp": "2024-01-15T10:30:45.123Z",
  "caller": "gateway/handler.go:42",
  "message": "API request processed",
  "request_id": "req-abc123",
  "user_id": "user-456",
  "provider": "openai",
  "model": "gpt-4",
  "method": "POST",
  "url": "/v1/chat/completions",
  "status": 200,
  "tokens": 150,
  "cost": 0.003,
  "latency_ms": 234
}
```

### Text Format
```
2024-01-15T10:30:45.123Z	INFO	gateway/handler.go:42	API request processed	request_id=req-abc123 user_id=user-456 provider=openai model=gpt-4 method=POST url=/v1/chat/completions status=200 tokens=150 cost=0.003 latency_ms=234
```

## Output Examples

```go
// Request logging
logger.Info("API request",
    logger.Method("POST"),
    logger.URL("/v1/chat/completions"),
    logger.RequestID("req-123"),
)

// Response logging
logger.Info("API response",
    logger.Status(200),
    logger.Tokens(150),
    logger.Cost(0.003),
    logger.Latency(234),
)

// Error logging
logger.Error("Provider request failed",
    logger.Err(err),
    logger.ProviderID("openai"),
    logger.String("error_type", "timeout"),
)

// Health check
logger.Info("Health check",
    logger.String("component", "database"),
    logger.Bool("healthy", true),
    logger.Int64("latency_ms", 5),
)
```
