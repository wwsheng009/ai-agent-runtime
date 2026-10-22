# 远程部署 prompt 模板（aicli remote tools）

面向"把程序部署到另一台 Linux 服务器"的场景。所有模板都假设目标机已按
`docs/plan/aicli-remote-tools-ssh-http-design-20261021.md` 装好
`aicli-remote-tools`，并通过 `mcp.yaml` + `remote-targets.yaml` 挂载。

---

## 0. 先分清两件事

| | 谁来做 | 怎么做 |
|---|---|---|
| **A. 部署远程工具服务本身**（一次性） | 你 / 运维 | 手工或脚本上传二进制 + 写配置 + 起进程。**agent 不能给自己装手脚** |
| **B. 用远程工具部署业务程序** | agent | prompt 驱动，全程走 `remote_*` 工具 |

下面 §1 是 A，§2 起是 B。

---

## 1. 一次性：把远程工具服务装到目标机

```bash
# 本机构建静态二进制（注意 GOOS/GOARCH 必须匹配目标机）
cd backend
GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" \
  -o aicli-remote-tools ./cmd/aicli-remote-tools

# 送到目标机（两种都行）
scp aicli-remote-tools ops@target:/opt/aicli/bin/
# 或用本仓库的 sftp-client
sftp-client ops@target aicli-remote-tools /opt/aicli/bin/aicli-remote-tools
```

目标机上放配置（示例见设计文档 §7）：

```yaml
# /etc/aicli/remote.yaml
workspace: /srv/svc
profile: ops
toolPrefix: remote_svc
shell:
  allowedCommands: [./status.sh, ./start.sh, ./stop.sh, curl, sha256sum, ln, mv, cp, mkdir, ls, cat]
  deniedCommands:  [rm, sh, bash]
  denyShellMetachars: true
http:
  listen: 127.0.0.1:18765
  tokenEnv: AICLI_REMOTE_TOKEN
```

启动（systemd 或手工）：

```bash
AICLI_REMOTE_TOKEN=<32+ 字节随机值> /opt/aicli/bin/aicli-remote-tools serve --http --config /etc/aicli/remote.yaml
```

本机 `mcp.yaml`：

```yaml
mcpServers:
  svc:
    type: streamable
    url: http://127.0.0.1:18765/mcp     # 或 ssh 隧道后的本地端口
    executionScope: remote
    trustLevel: trusted_remote
    headers:
      Authorization: Bearer ${AICLI_REMOTE_TOKEN}
    enabled: true
```

本机 `.aicli/remote-targets.yaml`（**这一步不能漏**，否则写/执行会被拒）：

```yaml
remoteTargets:
  svc:
    transport: http
    remote:
      listen: 127.0.0.1:18765
      workspace: /srv/svc
      toolPrefix: remote_svc
      profile: ops
      tokenEnv: AICLI_REMOTE_TOKEN
    policy:
      allowRead: true
      allowWrite: true
      allowShell: true
```

自检：

```bash
aicli-remote-tools tools --config /etc/aicli/remote.yaml   # 目标机
aicli mcp list                                             # 本机，应显示 connected + N tools
```

---

## 2. 核心约束：写入能力决定 prompt 怎么写

| 要传的东西 | 能不能用 `remote_*__write` | 正确做法 |
|---|---|---|
| 纯文本源码 / 配置 / 脚本 | ✅ 可以 | 直接让 agent `write` |
| **二进制**（编译产物、镜像、压缩包） | ❌ 不行，`write` 只收字符串 | **带外送达**：`scp` / `sftp-client` / CI 推送到暂存目录，再让 agent 用 `cp` 就位 |

> 这一条必须在 prompt 里写死，否则 agent 会尝试 `base64` 分片重拼，既慢又容易错。

---

## 3. 部署 prompt 模板（可直接改用）

