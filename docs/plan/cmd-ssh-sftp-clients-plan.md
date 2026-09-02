# SSH / SFTP 客户端实现方案 (cmd/ssh-client & cmd/sftp-client)

状态：planned
日期：2026-09-01
适用仓库：`E:\projects\ai\ai-agent-runtime`

## 1. 背景与目标

### 1.1 动机

当前项目缺少原生 SSH/SFTP 命令行工具。在自动化运维、远程执行、文件传输等场景下，需要依赖外部 OpenSSH 客户端（`ssh.exe`/`sftp.exe`），增加了外部依赖和跨平台差异维护成本。规划两个独立的 CLI 程序，直接通过 Go 的 SSH 库实现，提供与 OpenSSH 兼容的体验。

### 1.2 目标

| 目标 | 说明 |
|------|------|
| **SSH 客户端** | 交互式远程 shell、远程命令执行、端口转发、SCP 等，与 `ssh` 命令兼容 |
| **SFTP 客户端** | 交互式/批处理文件传输，与 `sftp` 命令兼容 |
| **用户名密码登录** | 支持 `-p password` 方式或交互式密码输入 |
| **公钥免密登录** | 支持 RSA/ECDSA/Ed25519 密钥、`-i` 指定密钥文件、ssh-agent |
| **OpenSSH 兼容** | 解析 `~/.ssh/config`、校验 `known_hosts`、支持标准密钥格式与命令行选项 |
| **跨平台** | Windows / Linux / macOS 一致体验 |

### 1.3 非目标（首版不包含）

- SSH 服务端（`sshd`）
- 完整的 SCP 协议（可用 SFTP 替代）
- X11 转发
- SOCKS 代理转发
- CA 签名证书认证（SSH Certificate，由证书颁发机构签名的用户证书）

> **术语说明**：本方案的"公钥免密登录"（`-i` / `~/.ssh/id_*`）即用户所说的"证书登录"，是首版支持的核心能力；CA 签名证书（`ssh-keygen -s ca_key` 生成的 `*-cert.pub`）属于独立机制，首版不支持。

## 2. 总体架构

### 2.1 分层设计

```
┌─────────────────────────────────────────────────────────────────┐
│                        CLI 入口层                                 │
│  ┌─────────────────────────┐  ┌──────────────────────────────┐  │
│  │  cmd/ssh-client/main.go  │  │  cmd/sftp-client/main.go     │  │
│  │  (flag / cobra 解析)     │  │  (flag / cobra 解析)        │  │
│  └───────────┬─────────────┘  └──────────────┬───────────────┘  │
├──────────────┼────────────────────────────────┼──────────────────┤
│              ▼                                ▼                  │
│  ┌──────────────────────────────────────────────────────────┐  │
│  │               internal/sshclient/                         │  │
│  │  ┌─────────────┐ ┌──────────────┐ ┌──────────────────┐  │  │
│  │  │ auth.go     │ │ conn.go      │ │ session.go       │  │  │
│  │  │ 认证策略    │ │ 连接管理     │ │ 会话/通道       │  │  │
│  │  ├─────────────┤ ├──────────────┤ ├──────────────────┤  │  │
│  │  │ config.go   │ │ hostkey.go   │ │ sftp.go          │  │  │
│  │  │ OpenSSH     │ │ known_hosts  │ │ SFTP 操作封装   │  │  │
│  │  │ config 解析  │ │ 校验         │ │                  │  │  │
│  │  └─────────────┘ └──────────────┘ └──────────────────┘  │  │
│  └──────────────────────────────────────────────────────────┘  │
│                                │                                │
│                                ▼                                │
│  ┌──────────────────────────────────────────────────────────┐  │
│  │             golang.org/x/crypto/ssh                      │  │
│  │           github.com/pkg/sftp                            │  │
│  └──────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────┘
```

### 2.2 核心设计原则

