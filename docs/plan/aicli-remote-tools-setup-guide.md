# 远程工具配置指南（从零到可用）

目标：让本机 aicli 能通过 `remote_<x>__*` 工具操作另一台 Linux 主机。
按顺序做完 §1–§4 就能用，§5 是排错，§6 是几种常见拓扑。

---

## 0. 三个文件、两个角色

```
目标机（被操作的那台）             本机（运行 aicli chat 的那台）
├─ aicli-remote-tools 二进制      ├─ aicli（已装）
├─ remote.yaml      ← 服务端配置  ├─ .aicli/mcp.yaml            ← 怎么连
└─ 启动 serve --http 或 --stdio   └─ .aicli/remote-targets.yaml ← 允许做什么
```

**关键**：`mcp.yaml` 只决定"连得上"，`remote-targets.yaml` 才决定"能做哪些操作"。
**两个都要有**。只配 `mcp.yaml` 时，读工具能用，写和 shell 全部拒绝
（报 `remote target "x" is not configured for this session`）。

---

## 1. 目标机：装二进制

```bash
# 在目标机上确认架构
uname -m        # x86_64 → amd64；aarch64 → arm64

# 本机构建静态二进制（在 backend/ 下）
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
  go build -trimpath -ldflags "-s -w" \
  -o aicli-remote-tools ./cmd/aicli-remote-tools

# 传过去（任选）
scp aicli-remote-tools ops@target:/opt/aicli/bin/
# 或
sftp-client ops@target aicli-remote-tools /opt/aicli/bin/aicli-remote-tools
```

`CGO_ENABLED=0` 很重要：默认静态链接，目标机不需要 glibc 版本匹配。

自检：

```bash
/opt/aicli/bin/aicli-remote-tools doctor --workspace /srv/app
```

`doctor` 会打印生效配置并检查常见问题（profile 与 allowlist 是否自相矛盾等）。

真实输出长这样（重点看最后几行的 `warning` 和 `result`）：

```
aicli-remote-tools doctor (0.1.0)
  workspace        : /tmp/aicli-cfg/ws
  allowed roots    : /tmp/aicli-cfg/ws
  profile          : standard
  server name      : wsl
  tool prefix      : remote_wsl
  description      : explicit
  tools (8)        : remote_wsl__append_write, ... remote_wsl__view, remote_wsl__write
  audit            : /tmp/aicli-cfg/audit.ndjson (writable=true)
  jobs root        : - (writable=false)
  artifacts root   : - (writable=false)
  http listen      : 127.0.0.1:18770 (token set=false len=0)
  warning: only the workspace root is allowed; add --allowed-roots for extra paths
  result           : ok
```

`token set=false` 说明启动时还没 export 环境变量 —— 这一项在服务端启动时才生效，
所以 doctor 里显示 false 不代表配置错，但**启动前必须设好**。

---

## 2. 目标机：写 `remote.yaml`

最小可用：

```yaml
workspace: /srv/app          # 相对路径的锚点，同时也是默认 jail 根
profile: readonly            # 先只读跑通，再逐步放开
toolPrefix: remote_srv       # 工具名会变成 remote_srv__view 等
targetName: srv              # 审计与描述里显示的名字
descriptionStyle: explicit   # 建议开：描述里带 [remote:srv]，模型不会误认成本地文件
```

要读写：

```yaml
workspace: /srv/app
profile: standard            # 加 write/edit/append_write/apply_patch
toolPrefix: remote_srv
targetName: srv
descriptionStyle: explicit
```

要执行命令：

