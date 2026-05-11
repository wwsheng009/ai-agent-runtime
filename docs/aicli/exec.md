# aicli exec 操作手册

`aicli exec` 是面向脚本、CI 和自动化任务的 headless 执行入口。它复用 `aicli chat` 的 provider/model、profile/agent、tools/skills、权限策略和会话能力，但输出契约更稳定，适合被程序消费。

本文档涵盖：

- [一、适用场景](#一适用场景)
- [二、基础用法](#二基础用法)
- [三、输出模式](#三输出模式)
- [四、会话与恢复](#四会话与恢复)
- [五、代码审查](#五代码审查)
- [六、Schema 校验](#六schema-校验)
- [七、配置覆盖](#七配置覆盖)
- [八、权限、工具和 Skills](#八权限工具和-skills)
- [九、退出码](#九退出码)
- [十、CI 使用建议](#十ci-使用建议)
- [十一、排障](#十一排障)

---

## 一、适用场景

优先使用 `aicli exec` 的场景：

- 在 CI 中执行一次性任务，例如生成摘要、检查变更、输出结构化 JSON。
- 在脚本中调用模型，并需要稳定的 stdout/stderr 契约。
- 需要 JSONL 事件流，跟踪工具调用、warning、turn 状态等执行过程。
- 需要从命令行恢复历史会话，而不是进入交互式 chat 后使用 `/resume`。
- 需要专用代码审查入口，例如 `aicli exec review --base main`。

仍可继续使用其他命令的场景：

- 需要人类交互式对话：使用 `aicli` 或 `aicli chat`。
- 只想把 stdin 送给 LLM 做一次轻量处理：`aicli pipe` 仍可用。
- 需要测试 provider 端点连通性：使用 `aicli test`。

---

## 二、基础用法

### 发送单条提示词

```bash
aicli exec "解释这个仓库的启动流程"
```

默认 stdout 只输出最终 assistant 文本。配置摘要、warning、debug 日志应走 stderr。

### 结合 stdin

```bash
cat main.go | aicli exec -p "分析这段代码的风险"
```

组合规则：

| 输入方式 | 最终 prompt |
|---|---|
| `aicli exec "prompt"` | 命令行参数 |
| `echo data \| aicli exec` | stdin 内容 |
| `echo data \| aicli exec -p "指令"` | `指令` + stdin 内容 |

### 指定 provider 和 model

```bash
aicli exec --provider nvidia --model gpt-4.1 "生成变更摘要"
```

短参数：

```bash
aicli exec -P nvidia -m gpt-4.1 "生成变更摘要"
```

### 使用 profile 或 agent

```bash
aicli exec --profile dev "检查当前实现"
aicli exec --profile ./profiles/dev --agent reviewer "审查代码"
```

`exec` 会复用 `chat` 的 profile/agent 解析逻辑，因此 profile 中的 system prompt、runtime config、tool policy、skills 目录等会继续生效。

### 超时控制

```bash
aicli exec --timeout 5m "执行一次较长任务"
aicli exec --request-timeout 60s "只限制单次 LLM 请求超时"
```

区别：

| 选项 | 含义 |
|---|---|
| `--timeout` | 整次 `exec` wall-clock 上限 |
| `--request-timeout` | 单次 LLM 请求上限，留空使用配置 |

---

## 三、输出模式

`exec` 有三种主要输出模式。

| 模式 | 命令 | stdout | 适合场景 |
|---|---|---|---|
| 默认 text | `aicli exec "..."` | 最终 assistant 文本 | 人类阅读、简单脚本 |
| 最终 JSON | `aicli exec --output json "..."` | 单个 JSON 对象 | CI 读取最终结果 |
| JSONL 事件流 | `aicli exec --json "..."` | 每行一个 JSON 事件 | 实时观察过程、工具调用追踪 |

### 默认 text

```bash
aicli exec "写一句发布摘要"
```

stdout 示例：

```text
MOCK_OK: 写一句发布摘要
```

### 最终 JSON

```bash
aicli exec --output json "写一句发布摘要"
```

stdout 示例：

```json
{"status":"completed","message":"...","session_id":"...","model":"...","provider":"...","usage":{"input_tokens":10,"output_tokens":20,"total_tokens":30},"duration_ms":1234}
```

如果启用 root 级 `--envelope`：

```bash
aicli --envelope exec --output json "写一句发布摘要"
```

输出会包装成统一 envelope：

```json
{"ok":true,"command":"exec","data":{"status":"completed","message":"..."}}
```

### JSONL 事件流

```bash
aicli exec --json "创建一个 hello world 程序"
```

stdout 每行都是完整 JSON：

```json
{"version":1,"sequence":1,"timestamp":"...","thread_id":"thread_xxx","type":"thread.started","data":{"thread_id":"thread_xxx","model":"...","provider":"..."}}
{"version":1,"sequence":2,"timestamp":"...","thread_id":"thread_xxx","type":"turn.started","data":{"turn_id":"turn_xxx","prompt":"创建一个 hello world 程序"}}
{"version":1,"sequence":3,"timestamp":"...","thread_id":"thread_xxx","type":"turn.completed","data":{"turn_id":"turn_xxx","status":"completed"}}
```

常见事件类型：

| type | 含义 |
|---|---|
| `thread.started` | 执行线程启动 |
| `turn.started` | 一轮模型交互开始 |
| `item.started` | 工具调用或子项开始 |
| `item.updated` | 消息、工具或其他子项更新 |
| `item.completed` | 工具调用或子项结束 |
| `turn.completed` | 一轮交互成功结束 |
| `turn.failed` | 一轮交互失败 |
| `warning` | 可恢复警告 |
| `error` | 错误事件 |

`--json` 和 `--output json` 不能同时使用。前者是过程事件流，后者是最终结果 JSON。

### 写入最后消息文件

```bash
aicli exec --output-last-message result.md "生成操作结果"
```

`--output-last-message` 不改变 stdout。它只把最终 assistant 文本写入指定文件，适用于 CI artifact 或后续脚本读取。

---

## 四、会话与恢复

默认情况下，`aicli exec` 会按 chat 现有机制创建或使用 runtime session。你可以显式指定 session 目录：

```bash
aicli exec --session-dir ./.aicli/sessions "记录这次分析"
```

### 临时会话

如果不希望写入持久化会话：

```bash
aicli exec --ephemeral "只执行一次，不保存会话"
```

限制：

- `--ephemeral` 不创建、不写入 runtime session。
- `--ephemeral` 不能与 `--session-dir` 或 `--title` 同时使用。
- `aicli exec resume` 不支持 `--ephemeral`。

### 设置会话标题

```bash
aicli exec --title "release-review" "分析当前发布风险"
```

### 恢复最近会话

```bash
aicli exec resume --last
```

没有 prompt 时，`resume --last` 只加载最近可恢复会话，并输出最后一条 assistant 消息，不发送新的模型请求。

### 恢复最近会话并继续提问

```bash
aicli exec resume --last "继续上次的任务，给出下一步"
```

`--last` 后面的 positional args 全部作为 prompt，不会被猜测成 session id。

### 恢复指定会话

```bash
aicli exec resume session_20260511090822_oY0AShHr
```

恢复指定会话并继续提问：

```bash
aicli exec resume session_20260511090822_oY0AShHr "总结这次会话"
```

### resume 的常用组合

```bash
# JSONL 事件流
aicli exec resume --last --json "继续执行"

# 写出最后消息
aicli exec resume --last --output-last-message last.md

# 指定模型继续
aicli exec resume --last --provider nvidia --model gpt-4.1 "继续"
```

---

## 五、代码审查

`aicli exec review` 是代码审查专用入口。它会读取 Git diff，构造审查 prompt，再走与 `exec` 相同的模型执行链路。

### 默认审查未提交更改

```bash
aicli exec review
```

等价于：

```bash
aicli exec review --uncommitted
```

未提交更改包括：

- staged 文件
- unstaged 文件
- untracked 文件

### 使用自定义审查指令

```bash
aicli exec review "重点检查安全漏洞和权限绕过"
```

positional args 是审查指令，不是审查目标。未指定目标时仍默认审查未提交更改。

### 审查相对基础分支的变更

```bash
aicli exec review --base main
```

内部使用类似 `git diff main...HEAD` 的范围。

### 审查指定提交

```bash
aicli exec review --commit abc1234
```

可附带提交标题：

```bash
aicli exec review --commit abc1234 --commit-title "fix auth bug"
```

### 输出 JSON 结果

```bash
aicli exec review --base main --output json
```

### 保存审查结果

```bash
aicli exec review --uncommitted --output-last-message review.md
```

### diff 截断

当 diff 很大时，`exec review` 会按字节上限截断，并发出 warning。审查结果可能不完整，此时建议缩小范围：

```bash
git diff --stat
aicli exec review --base main --output-last-message review-main.md
```

---

## 六、Schema 校验

`--output-schema` 用于校验最终 assistant 消息。它不校验 JSONL 事件本身。

适用场景：

- 要求模型返回固定 JSON 结构。
- CI 中需要明确区分“执行成功但格式不合格”。
- 下游脚本依赖特定字段。

### 使用 schema 文件

`schema.json`：

```json
{
  "type": "object",
  "required": ["summary"]
}
```

运行：

```bash
aicli exec --output-schema schema.json "只返回 JSON，包含 summary 字段"
```

如果最终 assistant 消息不是合法 JSON，或缺少 required 字段，命令返回 exit code `3`。

### 使用内联 schema

Linux / macOS shell：

```bash
aicli exec --output-schema '{"type":"object","required":["summary"]}' "只返回 JSON"
```

PowerShell 中建议优先使用 schema 文件，避免引号转义导致 CLI 收到的 JSON 不是预期内容：

```powershell
@'
{"type":"object","required":["summary"]}
'@ | Set-Content -Path schema.json -Encoding ASCII

aicli exec --output-schema schema.json "只返回 JSON"
```

### schema 失败输出

schema 校验失败时：

- exit code 为 `3`
- JSONL 模式会输出 `turn.failed` 和 `error`
- 不输出 `turn.completed`
- 不写成功的最终结果

---

## 七、配置覆盖

root 命令已经使用 `-c/--config` 表示配置文件路径，因此 `exec` 使用 `-C/--config-override` 做细粒度覆盖。

### 指定配置文件

```bash
aicli --config ~/.aicli/config.yaml exec "hello"
```

短参数：

```bash
aicli -c ~/.aicli/config.yaml exec "hello"
```

### 覆盖模型

```bash
aicli exec -C model=gpt-4.1 "使用临时模型执行"
```

### 覆盖 provider endpoint

```bash
aicli exec \
  -C provider.base_url=http://127.0.0.1:8080 \
  -C provider.api_key=test-key \
  "使用本地 mock 服务"
```

当前支持的 key：

| key | 说明 |
|---|---|
| `model` | 覆盖默认 provider 的默认模型 |
| `provider.base_url` | 覆盖默认 provider 的 base URL |
| `provider.api_key` | 覆盖默认 provider 的 API key |
| `provider.forward_url` | 覆盖默认 provider 的 forward URL |

不支持的 key 会返回参数错误。

---

## 八、权限、工具和 Skills

`exec` 复用 `chat` 的工具和权限机制。

### 权限模式

```bash
aicli exec --permission-mode plan "检查代码，不要修改文件"
aicli exec --permission-mode accept_edits "修复格式问题"
aicli exec --permission-mode bypass_permissions "执行自动修复"
```

快捷模式：

```bash
aicli exec --yolo "执行自动修复"
```

`--yolo` 等价于 `--permission-mode bypass_permissions`。在 CI 或不可信输入场景中应谨慎使用。

### 审批复用

```bash
aicli exec --approval-reuse off "运行任务"
aicli exec --approval-reuse session_readonly_shell "运行只读检查"
```

可选值：

| 值 | 说明 |
|---|---|
| `off` | 不复用审批 |
| `session_readonly_shell` | 当前 session 内复用只读 shell 审批 |
| `team_readonly_shell` | team 范围内复用只读 shell 审批 |

### 禁用工具

```bash
aicli exec --disable-tools "只用模型回答，不暴露工具"
```

### Skills 选项

```bash
aicli exec \
  --skills-dir ./skills \
  --skills-top-k 5 \
  --skills-mode auto \
  "根据仓库选择合适技能"
```

常用选项：

| 选项 | 说明 |
|---|---|
| `--skills-dir` | 追加外部 skills 目录，可重复指定 |
| `--skills-top-k` | 暴露给模型的候选 skills 数量 |
| `--skills-mode` | skills 暴露模式，常见值为 `auto`、`prefer`、`only` |
| `--skills-debug` | 输出 skill route 候选和暴露结果 |

### 图片附件

```bash
aicli exec --image screenshot.png "分析这个截图"
```

可重复指定：

```bash
aicli exec -i before.png -i after.png "比较两张截图"
```

---

## 九、退出码

`aicli exec` 的退出码用于脚本判断失败类型。

| exit code | 场景 |
|---:|---|
| `0` | 成功 |
| `1` | provider、API、工具执行或其他运行时失败 |
| `2` | 参数或配置错误 |
| `3` | `--output-schema` 校验失败 |
| `124` | 整次执行超时 |
| `130` | 用户中断 |

示例：

```bash
aicli exec --output-schema schema.json "只返回 JSON"
case "$?" in
  0) echo "ok" ;;
  3) echo "model output schema invalid" ;;
  124) echo "timeout" ;;
  *) echo "failed" ;;
esac
```

PowerShell：

```powershell
aicli exec --output-schema schema.json "只返回 JSON"
switch ($LASTEXITCODE) {
  0 { "ok" }
  3 { "model output schema invalid" }
  124 { "timeout" }
  default { "failed: $LASTEXITCODE" }
}
```

---

## 十、CI 使用建议

### 生成最终结果 JSON

```bash
aicli exec --output json "总结当前变更" > result.json
```

### 校验 JSONL 事件流

```bash
aicli exec --json "执行任务" 2>stderr.log | while read line; do
  echo "$line" | jq . >/dev/null || exit 1
done
```

不要使用 `2>&1` 合并 stderr，否则 stderr 中的 warning/debug 可能破坏 JSONL stdout。

### 代码审查并保存结果

```bash
aicli exec review --base main --output-last-message review.md
```

### 生成提交说明

如果只是根据 diff 生成提交说明，推荐把 diff 通过 stdin 提供给模型，并禁用工具：

```bash
git diff --cached | aicli exec --disable-tools -p "根据以下 staged diff 写一个中文提交说明。只输出提交信息，不要解释。"
```

未暂存的变更可使用：

```bash
git diff HEAD | aicli exec --disable-tools -p "根据以下 git diff 写一个中文提交说明。只输出提交信息，不要解释。"
```

生成 Conventional Commits 风格提交说明：

```bash
git diff --cached | aicli exec --disable-tools -p "根据以下 staged diff 生成一条 Conventional Commits 风格的中文提交说明。格式为 type(scope): subject。只输出提交说明本身。"
```

如果需要保存到文件并直接提交：

```bash
git diff --cached | aicli exec --disable-tools --output-last-message commit-message.txt -p "根据以下 staged diff 写一个中文提交说明。只输出提交信息。"
git commit -F commit-message.txt
```

不要只运行 `aicli exec "写一个提交说明"` 并期待模型自动读取仓库。`exec` 是非交互命令，模型如果尝试调用 shell、读取文件或询问用户，可能会触发权限审批失败。

### 结构化输出和 schema 双保险

```bash
cat > schema.json <<'JSON'
{"type":"object","required":["summary","risk"]}
JSON

aicli exec \
  --output-schema schema.json \
  --output-last-message result.json \
  "只返回 JSON，字段包含 summary 和 risk"
```

### 本地 mock 或代理 endpoint

```bash
aicli --config ./aicli.yaml exec \
  -C provider.base_url=http://127.0.0.1:8080 \
  -C provider.api_key=test-key \
  "hello"
```

---

## 十一、排障

### `--json` 和 `--output json` 冲突

错误示例：

```bash
aicli exec --json --output json "hello"
```

修正：

```bash
# 需要过程事件
aicli exec --json "hello"

# 只需要最终 JSON
aicli exec --output json "hello"
```

### schema 在 PowerShell 中解析失败

如果看到类似：

```text
解析 output schema 失败
```

优先把 schema 写入文件，再传文件路径：

```powershell
@'
{"type":"object","required":["summary"]}
'@ | Set-Content -Path schema.json -Encoding ASCII

aicli exec --output-schema schema.json "只返回 JSON"
```

### `exec resume --ephemeral` 被拒绝

这是预期行为。`resume` 依赖持久化会话，因此不能使用 `--ephemeral`。

```bash
# 错误
aicli exec resume --last --ephemeral

# 正确
aicli exec resume --last
```

### `exec review` 提示不是 Git 仓库

`exec review` 必须在 Git 工作区内执行。

```bash
git rev-parse --is-inside-work-tree
```

如果当前目录不是仓库，请切换到仓库目录后再运行：

```bash
cd /path/to/repo
aicli exec review --uncommitted
```

### JSONL 校验失败

确认没有合并 stderr：

```bash
# 推荐
aicli exec --json "hello" 2>stderr.log | jq .

# 不推荐
aicli exec --json "hello" 2>&1 | jq .
```

### `interactive approval required in --no-interactive mode`

如果看到类似错误：

```text
Error: interactive approval required in --no-interactive mode
```

说明模型在 `exec` 的非交互模式下尝试执行需要人工审批的动作，例如调用 shell、读取或写入文件、发起工具调用，或者通过 `ask_user_question` 询问用户。`exec` 不能弹出交互式审批，因此会失败。

常见触发方式：

```bash
aicli exec "写一个提交说明"
```

这个 prompt 没有提供 Git diff，模型可能会尝试自己运行 `git diff`、`ls`、读取文件或写文件，从而触发审批。

推荐处理方式是显式提供上下文，并在纯文本转换任务中禁用工具：

```bash
git diff --cached | aicli exec --disable-tools -p "根据以下 staged diff 写一个中文提交说明。只输出提交信息，不要解释。"
```

如果需要模型自行检查仓库，可以显式放宽权限：

```bash
aicli exec --yolo "查看当前 git diff，并写一个中文提交说明。只输出提交信息。"
```

`--yolo` 会绕过权限审批，适合本地可信仓库中的明确任务；不要在 CI、不可信输入或不清楚模型会执行哪些命令时使用。

如果目标是审查代码变更，优先使用专用入口：

```bash
aicli exec review --uncommitted
```

### 没有 prompt

以下命令会返回参数错误：

```bash
aicli exec
```

提供 prompt、`-p` 或 stdin：

```bash
aicli exec "hello"
aicli exec -p "hello"
echo "hello" | aicli exec
```

### 查看当前支持的 flags

```bash
aicli exec --help
aicli exec resume --help
aicli exec review --help
```

---

## 相关文档

- [install.md](./install.md)
- [aicli skills 使用说明](../skill_runtime/aicli_skills_usage.md)
- [exec 增强方案](../plan/aicli-exec-subcommand-enhancement-plan.md)