1. **共享认证与连接层**：`internal/sshclient` 包封装 SSH 连接建立、认证、known_hosts 校验、`~/.ssh/config` 解析等逻辑，SSH 和 SFTP 客户端共用。
2. **CLI 独立**：`cmd/ssh-client` 和 `cmd/sftp-client` 各自独立，互不依赖，只依赖 `internal/sshclient`。
3. **OpenSSH 兼容优先**：命令行选项、配置读取、密钥格式、known_hosts 校验等行为优先对齐 OpenSSH 客户端规范。
4. **最小外部依赖**：仅依赖 `golang.org/x/crypto/ssh` 和 `github.com/pkg/sftp`，不引入额外 SSH 实现。

## 3. 目录与模块规划

### 3.1 目录结构

```
backend/
├── cmd/
│   ├── ssh-client/                    # SSH 客户端入口
│   │   ├── main.go                    # 入口：flag 解析 + 调用
│   │   ├── main_test.go               # 集成测试
│   │   └── README.md
│   └── sftp-client/                   # SFTP 客户端入口
│       ├── main.go
│       ├── main_test.go
│       └── README.md
└── internal/
    └── sshclient/                     # 共享 SSH 认证/连接库
        ├── auth.go                    # 认证策略（密码/公钥/agent）
        ├── auth_test.go
        ├── conn.go                    # 连接建立、重连、复用
        ├── conn_test.go
        ├── config.go                  # ~/.ssh/config 解析器
        ├── config_test.go
        ├── hostkey.go                 # known_hosts 校验
        ├── hostkey_test.go
        ├── session.go                 # 交互式会话/远程命令执行
        ├── session_test.go
        ├── sftp.go                    # SFTP 操作封装
        ├── sftp_test.go
        └── doc.go                     # 包文档
```

### 3.2 模块职责

| 内部包 | 文件 | 职责 |
|--------|------|------|
| `sshclient` | `auth.go` | 认证方法选择器：按配置依次尝试 password、publickey、keyboard-interactive、agent |
| `sshclient` | `conn.go` | 建立 `ssh.Client` 连接、TCP 拨号、超时控制、连接复用 |
| `sshclient` | `config.go` | 解析 `~/.ssh/config`（Host、User、HostName、Port、IdentityFile、StrictHostKeyChecking 等常用指令子集，完整列表见 §5.1.1） |
| `sshclient` | `hostkey.go` | 读取/校验 `known_hosts`，支持 `HostKeyCallback` 策略 |
| `sshclient` | `session.go` | 封装 `ssh.Session`，只提供远程命令执行与会话生命周期管理（不含交互式 PTY 逻辑） |
| `sshclient` | `sftp.go` | 封装 `github.com/pkg/sftp` 的 `Client.Open/Read/Write/Stat/ReadDir/Mkdir/Remove` 等操作 |

## 4. 认证能力设计

### 4.1 认证方法流程图

```
CLI 参数 / .ssh/config 解析
          │
          ▼
┌─────────────────────┐
│  认证策略编排        │
│  sshclient.AuthMethod│
└────────┬────────────┘
         │
         ├─ 1. PublicKey (如果指定了 -i 或 IdentityFile)
         │     ├─ 读取 PEM / OpenSSH 格式私钥
         │     ├─ 无密码→直接 Signer
         │     └─ 有密码→提示输入 passphrase 解密
         ├─ 2. SSH-Agent (优先)
         │     ├─ 连接 $SSH_AUTH_SOCK (Unix) / pageant (Windows)
         │     └─ 通过 agent 签名
         ├─ 3. Password (如果指定了 --password 或提示输入)
         │     └─ ssh.Password(password)
         └─ 4. Keyboard-Interactive (fallback)
               └─ 处理挑战-响应认证
```

### 4.2 认证方法详情

| 认证方法 | CLI 触发方式 | 对应 OpenSSH 选项 | 说明 |
|----------|-------------|-------------------|------|
| **密码** | `--password <pwd>` / 交互提示 | `sshpass` / `SSH_ASKPASS` | 仅 `--password` 长选项（无短选项，避免与端口标志冲突）；交互模式若未提供密码则从终端读取 |
| **公钥** | `--identity-file <path>` / `-i <path>` | `-i identity_file` | 支持 RSA (1024/2048/4096)、ECDSA (P256/P384/P521)、Ed25519。未指定 `-i` 时按 **Ed25519 → ECDSA → RSA** 顺序自动尝试 `~/.ssh/id_ed25519`、`~/.ssh/id_ecdsa`、`~/.ssh/id_rsa`（与 OpenSSH 一致） |
| **ssh-agent** | 默认尝试（不传密码和 -i 时） | agent forwarding | Unix 通过 `$SSH_AUTH_SOCK` 连接；Windows 支持 `pageant` (PuTTY) 或 OpenSSH 自带的 `ssh-agent` |
| **Keyboard-Interactive** | 自动 fallback | 无需显式选项 | 处理服务器端挑战-响应，如一次性密码或自定义认证 |

