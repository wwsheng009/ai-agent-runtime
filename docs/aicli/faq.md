# aicli FAQ

常见启动、登录、模型切换与环境问题。快速上手见 [quickstart.md](./quickstart.md)；完整配置说明见 [install.md](./install.md)。

## 1. 找不到配置 / `providers` 为空

先确认当前实际加载了哪个配置：

```bash
aicli config
aicli config --output json
```

常见原因：

- 还没执行 `aicli init --global` 或 `aicli login`
- 用户级 `~/.aicli/config.yaml` 不存在，当前 cwd 下也没有可用的项目级配置
- 当前加载的配置里 `providers.items` 仍为空（只初始化了 starter，还没 login）
- 从仓库根运行时，误以为会自动读取 `backend/configs/config.yaml`（默认不会）

说明：用户级 `$HOME/.aicli/config.yaml` 优先级高于项目级 `./.aicli/config.yaml`；项目级不会覆盖用户级。若两边都存在，以用户级为准。

处理建议：

```bash
aicli init --global
aicli login --provider openai --protocol openai --base-url https://api.openai.com --api-key sk-... --set-default
# 或显式指定配置
aicli -c ~/.aicli/config.yaml config
```

## 2. `aicli login` 校验 models endpoint 失败

优先用 dry-run 排查，不写配置：

```bash
aicli login --provider openai --protocol openai --base-url https://api.openai.com --api-key sk-... --dry-run --json
```

检查项：

- `base_url` 是否正确（不要把完整 chat path 写进 base_url）
- `models-path` 是否需要覆盖（本地兼容服务常见）
- API key / 网络代理是否可用
- provider `protocol` 是否匹配上游

本地 OpenAI 兼容服务示例：

```bash
aicli login \
  --provider local \
  --protocol openai \
  --base-url http://127.0.0.1:4000 \
  --models-path /v1/models
```

若上游 models 路径非默认，务必显式传 `--models-path`。

## 3. chat 里 `/model` 切换失败

先看状态：

```text
/model status
/status
```

再检查：

```bash
aicli config --models
aicli doctor provider --provider <name> --model <model>
```

常见原因：

- `provider 'xxx' not found`：先 `aicli login` / `aicli config` 确认 provider 已写入
- `没有可用的 providers`：配置里 `providers.items` 为空，或都未启用
- `provider 'xxx' 未配置默认模型`：给该 provider 设置 `default_model`，或 `/model --provider xxx --model <name>`
- 目标 provider 未启用，或 `supported_models` 未包含该模型
- 当前配置文件不可写，偏好没有落盘
- 启动参数 `--provider/--model` 覆盖了 session / 配置默认值

## 4. HTTP 401 / Invalid API key

优先检查：

1. `.env` 或环境变量是否已设置（`OPENAI_API_KEY` 等）
2. `config.yaml` 是否写成了 `${OPENAI_API_KEY:-}` 且变量确实存在
3. 是否写到了**当前实际加载**的配置文件（`aicli config` 会显示当前配置来源）

```bash
aicli config
aicli test --provider openai --message "ping"
aicli doctor provider --provider openai
```

重新登录：

```bash
aicli login --provider openai --protocol openai --base-url https://api.openai.com --api-key sk-... --set-default
```

## 5. Windows 安装后找不到 `aicli`

安装脚本默认写入 `%LOCALAPPDATA%\Programs\aicli` 并追加用户 PATH。  
**需要新开一个 PowerShell / Windows Terminal** 才会生效。

若仍找不到：

```powershell
Get-Command aicli -ErrorAction SilentlyContinue
$env:Path -split ';' | Select-String aicli
& "$env:LOCALAPPDATA\Programs\aicli\aicli.exe" version
```

## 6. 临时使用另一份配置

```bash
aicli -c ./mycfg.yaml config
aicli -c ./mycfg.yaml chat
aicli --config ~/.aicli/config.yaml doctor provider
```

chat 内也可用启动 flag 覆盖一轮偏好：

```bash
aicli --provider openai --model gpt-4.1 --reasoning-effort medium --stream
```

## 7. 日志在哪里

默认常见路径：

- Linux / macOS：`~/.aicli/logs/aicli.log`
- Windows：`%USERPROFILE%\.aicli\logs\aicli.log`

也可用：

```bash
aicli --logfile ./aicli.log chat
```

或在配置中设置 `aicli.log.file_path`。

## 8. 仓库里的示例配置为什么读不到

`./configs/config.yaml` 是相对**当前工作目录**解析的。仓库示例实际位于 `backend/configs/config.yaml`：

- 从 `backend` 目录运行时，可能被默认候选命中
- 从仓库根运行时，请显式指定：

```bash
aicli -c backend/configs/config.yaml config
# 或创建项目级配置
aicli init
```

## 9. doctor 相关怎么用

```bash
# 复现 provider 调用链
aicli doctor provider
aicli doctor provider --provider openai --model gpt-4.1 --json

# 预览子 Agent / Team 难度路由（不调用模型）
aicli doctor subagent-route --difficulty hard --goal "review auth changes" --json
```

- `doctor provider`：对指定 provider 跑可复现调用矩阵，适合排查 401、超时、工具链是否暴露
- `doctor subagent-route`：只 dry-run 路由结果，不会真正调模型

## 相关文档

- [quickstart.md](./quickstart.md)
- [install.md](./install.md)
- [exec.md](./exec.md)
- [agents.md](./agents.md)
- [tool_image_generate.md](./tool_image_generate.md)
