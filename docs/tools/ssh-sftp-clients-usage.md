# SSH / SFTP 客户端使用手册

> 对应程序：`cmd/ssh-client`（`ssh-client.exe`）与 `cmd/sftp-client`（`sftp-client.exe`）
> 版本：0.1.0

---

## 目录

1. [概述](#1-概述)
2. [ssh-client（SSH 客户端）](#2-ssh-clientssh-客户端)
3. [sftp-client（SFTP 客户端）](#3-sftp-clientsftp-客户端)
4. [认证方式](#4-认证方式)
5. [主机密钥校验](#5-主机密钥校验)
6. [超时与防卡死机制](#6-超时与防卡死机制)
7. [常见问题](#7-常见问题)
8. [构建与安装](#8-构建与安装)

---

## 1. 概述

这两个工具是项目原生的 SSH/SFTP 命令行客户端，基于 Go 的 `golang.org/x/crypto/ssh` 实现，提供与 OpenSSH 兼容的交互体验，无需依赖外部 `ssh.exe` / `sftp.exe`。

| 程序 | 功能 |
|---|---|
| `ssh-client` | 交互式远程 shell、远程命令执行、本地/远程端口转发 |
| `sftp-client` | 交互式/批处理文件传输、命令行直传（上传/下载/列目录） |

**共同特性：**

- 公钥认证（Ed25519 / ECDSA / RSA，支持 `-i` 指定密钥文件）
- ssh-agent 自动代理密钥
- 密码认证（命令行 `--password` 或交互式输入）
- `~/.ssh/config` 解析（Host、HostName、Port、User、IdentityFile 等）
- `known_hosts` 主机密钥校验（`StrictHostKeyChecking` 支持 yes / accept-new / no）
- `-o` 选项（`ConnectTimeout`、`ServerAliveInterval`、`ServerAliveCountMax` 等）
- 跨平台：Windows / Linux / macOS

---

## 2. ssh-client（SSH 客户端）

### 2.1 用法

```
ssh-client [options] [user@]host [command]
```

### 2.2 选项说明

| 选项 | 简写 | 默认值 | 说明 |
|------|------|--------|------|
| `--port` | `-p` | 22 | SSH 端口 |
| `--user` | `-l` | 当前 OS 用户 | 登录用户名 |
| `--identity-file` | `-i` | 自动搜索 `~/.ssh/id_*` | 私钥文件路径（可重复） |
| `--password` | — | 交互式输入 | 密码（非交互编配） |
| `--option` | `-o` | — | OpenSSH 配置选项，`key=value` 格式（见下方白名单） |
| `--quiet` | `-q` | false | 静默模式（抑制警告/横幅） |
| `--verbose` | `-v` | false | 调试输出 |
| `--config-file` | `-F` | `~/.ssh/config` | ssh_config 文件路径 |
| `--no-session` | `-N` | false | 仅端口转发，不执行远程命令 |
| `--local-forward` | `-L` | — | 本地端口转发 `bind:port:host:hostport` |
| `--remote-forward` | `-R` | — | 远程端口转发 `bind:port:host:hostport` |
| `--no-tty` | `-T` | false | 禁止分配伪终端 |
| `--tty` | `-t` | false | 强制分配伪终端 |
| `--version` | `-V` | — | 显示版本号 |
| `--ipv4` | `-4` | false | 仅使用 IPv4 |
| `--ipv6` | `-6` | false | 仅使用 IPv6 |
| `--compress` | `-C` | false | 启用压缩 |
| `--timeout` | — | 30s | 连接超时秒数（TCP 拨号 + SSH 握手总上限） |
| `--known-hosts-file` | — | `~/.ssh/known_hosts` | known_hosts 文件路径 |
| `--help` | `-h` | — | 显示帮助信息并退出 |

### 2.3 `-o` 白名单

以下选项可通过 `-o key=value` 设置：

| 键 | 示例 | 说明 |
|---|---|---|
| `ConnectTimeout` | `-o ConnectTimeout=10` | 连接超时（秒） |
| `ServerAliveInterval` | `-o ServerAliveInterval=15` | 保活间隔（秒） |
| `ServerAliveCountMax` | `-o ServerAliveCountMax=3` | 保活失败最大次数 |
| `StrictHostKeyChecking` | `-o StrictHostKeyChecking=no` | 主机密钥校验模式：yes / accept-new / no |
| `UserKnownHostsFile` | `-o UserKnownHostsFile=NUL` | known_hosts 文件路径 |
| `HostKeyAlgorithms` | `-o HostKeyAlgorithms=ssh-ed25519` | 主机密钥算法白名单（逗号分隔） |
| `PreferredAuthentications` | `-o PreferredAuthentications=password` | 认证方法优先级 |
| `LogLevel` | `-o LogLevel=DEBUG` | 日志级别 |
| `Compression` | `-o Compression=yes` | 启用压缩 |
| `ProxyJump` | — | 仅解析，未实现（忽略并警告） |

### 2.4 示例

**交互式 shell：**

```bash
ssh-client user@example.com
# 或指定端口和密钥
ssh-client -i ~/.ssh/id_ed25519 -p 2222 testuser@localhost
```

**远程命令执行：**

```bash
ssh-client user@host "ls -la"
ssh-client -p 2222 user@host "df -h && free -m"
```

**密码认证（非交互式）：**

```bash
ssh-client --password 'secret' user@host "uptime"
```

**端口转发：**

```bash
# 本地转发：访问本地 8080 相当于访问远程的 localhost:80
ssh-client -N -L 8080:localhost:80 user@host

# 远程转发：远程访问 localhost:2222 相当于访问本地的 localhost:22
ssh-client -N -R 2222:localhost:22 user@host
```

**管道模式（stdin 转发到远程命令）：**

```bash
echo 'uname -a' | ssh-client user@host
cat data.txt | ssh-client user@host "grep pattern"
```

**超时设置：**

```bash
# 3 秒连接超时（TCP 拨号 + SSH 握手），防止卡死
ssh-client --timeout 3 user@host "echo ok"
# 或通过 -o
ssh-client -o ConnectTimeout=3 user@host "echo ok"
```

**保活检测：**

```bash
ssh-client -o ServerAliveInterval=15 -o ServerAliveCountMax=3 user@host
```

---

## 3. sftp-client（SFTP 客户端）

### 3.1 用法

```
sftp-client [options] [user@]host[:remote-path] [local-path...]
```

### 3.2 模式

sftp-client 支持三种工作模式，自动根据参数选择：

| 模式 | 命令示例 | 说明 |
|------|---------|------|
| **交互式** | `sftp-client user@host` | 进入 SFTP 命令提示符，手动输入命令 |
| **批处理** | `sftp-client -b batch.txt user@host` | 从文件逐行读取命令执行 |
| **直传模式** | 见下方 | 上传/下载/列目录，一行命令完成 |

### 3.3 直传模式

**下载：**

```bash
sftp-client user@host:remote/path/file.txt local-file.txt
sftp-client -R user@host:remote/dir/ local-dir/
```

**上传：**

```bash
sftp-client user@host local-file.txt remote/path/
sftp-client -R user@host local-dir/ remote/dir/
```

**列目录：**

```bash
sftp-client user@host:remote/dir/
```

### 3.4 选项说明

| 选项 | 简写 | 默认值 | 说明 |
|------|------|--------|------|
| `--port` | `-P` | 22 | SSH 端口 |
| `--user` | `-l` | 当前 OS 用户 | 登录用户名 |
| `--identity-file` | `-i` | 自动搜索 | 私钥文件路径（可重复） |
| `--password` | — | 交互式输入 | 密码 |
| `--option` | `-o` | — | OpenSSH 配置选项 |
| `--quiet` | `-q` | false | 静默模式 |
| `--verbose` | `-v` | false | 调试输出 |
| `--config-file` | `-F` | `~/.ssh/config` | ssh_config 文件路径 |
| `--batch` | `-b` | — | 批处理文件路径 |
| `--recursive` | `-R` | false | 递归传输目录 |
| `--force` | `-f` | false | 强制覆盖已存在的文件 |
| `--version` | `-V` | — | 显示版本号 |
| `--ipv4` | `-4` | false | 仅使用 IPv4 |
| `--ipv6` | `-6` | false | 仅使用 IPv6 |
| `--timeout` | — | 30s | 连接超时秒数 |
| `--known-hosts-file` | — | `~/.ssh/known_hosts` | known_hosts 文件路径 |
| `--help` | `-h` | — | 显示帮助信息并退出 |

### 3.5 交互/批处理命令

在交互式 shell 或批处理文件中支持以下命令（批处理中每行一条命令，`#` 开头为注释）：

| 命令 | 说明 |
|------|------|
| `ls [-la] [path]` | 列出远程目录 |
| `lls [path]` | 列出本地目录 |
| `cd <path>` | 切换远程目录 |
| `lcd <path>` | 切换本地目录 |
| `pwd` | 显示远程工作目录 |
| `lpwd` | 显示本地工作目录 |
| `get [-r] <remote> [local]` | 下载文件/目录（`-r` 递归下载） |
| `put [-r] <local> [remote]` | 上传文件/目录（`-r` 递归上传） |
| `rm <file>` | 删除远程文件 |
| `rmdir <dir>` | 删除远程空目录 |
| `mkdir <dir>` | 创建远程目录 |
| `chmod <mode> <path>` | 修改远程文件权限 |
| `chown <uid>:<gid> <path>` | 修改远程文件属主 |
| `rename <old> <new>` | 重命名远程文件/目录 |
| `symlink <target> <link>` | 创建远程符号链接 |
| `stat <path>` | 显示远程文件信息 |
| `echo <text>` | 本地打印文本 |
| `!<command>` | 执行本地 shell 命令 |
| `help` / `?` | 显示帮助 |
| `quit` / `exit` / `bye` | 退出 |

### 3.6 示例

**交互式会话：**

```bash
sftp-client -P 2222 -i ~/.ssh/id_ed25519 testuser@localhost
sftp> ls data/
sftp> get data/hello.txt ./
sftp> quit
```

**批处理文件（`batch.txt`）：**

```
# 测试传输
ls data/
stat data/hello.txt
echo Batch test complete
```

执行：`sftp-client -b batch.txt -P 2222 testuser@localhost`

**直传（命令行单次传输）：**

```bash
# 下载
sftp-client -P 2222 testuser@localhost:data/hello.txt ./

# 上传
sftp-client -P 2222 testuser@localhost ./myfile.txt data/

# 递归上传目录
sftp-client -R -P 2222 testuser@localhost ./mydir/ data/

# 列目录
sftp-client -P 2222 testuser@localhost:data/
```

---

## 4. 认证方式

认证按以下优先级顺序尝试：

1. **公钥认证**（`-i` 指定私钥文件，或自动搜索 `~/.ssh/id_ed25519`、`~/.ssh/id_ecdsa`、`~/.ssh/id_rsa`）
2. **ssh-agent**（运行中的 ssh-agent 自动提供密钥。若 `-i` 指定了文件且 `-o IdentitiesOnly=yes`，则跳过 agent）
3. **密码认证**（`--password` 显式提供，或交互式输入）
4. **keyboard-interactive**（密码认证的 fallback，某些服务器要求）

**示例：指定密钥文件**

```bash
ssh-client -i ~/.ssh/id_ed25519 -p 2222 user@host
sftp-client -i mykey.pem -P 2222 user@host
```

**示例：密码认证**

```bash
ssh-client --password 'mypassword' user@host "echo ok"
sftp-client --password 'mypassword' -P 2222 user@host:data/
```

**示例：仅使用密码（跳过密钥）：**

```bash
ssh-client -o PreferredAuthentications=password --password 'mypassword' user@host
```

---

## 5. 主机密钥校验

`StrictHostKeyChecking` 选项控制对远程主机密钥的校验策略。

| 模式 | 说明 |
|------|------|
| `accept-new`（默认） | 首次连接自动接受并追加到 `known_hosts`；已知主机密钥变更时报错拒绝 |
| `yes` | 严格模式：未知主机直接拒绝 |
| `no` | 跳过校验（输出警告，不推荐用于生产环境） |

**示例：**

```bash
# 首次连接自动接受（默认）
ssh-client user@host "echo ok"

# 严格模式（拒绝未知主机）
ssh-client -o StrictHostKeyChecking=yes user@host

# 跳过校验（自动化/测试环境）
ssh-client -o StrictHostKeyChecking=no user@host
```

---

## 6. 超时与防卡死机制

### 连接超时（`ConnectTimeout` / `--timeout`）

- **默认值**：30 秒
- **作用范围**：TCP 拨号 + SSH 握手（密钥交换 + 认证）全过程
- **效果**：若服务器 accept TCP 连接后不回复 SSH 协议（如 docker-proxy 转发但容器内 sshd 未就绪），超过时间后报错退出，不会永久阻塞
- 设置方式：`--timeout 10` 或 `-o ConnectTimeout=10`

### 保活检测（`ServerAliveInterval` / `ServerAliveCountMax`）

- 默认关闭（`ServerAliveInterval=0`）
- 启用后，客户端定期发送 keepalive 请求；若连续 `ServerAliveCountMax` 次无响应，自动断开连接
- 推荐设置：`-o ServerAliveInterval=15 -o ServerAliveCountMax=3`（约 45 秒检测到死链）

### 命令行超时

- 远程命令执行与交互式 shell 本身没有内置超时（这是 SSH 协议的正常行为：服务器可能长时间运行命令）
- 若需要限制命令执行时间，可在远程端使用 `timeout` 命令：
  ```bash
  ssh-client user@host "timeout 10 sleep 999"    # 10 秒强制终止
  ```

---

## 7. 常见问题

### 7.1 连接超时失败

```
ssh-client: connection failed: ssh handshake with 127.0.0.1:2222: ssh: handshake failed: read tcp ... i/o timeout
```

**原因**：TCP 连接建立后，SSH 握手未在超时时间内完成。
**排查**：检查 SSH 服务器是否正常运行（`docker ps`、`systemctl status sshd`）；确认端口映射正确；确认服务器未在连接后 hang。

### 7.2 认证失败

```
ssh-client: connection failed: authentication failed: 127.0.0.1:22 (check username, password, or key)
```

**原因**：认证方法均被拒绝。检查用户名、密码、密钥文件路径是否正确。
**排查**：`ssh-client -v ...` 开启调试输出；确认服务器端 `/etc/ssh/sshd_config` 允许密码认证或公钥认证。

### 7.3 主机密钥变更

```
ssh-client: host key mismatch for "hostname": ... If you trust this host, remove the offending key from known_hosts
```

**原因**：远程主机的密钥已改变（可能是重装系统或中间人攻击）。
**解决**：手动从 `~/.ssh/known_hosts` 中移除旧条目，或使用 `-o StrictHostKeyChecking=accept-new` 重新接受。

### 7.4 文件传输速度慢

SFTP 传输受限于底层 SSH 加密通道和网络延迟，没有加速选项。若需高速传输，考虑使用 `rsync` 或 `scp` 配合压缩选项。

### 7.5 Windows 上的注意事项

- 密钥文件路径使用正斜杠或双反斜杠：`-i C:/Users/name/.ssh/id_ed25519` 或 `-i C:\\Users\\name\\.ssh\\id_ed25519`
- `known_hosts` 文件默认位于 `%USERPROFILE%\.ssh\known_hosts`
- `~/.ssh/config` 也支持 Windows 路径（`~` 展开为 `%USERPROFILE%`）
- 交互式 shell 中，终端功能（如窗口大小变化信号）依赖于 `SIGWINCH`，在 Windows 上可能不完全支持，但基本功能正常

---

## 8. 构建与安装

### 从源码构建

```bash
cd backend

# 构建 ssh-client
go build -o ssh-client.exe ./cmd/ssh-client/

# 构建 sftp-client
go build -o sftp-client.exe ./cmd/sftp-client/

# 交叉构建（Linux amd64）
GOOS=linux GOARCH=amd64 go build -o ssh-client-linux ./cmd/ssh-client/
GOOS=linux GOARCH=amd64 go build -o sftp-client-linux ./cmd/sftp-client/
```

### 构建产物

- `ssh-client.exe` / `ssh-client-linux` — SSH 客户端
- `sftp-client.exe` / `sftp-client-linux` — SFTP 客户端

### 依赖

- Go 1.22+
- `golang.org/x/crypto`（SSH 协议实现）
- `github.com/spf13/pflag`（命令行参数解析）

---

> 文档版本：v0.1.0 · 最后更新：2026-09-02