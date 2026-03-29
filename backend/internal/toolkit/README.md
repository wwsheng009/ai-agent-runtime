# Toolkit - 工具调用框架

> 统一工具调用系统，支持 MCP 暴露和直接调用

## 概述

ToolKit 是 AI Gateway 的统一工具调用框架，提供：

- **统一接口**：所有工具实现相同的 `Tool` 接口
- **双模式支持**：支持直接调用和 MCP 暴露
- **零依赖**：不依赖外部框架，轻量级实现
- **向后兼容**：通过适配器兼容现有 `FunctionRegistry`

## 目录结构

```
internal/toolkit/
├── interface.go      # 工具接口定义
├── base.go           # 工具基类
├── registry.go       # 工具注册表
├── adapter.go        # Function 适配器
├── mcp_adapter.go    # MCP 适配器
├── registry_test.go  # 单元测试
└── tools/            # 内建工具
    ├── bash.go       # Shell 命令执行
    ├── view.go       # 文件查看
    ├── edit.go       # 文件编辑
    ├── write.go      # 文件写入
    ├── glob.go       # 文件名模式匹配
    ├── grep.go       # 文件内容搜索
    ├── ls.go         # 目录列表
    ├── download.go   # 文件下载
    ├── fetch.go      # HTTP 获取
    ├── multiedit.go  # 多处编辑
    ├── todos.go      # 任务管理
    ├── sourcegraph.go # 代码搜索
    └── web_search.go # 网络搜索
```

## 内建工具列表

| 工具 | 优先级 | 描述 |
|------|--------|------|
| bash | P0 | 执行 Shell 命令，支持跨平台 |
| view | P0 | 查看文件内容，支持偏移量和限制 |
| edit | P0 | 文件编辑，单处文本替换 |
| write | P0 | 文件写入，自动创建父目录 |
| glob | P1 | 文件名模式匹配，支持 ** 通配符 |
| grep | P1 | 文件内容搜索，支持正则表达式 |
| ls | P1 | 目录列表，树形结构展示 |
| download | P2 | 从 URL 下载文件到本地 |
| fetch | P2 | 获取 URL 内容，支持 text/markdown/html |
| multiedit | P3 | 多处编辑，按顺序应用编辑操作 |
| todos | P3 | 任务管理，结构化任务列表 |
| sourcegraph | P3 | 代码搜索，使用 Sourcegraph API |
| web_search | P3 | 网络搜索，使用 DuckDuckGo |

## 快速开始

### 1. 创建注册表并注册工具

```go
package main

import (
    "context"
    "github.com/ai-gateway/gateway/internal/toolkit"
    "github.com/ai-gateway/gateway/internal/toolkit/tools"
)

func main() {
    // 创建工具注册表
    registry := toolkit.NewRegistry()

    // 注册 P0 工具
    registry.Register(tools.NewBashTool())
    registry.Register(tools.NewViewTool())
    registry.Register(tools.NewEditTool())
    registry.Register(tools.NewWriteTool())

    // 注册 P1 工具
    registry.Register(tools.NewGlobTool())
    registry.Register(tools.NewGrepTool())
    registry.Register(tools.NewLsTool())

    // 注册 P2 工具
    registry.Register(tools.NewDownloadTool())
    registry.Register(tools.NewFetchTool())

    // 注册 P3 工具
    registry.Register(tools.NewMultieditTool())
    registry.Register(tools.NewTodosTool())
    registry.Register(tools.NewSourcegraphTool())
    registry.Register(tools.NewWebSearchTool())
}
```

### 2. 直接调用工具

```go
ctx := context.Background()

// 获取工具
bashTool := registry.Get("bash")

// 执行工具
result, err := bashTool.Execute(ctx, map[string]interface{}{
    "command": "echo hello",
})
if err != nil {
    panic(err)
}

fmt.Println(result.Content) // 输出: hello
```

### 3. 转换为 Function 格式

```go
// 转换为 functions.Function 格式（兼容 FunctionRegistry）
functions := registry.ToFunctions()

for _, fn := range functions {
    fmt.Printf("Function: %s\n", fn.Name)
}
```

### 4. 转换为 MCP 格式

```go
// 转换为 MCP Tool 格式
mcpTools := registry.ToMCPTools()

for _, tool := range mcpTools {
    fmt.Printf("MCP Tool: %s\n", tool.Name)
}
```

## 创建自定义工具

### 1. 实现 Tool 接口

```go
package mytool

import (
    "context"
    "github.com/ai-gateway/gateway/internal/toolkit"
)

type MyTool struct {
    *toolkit.BaseTool
}

func NewMyTool() *MyTool {
    parameters := map[string]interface{}{
        "type": "object",
        "properties": map[string]interface{}{
            "input": map[string]interface{}{
                "type":        "string",
                "description": "输入参数",
            },
        },
        "required": []string{"input"},
    }

    return &MyTool{
        BaseTool: toolkit.NewBaseTool(
            "my_tool",           // 工具名称
            "我的自定义工具",      // 工具描述
            "1.0.0",             // 版本
            parameters,          // 参数定义
            true,                // 支持直接调用
        ),
    }
}

func (t *MyTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
    input, ok := params["input"].(string)
    if !ok {
        return &toolkit.ToolResult{
            Success: false,
            Error:   fmt.Errorf("input 参数缺失"),
        }, nil
    }

    // 执行工具逻辑
    result := "处理结果: " + input

    return &toolkit.ToolResult{
        Success: true,
        Content: result,
        Metadata: map[string]interface{}{
            "input": input,
        },
    }, nil
}
```

### 2. 注册自定义工具

```go
registry.Register(NewMyTool())
```

## 工具接口定义

```go
// Tool 工具接口
type Tool interface {
    Name() string
    Description() string
    Version() string
    Parameters() map[string]interface{}
    Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error)
    CanDirectCall() bool
}

// ToolResult 工具执行结果
type ToolResult struct {
    Success  bool
    Content  string
    Error    error
    Metadata map[string]interface{}
}
```

## 架构设计

```
ToolRegistry (统一注册表)
├── 直接调用 (< 1ms) - 高性能路径
│   └── tool.Execute(ctx, params)
├── MCP 调用 (~10ms) - 远程调用
│   └── registry.ToMCPTools() → MCP Server
└── Function 适配器 (~5ms) - 向后兼容
    └── registry.ToFunctions() → FunctionRegistry
```

## 测试

```bash
# 运行单元测试
go test ./internal/toolkit/... -v

# 运行测试并查看覆盖率
go test ./internal/toolkit/... -cover
```

## 最佳实践

1. **参数验证**：在 Execute 方法开头验证必需参数
2. **错误处理**：使用 ToolResult.Error 返回错误，不要 panic
3. **上下文传递**：支持 context.Context 以便取消操作
4. **幂等性**：工具执行应该是幂等的（相同输入产生相同输出）
5. **文档完整**：提供清晰的工具描述和参数说明
6. **变更追踪**：对会修改文件的工具，在 `ToolResult.Metadata` 中返回 `mutated_paths`（字符串数组），用于回滚与审计。

## 相关文档

- [工具调用框架设计](../../docs/mcp/06-tool-framework.md)
- [工具迁移计划](../../docs/mcp/07-tool-migration-plan.md)
- [MCP 架构设计](../../docs/mcp/02-architecture.md)