### 4.3 密钥格式支持

| 格式 | 支持情况 | 解析方式 |
|------|---------|---------|
| PEM (RFC 7468) | ✅ 首版 | `ssh.ParseRawPrivateKey` + `ssh.NewSignerFromKey` |
| OpenSSH 格式 (bcrypt/pbkdf2) | ✅ 首版 | `ssh.ParseRawPrivateKeyWithPassphrase` |
| PPK (PuTTY) | ❌ 首版不支持 | 需第三方库或转换 |
| SSH 证书 (CA 签名) | ❌ 首版不支持 | 后续扩展 |

## 5. OpenSSH 兼容性设计

### 5.1 ~/.ssh/config 解析

支持的指令（首版优先实现最常用的，后续可扩展）：

| 指令 | 映射 | 优先级 |
|------|------|--------|
| `Host` | 匹配块 | CLI 目标主机名匹配时使用 |
| `HostName` | 连接目标主机 | CLI 未指定 IP 时使用 |
| `Port` | 连接端口 | CLI 端口选项覆盖（`ssh-client -p` / `sftp-client -P`，对齐 OpenSSH） |
| `User` | 登录用户名 | CLI `user@host` 的 user 覆盖 |
| `IdentityFile` | 密钥文件路径 | CLI `-i` 覆盖 |
| `IdentitiesOnly` | 是否只使用指定密钥 | 默认 false |
| `PreferredAuthentications` | 认证方法优先级 | 控制认证顺序 |
| `ProxyJump` | 跳板机 | 首版不实现；解析到该指令时输出警告并忽略 |
| `StrictHostKeyChecking` | HostKeyCallback 策略 | 控制 known_hosts 校验严格度 |
| `UserKnownHostsFile` | known_hosts 路径 | 默认 `~/.ssh/known_hosts` |
| `ConnectTimeout` | 连接超时 | CLI `-o ConnectTimeout=...` 覆盖 |
| `ServerAliveInterval` | 心跳/保活 | 保持连接检测 |

解析策略：使用 `golang.org/x/crypto/ssh` 内置的配置解析能力，或自行实现最小配置解析器（因为 `x/crypto` 没有提供完整的 config 解析器，可能需要用 `github.com/kevinburke/ssh_config` 或自行实现）。

### 5.1.1 `-o` 选项白名单（首版支持）

`-o key=value` 仅接受以下白名单，其余选项解析到后输出警告并忽略，避免出现 OpenSSH 支持但本客户端未实现的静默差异：

| `-o` 选项 | 含义 | 对应实现 |
|-----------|------|---------|
| `StrictHostKeyChecking` | known_hosts 严格度（yes/no/accept-new） | §5.2 HostKeyCallback 策略 |
| `UserKnownHostsFile` | known_hosts 文件路径 | §5.2 读取路径 |
| `ConnectTimeout` | 连接超时（秒） | `conn.go` 拨号超时 |
| `ServerAliveInterval` | 保活心跳间隔（秒） | `session.go` 心跳 |
| `ServerAliveCountMax` | 保活失败最大次数 | `session.go` 心跳 |
| `HostKeyAlgorithms` | 主机密钥算法白名单 | `ssh.ClientConfig.HostKeyAlgorithms` |
| `PreferredAuthentications` | 认证方法优先级 | §4.2 认证编排 |
| `LogLevel` | 日志级别 | 日志输出控制 |
| `Compression` | 是否启用压缩 | `ssh.Config` 压缩 |

### 5.2 known_hosts 校验