```yaml
workspace: /srv/app
profile: ops
toolPrefix: remote_srv
targetName: srv
descriptionStyle: explicit

shell:
  allowedCommands:           # 必须是 argv 前缀，空着等于全拒
    - ./deploy.sh
    - systemctl restart app
    - ls
    - cat
  deniedCommands:            # 永远优先
    - rm
    - dd
  denyShellMetachars: true   # 拒 ; && || | ` $( > < 换行（引号内不算）
  envAllowlist:              # 空 = 不继承任何环境变量
    - HOME
    - PATH
```

HTTP 服务端配置：

```yaml
http:
  listen: 127.0.0.1:18765    # 默认只绑 loopback
  path: /mcp
  tokenEnv: AICLI_REMOTE_TOKEN
  # 非 loopback 时 tokenEnv 必填，否则拒绝启动
```

审计与作业（可选，但强烈建议）：

```yaml
audit:
  path: /var/log/aicli/remote-audit.ndjson   # 0600，NDJSON，记录每次调用

jobs:
  root: /var/lib/aicli/jobs                  # 不配则 job_* 工具不暴露

artifacts:
  root: /var/lib/aicli/artifacts             # 不配则 artifact_read 不暴露
```

---

## 3. 目标机：启动服务

```bash
export AICLI_REMOTE_TOKEN="$(head -c 32 /dev/urandom | base64)"

# 前台试跑
/opt/aicli/bin/aicli-remote-tools serve --http --config /etc/aicli/remote.yaml

# 确认工具面
/opt/aicli/bin/aicli-remote-tools tools --config /etc/aicli/remote.yaml
```

`tools` 的输出就是你将要看到的工具名，抄下来对照本机的 `mcp list`。

生产用 systemd：

```ini
# /etc/systemd/system/aicli-remote.service
[Unit]
Description=aicli remote tools
After=network.target

[Service]
Type=simple
EnvironmentFile=/etc/aicli/remote.env     # 存放 AICLI_REMOTE_TOKEN
ExecStart=/opt/aicli/bin/aicli-remote-tools serve --http --config /etc/aicli/remote.yaml
Restart=always
RestartSec=2
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=/srv/app /var/log/aicli /var/lib/aicli
KillMode=control-group

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now aicli-remote
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:18765/mcp    # 401 = 活着
```

---

## 4. 本机：两个配置文件

放在**项目根目录**的 `.aicli/` 下（项目级优先），或 `~/.aicli/`（用户级）。

### 4a. `.aicli/mcp.yaml` —— 怎么连

```yaml
mcpServers:
  srv:
    name: srv
    description: Production host via aicli-remote-tools
    type: streamable
    url: http://127.0.0.1:18765/mcp
    trustLevel: trusted_remote
    executionScope: remote        # 必须写，否则按本地 MCP 处理，策略会走错分支
    enabled: true
    headers:
      Authorization: Bearer ${AICLI_REMOTE_TOKEN}
    timeout: 120s
```

`executionScope: remote` 是**必填项**：SSH 拉的 stdio 进程在本地看来是
`type: stdio`，不写这一项策略会认为它是本地工具。

### 4b. `.aicli/remote-targets.yaml` —— 连哪些机器（不再管权限）

```yaml
remoteTargets:
  srv:
    transport: http
    remote:
      listen: 127.0.0.1:18765
      workspace: /srv/app
      toolPrefix: remote_srv
      profile: standard
      tokenEnv: AICLI_REMOTE_TOKEN
```

这个文件现在**只负责"连哪些机器"**。它曾经还有一个 `policy:` 块
（`allowRead`/`allowWrite`/`allowShell`/`subagentTargets`），已于 2026-10-22 移除：
远程工具与本地工具走同一条判定链——工具自己声明它是读、写、还是执行，
会话的 permission mode 决定放行还是拦截。在本地再维护一份"能不能写"的开关，
等于对同一个问题给第二个答案，而且它会**压制** mode
（`accept_edits` 本该放行远程写，却被 `BlockRemoteWrites` 提前拒掉）。

旧文件里还留着 `policy:` 的不会报错，但会收到一条
`警告: remoteTargets: ... no longer gates anything`，提醒你去掉它。

**要限制某个远程目标能做什么，用这三个既有入口**：

| 想做的事 | 怎么做 |
|---|---|
| 本次会话只能读 | `aicli chat --permission-mode plan` |
| 关掉某个目标的写 | `--deny-tool "remote_prod_*"`（工具面名单，支持前缀通配） |
| 目标根本不暴露写工具 | 目标侧 `profile: readonly`（服务端 `tools/list` 就不返回） |

`--remote http://host#standard` 里的 `#depth` 也照这个口径理解：它是**给服务端
的 profile 请求**，不是本地授权。`ssh://` 传输会真的带上它（服务端启动时用这个
profile）；`http://` 传输的目标服务已在运行、本地改不了它，所以会明确提示
"`#standard` 未生效"，而不是假装限制了什么。

### 4c. 设置 token

```bash
# Linux/macOS
export AICLI_REMOTE_TOKEN="<与服务端相同的值>"
# Windows PowerShell
$env:AICLI_REMOTE_TOKEN = "<与服务端相同的值>"
# 或写进 .aicli/.env（aicli 会自动加载）
```

---

## 5. 验证

```bash
# 1. 连得上吗
aicli mcp list
#   期望：状态 connected，工具数量与目标机 tools 输出一致

# 2. 工具有哪些
aicli mcp tools srv

# 3. 授权对不对（本地策略，不联网）
go run ./backend/scripts/permission_layers.go .
#   逐工具打印 ALLOW/DENY，一眼看出哪层拦的

# 4. 真调一次
aicli mcp test srv remote_srv__ls '{"path":"."}'

# 5. 在会话里用
aicli chat --allow-tool "remote_srv__*" --prompt "列出 /srv/app 下的文件"
```

`mcp test` 的真实输出（实测：读到 WSL 上的真实文件）：

```
$ aicli mcp test wsl remote_wsl__view '{"file_path":"a.txt"}'
工具调用结果:
─────────────────────────────────────────
  === a.txt (offset 1, limit 400) ===
1: hello
```

注意 `mcp test` 用的是**工具名**（`remote_wsl__view`），不是裸名 `view`。

---

## 6. 三种部署拓扑

### 6a. 本机能直连（内网、VPN）

```ini
url: http://10.0.0.9:18765/mcp
```
目标侧必须绑 `0.0.0.0:18765` **并配 token**（非 loopback 无 token 会拒绝启动）。
更安全：绑 loopback，本机用 SSH 隧道。

### 6b. SSH 本地转发（推荐，最常用）

```bash
# 本机开一条隧道
ssh -N -L 18765:127.0.0.1:18765 ops@target
# 或本仓库的客户端（带断线重连）
ssh-client -N -L 18765:127.0.0.1:18765 ops@target
```

然后 `mcp.yaml` 写 `url: http://127.0.0.1:18765/mcp`。
目标侧可以只绑 loopback，无需对外暴露端口。

### 6c. SSH stdio（目标机一个端口都不开）

不写 `mcp.yaml`，只写 `remote-targets.yaml`：

```yaml
remoteTargets:
  srv:
    transport: ssh-stdio
    ssh:
      host: 10.0.0.9
      user: ops
      identityFile: ~/.ssh/id_ed25519
    remote:
      workspace: /srv/app
      binary: /opt/aicli/bin/aicli-remote-tools
      profile: standard
      toolPrefix: remote_srv
    policy:
      allowRead: true
      allowWrite: true
```

展开后会得到：

```
ssh-client -T -p 22 -i ~/.ssh/id_ed25519 --stdio ops@10.0.0.9 -- \
  /opt/aicli/bin/aicli-remote-tools serve --stdio --config-b64 <base64>
```

每次调用起一个进程，目标机**不监听任何端口**。

---

## 7. 多目标

一台 agent 可以连多台，互不干扰：

```yaml
remoteTargets:
  dev:
    transport: http
    remote:
      listen: 127.0.0.1:18765
      workspace: /tmp/dev
      toolPrefix: remote_dev
    policy: {allowRead: true, allowWrite: true, allowShell: true}

  prod:
    transport: ssh-http
    ssh: {host: 10.0.0.9, user: ops, localForward: 127.0.0.1:18766}
    remote:
      listen: 127.0.0.1:18766
      workspace: /srv/app
      toolPrefix: remote_prod
    policy: {allowRead: true}          # 生产只读
```

结果：`remote_dev__shell` 可用，`remote_prod__write` 被拒。
`toolPrefix` 必须唯一，否则工具名冲突。

---

## 8. 排错

| 现象 | 层 | 原因 | 修法 |
|---|---|---|---|
| `HTTP 401` | L1 | token 没带 / 不匹配 | 检查 `AICLI_REMOTE_TOKEN` 两边一致 |
| 工具数量比预期少 | L2 | profile 档位低 / `tools` 过滤 / `readOnly: true` | 改 `profile`，或看 `tools` 列表 |
| `job_*` 不存在 | L2 | `jobs.root` 没配 | 配 `jobs.root`，profile 需 `ops`/`custom` |
| `artifact_read` 不存在 | L2 | `artifacts.root` 没配 | 配 `artifacts.root` |
| `not configured for this session` | L3 | 没有 `remote-targets.yaml` | 加声明，或补 `policy` 块 |
| `does not allow write/shell` | L3 | `policy` 开关是 false | 打开对应开关 |
| `PATH_DENIED` | L4a | 路径在 jail 外 / 软链逃逸 | 检查 `workspace`、`allowedRoots` |
| `COMMAND_DENIED` | L4b | 不在 allowlist / 命中 deny / 含元字符 | 改 `allowedCommands`，或拆成多条命令 |
| `AGENT_PERMISSION` | L3 | 本地策略 | 这个**不会**出现在目标机审计里 |
| 服务起不来 | L1 | 非 loopback 且无 token | 配 `http.tokenEnv`，或绑 loopback |

最快的定位方式：

```bash
go run ./backend/scripts/permission_layers.go .     # 看 L2/L3
aicli-remote-tools doctor --config remote.yaml      # 看目标和配置本身
tail -f /var/log/aicli/remote-audit.ndjson          # 看 L4（每条都记录决策）
```

**判断"请求有没有出去"**：L3 拒绝时目标机审计里查不到任何记录。
查不到 → 本地拦的；查得到 → 目标侧拦的，审计里的 `error_code` 就是原因。

---

## 9. 最小演练（五分钟）

目标机：

```bash
mkdir -p /srv/demo && echo hello > /srv/demo/a.txt
cd /srv/demo
/opt/aicli/bin/aicli-remote-tools serve --http \
  --workspace /srv/demo --profile standard --tool-prefix remote_demo \
  --http-listen 127.0.0.1:18765
```

本机 `.aicli/mcp.yaml`：

```yaml
mcpServers:
  demo:
    type: streamable
    url: http://127.0.0.1:18765/mcp
    executionScope: remote
    trustLevel: trusted_remote
    enabled: true
```

本机 `.aicli/remote-targets.yaml`：

```yaml
remoteTargets:
  demo:
    transport: http
    remote: {listen: 127.0.0.1:18765, workspace: /srv/demo, toolPrefix: remote_demo}
    policy: {allowRead: true, allowWrite: true}
```

本机：

```bash
ssh -N -L 18765:127.0.0.1:18765 ops@target &
aicli mcp list
aicli mcp test demo remote_demo__view '{"file_path":"a.txt"}'
```
