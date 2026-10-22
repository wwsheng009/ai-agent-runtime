# 远程工具使用手册（aicli-remote-tools + aicli --remote）

> 对应程序：
> - 目标机：`backend/cmd/aicli-remote-tools`（`aicli-remote-tools`）
> - 本机：`backend/cmd/aicli`（`aicli chat --remote`）
>
> 作用：让本机 Agent 像使用本地工具一样，读写另一台主机上的文件、执行命令。
> 远程工具统一带前缀（如 `remote_srv__view`），不会和本地工具重名。
>
> 默认不启用；`--remote` 是**会话级加法**，与已有 MCP 服务器共存，不独占工具面。

---

## 目录

1. [拓扑与传输](#1-拓扑与传输)
2. [目标机部署](#2-目标机部署)
3. [启动目标机服务](#3-启动目标机服务)
4. [本机连接](#4-本机连接)
5. [工具面与 profile](#5-工具面与-profile)
6. [工作目录与路径边界](#6-工作目录与路径边界)
7. [命令管控](#7-命令管控)
8. [权限模型](#8-权限模型)
9. [凭据、认证与审计](#9-凭据认证与审计)
10. [多目标与声明式配置](#10-多目标与声明式配置)
11. [故障排查](#11-故障排查)
12. [安全清单](#12-安全清单)
13. [相关文档](#13-相关文档)

---

## 1. 拓扑与传输

```
本机（运行 aicli chat）                         目标机（被操作）
┌──────────────────────┐                     ┌──────────────────────────┐
│ aicli + Agent        │   MCP (Streamable)  │ aicli-remote-tools       │
│  remote_srv__view  ──┼────────────────────►│  workspace jail          │
│  remote_srv__shell   │   HTTP 或 SSH stdio │  command policy          │
└──────────────────────┘                     │  audit / jobs / artifacts│
                                             └──────────────────────────┘
```

| 传输 | 连接方式 | 适用场景 | 目标机是否常驻 |
|------|----------|----------|----------------|
| `http` / `https` | 直连 `host:port`，Bearer Token | 内网、已做 SSH 端口转发、容器 | 是（`serve --http`） |
| `ssh-stdio` | 本机 `ssh-client` 拉起远端进程，走 stdin/stdout | 不想开放端口，复用 SSH 凭据 | 否（每次会话启动） |
| `ssh-http` | SSH 本地转发 + HTTP（需先手动启动转发） | 高级拓扑 | 是 + 转发进程 |

首次接入建议先用 `http` 在本机回环打通，再扩展到 SSH 或跨机。

---

## 2. 目标机部署

### 2.1 构建二进制

在本机 `backend/` 下交叉编译（目标机不需要 Go 环境）：

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
  go build -trimpath -ldflags "-s -w" \
  -o aicli-remote-tools ./cmd/aicli-remote-tools
```

`uname -m` 为 `aarch64` 时把 `GOARCH` 换成 `arm64`。`CGO_ENABLED=0` 生成静态二进制，避免目标机 glibc 版本不匹配。

### 2.2 安装到目标机

```bash
sudo install -m 0755 aicli-remote-tools /usr/local/bin/aicli-remote-tools
# 或
scp aicli-remote-tools ops@target:/tmp/
```

### 2.3 自检

```bash
aicli-remote-tools doctor --workspace /srv/app --profile ops
aicli-remote-tools doctor --config /etc/aicli/remote.yaml --json
aicli-remote-tools tools  --config /etc/aicli/remote.yaml
```

`doctor` 会打印**归一化后的生效值**（`workspace`、`allowedRoots`、profile、工具列表、audit/jobs/artifacts 是否可写、HTTP 地址与 token 是否已设置），并在配置自相矛盾时返回非零退出码。

> `token set=false` 只表示当前 shell 没导出该变量；服务真正启动前必须设置好。

---

## 3. 启动目标机服务

### 3.1 最小配置

```yaml
# /etc/aicli/remote.yaml
workspace: /srv/app        # 必填：相对路径锚点 + 默认 jail 根 + 默认 cwd
profile: readonly          # 先只读跑通，再逐步放开
toolPrefix: remote_srv     # 工具名：remote_srv__view / remote_srv__write / ...
targetName: srv            # 描述与审计里显示的名字
descriptionStyle: explicit # 描述里带 [remote:srv]，模型不会误认成本地文件
```

### 3.2 HTTP 常驻服务

```bash
export AICLI_REMOTE_TOKEN="$(openssl rand -hex 32)"

aicli-remote-tools serve --http \
  --config /etc/aicli/remote.yaml
```

默认监听 `127.0.0.1:18765/mcp`。常用覆盖项：

```bash
aicli-remote-tools serve --http \
  --config /etc/aicli/remote.yaml \
  --http-listen 127.0.0.1:18765 \
  --http-path /mcp \
  --http-token-env AICLI_REMOTE_TOKEN
```

- **非回环监听必须配置 token**，否则启动失败（fail-closed）。
- token 只从环境变量读取，不接受命令行参数，也不会写进日志。
- HTTPS：同时提供 `--tls-cert` 与 `--tls-key`；mTLS 另见 `http.clientCAFile` / `http.requireClientCert`。

### 3.3 systemd 示例

```ini
# /etc/systemd/system/aicli-remote-tools.service
[Unit]
Description=aicli remote tool server
After=network.target

[Service]
User=aicli
Group=aicli
EnvironmentFile=/etc/aicli/remote.env
ExecStart=/usr/local/bin/aicli-remote-tools serve --http --config /etc/aicli/remote.yaml
Restart=on-failure

# 进程级隔离（推荐）
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
ReadWritePaths=/srv/app -/var/lib/aicli
NoNewPrivileges=yes

[Install]
WantedBy=multi-user.target
```

```bash
# /etc/aicli/remote.env（chmod 600）
AICLI_REMOTE_TOKEN=<随机长串>
```

### 3.4 SSH stdio（无常驻进程）

目标机只需装好二进制，不需要启动服务。本机 `--remote ssh://...` 会通过 `ssh-client` 拉起：

```bash
ssh-client -T ops@target -- aicli-remote-tools serve --stdio --config-b64 <base64>
```

因此：

- 目标机的 `aicli-remote-tools` 必须在 PATH 中，或用 `?binary=/abs/path` 指定。
- SSH 认证、跳板机、known_hosts 全部沿用本机 SSH 配置。
- 工作区、profile、toolPrefix 通过 base64 配置传入，不经过 shell 拼接。

---

## 4. 本机连接

### 4.1 HTTP 直连

```bash
# 目标机已在本机 18765 端口（或已做 SSH 本地转发）
export AICLI_REMOTE_TOKEN="<与目标机相同的 token>"

aicli chat --remote http://127.0.0.1:18765
```

HTTPS 直接把 scheme 换成 `https://`。

### 4.2 SSH 直连

```bash
# 只读（默认）
aicli chat --remote ssh://ops@target

# 指定远端工作区与深度
aicli chat --remote ssh://ops@target/srv/app#ops

# 非默认端口 / 指定私钥 / 指定二进制（查询参数写在 #depth 之前）
aicli chat --remote 'ssh://ops@target:2222/srv/app?identity=~/.ssh/id_ed25519&binary=/usr/local/bin/aicli-remote-tools#standard'
```

### 4.3 spec 语法

```
<scheme>://[user@]host[:port][/path][?workspace=...&binary=...&identity=...][#depth]
```

| 部分 | 说明 |
|------|------|
| `scheme` | `http` / `https` / `ssh`，必填 |
| `host` | 必填；HTTP 默认端口 `18765` |
| `/path` | 仅 SSH：远端 workspace；省略时服务端用 `"."`（登录用户家目录） |
| `#depth` | `readonly`（默认）/ `standard` / `ops` |
| `?workspace=` | 仅 SSH：显式远端工作区，优先级高于 URL path |
| `?binary=` | 仅 SSH：远端二进制路径，默认 `aicli-remote-tools`（走 PATH） |
| `?identity=` | 仅 SSH：私钥路径 |

> 查询参数必须写在 `#depth` **之前**（`...?query#depth`）。解析器先按最后一个 `#` 切出 depth，把 `#` 后面的整段都当作 depth；写成 `#ops?identity=...` 会得到 `unknown depth` 错误。

**`#depth` 的真实含义**：

| 传输 | 效果 |
|------|------|
| `ssh-stdio` | 作为**目标服务端的 profile** 传入，真正决定暴露哪些工具 |
| `http` / `https` | **不会改变已运行的服务**；仅打印警告。要改工具面，请修改目标机配置并重启 |

这条差异是刻意显式提示的：`--remote http://host#ops` 不会伪装成"已收窄/已放开"。

### 4.4 token 放在哪里

**HTTP / HTTPS 传输**下，客户端从**本机环境变量** `AICLI_REMOTE_TOKEN` 读取，并作为 `Authorization: Bearer ...` 发送。SSH stdio 由 SSH 自身认证，不需要 Bearer token。

`aicli` 启动时会自动加载 `.env`（首个存在的文件生效，且不覆盖已存在的进程环境变量）：

1. `<工作区>/.aicli/.env`（项目级）
2. `~/.aicli/.env`（用户级，所有项目共用）
3. `./.env`
4. `./configs/.env`（旧路径）

推荐：

```bash
# ~/.aicli/.env
AICLI_REMOTE_TOKEN=<token>
```

```bash
chmod 600 ~/.aicli/.env
```

> 不要把 token 写进 `remote-targets.yaml` 或 `mcp.yaml` —— 这两个文件通常需要提交。
> 临时覆盖仍可用 `export` / `$env:AICLI_REMOTE_TOKEN=...`，进程环境变量优先于 `.env`。

### 4.5 与已有 MCP 的关系

- `--remote` 合成的服务器**在内存中**，不写入任何配置文件。
- 它与 `mcp.yaml` 中的服务器**合并**，不替换；其他 MCP 工具仍然可用。
- 同名服务器冲突时，命令行 `--remote` 优先（因为这是你本次显式输入的）。
- 一个 `--remote` 只能指定一个目标；多目标见 [§10](#10-多目标与声明式配置)。

启动时会打印一行提示，例如：

```
remote: added server "127-0-0-1" (transport=streamable, requested profile=ops); tool names come from the target's own toolPrefix
```

若目标连接失败，会明确报错，而不是静默降级为空工具面。

### 4.6 网络断连与重连

**HTTP / Streamable**

- 每个 tool call 是一次 POST + SSE 响应；**没有针对工具调用的透明重试**。
- 远程 MCP 会话启用 SDK keepalive ping（30s）；ping 失败会关闭会话，通常 30s 左右发现死链。
- 客户端健康检查每 `global.healthCheckInterval`（默认 1m；ITSM 的 `mcp.yaml` 为 1m）执行一次 `ListTools` 探针，失败则重建 MCP 会话；检测 + 重连通常需要 1–2 分钟。
- 下一次工具调用如果发现 client 已断开，会**在调用前同步重建会话**；如果已有重连在进行，则返回“正在重连”，不会重放上一次调用。
- 断连时当前调用会失败：`view` / `ls` / `glob` / `grep` / `status` 可以安全重试；`write` / `shell` / 部署脚本不要盲目重试，先查状态再决定。
- 服务端 `ReadHeaderTimeout=10s`、`IdleTimeout=120s`，不设整体 `WriteTimeout`（允许长调用）。如果是网络黑洞（没有 FIN/RST），handler 可能一直运行到 `limits.commandTimeout`（238 为 30m，默认 120s）才结束。
- 请求取消或超时会杀掉整个进程组，避免构建子进程残留。
- `limits.maxConcurrentCalls` 已在 HTTP MCP 入口强制执行：超出时返回 **429** + `Retry-After: 1`；`/metrics` 不占并发名额。

**SSH stdio**

- 每个 MCP 会话一个 `ssh-client` + 远端 `aicli-remote-tools serve --stdio` 进程；连接断开即会话结束。
- `--remote ssh://` 生成的参数已带：

  ```
  -o ServerAliveInterval=15 -o ServerAliveCountMax=3
  ```

  死链约 45 秒内会被发现。
- `ssh-client` 的自动重连/退避只用于 `-N` 端口转发模式；**stdio 模式不会自动重连**，由 MCP 健康检查重建会话。
- `ssh-client -C` / `Compression yes` 会输出明确告警：`x/crypto/ssh` 只协商 `none`，压缩不生效。
- 长构建/部署用 `setsid nohup` 的 detached 脚本，或启用 `jobs.root` 后使用 `job_start` / `job_output`；不要让 SSH 连接成为任务的唯一生命周期。

**推荐做法**

- 部署脚本做成幂等、可检查点：`status → build → wait → up`；
- 长任务放到后台（detached 或 `job_*`），断线后重新连接再查结果；
- 监控审计/访问日志以及 `mcp.health.failed` / `mcp.reconnected` 生命周期事件。

---

## 5. 工具面与 profile

### 5.1 profile 决定"有哪些工具"

| profile | 暴露的工具 |
|---------|------------|
| `readonly`（默认） | `view` `ls` `glob` `grep` |
| `standard` | readonly + `write` `edit` `append_write` `apply_patch` |
| `ops` | standard + `shell` |
| `custom` | `tools:` 列表 + 已配置的 job/artifact 能力工具 |

补充规则：

- `readOnly: true` 是硬覆盖：即使 profile 写成 `ops`，也不会暴露写工具。
- `jobs.root` 非空且 profile 为 `ops`/`custom` 时，追加 `job_start` `job_output` `job_cancel` `job_list`。
- `artifacts.root` 非空时，追加 `artifact_read`（用于取回被折叠的完整输出）。
- `tools:` 在非 `custom` profile 下是**额外的收窄过滤器**，不能扩大 profile。

### 5.2 工具名前缀

目标机：

```yaml
toolPrefix: remote_srv
```

模型看到的名字：

```
remote_srv__view
remote_srv__write
remote_srv__shell
```

- 前缀必须是 `[A-Za-z0-9_-]+`，长度 ≤ 24。
- 通过 `--remote` 建立 SSH 目标时，客户端会把 `remote_<host>`（点号转横线，例如 `10.0.0.9` → `remote_10-0-0-9`）作为 toolPrefix 传入；HTTP 目标以服务端自身配置为准。
- 前缀保证远程工具不会遮蔽本地同名工具（`view`/`ls`/`glob`/`grep` 尤其重要）。

### 5.3 查看实际暴露面

```bash
aicli-remote-tools tools --config /etc/aicli/remote.yaml
# 或 MCP 层：tools/list
```

---

## 6. 工作目录与路径边界

这是远程工具最容易误解的部分：**文件工具和 shell 受不同的边界约束**。

### 6.1 workspace：默认 cwd + 相对路径锚点 + jail 根

```yaml
workspace: /srv/app
allowedRoots:
  - /srv/shared     # 可选：额外放行的绝对根
```

`workspace` 在启动时被 `filepath.Abs` + `Clean` 成绝对路径，并做存在性校验（必须是目录）。之后：

| 行为 | 结果 |
|------|------|
| `view src/main.go` | `/srv/app/src/main.go`（相对 workspace，**不是**进程 CWD） |
| `view /srv/shared/x.txt` | 允许（在 `allowedRoots` 内） |
| `view /etc/passwd` | `PATH_DENIED` |
| `view ../etc/passwd` | `PATH_DENIED`（Clean 后前缀校验失败） |
| `ws/link -> /etc`，`view link/passwd` | `PATH_DENIED`（符号链接解析后二次校验） |

生效根集合 = `workspace` + `allowedRoots`（去重，workspace 永远在内）。

启动时的相对路径（如 `--workspace app`）按**启动进程的当前目录**解析；服务跑起来后，进程 CWD 不再影响任何工具路径。

### 6.2 启动时指定 cwd

最直接的方式就是把 `workspace` 指到想要的位置：

```bash
aicli-remote-tools serve --http --workspace /srv/app --profile ops
```

或：

```yaml
workspace: /srv/app
```

```bash
aicli-remote-tools serve --http --config /etc/aicli/remote.yaml
```

优先级：**命令行 flag > 配置文件 > 内置默认**。

### 6.3 只改默认 cwd、不缩小 jail

现有配置没有独立的 `defaultWorkdir` 字段，但可以这样组合：

```yaml
workspace: /srv/app/src        # 默认 cwd = src/
allowedRoots:
  - /srv/app                   # 仍然允许访问整个 app/
```

效果：

```bash
remote_srv__shell {"command":"pwd"}
# /srv/app/src

remote_srv__view {"file_path":"../README.md"}
# 允许：/srv/app/README.md
```

反过来（jail 在父目录、默认 cwd 指向子目录）目前不支持，需要新增配置字段。

### 6.4 单次调用覆盖 workdir

`shell` 与 `job_start` 支持 `workdir`：

```json
{"command":"go test ./...", "workdir":"services/api"}
```

- 相对路径：相对 `workspace`
- 绝对路径：必须落在 `workspace` / `allowedRoots` 内
- 必须是已存在的目录
- 不传时：使用**本会话生效的 workspace 根**

> 会话通过 MCP Roots 收窄后，默认 cwd 会跟随收窄后的根（不是服务端的 base root）。

### 6.5 shell 命令内部的路径不受 jail 约束

这是必须明确的边界：

| 操作 | 是否受路径 jail |
|------|-----------------|
| `view` / `ls` / `glob` / `grep` / `write` / `edit` / `apply_patch` 的参数 | ✅ |
| `shell` / `job_start` 的 `workdir` | ✅ |
| `shell` 命令文本中的路径（如 `cat /etc/hostname`） | ❌ |

实测（目标机 workspace = `/home/user/ws`）：

```
remote_srv__view  /etc/hostname           → PATH_DENIED
remote_srv__shell {"workdir":"/etc"}      → PATH_DENIED
remote_srv__shell {"command":"cat /etc/hostname"} → 成功
```

原因是 `shell` 真正执行的是 `sh -c "<command>"`（Windows 为 `cmd.exe /c`），进程以服务账号身份运行，拥有该账号的全部文件权限。`denyShellMetachars` 只拦截 `; && || |`、反引号、`$(`、`>`、`<` 和换行，不拦截路径参数。

**如果要求"命令也只能访问 workspace"**，必须使用 OS 级隔离，而不是再加一层 agent 配置：

1. 专用低权用户 + 仅 workspace 可读写（最低成本）
2. systemd 命名空间（`ProtectSystem=strict`、`ProtectHome=yes`、`ReadWritePaths=...`）
3. 容器 / `bwrap`，只 bind mount workspace

示例见 [§3.3](#33-systemd-示例) 与 [§12](#12-安全清单)。

### 6.6 MCP Roots：只能收窄

客户端可以在 MCP 会话中声明 roots（`file://` URI）。服务端执行**交集**：

- 声明 `.../ws/src` → 会话只能在 `src/` 内操作
- 声明 `/etc` → 被拒绝并记录 violation，不会扩大权限
- 不声明 → 使用服务端完整 workspace

Roots 中的相对路径按服务端 workspace 解析；指向不存在目录的声明会被忽略。
若所有声明都无效，会话回退到服务端完整 workspace（仍然不会超出服务端允许的根）。

---

## 7. 命令管控

`shell` 是显式 opt-in：`profile: ops` 只是让工具出现，**能否执行某条命令由目标侧策略决定**。

### 7.1 四道闸（顺序固定）

策略真源：`internal/remotetools/sandbox.go` 的 `CheckCommand`。

| 顺序 | 检查 | 行为 |
|------|------|------|
| 1 | `deniedCommands` / `deniedCommandPatterns` | **永远优先**，即使同时出现在 allowlist 中也拒绝 |
| 2 | `allowedCommands` / `allowedCommandPatterns` | 至少一个非空；两者都空 = 全拒（与本地"空=全放"相反） |
| 3 | `denyShellMetachars` | 拒绝 `;` `&&` `\|\|` `\|` 反引号 `$(` `>` `<` 和换行（引号内不算） |
| 4 | 破坏性分类器 | 共享 `shellrisk`：递归删除根/家目录等，即使二进制在 allowlist 中也拒绝 |

### 7.2 两种匹配方式：argv 前缀 与 整条命令通配

```yaml
shell:
  allowedCommands:
    - echo
    - git status
    - lscpu
  allowedCommandPatterns:      # 显式 opt-in，适合脚本族/可变参数
    - "deploy238_*.sh"
    - "deploy238_*.sh *"
    - "systemctl restart itsm-*"
  deniedCommands:
    - rm
    - dd
  deniedCommandPatterns:
    - "sh -c *"
    - "rm -rf *"
```

`allowedCommands` / `deniedCommands` 的语义不变：

| 条目 | 匹配 | 不匹配 |
|------|------|--------|
| `echo` | `echo hi`、`echo -n x` | `echox` |
| `git status` | `git status --short` | `git commit` |
| `lscpu` | `lscpu`、`lscpu -e` | — |
| `*` | 全部（仍受第 3、4 道闸约束） | — |

`allowedCommandPatterns` / `deniedCommandPatterns` 是**整条命令**的通配：

| pattern | 匹配 | 不匹配 |
|---------|------|--------|
| `deploy238_*.sh` | `deploy238_a.sh` | `deploy238_a.sh --force` |
| `deploy238_*.sh *` | `deploy238_a.sh --force`、`deploy238_a.sh a b` | `deploy238_a.sh`（pattern 要求一个空格） |
| `systemctl restart itsm-*` | `systemctl restart itsm-backend` | `systemctl stop itsm-backend` |
| `kubectl rollout restart deployment/*` | `... deployment/itsm` | `... deployment/itsm --watch` |
| `sh -c *`（deny） | `sh -c "echo hi"` | — |

规则：

- 只支持 `*`（任意长度）与 `?`（单个字符），**不是正则**；
- 匹配不区分大小写；
- 命令与 pattern 都会先做 quote-aware token 化，再以单空格连接；
- `argv[0]` 统一取 basename，所以 `./deploy.sh`、`/opt/tools/deploy.sh`、`deploy.sh` 是同一个程序；
- 通配是**全串匹配**，`*` 可以跨空格：需要额外参数时在 pattern 末尾写 ` *`；
- `deniedCommandPatterns` 先于所有 allow 规则；
- `*` 仍然是显式全放行。

> ⚠️ `allowedCommandPatterns` 允许程序名通配（例如 `deploy238_*.sh`）。如果 workspace 可写，
> 这等价于「可以先创建 `deploy238_evil.sh` 再执行」。只在受控 target 上使用；更稳的写法是
> 固定受控入口，把可变部分放到参数里，例如 `itsm-deploy-238 deploy-script deploy238_*.sh`。

### 7.3 执行约束

| 约束 | 默认值 / 行为 |
|------|---------------|
| 超时 | 120s；调用方可传 `timeout_ms` |
| 输出上限 | 256 KiB（`limits.maxOutputBytes`）；超限折叠中间并明确标注 |
| 完整输出 | 配置 `artifacts.root` 后归档，返回 `artifact_id` 可翻页 |
| 进程组 | 独立进程组；超时杀整棵进程树 |
| 环境变量 | 默认**不继承**；只透传 `shell.envAllowlist` 列出的变量 |
| 工作目录 | 必须是 jail 内的已存在目录 |

`envAllowlist` 为空意味着命令在几乎空的环境里运行（`HOME`、`PATH` 都不会自动带过去）。如果命令依赖 PATH，显式加入：

```yaml
shell:
  envAllowlist: [HOME, PATH, LANG]
```

### 7.4 常见错误

```
COMMAND_DENIED: command "lscpu" is not in the allowlist
```

→ 把 `lscpu` 加入 `shell.allowedCommands` 并重启目标服务。

```
COMMAND_DENIED: remote shell is fail-closed: no allowedCommands/allowedCommandPatterns configured
```

→ `profile: ops` 但两种 allow 都为空。这是有意的 fail-closed。

```
COMMAND_DENIED: command "deploy" is denied by pattern "deploy*"
```

→ 命中了 `deniedCommandPatterns`；deny 永远优先于 allow。

```
COMMAND_DENIED: command contains a pipe (|)
```

→ 去掉管道/重定向/命令拼接；需要组合逻辑时写脚本并在 allowlist 中放行该脚本。

---

## 8. 权限模型

### 8.1 只有一层：本地 permission mode

远程工具不引入额外的授权层。它们和本地工具、普通 MCP 工具一样，由**会话的 permission mode** 决定是否放行：

```bash
aicli chat --remote http://127.0.0.1:18765 --permission-mode default
aicli chat --remote ... --plan
aicli chat --remote ... --accept-edits
aicli chat --remote ... --yolo          # = bypass_permissions
```

| mode | 读类工具 | 写类工具 | 执行类（shell） |
|------|----------|----------|------------------|
| `default` | 放行 | 需审批 | 需审批 |
| `accept_edits` | 放行 | 自动放行 | 仍需审批 |
| `plan` | 放行（只读探索） | 拒绝 | 拒绝 |
| `bypass_permissions` / `--yolo` | 放行 | 放行 | 放行 |
| `dont_ask` | 放行 | 未预授权则拒绝 | 未预授权则拒绝 |

关键点：

- 这里的"写类/执行类"由**工具自身属性**判定，与工具名前缀无关；`remote_srv__write` 和本地 `write` 走同一套判定。
- `plan` 模式下远程写工具与 `shell` 都会被拒绝，不会因为"它是远程的"而绕过。
- `accept_edits` 只放宽文件编辑，不会自动执行命令。
- `--deny-tool 'remote_srv__shell'` 可以硬拒绝某个远程工具（优先于 allow 规则）。

### 8.2 目标侧策略是第二道、独立的边界

```
本地 permission mode  → 允不允许发起这次调用（allow / ask / deny）
目标 profile          → 这个工具是否存在
目标 command policy   → 这条命令能不能执行
```

三者是**与**关系。即使本地 `--yolo`，目标侧 `allowedCommands` 仍然会拒绝未授权的命令。

### 8.3 已废弃的 `policy:` 块

旧版 `remote-targets.yaml` 支持 `policy: {allowWrite, allowShell, subagentTargets...}`。这些字段**已不再生效**，加载时会打印警告：

```
target "srv": the `policy:` block (allowWrite) no longer gates anything;
remote tools are governed by the session permission mode like local ones ...
```

处理方式：删除 `policy:` 块。需要收窄时用：

1. 目标机 `profile`（决定工具面）
2. 本地 `--permission-mode` / `--deny-tool`
3. 目标机 `shell.allowedCommands`

---

## 9. 凭据、认证与审计

### 9.1 Token

| 侧 | 配置 |
|----|------|
| 目标机 | 环境变量（默认 `AICLI_REMOTE_TOKEN`），由 `http.tokenEnv` 指定变量名 |
| 本机 | 同名环境变量；`--remote` 自动加 `Authorization: Bearer ...` |

- 回环监听（`127.0.0.1` / `::1` / `localhost`）可以不带 token。
- 非回环监听必须配置 token，否则启动失败。
- 客户端若拿不到 token，请求会得到 `401 Unauthorized`；不会静默降级。
- 生产环境请用 `chmod 600` 保护 `.env` / `remote.env`，并在 systemd 中用 `EnvironmentFile=` 注入，而不是写在 unit 的 `ExecStart` 里。

### 9.2 TLS / mTLS

```yaml
http:
  listen: 0.0.0.0:18765
  tlsCert: /etc/aicli/server.crt
  tlsKey: /etc/aicli/server.key
  clientCAFile: /etc/aicli/client-ca.crt
  requireClientCert: true
```

- `tlsCert` / `tlsKey` 必须成对出现。
- `clientCAFile` + `requireClientCert` 启用 mTLS，可与 Bearer Token 叠加。
- 客户端使用 `https://...`；证书校验默认开启，测试环境才考虑跳过。

### 9.3 审计

```yaml
audit:
  path: /var/log/aicli/remote-audit.ndjson
  maxSizeMB: 64
  maxBackups: 5
  commandPreviewBytes: 0      # 0=不记录命令文本；>0 记录前 N 字节
```

每次工具调用写一行 NDJSON。字段：

| 字段 | 说明 |
|------|------|
| `ts` / `target` / `transport` | 时间、目标名、传输 |
| `tool` | 工具名（含前缀） |
| `decision` | `allow` / `deny` / `error` |
| `reason` | 错误码或策略原因 |
| `workspace` / `paths` | 工作区、被修改的路径 |
| `program` | `argv[0]` basename（shell/job_start） |
| `command_hash` | 归一化命令的 SHA-256 |
| `policy_rule` | 命中的规则，如 `allowedCommandPatterns:deploy238_*.sh` |
| `exit_code` | shell/job 的真实退出码（失败时保留） |
| `bytes_out` / `truncated` / `duration_ms` | 输出与耗时 |
| `principal` / `remote_addr` / `session_id` / `tool_call_id` | 凭据标签、来源地址、MCP 会话、调用关联 id |
| `command_preview` | 仅当 `commandPreviewBytes > 0` 时出现 |

**默认不记录文件内容，也不记录完整命令行**：只记录程序名、命令哈希和命中规则。
`commandPreviewBytes` 是显式 opt-in，开启后可能把 token/密码写进审计，需自行评估。

```bash
tail -f /var/log/aicli/remote-audit.ndjson | jq .
```

审计写入是 best-effort：写失败只报 stderr / journal，不会阻断工具调用；请同时监控服务日志。

### 9.4 HTTP 访问日志与指标

```yaml
http:
  accessLog: /var/log/aicli/remote-access.ndjson
  accessLogMaxSizeMB: 64
  accessLogMaxBackups: 5
```

访问日志记录**每个 HTTP 请求**（包括 401 和 `/metrics`）：

| 字段 | 说明 |
|------|------|
| `ts` / `remote_addr` | 时间、客户端地址 |
| `method` / `path` / `status` | 请求方法与状态码 |
| `bytes` / `duration_ms` | 响应字节与耗时 |
| `user_agent` | 客户端 UA |

不会记录 `Authorization` 头。与工具审计配合，可以区分「请求被 401 拒绝」和「工具被策略拒绝」。

指标端点（与 MCP 同端口、同一 Bearer Token）：

```bash
curl -H "Authorization: Bearer $AICLI_REMOTE_TOKEN" \
  http://127.0.0.1:18765/metrics
```

返回总调用数、失败/拒绝数、输出字节、最近延迟，以及按工具的调用计数。计数在进程内，重启归零。

### 9.5 后台作业与完整输出

```yaml
jobs:
  root: /var/lib/aicli/jobs          # 非空才暴露 job_* 工具
artifacts:
  root: /var/lib/aicli/artifacts     # 非空才暴露 artifact_read
  maxBytes: 33554432
```

- `job_*` 需要持久化目录；没有配置时工具不会出现在工具面里，而不是"出现了但一用就报错"。
- 输出被折叠时，服务端先归档完整内容并返回 `artifact_id`，模型可用 `artifact_read` 翻页。

---

## 10. 多目标与声明式配置

### 10.1 什么时候需要

- 只有一台目标：直接用 `--remote`，不需要任何文件。
- 多台目标、需要固定拓扑、需要在团队内共享连接声明：用 `.aicli/remote-targets.yaml`。
- 已有 `mcp.yaml` 的普通 MCP 服务器：照常保留，与上述两者共存。

### 10.2 文件位置

按顺序查找，首个存在者生效：

1. `<工作区>/.aicli/remote-targets.yaml`
2. `~/.aicli/remote-targets.yaml`

### 10.3 示例

```yaml
remoteTargets:
  wsl:
    transport: ssh-stdio
    ssh:
      host: 127.0.0.1
      user: wangweisheng
      port: 22
      # identityFile: ~/.ssh/id_ed25519
    remote:
      workspace: /home/wangweisheng/aicli-demo/ws
      binary: /home/wangweisheng/aicli-demo/aicli-remote-tools
      profile: ops
      toolPrefix: remote_wsl
      descriptionStyle: explicit

  prod:
    transport: http
    remote:
      listen: 127.0.0.1:18765     # 本机可直连的地址（含 SSH 转发后的回环）
      workspace: /srv/app         # http 下仅用于通过校验；实际 jail 由运行中的服务决定
      profile: readonly           # http 下不会改变已运行服务，仅提示
      toolPrefix: remote_prod
      tokenEnv: AICLI_REMOTE_TOKEN
      descriptionStyle: explicit
```

### 10.4 合并规则

- 声明生成的是**普通 MCP 服务器条目**，不需要再写一份 `mcp.yaml`。
- 与 `mcp.yaml` 同名服务器视为冲突并报错，必须只保留一份声明。
- `--remote` 与声明可以同时使用：声明先合并，`--remote` 后合并；同名时命令行优先。
- `transport: ssh-http` 需要先自行启动 `ssh-client -N -L <local>:<remote>`；未启动时调用会失败并提示。

### 10.5 相关告警

| 告警 | 含义 | 处理 |
|------|------|------|
| `the \`policy:\` block (...) no longer gates anything` | 旧授权字段已失效 | 删除 `policy:`，改用 mode / profile / allowlist |
| `profile "ops" is not applied to an http target` | HTTP 服务已运行，`#depth` 改不了它 | 修改目标机配置并重启 |
| `the remote profile "ops" must be started with the same workspace` | SSH 传入了 profile，需确认远端配置一致 | 检查远端 `--workspace` / base64 配置 |

---

## 11. 故障排查

| 现象 | 常见原因 | 处理 |
|------|----------|------|
| `401 Unauthorized` | 本机未设置 token / token 不一致 | 检查两侧 `AICLI_REMOTE_TOKEN`；确认 `.env` 位置 |
| `connection refused` | 目标服务未启动 / 端口写错 / SSH 转发未建立 | `doctor` 看监听地址；`ss -ltnp`；重建转发 |
| `remote: added server ...` 后无远程工具 | 服务端 profile 未包含目标工具 / prefix 冲突 | `tools` 子命令确认工具面；检查 `toolPrefix` |
| 启动直接失败：`workspace ... is not accessible` | 路径不存在或不是目录 | 修正 `workspace`；注意相对路径按启动 CWD 解析 |
| `PATH_DENIED` | 路径越出 `workspace`/`allowedRoots`，或符号链接逃逸 | 改路径，或把根加入 `allowedRoots` |
| `COMMAND_DENIED: not in the allowlist` | 目标侧 allowlist 未包含该命令 | 加入 `shell.allowedCommands` 并重启 |
| `COMMAND_DENIED: no allowedCommands configured` | `profile: ops` 但 allowlist 为空 | 显式配置 allowlist（fail-closed） |
| 命令找不到（`command not found`） | `envAllowlist` 为空导致没有 `PATH` | 加入 `PATH`、`HOME` 等 |
| `TIMEOUT` | 命令超过 `limits.commandTimeout` | 传 `timeout_ms` 或调整目标配置 |
| 输出中间被省略 | 超过 `limits.maxOutputBytes` | 用 `grep`/`tail` 收窄，或配置 `artifacts.root` 后翻页 |
| `policy:` 告警 | 使用了已废弃的授权块 | 删除，改用 mode / profile / allowlist |
| `#ops` 在 HTTP 下无效 | 服务已运行，profile 固定 | 修改目标机配置并重启 |
| SSH 模式报 `binary not found` | 远端 PATH 中没有二进制 | 用 `?binary=/abs/path` |

### 11.1 快速定位顺序

```bash
# 1) 目标机配置与监听
aicli-remote-tools doctor --config /etc/aicli/remote.yaml
ss -ltnp | grep 18765

# 2) 目标机工具面
aicli-remote-tools tools --config /etc/aicli/remote.yaml

# 3) 本机 token
echo "${AICLI_REMOTE_TOKEN:-<unset>}"    # PowerShell: $env:AICLI_REMOTE_TOKEN

# 4) 直接探测 MCP 端点（应返回 401 而不是连接失败）
curl -i http://127.0.0.1:18765/mcp
```

---

## 12. 安全清单

- [ ] 目标服务以**专用低权用户**运行，只对 workspace 有写权限。
- [ ] `workspace` 指向业务目录，不要指向 `/`、`/home`、`/etc`。
- [ ] `allowedRoots` 只添加确实需要共享的目录，不要整盘放行。
- [ ] 非回环监听必须启用 token；公网/跨网段再加 TLS 或 mTLS。
- [ ] `chmod 600` 保护 `.env` / `remote.env`；token 不进 shell history。
- [ ] `shell.allowedCommands` 从最小集合开始；不要放行 `sh`、`bash`、`python`、`env` 等可执行任意代码的解释器。
- [ ] `deniedCommands` 至少包含 `rm`、`dd`、`mkfs`、`shutdown` 等。
- [ ] 需要"命令也出不去"时，使用 systemd 命名空间或容器，而不是仅依赖 allowlist。
- [ ] 定期检查审计日志中的 deny 记录与异常命令。
- [ ] 升级后运行 `doctor` 与 `go test ./internal/remotetools`，确认配置仍被接受。

---

## 13. 相关文档

- 设计文档：[docs/plan/aicli-remote-tools-ssh-http-design-20261021.md](../plan/aicli-remote-tools-ssh-http-design-20261021.md)
- 工具层抽象方案：[docs/plan/tool-layer-abstraction-design-20261022.md](../plan/tool-layer-abstraction-design-20261022.md)
- 从零配置（含历史背景）：[docs/plan/aicli-remote-tools-setup-guide.md](../plan/aicli-remote-tools-setup-guide.md)
  > 注意：该文成文较早，其中 `remote-targets.yaml` 的 `policy:` 授权块已废弃；以本手册为准。
- 权限收敛记录：[docs/plan/aicli-remote-tools-permissions.md](../plan/aicli-remote-tools-permissions.md)
- SSH / SFTP 客户端：[docs/user-guide/ssh-client.md](ssh-client.md)
- aicli 主手册：[docs/user-guide/aicli.md](aicli.md)
- 运行时权限模式说明：`docs/analysis/commandcode-permissions-design-borrowing-20260926.md`