| 策略 | 对应 HostKeyCallback | 说明 |
|------|---------------------|------|
| **Strict** (默认) | `ssh.FixedHostKey` / 自定义 `known_hosts` 解析 | 自动信任首次连接并记录；后续连接校验指纹 |
| **AcceptNew** | 首次自动添加，后续严格校验 | 对齐 OpenSSH `StrictHostKeyChecking=accept-new` |
| **Insecure** | `ssh.InsecureIgnoreHostKey` | 跳过校验（仅用于开发/测试，输出警告） |
| **Custom** | 携带 `@cert-authority` 支持 | 首版不实现，预留接口 |

### 5.3 命令行选项兼容

#### SSH 客户端 (`ssh-client`)

```
ssh-client [options] [user@]host [command]

选项：
  -p, --port <port>           SSH 端口（默认 22，对齐 OpenSSH `ssh -p`）
  -l, --user <user>           登录用户名
  -i, --identity-file <path>  私钥文件路径
  --password <password>       密码（无短选项，避免与端口 `-p` 冲突；未提供则交互式输入）
  -o, --option <key=value>    OpenSSH 兼容选项（如 -o StrictHostKeyChecking=no）
  -q, --quiet                 静默模式（抑制警告与横幅）
  -v, --verbose               详细输出（调试模式）
  -F, --config-file <path>    ssh_config 文件路径（默认 ~/.ssh/config，对齐 OpenSSH `-F`）
  -N, --no-session            不执行远程命令（仅用于端口转发）
  -L, --local-forward <bind:port:host:hostport>  本地端口转发
  -R, --remote-forward <bind:port:host:hostport> 远程端口转发
  -T, --no-tty                不分配伪终端
  -t, --tty                   强制分配伪终端
  -V, --version               显示版本
  -4, --ipv4                  仅使用 IPv4
  -6, --ipv6                  仅使用 IPv6
  -C, --compress              启用压缩（如果服务器支持）
  --timeout <seconds>         连接超时
  --known-hosts-file <path>   known_hosts 文件路径
```

#### SFTP 客户端 (`sftp-client`)

```
sftp-client [options] [user@]host[:path] [path2]

选项：
  -P, --port <port>           SSH 端口（默认 22，对齐 OpenSSH `sftp -P`）
  -l, --user <user>           登录用户名
  -i, --identity-file <path>  私钥文件路径
  --password <password>       密码（无短选项，避免与端口 `-P` 冲突；未提供则交互式输入）
  -o, --option <key=value>    OpenSSH 兼容选项
  -q, --quiet                 静默模式（抑制警告与横幅）
  -v, --verbose               详细输出
  -F, --config-file <path>    ssh_config 文件路径（默认 ~/.ssh/config，对齐 OpenSSH `-F`）
  -b, --batch <file>          批处理命令文件（每行一个命令）
  -R, --recursive             递归操作
  -f, --force                 强制执行（覆盖已存在文件等）
  -V, --version               显示版本
  -4, --ipv4                  仅使用 IPv4
  -6, --ipv6                  仅使用 IPv6
  --timeout <seconds>         连接超时
  --known-hosts-file <path>   known_hosts 文件路径
```

## 6. SSH 客户端功能设计

### 6.1 交互式远程 Shell

- 分配 PTY（通过 `ssh.Session.RequestPty`）
- 设置 terminal modes（echo、行数、列数等）
- 本地终端原始模式（通过 `golang.org/x/term` 设置 raw mode）
- 窗口大小变更传播（`SIGWINCH` → `ssh.Session.WindowChange`）
- 信号转发（`SIGINT` → Ctrl+C, `SIGTERM` → 关闭会话）
- 退出码传递（`ssh.Session.Wait()` 返回的 exit status）

### 6.2 远程命令执行

- 不分配 PTY 时直接执行 `session.Run(command)`
- 捕获 stdout/stderr
- 返回退出码
- 支持管道模式：`echo "command" | ssh-client host`

### 6.3 端口转发

- 本地端口转发（`-L`）：本地监听 → 通过 SSH 隧道 → 远程目标
- 远程端口转发（`-R`）：远程监听 → 通过 SSH 隧道 → 本地目标
- 使用 `ssh.Client.Listen` / `ssh.Client.Dial` 和通道监听

### 6.4 会话管理