```text
# 部署任务：<服务名> 从 <旧版本> 升级到 <新版本>

## 背景

目标主机是可访问的 Linux 服务器，你**只能**通过 `remote_<target>__*` 工具操作它。
本机（运行你的这台机器）**不是**目标机：不要在本机执行命令，也不要创建文件。

## 现场当前状态（部署前）

- 部署根目录：<绝对路径>
- 布局约定：
  - `releases/<version>/` —— 每个版本的不可变产物
  - `current` —— 指向 `releases/<version>` 的软链
  - `<entry>` —— 指向 `current/<binary>` 的稳定入口
  - `run/*.pid`、`logs/*.log`、`VERSION`
  - `start.sh` / `stop.sh` / `status.sh`
- 当前部署 <旧版本>，进程在跑，监听 <host>:<port>

## 本次要部署的产物

已由 CI 送到暂存目录（**不要自己构建**）：

- 二进制：<暂存绝对路径>
- 期望 sha256：<校验值>

## 目标

把 <新版本> 部署上去，替换 <旧版本>，服务不中断超过必要时间。

### 硬性要求

1. **发布前先校验产物**：`sha256sum` 必须等于期望值。不匹配立即停止，不要部署。
2. **版本目录不可变**：新产物放 `releases/<新版本>/`，不要覆盖 `releases/<旧版本>/`。
3. **端口必须显式指定**：<版本差异导致的默认值变化要写在这里>。
4. **切换用软链**：先建 `current.new` 再 `mv -T current.new current`，避免 `current` 短暂消失。
5. **保留回滚能力**：旧版本目录必须仍在，`current` 可一键切回。
6. **同步所有版本标记**：`VERSION` 文件必须和 `current` 指向的版本一致
   （回滚时也要一起改，否则会出现"文件写 2.0.0、实际跑 1.0.0"）。
7. **验证**（贴真实输出，不要复述）：
   - `status.sh` 显示的版本正确
   - `/healthz` 返回新版本号
   - 新版独有的端点/能力可用
   - 监听端口正确（用 `ss` 确认，**没有**漂移到别的端口）
8. 任一步失败 → **回滚到旧版本**并报告原因。

### 允许使用的命令

<列白名单；并注明禁止管道/重定向/; /&&/$(...)>

## 交付

按顺序执行，每步都基于**真实工具输出**判断，不要假设。
最后一段话汇报：部署结果、验证证据（healthz / metrics / ss 的真实输出）、回滚路径。
```

---

## 4. 几个必须写进 prompt 的点

| 要点 | 不写会怎样 |
|---|---|
| 「本机不是目标机」 | 模型用本地 `shell` 操作，全部白做 |
| 产物在暂存目录、不要自己构建 | 模型尝试在目标机 `go build`（通常没有 Go 工具链） |
| sha256 期望值 | 无法发现传输损坏 / 送错版本 |
| 端口显式指定 | 新版本默认值一变，服务就漂移到别的端口，健康检查全挂 |
| `mv -T` 原子切换 | `ln -sfn` 直接覆盖时 `current` 可能短暂不存在，重启脚本读到空路径 |
| 「保留旧版本目录」 | 模型可能"清理"掉旧 release，回滚能力归零 |
| 多条命令禁止 `;` / `&&` | 目标侧 `denyShellMetachars` 会拒，模型要绕成多次调用 |
| 要求贴真实输出 | 模型倾向于总结"已成功"，无法验证 |

---

## 5. 权限与审批怎么配

部署类任务一次会产生十几到几十条命令。逐条交互审批会让会话变成"点确认模拟器"。

推荐：

```bash
# 预授权整个远程工具面，人工确认放在部署前后两个节点
aicli chat --allow-tool "remote_svc__*" --permission-mode accept_edits
```

不适合：

- 让模型在 `accept_edits` 下逐条问 —— 一次部署能产生 15+ 次审批
- 把远程写权限开成"所有 target" —— 用 `remote-targets.yaml` 按 target 收窄

无人值守（CI / 夜间任务）：

```bash
aicli chat --permission-mode dont_ask --allow-tool "remote_svc__*" --prompt "$(cat deploy-prompt.md)" --no-interactive
```

`dont_ask` 会让未授权调用直接失败而不是挂起等审批 —— 这比 `bypass_permissions` 更适合无人值守，
因为**越权会显式报错**而不是静默放行。

---

## 6. 推荐加一道"部署后自检脚本"

prompt 里的"贴真实输出"依赖模型自觉。更可靠的做法是在目标机放一个验收脚本，
让**外部**（你或 CI）在模型回合结束后独立重跑：

```bash
go run ./scripts/remote_verify_only.go -verify ./verify-deploy.sh
```

`verify-deploy.sh` 的写法见设计文档 §10.3.1：只读目标机真实状态，
最后打印 `VERIFY_OK`。这样"模型说成功"和"确实成功"是两件独立的事。
