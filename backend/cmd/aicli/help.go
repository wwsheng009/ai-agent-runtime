package main

const rootCommandLongHelp = `aicli 是 ai-agent-runtime 的命令行工具，默认进入交互式 chat。

常用能力：
  - 初始化配置：aicli init [--global]
  - 登录 provider：aicli login
  - 交互式 chat / 顶层 resume / session resume / slash commands
  - 配置查看与 provider 管理
  - doctor 诊断、端点测试、上下文测试
  - headless exec、图片生成、MCP、管道模式

首次推荐路径：
  aicli init --global
  aicli login --provider openai --protocol openai --base-url https://api.openai.com --api-key sk-... --set-default
  aicli

文档入口：
  - docs/aicli/quickstart.md
  - docs/aicli/install.md
  - docs/aicli/faq.md
  - docs/aicli/exec.md
  - docs/aicli/agents.md
  - docs/aicli/README.md
  - docs/skill_runtime/aicli_skills_usage.md`

const rootCommandExampleHelp = `  # 首次使用
  aicli init --global
  aicli login --provider openai --protocol openai --base-url https://api.openai.com --api-key sk-... --set-default
  aicli

  # 配置与 provider
  aicli config
  aicli config --provider openai
  aicli config --models
  aicli provider list
  aicli provider show openai --models

  # 交互式聊天（默认）
  aicli
  aicli --prompt "检查当前项目"            # 自动提交一次，完成后继续交互
  aicli chat
  aicli chat --prompt "检查当前项目"
  aicli chat --provider openai --model gpt-4.1
  aicli resume
  aicli resume session_xxx
  aicli chat --resume

  # 诊断
  aicli doctor provider
  aicli doctor provider --provider openai --model gpt-4.1

  # 测试端点 / 上下文
  aicli test --model gpt-4 --message "Hello"
  aicli test --provider openai --stream
  aicli context --model gpt-4.1

  # 图片 / headless
  aicli image "一只在月光下奔跑的猫"
  aicli exec "解释这段代码的作用"`

const configCommandLongHelp = `交互式管理配置；也可通过 flags 输出 providers、provider_groups、models 等只读信息。

配置加载顺序、starter 与 login 后常见查看方式见 docs/aicli/install.md；
providers 为空等排错见 docs/aicli/faq.md。`

const configCommandExampleHelp = `  aicli config                        # 默认进入交互式配置管理
  aicli config --no-tui                # 显示配置摘要
  aicli config --provider nvidia       # 只显示指定 provider
  aicli config --groups                # 只显示 provider groups
  aicli config --models                # 列出所有可用模型
  aicli config --tui                   # 显式进入交互式配置管理
  aicli config --output json           # 结构化 JSON 输出`

const testCommandLongHelp = `向配置的 endpoint 发送测试请求。

用于快速验证 provider base_url、鉴权与模型是否可用；比 doctor 更轻量。
常见 401 / providers 为空等问题见 docs/aicli/faq.md。
端点测试示例见 docs/aicli/install.md。`

const testCommandExampleHelp = `  aicli test --model gpt-4 --message "Hello"
  aicli test --provider nvidia --message "测试"
  aicli test --provider bigmodel --path "/v1/messages" --message "Hello"
  aicli test --stream                                   # 测试流式响应
  aicli test --model gpt-4 --output text               # 只输出结果文本
  aicli test --model gpt-4 --output json               # 输出结构化 JSON`

const contextCommandLongHelp = `测试模型的最大上下文窗口和最大生成长度。

通过逐步增大输入 / 输出探测 provider 实际可接受的 token 边界。
相关示例与配置说明见 docs/aicli/install.md。`

const contextCommandExampleHelp = `  aicli context --model glm-4.7
  aicli context --provider nvidia --model gpt-4
  aicli context --model gpt-4 --step 5000
  aicli context --model gpt-4 --max-output-only
  aicli context --model gpt-4 --start 10000 --end 20000
  aicli context --model gpt-4 --output json`

const pipeCommandLongHelp = `从标准输入读取数据，结合提示词发送给 AI 处理。

支持两种模式：
  - 缓冲模式（默认）：读取所有输入后一次性发送
  - 流式模式（--stream）：实时处理管道输入

使用场景：
  - 日志分析：tail -f app.log | aicli pipe -p "分析异常"
  - 文件处理：cat file.txt | aicli pipe -p "翻译成法语"
  - CI/CD：git diff | aicli pipe -p "生成 PR 描述"

与 headless 代理（tools / resume / review）的边界见 docs/aicli/exec.md；
管道示例见 docs/aicli/install.md。`

const pipeCommandExampleHelp = `  # 日志监控
  tail -f app.log | aicli pipe -p "如果出现异常，请通知我"

  # 翻译
  echo "Hello World" | aicli pipe -p "翻译成中文"

  # 流式处理
  tail -f app.log | aicli pipe -p "分析日志" --stream

  # 指定模型
  cat data.json | aicli pipe -p "格式化这个 JSON" --model gpt-4

  # JSON 输出
  echo "Hello" | aicli pipe -p "翻译成中文" --output json

  # CI 场景
  git diff main...HEAD | aicli pipe -p "为新代码生成 PR 描述"`
