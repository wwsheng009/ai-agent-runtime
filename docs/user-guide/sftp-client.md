# sftp-client 使用手册

> 对应程序：`cmd/sftp-client`（`sftp-client` / `sftp-client.exe`）
> 作用：项目原生的 OpenSSH 兼容 SFTP 客户端——交互式/批处理文件传输、命令行直传（上传/下载/列目录）。基于 `golang.org/x/crypto/ssh`，无需外部 `sftp.exe`。

---

## 目录

1. [概述](#1-概述)
2. [用法与模式](#2-用法与模式)
3. [参数](#3-参数)
4. [交互式命令](#4-交互式命令)
5. [批处理模式](#5-批处理模式)
6. [认证与安全](#6-认证与安全)
7. [超时与防卡死](#7-超时与防卡死)
8. [示例](#8-示例)
9. [相关文档](#9-相关文档)

---

## 1. 概述

`sftp-client` 提供与 OpenSSH sftp 兼容的文件传输体验，支持四种模式：

| 模式 | 命令形式 |
|------|----------|
| 交互式 | `sftp-client user@host` |
| 批处理 | `sftp-client -b batch.txt user@host` |
| 上传 | `sftp-client user@host local-file remote-path` |
| 下载 | `sftp-client user@host:remote-path local-file` |
| 列目录 | `sftp-client user@host:remote-dir/` |

---

## 2. 用法

```text
Usage: sftp-client [options] [user@]host[:remote-path] [local-path...]
```

- **目录传输**需要 `-R/--recursive`；已存在的文件默认跳过，除非 `-f/--force`
- **批处理**（`-b`）在第一条失败命令处中止并返回非零退出码

---

## 3. 参数

| 参数 | 短选项 | 说明 |
|------|--------|------|
| `--port <n>` | `-P` | SSH 端口（默认 22） |
| `--user <name>` | `-l` | 登录用户名 |
| `--identity-file <path>` | `-i` | 私钥文件路径（可重复） |
| `--password <pw>` | — | 密码（无短选项；省略则交互输入） |
| `--option <kv>` | `-o` | OpenSSH 配置项（`key=value`，可重复） |
| `--quiet` | `-q` | 静默模式 |
| `--verbose` | `-v` | 调试输出 |
| `--config-file <path>` | `-F` | ssh_config 路径（默认 `~/.ssh/config`） |
| `--batch <file>` | `-b` | 批处理文件（每行一条命令） |
| `--recursive` | `-R` | 递归目录传输 |
| `--force` | `-f` | 覆盖已存在文件 |
| `--version` | `-V` | 显示版本 |
| `--ipv4` / `--ipv6` | `-4` / `-6` | 仅用 IPv4 / IPv6 |
| `--timeout <s>` | — | 连接超时（秒） |
| `--known-hosts-file <path>` | — | known_hosts 路径 |
| `--help` | `-h` | 帮助 |

---

## 4. 交互式命令

启动后进入 `sftp>` 提示符，支持命令（完整列表输入 `help`）：

| 命令 | 说明 |
|------|------|
| `ls [-la] [path]` | 列远程目录 |
| `lls [path]` | 列本地目录 |
| `cd <path>` | 切换远程目录 |
| `get [-r] <remote> [local]` | 下载文件/目录 |
| `put [-r] <local> [remote]` | 上传文件/目录 |
| `rm <file>` / `rmdir <dir>` | 删除远程文件 / 空目录 |
| `mkdir <dir>` | 创建远程目录 |
| `chmod <mode> <path>` | 修改远程权限 |
| `chown <uid>:<gid> <path>` | 修改远程属主 |
| `rename <old> <new>` | 重命名 |
| `symlink <target> <link>` | 创建符号链接 |
| `stat <path>` | 查看远程文件信息 |
| `echo <text>` | 本地打印 |
| `!<command>` | 执行本地 shell 命令 |
| `help` / `?` | 帮助 |
| `quit` / `exit` / `bye` | 退出 |

---

## 5. 批处理模式

批处理文件每行一条交互式命令，遇到首条失败即中止：

```text
# batch.txt
ls -la
get -r /data/logs ./logs
quit
```

```bash
sftp-client -b batch.txt user@host
```

---

## 6. 认证与安全

认证方式与 `ssh-client` 相同：公钥 → ssh-agent → 密码 → keyboard-interactive；支持证书（自动探测 `<key>-cert.pub` 或 `-o CertificateFile`）。主机密钥校验默认 `accept-new`，可用 `-o StrictHostKeyChecking=yes|no` 调整。

---

## 7. 超时与防卡死

- 连接建立受 `ConnectTimeout` 约束（默认 30s；`--timeout N` 或 `-o ConnectTimeout=N` 修改）
- 死链检测：`-o ServerAliveInterval=15 -o ServerAliveCountMax=3`

---

## 8. 示例

```bash
# 交互式会话
sftp-client user@host

# 上传单文件
sftp-client user@host ./local.txt /remote/path/

# 上传目录（递归）
sftp-client -R user@host ./logs /remote/logs/

# 下载文件
sftp-client user@host:/remote/file.txt ./local.txt

# 下载目录
sftp-client -R user@host:/data/logs ./logs

# 列目录
sftp-client user@host:/var/www/

# 批处理
sftp-client -b batch.txt user@host
```

---

## 9. 相关文档

- [docs/tools/ssh-sftp-clients-usage.md](../tools/ssh-sftp-clients-usage.md) — 完整手册（认证、config、FAQ）
- [ssh-client 使用手册](ssh-client.md)
- [ssh-keygen 使用手册](ssh-keygen.md)
