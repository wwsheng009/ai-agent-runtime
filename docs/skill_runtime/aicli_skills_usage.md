# `aicli` / `aicli chat` 与 Skills 使用说明

> 迁移说明（2026-03-30）：
> - 当前 `aicli` 实现在 `E:\projects\ai\ai-agent-runtime\backend\cmd\aicli`
> - 下面的 `go run ./cmd/aicli ...` 示例默认都在 `E:\projects\ai\ai-agent-runtime\backend` 目录执行

## 目标

`aicli`（默认进入 chat）和 `aicli chat` 现在都支持把 skills 作为 AI 可调用 functions 暴露给模型。

当前链路：

`aicli` / `aicli chat` -> route candidate skills -> model tool selection -> skill executor -> workflow / MCP / LLM

如果你想看更底层的“模型如何感知 skill、如何发起 `skill__*` 调用、`prompt` 在哪里被消费”，请同时阅读：

- `docs/skill_runtime/skill_invocation_mechanism.md`

如果你需要更偏“CLI 使用面”的说明，例如安装、配置加载顺序、`aicli` 默认 chat / `aicli chat` 常用命令、`/call`、`/tool`、`/skill`、`/skills` 这类 chat 斜杠命令，请同时阅读：

- `docs/aicli/README.md`
- `docs/aicli/install.md`

## Skills 来源

- 系统级 skills：`skills_runtime.skill_dir`
- 外部扩展 skills：`skills_runtime.extra_skill_dirs`
- 临时追加目录：`aicli --skills-dir <dir>`（等价于 `aicli chat --skills-dir <dir>`）

## `aicli` / `aicli chat` 加载顺序

当前 `aicli` / `aicli chat` 会按下面顺序装配可调用能力：

1. 初始化 `FunctionRegistry`
2. 加载 runtime toolkit tools
3. 如已启用 MCP，则合并 MCP tools
4. 初始化 skills runtime
5. 将每个 skill 注册为 `skill__*` function
6. 预构建非 skill builtin tools 的 schema cache

因此，最终发送给模型的可调用能力集合由两部分组成：

- builtin tools
- route 选中的 skill functions

而不是“每轮都全量发送所有 tool + 所有 skill”。

当前这些对象在实现上已收口到统一 catalog 管理：

- function registry
- protocol-specific function builder
- builtin tool schema cache
- request-time function selection
- skills binding / exposure cache
- builtin tool / skill function 的统一 descriptor 视图

在交互式 `aicli chat` 中，也可以直接查看这份 catalog：

```text
/functions
/functions --json
/functions Run shell command echo PREVIEW_OK
/functions Run shell command echo PREVIEW_OK --json
/function skill__run_shell_command
/function skill__run_shell_command --json
/skills
/skills image
```

其中：

- `/functions` 会列出当前已加载的 builtin tools 与 skill functions
- `/functions --json` 会输出机器可读的 catalog 视图
- `/functions <prompt>` 会预览该 prompt 下最终暴露给模型的 builtin / skill functions
- `/functions <prompt> --json` 会输出机器可读的 exposure report
- `/function <name>` 会显示单个 function 的 kind、capability、category、triggers 与 metadata
- `/function <name> --json` 会输出机器可读的 descriptor 视图
- `/skills` 会列出当前已加载的 skill functions，并提示输入编号或 skill 名称，随后再输入 prompt 直接执行
- `/skills <query>` 会先按关键字过滤 skill，再进入选择
- `/skill <name> <prompt>` 仍然保留用于直接执行已知 skill

## 暴露控制

### `skills-top-k`

控制每轮请求最多暴露给模型的候选 skill 数量。

```bash
aicli chat --provider nvidia --skills-top-k 3
```

配置项：

```yaml
skills_runtime:
  aicli_skill_exposure_top_k: 5
```

### `skills-mode`

控制 skills 与内置 tools 的暴露关系。

- `auto`
  - 默认模式
  - 内置 tools 保持可用
  - skills 先 route，再按 top-K 暴露

- `prefer`
  - 如果 prompt 命中了 skill 候选，只暴露候选 skills
  - 如果没有 skill 命中，则保留内置 tools

- `only`
  - 仅暴露经 route 筛出的候选 skills
  - 不暴露内置 tools

CLI 示例：

```bash
aicli chat --provider nvidia --skills-top-k 1 --skills-mode prefer
```

配置示例：

