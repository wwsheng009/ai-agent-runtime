# UI 组件库架构设计

## 目录结构

```
cmd/aicli/
├── ui/                           # UI 组件库
│   ├── README.md                 # 组件说明文档（本文件）
│   ├── theme.go                  # 主题定义（颜色、样式）
│   ├── icons.go                  # 图标定义
│   ├── input.go                  # 输入提示符组件
│   ├── inputbox.go               # 输入框组件（支持历史记录）
│   ├── output.go                 # 输出显示组件
│   ├── message.go                # 消息显示组件
│   ├── welcome.go                # 欢迎界面组件
│   ├── info.go                   # 信息显示组件（会话信息等）
│   ├── separator.go              # 分隔线组件
│   ├── progress.go               # 进度条组件
│   ├── shell_feedback.go         # Shell 命令执行反馈组件
│   ├── status.go                 # 状态指示组件（成功/错误/警告）
│   ├── statusbar.go              # 状态栏组件（固定显示重要状态）
│   ├── terminal.go               # 终端控制组件（光标位置、屏幕更新）
│   └── layout.go                 # 屏幕布局管理器（区域划分）
```

## 组件说明

### 1. theme.go - 主题定义
- 定义颜色主题（支持亮色/暗色模式）
- 定义样式常量（边框样式、对齐方式等）
- 提供主题切换功能
- 集成 color 包的能力

### 2. icons.go - 图标定义
- 定义所有使用的图标字符
- 支持 Unicode 图标
- 提供 GetIcon(name) 方法获取图标

### 3. input.go - 输入提示符组件
- PromptUser() - 用户输入提示符（仅保留 `>` 前缀，带颜色）
- PromptAssistant() - 助手输入提示符（通常不使用，但保留扩展性）
- 输入框高亮和光标支持

### 4. output.go - 输出显示组件
- FormatOutput() - 格式化输出文本
- 支持代码块高亮
- 支持自动换行和缩进

### 5. message.go - 消息显示组件
- DisplayUserMessage() - 显示用户消息
- DisplayAssistantMessage() - 显示助手消息
- DisplaySystemMessage() - 显示系统消息
- 支持消息分组和时间戳

### 6. welcome.go - 欢迎界面组件
- PrintWelcome() - 打印欢迎界面
- 显示应用信息、版本、快捷键说明
- ASCII 艺术字（可选）

### 7. info.go - 信息显示组件
- PrintSessionInfo() - 打印会话信息（Provider、Model、Stream 等）
- PrintStatus() - 打印状态信息
- 表格格式化输出

### 8. separator.go - 分隔线组件
- PrintSeparator() - 打印分隔线
- PrintThinSeparator() - 打印细分隔线
- 支持自定义分隔符样式

### 9. progress.go - 进度条组件
- PrintProgress() - 打印进度条
- PrintSpinner() - 打印旋转加载器
- 支持百分比显示

### 10. shell_feedback.go - Shell 命令执行反馈组件
- DisplayShellCommand() - 显示执行的命令
- DisplayShellOutput() - 显示命令输出
- DisplayShellError() - 显示命令错误
- 命令执行状态指示

### 11. status.go - 状态指示组件
- PrintSuccess() - 成功提示
- PrintError() - 错误提示
- PrintWarning() - 警告提示
- PrintInfo() - 信息提示
- 支持不同颜色和图标

### 12. statusbar.go - 状态栏组件
- Update() - 更新状态栏项
- SetThinking() - 设置 AI 思考状态（固定显示在状态栏）
- SetModel(), SetTokens(), SetMsgCount() - 更新重要状态信息
- 固定在屏幕底部，不影响聊天信息流

### 13. terminal.go - 终端控制组件
- MoveTo(), MoveToRow() - 移动光标位置
- Clear(), ClearFromCursor() - 清屏和光标清除
- SaveCursor(), RestoreCursor() - 光标位置保存和恢复
- HideCursor(), ShowCursor() - 光标显示控制

### 14. layout.go - 屏幕布局管理器
- NewLayout() - 创建新的布局（支持简单/高级模式）
- ChatArea(), InputArea(), StatusArea() - 获取各区域
- PrintMessage() - 在聊天区域打印消息
- Render() - 渲染完整布局

### 15. inputbox.go - 输入框组件
- Read() - 读取单行输入
- ReadMultiLine() - 读取多行输入
- 支持历史记录导航
- 输入验证和清理

## 设计原则

1. **单一职责** - 每个组件只负责一个功能
2. **可组合性** - 组件可以互相组合使用
3. **可配置性** - 支持通过主题自定义样式
4. **可测试性** - 组件逻辑易于单元测试
5. **向后兼容** - 不破坏现有功能
6. **性能优先** - 避免不必要的字符串分配

## 布局规范

