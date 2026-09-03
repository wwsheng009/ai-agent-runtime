# ssh-client 使用手册

> 对应程序：`cmd/ssh-client`（`ssh-client` / `ssh-client.exe`）
> 作用：项目原生的 OpenSSH 兼容 SSH 客户端——交互式远程 shell、远程命令执行、本地/远程端口转发。基于 `golang.org/x/crypto/ssh`，无需外部 `ssh.exe`。

---

## 目录

1. [概述](#1-概述)
2. [用法](#2-用法)
3. [参数](#3-参数)
4. [认证方式](#4-认证方式)
5. [主机密钥校验](#5-主机密钥校验)
6. [端口转发](#6-端口转发)
7. [超时与防卡死](#7-超时与防卡死)
8. [示例](#8-示例)
9. [相关文档](#9-相关文档)

---

## 1. 概述

`ssh-client` 提供与 OpenSSH 兼容的 SSH 客户端体验：

- 交互式远程 shell
- 远程命令执行（支持管道模式：stdin 转发给远程命令）
- 本地端口转发（`-L`）与远程端口转发（`-R`）
- 公钥认证（Ed25519 / ECDSA / RSA）、SSH 用户证书认证、ssh-agent、密码认证
- `~/.ssh/config` 解析与 `-o` 选项
- `known_hosts` 主机密钥校验
- 跨平台：Windows / Linux / macOS

---

## 2. 用法

```text
Usage: ssh-client [options] [user@]host [command]
```

| 模式 | 示例 |
|------|------|
| 交互式 shell | `ssh-client user@example.com` |
| 远程命令 | `ssh-client user@host "ls -la"` |
| 端口转发（无会话） | `ssh-client -N -L 8080:localhost:80 user@host` |
| 管道模式 | `echo 'uname -a' \| ssh-client user@host` |

---

## 3. 参数

| 参数 | 短选项 | 说明 |
|------|--------|------|
| `--port <n>` | `-p` | SSH 端口（默认 22） |
| `--user <name>` | `-l` | 登录用户名 |
| `--identity-file <path>` | `-i` | 私钥文件路径（可重复） |
| `--password <pw>` | — | 密码（无短选项；省略则交互输入） |
| `--option <kv>` | `-o` | OpenSSH 配置项（`key=value` 或 `'key value'`，可重复） |
| `--quiet` | `-q` | 静默模式 |
| `--verbose` | `-v` | 调试输出 |
| `--config-file <path>` | `-F` | ssh_config 路径（默认 `~/.ssh/config`） |
| `--no-session` | `-N` | 不执行远程命令（仅转发） |
| `--local-forward <spec>` | `-L` | 本地转发 `[bind:]port:host:hostport`（bind 默认 localhost） |
| `--remote-forward <spec>` | `-R` | 远程转发 `[bind:]port:host:hostport` |
| `--no-tty` | `-T` | 禁止分配伪终端 |
| `--tty` | `-t` | 强制分配伪终端 |
| `--version` | `-V` | 显示版本 |
| `--ipv4` / `--ipv6` | `-4` / `-6` | 仅用 IPv4 / IPv6 |
| `--compress` | `-C` | 启用压缩 |
| `--timeout <s>` | — | 连接超时（秒）；等价 `-o ConnectTimeout` |
| `--known-hosts-file <path>` | — | known_hosts 路径 |
| `--help` | `-h` | 帮助 |

---

## 4. 认证方式

认证顺序：**publickey → ssh-agent → password → keyboard-interactive**。

- **公钥**：`-i ~/.ssh/id_ed25519`（Ed25519 / ECDSA / RSA）
- **用户证书**：自动探测 `<key>-cert.pub`，或 `-o CertificateFile=<path>` 显式指定
- **ssh-agent**：自动代理已加载的密钥
- **密码**：`--password '...'`（脚本场景）或省略后交互输入

---

## 5. 主机密钥校验

通过 `-o StrictHostKeyChecking=yes|accept-new|no` 控制（默认 `accept-new`）：

```bash
# 严格校验（首次连接需人工确认指纹）
ssh-client -o StrictHostKeyChecking=yes user@host

# 自动接受新主机密钥（默认）
ssh-client user@host

# 跳过校验（仅限可信网络，不推荐）
ssh-client -o StrictHostKeyChecking=no user@host
```

---

## 6. 端口转发

```bash
# 本地转发：本机 8080 → 远程 localhost:80
ssh-client -N -L 8080:localhost:80 user@host

# 远程转发：远程 2222 → 本机 localhost:22
ssh-client -N -R 2222:localhost:22 user@host
```

`-N`（`--no-session`）表示不建立交互会话，仅保持转发。

---

## 7. 超时与防卡死

- **连接建立**受 `ConnectTimeout` 约束（默认 30s；`--timeout N` 或 `-o ConnectTimeout=N` 修改）
- **死链检测**：`-o ServerAliveInterval=15 -o ServerAliveCountMax=3`
- **ProxyCommand** 支持（通过配置文件）；`ProxyJump` 仅解析未实现

---

## 8. 示例

```bash
# 交互式 shell
ssh-client user@example.com

# 自定义端口 + 密钥执行远程命令
ssh-client -i ~/.ssh/id_ed25519 -p 2222 user@host "ls -la"

# 非交互密码执行
ssh-client --password 'secret' user@host "uptime"

# 本地端口转发（无会话）
ssh-client -N -L 8080:localhost:80 user@host

# 证书登录（自动探测 id_ed25519-cert.pub）
ssh-client -i ~/.ssh/id_ed25519 user@host
```

---

## 9. 相关文档

- [docs/tools/ssh-sftp-clients-usage.md](../tools/ssh-sftp-clients-usage.md) — 完整手册（认证、config、FAQ）
- [ssh-keygen 使用手册](ssh-keygen.md) — 密钥与证书生成
- [sftp-client 使用手册](sftp-client.md)