```yaml
skills_runtime:
  aicli_skill_exposure_mode: auto
```

## 什么时候用 `prefer`

适用于同一类事情既能被内置 tool 做，也能被 skill 做，但你希望优先走 skill 的场景。

典型例子：

- shell 命令执行
- 文件查看
- URL 获取

## 什么时候用 `only`

适用于：

- 严格验证 skill 选择行为
- 不希望模型绕过平台封装，直接走底层工具

这个模式更接近 `Intent Router -> Skill Engine -> Tool Call`。

### `skills-debug`

打印当前请求的统一 function selection 视图。

```bash
aicli chat --provider nvidia --skills-top-k 1 --skills-mode prefer --skills-debug
```

适用于：

- 调试 prompt 为什么没有命中期望 skill
- 检查 `prefer / only` 模式下 builtin tools 是否被抑制
- 检查最终真正发给模型的 `builtin + skill` 函数集合
- 观察 catalog 总量、route score、matched_by 与 details

`--skills-debug` 与 `/functions <prompt>` 现在共用同一份 exposure report，因此 CLI 预览和请求期调试看到的是同一套选择结果。`/functions --json` 和 `/functions <prompt> --json` 则直接把这份结果以结构化 JSON 输出出来，便于日志和外部工具消费。

如果启用了 `aicli chat --log-dir ...`，每轮 request 日志现在也会带上同一份 `function_exposure`，以及：

- `exposed_function_count`
- `exposed_functions`

这样终端预览、`skills-debug` 和会话日志三者看到的是同一套最终暴露结果。

这些 request / response / tool 事件现在还会共享同一组：

- `turn_id`
- `request_id`

因此可以按一轮用户输入或某一次模型请求，把暴露、响应、工具执行和结果摘要串起来。

工具真正执行后，会话日志还会追加一条 `tool_execution_summary`，包含：

- `call_count`
- `success_count`
- `error_count`
- `functions`
- 每个调用的 `tool_call_id / function / success / error / result_preview`

这样一次会话里可以同时看到：

- request 阶段暴露了哪些 functions
- 模型最终调用了哪些 function
- 每个 function 的执行结果摘要

## 当前实现优化点

为了降低 `aicli chat` 每轮请求的构造成本，当前实现已做两类优化：

### 1. builtin tool schema 缓存

会话初始化后，非 skill 的 builtin function schema 会缓存下来。

这样每轮请求不需要再：

- 遍历整个 `FunctionRegistry`
- 重新排序所有 functions
- 重新为 builtin tools 组 schema

### 2. skill router / skill schema 预构建

skills runtime 初始化后，会预构建：

- skill exposure router
- skill schema cache
- skill function 顺序索引

这样每轮请求只需要：

- route 当前 prompt
- 通过统一 catalog 选择 builtin / skill 函数集合
- 从 cache 中取出命中的 skill schema

而不需要每轮重新创建 router、重新遍历 registry，或在 `message.go` / `skills_integration.go` 两处分别拼装 schema。

## Prompt 写法建议

### 显式验证 skill 调用

```text
Do not use bash directly. You must call tool skill__run_shell_command with prompt set to echo AICLI_SKILL_OK, then return only the tool output.
```

### 验证 route-first 行为

配合 `--skills-mode prefer`：

```text
Run shell command echo AICLI_PREFER_OK and return only the command output.
```

### 一般任务

```text
Read file E:\\projects\\ai\\ai-agent-runtime\\README.md and give me a 3-line summary.
```

```text
Fetch https://example.com and summarize the page in 5 bullet points.
```

## 已验证示例

2026-03-10 实测：

```bash
cd E:\projects\ai\ai-agent-runtime\backend
go run ./cmd/aicli chat --provider nvidia --no-interactive --skills-top-k 1 --skills-mode prefer --message "Run shell command echo AICLI_PREFER_OK and return only the command output."
```

结果：

- 模型命中 `skill__run_shell_command`
- 最终输出 `AICLI_PREFER_OK`

## 当前限制

- `aicli` 仍不是统一的 `agent` 前门，而是 skills-as-functions 集成模式
- `only` 模式下，如果 route 没命中 skill，模型将没有可用内置 tool
- 当前只对 skills 做 top-K 暴露控制，不对普通 functions 做 top-K 控制
- skill 的 AI 可发现性依赖 route 暴露结果和 function description；模型并不会直接读取 `skill.yaml`