- 单个 SSH 连接支持多个 channel（shell + 转发 + SCP 等）
- 连接复用（`ssh.Client` 可复用）
- 优雅关闭（`SIGTERM` → 关闭会话 → 关闭连接）

## 7. SFTP 客户端功能设计

### 7.1 交互模式

启动后进入交互式命令提示符 `sftp>`，支持以下命令集：

| 命令 | 简写 | 功能 | 对应 OpenSSH |
|------|------|------|-------------|
| `ls [-la] [path]` | `ls` | 列出远程目录 | ✅ 相同 |
| `cd <path>` | `cd` | 切换远程目录 | ✅ 相同 |
| `pwd` | `pwd` | 显示远程当前目录 | ✅ 相同 |
| `lls [path]` | `lls` | 列出本地目录 | ✅ 相同 |
| `lcd <path>` | `lcd` | 切换本地目录 | ✅ 相同 |
| `lpwd` | `lpwd` | 显示本地当前目录 | ✅ 相同 |
| `get [-r] <remote> [local]` | `get` | 下载文件/目录 | ✅ 相同 |
| `put [-r] <local> [remote]` | `put` | 上传文件/目录 | ✅ 相同 |
| `rm <file>` | `rm` | 删除远程文件 | ✅ 相同 |
| `rmdir <dir>` | `rmdir` | 删除远程空目录 | ✅ 相同 |
| `mkdir <dir>` | `mkdir` | 创建远程目录 | ✅ 相同 |
| `chmod <mode> <path>` | `chmod` | 修改远程文件权限 | ✅ 相同 |
| `chown <uid>:<gid> <path>` | `chown` | 修改远程文件所有者 | ✅ 相同 |
| `rename <old> <new>` | `rename` | 重命名远程文件 | ✅ 相同 |
| `symlink <target> <link>` | `symlink` | 创建远程符号链接 | ✅ 相同 |
| `stat <path>` | `stat` | 显示远程文件信息 | ✅ 相同 |
| `!<command>` | `!` | 执行本地命令 | ✅ 相同 |
| `help` | `?` | 显示帮助 | ✅ 相同 |
| `quit` / `exit` | `q` | 退出 | ✅ 相同 |

### 7.2 批处理模式

- `-b <batchfile>`：从文件读取命令逐行执行
- 错误处理：遇到错误时是否继续（OpenSSH 默认停止）
- 支持 `echo` 和注释（`#`）

### 7.3 直接传输模式

- `sftp-client user@host:remote-path local-path`：下载文件
- `sftp-client user@host:remote-dir/`：列出远程目录
- 支持递归传输（`-r`）

## 8. 依赖引入

### 8.1 新增依赖

| 依赖 | 版本 | 用途 | 备注 |
|------|------|------|------|
| `golang.org/x/crypto` | 最新 | SSH 协议核心（`ssh` 子包） | 已在 indirect 中？需要显式 `go get` |
| `github.com/pkg/sftp` | v1.13+（固定版本） | SFTP 协议实现 | 使用 `golang.org/x/crypto/ssh` 作为底层；上游维护频率较低，需在 `go.mod` 固定版本并评估备选方案（见 §12.1） |
| `github.com/kevinburke/ssh_config` | 最新 | 解析 `~/.ssh/config`（可选） | 或自行实现最小解析器 |

### 8.2 已有可复用依赖

| 依赖 | 位置 | 复用方式 |
|------|------|---------|
| `golang.org/x/term` | 已有 | 终端原始模式设置、窗口大小获取 |
| `golang.org/x/sys` | 已有 | 平台相关系统调用（信号、窗口大小等） |
| `github.com/spf13/pflag` | 已有 | CLI 选项解析（复用现有模式） |
| `github.com/spf13/cobra` | 已有 | 如需子命令模式 |

## 9. 错误处理与安全

### 9.1 错误处理原则

- 连接失败 → 输出人类可读错误信息 + 退出码 255（与 OpenSSH 一致）
- 认证失败 → 明确提示是密码错误、密钥拒绝还是无可用方法
- 主机密钥变更 → 输出警告并拒绝连接（除非 `StrictHostKeyChecking=no`）
- 远程命令失败 → 传递远程退出码
- 网络超时 → 输出"Connection timed out"并退出