### 1. 启动头部
- 欢迎页中的键值信息使用固定 label 列宽
- `Version:`、`Description:` 等元信息左对齐到同一列
- 欢迎页提示项统一使用 `  图标 + 空格 + 文本` 结构

### 2. 会话信息区
- `PrintSessionInfo()` 输出的 `Provider / Model / Stream` 使用固定 label 宽度
- `Protocol / Endpoint / Host / Auth Keys / Timeout` 作为子级行，统一缩进到系统图标后的内容列
- chat 启动后追加的 `MCP / Reasoning Effort / Session / Title / History / Skills / Skills Mode / Skills Top-K` 使用另一组固定 label 宽度，保持纵向对齐

### 3. 消息区 Gutter
- `👤`、`>`、`🤖`、`ℹ️`、`🔧工具>`、`❌` 等前缀视为独立 gutter 列
- 首行格式为 `前缀列 + 内容列`
- 多行消息的续行必须直接对齐到首行内容列，而不是简单补两个空格
- assistant 内容列宽由 `AssistantContentIndent()` / `IndentAssistantContent()` 统一提供，避免不同调用方各自计算

### 4. 流式输出
- assistant 流式内容统一先缓冲，再在 finalize 阶段整体交给 Markdown formatter 处理
- 不在 chunk 级别混用“纯文本直出”和“Markdown 重放”，避免表格、列表、代码块在半结构状态下漏出原文
- actor 模式下如果已经发生 assistant delta 渲染，最终响应应走统一收口逻辑，不再整段 fallback 重打

### 5. Prompt / Thinking / Timeline
- 在管道或重定向场景中，`>` 提示符输出后要先换行，再显示 thinking 或 assistant 内容
- `[thinking] ...` 和异步 timeline 行共享 assistant gutter
- thinking、timeline、assistant 正文需要落在同一内容列，避免图标列和正文列交错

### 6. 启动选择器
- `检测到历史会话` summary 采用 `label: value` 列式布局
- 启动选项 `[1] / [2] / [3]` 使用固定编号列宽
- 历史会话列表中，`session id`、`state`、`最后使用时间` 按列对齐，标题作为下一行说明文本显示

### 7. 变更准则
- 新增 UI 输出前，优先判断它属于：头部信息、消息区、时间线、状态元信息、选择器 这五类中的哪一类
- 同一类输出优先复用已有 helper，而不是重新拼字符串
- 新增多行输出时，必须补一条“续行是否对齐到内容列”的测试
- 新增流式渲染逻辑时，必须补一条“chunk 被拆开时仍保持最终布局正确”的测试

## 使用示例

### 基础组件使用

```go
import "github.com/ai-gateway/gateway/cmd/aicli/ui"

// 使用主题
theme := ui.GetTheme(ui.ThemeDark)

// 显示欢迎界面
ui.PrintWelcome()

// 显示会话信息
ui.PrintSessionInfo(ui.SessionInfo{
    ProviderName: "openai",
    Protocol:     "openai",
    ModelName:    "gpt-4.1",
    EndpointURL:  "https://api.openai.com/v1/chat/completions",
    Host:         "api.openai.com",
})

// 获取用户输入
input := ui.PromptUser()

// 显示用户消息
ui.DisplayUserMessage(input)

// 显示助手消息
ui.DisplayAssistantMessage(response)

// 显示成功提示
ui.PrintSuccess("操作完成")
```

### 布局系统使用

```go
// 创建布局
layout := ui.NewLayout(ui.LayoutAdvanced)
layout.Enable()

// 创建状态栏
statusBar := ui.NewStatusBar(layout.StatusArea().Row)
statusBar.SetTerminal(layout.Terminal())
statusBar.WithDefaultStatus()

// 更新状态
statusBar.SetModel("gpt-4")
statusBar.SetTokens(1234)
statusBar.SetMsgCount(10)

// 显示 AI 思考状态
statusBar.SetThinking(true)
// ... 处理请求 ...
statusBar.SetThinking(false)

// 创建输入框
inputBox := ui.NewInputBox(layout)
input, err := inputBox.Read()
```

## 依赖关系

- `github.com/fatih/color` - 颜色支持
- `stringr` - 字符串处理（如需要）
- `internal/config` - 配置读取

## 迁移计划

1. 创建主题和图标定义文件
2. 创建基础组件（separator, status）
3. 创建消息渲染组件（message, output）
4. 创建欢迎和信息显示组件
5. 修改 chat.go 以使用新组件
6. 逐步替换现有的 fmt.Printf 调用
7. 测试和优化

## 注意事项

- 保持终端兼容性，不使用过于依赖终端特性的功能
- 考虑无彩色模式的回退方案
- 确保输出在不同终端宽度下正常显示
- 避免输出过多控制字符影响性能
