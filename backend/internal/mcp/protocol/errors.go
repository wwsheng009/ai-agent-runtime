package protocol

// MCP 错误码定义
const (
	// ParseError        = -32700 // 语法错误
	// InvalidRequest     = -32600 // 无效请求
	// MethodNotFound     = -32601 // 方法未找到
	// InvalidParams      = -32602 // 无效参数
	// InternalError      = -32603 // 内部错误

	// MCP 特定错误码
	ErrorCodeInvalidRequest     = -32600
	ErrorCodeMethodNotFound     = -32601
	ErrorCodeInvalidParams      = -32602
	ErrorCodeInternalError      = -32603

	// 资源未找到
	ErrorCodeResourceNotFound = -32001

	// 工具未找到
	ErrorCodeToolNotFound     = -32002

	// 服务器错误
	ErrorCodeServerError      = -32003
)

var (
	// 预定义错误
	ErrInvalidRequest     = &Error{Code: ErrorCodeInvalidRequest, Message: "Invalid Request"}
	ErrMethodNotFound     = &Error{Code: ErrorCodeMethodNotFound, Message: "Method not found"}
	ErrInvalidParams      = &Error{Code: ErrorCodeInvalidParams, Message: "Invalid params"}
	ErrInternalError      = &Error{Code: ErrorCodeInternalError, Message: "Internal error"}
	ErrResourceNotFound   = &Error{Code: ErrorCodeResourceNotFound, Message: "Resource not found"}
	ErrToolNotFound       = &Error{Code: ErrorCodeToolNotFound, Message: "Tool not found"}
	ErrServerError        = &Error{Code: ErrorCodeServerError, Message: "Server error"}
)

// NewError 创建新错误
func NewError(code int, message string, data interface{}) *Error {
	return &Error{
		Code:    code,
		Message: message,
		Data:    data,
	}
}

// NewInternalError 创建内部错误
func NewInternalError(message string) *Error {
	return &Error{
		Code:    ErrorCodeInternalError,
		Message: message,
	}
}

// NewMethodNotFoundError 创建方法未找到错误
func NewMethodNotFoundError(method string) *Error {
	return &Error{
		Code:    ErrorCodeMethodNotFound,
		Message: "Method not found",
		Data:    method,
	}
}

// NewInvalidParamsError 创建无效参数错误
func NewInvalidParamsError(message string) *Error {
	return &Error{
		Code:    ErrorCodeInvalidParams,
		Message: message,
	}
}