#### 退出码对照表（与 OpenSSH 对齐）

| 退出码 | 含义 | 说明 |
|--------|------|------|
| `0` | 成功 | 命令正常执行完成 |
| `1` | 远程命令执行失败 | 非零退出码由远程命令产生，原样透传 |
| `130` | 本地中断 | 用户按 Ctrl+C 中断 |
| `254` | 参数/配置错误 | 命令行参数非法、配置文件无法解析 |
| `255` | 连接失败/认证失败 | 网络错误、超时、认证失败、主机密钥校验失败 |

> SFTP 客户端批处理模式沿用 OpenSSH `sftp -b` 语义：批处理中任一条命令失败默认中止并返回对应退出码。

### 9.2 安全考虑

| 安全项目 | 措施 |
|---------|------|
| 密码处理 | 密码不在进程参数中持久化（`--password` 参数解析后立即覆盖）；交互模式使用 `golang.org/x/term.ReadPassword` 不回显 |
| 私钥处理 | 私钥内容仅保留在内存 `Signer` 中，不落盘；解密 passphrase 不在日志中输出 |
| 主机密钥校验 | 默认严格模式（首次连接提示确认，后续校验）；提供 `-o StrictHostKeyChecking=no` 但输出警告 |
| 中间人攻击 | 默认开启 `known_hosts` 校验，拒绝自动接受未知主机密钥 |
| 会话超时 | 支持 `ServerAliveInterval` + `ServerAliveCountMax` 检测死连接 |
| 日志安全 | 日志级别 `-v` 时仍屏蔽密码、私钥内容、会话内容 |

## 10. 测试策略

### 10.1 单层测试

| 层次 | 内容 | 工具 |
|------|------|------|
| 单元测试 | `auth.go` 认证策略编排、`config.go` 配置解析、`hostkey.go` known_hosts 校验 | `testing` + `testify` |
| 连接测试 | 使用 `sshd` Docker 容器或本地 SSH 服务器进行连接测试 | `docker testcontainers` 或 `exec.Command("sshd")` |
| 端到端测试 | 完整 SSH/SFTP 会话（启动临时 sshd 服务器） | `testing` + 协程编排 |

### 10.2 测试场景

- 密码认证成功/失败
- 公钥认证（无密码/有密码私钥）
- ssh-agent 认证
- known_hosts 首次连接/主机密钥变更/严格模式
- 交互式 shell 输入输出
- 远程命令执行 + 退出码
- 端口转发（本地/远程）
- SFTP 文件上传/下载/递归目录
- 超时与重连行为
- 大文件传输（边界条件）

### 10.3 测试基础设施

- 使用 `docker compose` 或 GitHub Actions 启动临时 `sshd` 容器
- 预生成测试用 SSH 密钥（rsa + ecdsa + ed25519）存入测试 fixture
- 模拟 `known_hosts` 文件场景
- 模拟 `~/.ssh/config` 解析场景

## 11. 实施阶段

### 阶段一：共享 SSH 认证/连接库（`internal/sshclient`）

| 任务 | 预计工时 | 产出 |
|------|---------|------|
| 1.1 基础认证方法（密码 + 公钥） | 2d | `auth.go` + 单元测试 |
| 1.2 ssh-agent 支持 | 1d | `auth.go` agent 扩展 |
| 1.3 连接建立与超时控制 | 1d | `conn.go` + 测试 |
| 1.4 ~/.ssh/config 解析 | 2d | `config.go` + 测试 |
| 1.5 known_hosts 校验 | 1d | `hostkey.go` + 测试 |
| 1.6 会话生命周期与远程命令封装 | 1d | `session.go` + 测试（不含交互式 PTY 逻辑，见 §3.2） |
| 合计 | 8d | 完整共享库 + 测试覆盖 |

### 阶段二：SSH 客户端 CLI（`cmd/ssh-client`）

| 任务 | 预计工时 | 产出 |
|------|---------|------|
| 2.1 CLI 框架 + 选项解析 | 1d | `main.go` 框架 |
| 2.2 交互式远程 shell | 2d | PTY 分配 + 终端原始模式 + 窗口调整 |
| 2.3 远程命令执行 | 1d | 命令执行 + 退出码传递 |
| 2.4 端口转发（本地/远程） | 2d | `-L` / `-R` 实现 |
| 2.5 集成测试 | 1d | Docker sshd 测试 |
| 合计 | 7d | 完整 SSH 客户端 |

