package errors

import (
	"errors"
	"fmt"
)

// RuntimeError 运行时错误
type RuntimeError struct {
	Code    ErrorCode
	Message string
	Cause   error
	Context map[string]interface{}
}

// Error 实现 error 接口
func (e *RuntimeError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("[%s] %s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

// Unwrap 支持错误解包
func (e *RuntimeError) Unwrap() error {
	return e.Cause
}

// Wrap 包装错误
func Wrap(code ErrorCode, message string, cause error) *RuntimeError {
	return &RuntimeError{
		Code:    code,
		Message: message,
		Cause:   cause,
	}
}

// WrapWithContext 包装错误并添加上下文
func WrapWithContext(code ErrorCode, message string, cause error, ctx map[string]interface{}) *RuntimeError {
	return &RuntimeError{
		Code:    code,
		Message: message,
		Cause:   cause,
		Context: ctx,
	}
}

// Is 检查错误类型
func Is(err error, code ErrorCode) bool {
	var runtimeErr *RuntimeError
	if errors.As(err, &runtimeErr) {
		return runtimeErr.Code == code
	}
	return false
}

// GetContext 获取错误上下文
func (e *RuntimeError) GetContext() map[string]interface{} {
	if e.Context == nil {
		return make(map[string]interface{})
	}

	// 返回副本以避免外部修改
	ctx := make(map[string]interface{}, len(e.Context))
	for k, v := range e.Context {
		ctx[k] = v
	}
	return ctx
}

// HasContext 检查是否有特定上下文的键
func (e *RuntimeError) HasContext(key string) bool {
	if e.Context == nil {
		return false
	}
	_, exists := e.Context[key]
	return exists
}

// GetContextValue 获取上下文值
func (e *RuntimeError) GetContextValue(key string) (interface{}, bool) {
	if e.Context == nil {
		return nil, false
	}
	val, exists := e.Context[key]
	return val, exists
}

// New 创建新的运行时错误
func New(code ErrorCode, message string) *RuntimeError {
	return &RuntimeError{
		Code:    code,
		Message: message,
	}
}

// Newf 创建带格式化消息的运行时错误
func Newf(code ErrorCode, format string, args ...interface{}) *RuntimeError {
	return &RuntimeError{
		Code:    code,
		Message: fmt.Sprintf(format, args...),
	}
}

// Prepend 在错误消息前添加前缀
func (e *RuntimeError) Prepend(prefix string) *RuntimeError {
	return &RuntimeError{
		Code:    e.Code,
		Message: prefix + e.Message,
		Cause:   e.Cause,
		Context: e.Context,
	}
}

// WithContext 添加上下文到错误
func (e *RuntimeError) WithContext(key string, value interface{}) *RuntimeError {
	newErr := &RuntimeError{
		Code:    e.Code,
		Message: e.Message,
		Cause:   e.Cause,
	}

	if e.Context != nil {
		newErr.Context = make(map[string]interface{}, len(e.Context)+1)
		for existingKey, existingValue := range e.Context {
			newErr.Context[existingKey] = existingValue
		}
	} else {
		newErr.Context = make(map[string]interface{}, 1)
	}

	newErr.Context[key] = value
	return newErr
}
