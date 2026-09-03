# ssh-keygen 使用手册

> 对应程序：`cmd/ssh-keygen`（`ssh-keygen` / `ssh-keygen.exe`）
> 作用：参考 OpenSSH ssh-keygen 实现的 SSH 密钥生成与证书签发工具。生成的证书格式完全遵循 OpenSSH PROTOCOL.certkeys，可直接用于 `ssh-client` / `sshd` 的 CertificateFile 认证，也可与 OpenSSH 的 ssh-keygen 交叉验证。

---

## 目录

1. [概述](#1-概述)
2. [用法](#2-用法)
3. [参数](#3-参数)
4. [证书选项](#4-证书选项)
5. [有效期格式](#5-有效期格式)
6. [示例](#6-示例)
7. [相关文档](#7-相关文档)

---

## 1. 概述

`ssh-keygen` 支持五种模式：

| 模式 | 说明 |
|------|------|
| **生成密钥对** | 默认模式，生成 Ed25519/RSA/ECDSA 私钥与公钥 |
| **签发用户证书** | `-s ca_key -I identity [-n principals] [-V validity] [-z serial] [-O option] key.pub...` |
| **签发主机证书** | 加 `-h` 标志 |
| **查看证书** | `-L -f cert.pub` |
| **查看指纹** | `-l -f key.pub` |
| **打印公钥** | `-y -f private_key` |

---

## 2. 用法

```text
ssh-keygen [-t type] [-b bits] [-f file] [-N passphrase] [-C comment]            生成密钥对
ssh-keygen -s ca_key -I identity [-h] [-n principals] [-V validity] [-z serial] [-O option] key.pub...
                                                                                  签发证书
ssh-keygen -L -f cert.pub                                                         查看证书
ssh-keygen -l -f key.pub                                                          查看指纹
ssh-keygen -y -f private_key                                                      打印公钥
```

---

## 3. 参数

| 参数 | 短选项 | 说明 |
|------|--------|------|
| `--type <type>` | `-t` | 密钥类型：`ed25519`（默认）、`rsa`、`ecdsa` |
| `--bits <n>` | `-b` | 密钥位数（RSA 默认 3072，ECDSA 默认 256） |
| `--file <path>` | `-f` | 密钥文件路径（`-L`/`-l`/`-y` 时为输入路径；否则为输出路径） |
| `--new-passphrase <pw>` | `-N` | 新私钥口令（默认空） |
| `--comment <text>` | `-C` | 公钥注释 |
| `--sign <ca_key>` | `-s` | CA 私钥路径（进入签发模式） |
| `--identity <key_id>` | `-I` | 证书身份标识（Key ID），`-s` 模式必填 |
| `--principals <names>` | `-n` | 逗号分隔的 principal 列表（用户或主机名，`-h` 时为主机名） |
| `--host-certificate` | `-h` | 签发主机证书（默认签发用户证书） |
| `--validity <spec>` | `-V` | 有效期，如 `+52w`、`-1h:+1d`、`20260101:20280101`、`always:forever` |
| `--serial <n>` | `-z` | 证书序列号（默认 0；`-z+` 自动递增） |
| `--option <opt>` | `-O` | 证书选项（可重复），见下节 |
| `--show-cert` | `-L` | 打印证书内容 |
| `--fingerprint` | `-l` | 打印公钥指纹 |
| `--print-public` | `-y` | 从私钥提取公钥 |
| `--quiet` | `-q` | 静默模式 |
| `--verbose` | `-v` | 调试输出 |
| `--help` | `-H` | 帮助 |

---

## 4. 证书选项

`-O` 选项（可重复，按顺序生效）：

| 选项 | 说明 |
|------|------|
| `clear` | 重置所有 `permit-*` 扩展为关闭 |
| `permit-pty` / `no-pty` | 允许 / 禁止 PTY 分配 |
| `permit-agent-forwarding` | 允许 agent 转发 |
| `permit-port-forwarding` | 允许端口转发 |
| `permit-X11-forwarding` | 允许 X11 转发 |
| `permit-user-rc` | 允许执行 `~/.ssh/rc` |
| `verify-required` | 要求 CA 密钥的用户验证 |
| `force-command=<command>` | 强制命令（登录后只能执行此命令） |
| `source-address=<addr>[,<addr>...]` | 限制源地址（IPv4/IPv6/CIDR） |
| `extension:<name>[=<value>]` | 添加任意扩展 |
| `critical:<name>[=<value>]` | 添加任意关键扩展（客户端必须支持，否则拒绝连接） |

---

## 5. 有效期格式

`-V` 参数支持以下格式：

| 格式 | 示例 | 说明 |
|------|------|------|
| `+duration` | `+52w` | 从现在起持续指定时长 |
| `-duration:+duration` | `-1h:+1d` | 过去 1 小时到未来 1 天 |
| `YYYYMMDD:YYYYMMDD` | `20260101:20280101` | 绝对起止日期 |
| `always:forever` | `always:forever` | 永不过期 |
| `YYYYMMDD` | `20261231` | 仅指定开始时间（到永远） |

时间单位：`w`（周）、`d`（天）、`h`（小时）、`m`（分钟）、`s`（秒）。

---

## 6. 示例

```bash
# 生成 Ed25519 密钥对（默认）
ssh-keygen

# 生成 RSA-4096 密钥对，带口令与注释
ssh-keygen -t rsa -b 4096 -f ~/.ssh/mykey -N "passphrase" -C "mykey@example.com"

# 签发用户证书（有效期 52 周，序列号 1）
ssh-keygen -s ca_ed25519 -I "user@example.com" -n alice -V +52w -z 1 ~/.ssh/id_ed25519.pub

# 签发主机证书
ssh-keygen -s ca_ed25519 -I "host.example.com" -h -n host.example.com -V +52w ~/.ssh/ssh_host_ed25519.pub

# 查看证书
ssh-keygen -L -f id_ed25519-cert.pub

# 查看公钥指纹
ssh-keygen -l -f id_ed25519.pub

# 从私钥提取公钥
ssh-keygen -y -f id_ed25519

# 带证书选项：限制源地址 + 强制命令
ssh-keygen -s ca_ed25519 -I "alice" -n alice -O source-address=192.168.1.0/24 -O force-command=/usr/bin/tailscaled ~/.ssh/id_ed25519.pub
```

---

## 7. 相关文档

- [ssh-client 使用手册](ssh-client.md) — 证书登录
- [sftp-client 使用手册](sftp-client.md)
- [docs/tools/ssh-sftp-clients-usage.md](../tools/ssh-sftp-clients-usage.md) — 认证与 config 说明