### 阶段三：SFTP 客户端 CLI（`cmd/sftp-client`）

| 任务 | 预计工时 | 产出 |
|------|---------|------|
| 3.1 SFTP 操作封装 | 1d | `sftp.go` 基础操作 |
| 3.2 CLI 框架 + 选项解析 | 1d | `main.go` 框架 + 连接 |
| 3.3 交互式命令解释器 | 2d | 命令循环 + 所有子命令 |
| 3.4 批处理模式 | 1d | `-b` 解析执行 |
| 3.5 直接传输模式 | 1d | 一键上传/下载 |
| 3.6 集成测试 | 1d | Docker sshd + SFTP 测试 |
| 合计 | 7d | 完整 SFTP 客户端 |

### 阶段四：文档与发布

| 任务 | 工时 |
|------|------|
| README (用法/示例) | 1d |
| 构建脚本集成（Makefile） | 0.5d |
| 新程序注册到 CI（参考现有 Makefile 中 `aicli`/`runtime-server` 的构建目标，新增 `ssh-client`/`sftp-client` 目标并加入 GitHub Actions 构建矩阵） | 1d |
| 可执行文件打包 | 0.5d |
| 合计 | 3d |

## 12. 风险与决策点

### 12.1 风险

| 风险 | 影响 | 缓解措施 |
|------|------|---------|
| `golang.org/x/crypto/ssh` 在 Windows 上 PTY 支持不完善 | 交互式 shell 体验差 | 使用 `golang.org/x/term` 处理 raw mode；Windows 上通过 conpty 或 `os.Stdin` 直接读写 |
| `~/.ssh/config` 解析复杂度高（Host 通配符、Include 指令） | 配置解析不完整 | 首版实现常用指令子集；使用 `github.com/kevinburke/ssh_config` 降低复杂度 |
| SSH 协议版本协商兼容性 | 无法连接部分老旧服务器 | 默认使用 SSH-2.0，支持 `-o HostKeyAlgorithms` 等降级选项 |
| Windows 上 `known_hosts` 路径不同 | 主机密钥校验失败 | 兼容 `%USERPROFILE%\.ssh\known_hosts` 和 POSIX 路径 |
| SFTP 大文件传输内存占用 | OOM | 流式读写（`io.Copy` 限 buffer），不分块加载到内存 |
| `github.com/pkg/sftp` 上游维护频率低，API 可能滞后于 `golang.org/x/crypto` | 依赖风险 | 在 `go.mod` 中固定 v1.13+ 版本并在 `go.sum` 锁定；同步评估备选方案（基于 `x/crypto/ssh` channel 自行实现 SFTP 子集传输） |

### 12.2 决策点

| 决策 | 选项 | 建议 |
|------|------|------|
| CLI 框架 | `pflag` vs `cobra` | 复用现有 `pflag` 模式，保持项目一致性；如需子命令可后期引入 `cobra` |
| ssh_config 解析 | 全量实现 vs 第三方库 | 优先使用 `github.com/kevinburke/ssh_config`，减少维护成本 |
| 私钥解密 | 交互提示 passphrase 程序化 | 交互式从终端读取；非交互模式通过 `SSH_ASKPASS` 或环境变量 `SSH_KEY_PASSPHRASE` |
| 端口转发实现 | 单连接 vs 多 channel | 单连接上多 channel 实现，更接近 OpenSSH 行为 |
| 构建产物命名 | `ssh-client.exe` / `sftp-client.exe` | 与现有 `aicli.exe`、`runtime-server.exe` 保持一致 |

## 13. 总结

本方案规划了两个与 OpenSSH 兼容的 CLI 程序（`cmd/ssh-client` 和 `cmd/sftp-client`），通过共享 `internal/sshclient` 库提供一致的 SSH 认证与连接能力。首版支持密码登录和公钥免密登录，兼容 `~/.ssh/config` 和 `known_hosts`，确保安全基线。实施分为四个阶段，总计约 25 个工作日。