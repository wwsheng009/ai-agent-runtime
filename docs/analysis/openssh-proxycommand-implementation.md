# OpenSSH ProxyCommand 实现分析

> 目标：理解 OpenSSH 客户端 `ProxyCommand` 的完整实现机制，为我们的
> Go 版 `ssh-client` / `sftp-client` 实现该功能提供精确参考。
>
> 源码版本：OpenSSH portable master（`sshconnect.c`、`readconf.c`、`ssh.c`）
> 以及微软 Win32-OpenSSH 移植分支的 `sshconnect.c`。

---

## 1. 总体机制

`ProxyCommand` 的核心思想：**不直接建立 TCP 连接到目标服务器，而是启动一个
外部命令（代理命令），把该命令的 stdin/stdout 当作 SSH 传输层来使用。**

数据流：

```
ssh 客户端  <---read/out---  [代理命令进程]  <---TCP--->  远程 sshd
            ---write/in-->  [stdin/stdout]
```

SSH 的整个协议（版本交换、KEX、认证、会话）都跑在这条"管道"上，
客户端本身**不做**到目标主机的 DNS 解析 / TCP 连接（那是代理命令的职责）。

### 连接分发：`ssh_connect()`

`sshconnect.c:574-601` 是分发的唯一入口：

```c
int
ssh_connect(struct ssh *ssh, const char *host, const char *host_arg,
    struct addrinfo *addrs, struct sockaddr_storage *hostaddr, u_short port,
    int connection_attempts, int *timeout_ms, int want_keepalive)
{
    int in, out;

    if (options.proxy_command == NULL) {
        return ssh_connect_direct(ssh, host, addrs, hostaddr, port,
            connection_attempts, timeout_ms, want_keepalive);
    } else if (strcmp(options.proxy_command, "-") == 0) {
        /* 特殊值 "-"：直接使用本进程的 stdin/stdout */
        if ((in = dup(STDIN_FILENO)) == -1 ||
            (out = dup(STDOUT_FILENO)) == -1) {
            ...
        }
        if ((ssh_packet_set_connection(ssh, in, out)) == NULL)
            return -1;
        return 0;
    } else if (options.proxy_use_fdpass) {
        return ssh_proxy_fdpass_connect(ssh, host, host_arg, port,
            options.proxy_command);
    }
    return ssh_proxy_connect(ssh, host, host_arg, port,
        options.proxy_command);
}
```

4 条路径：
1. `proxy_command == NULL` → 普通直连 `ssh_connect_direct()`
2. `proxy_command == "-"` → 用 ssh 进程自身的 stdin/stdout 作为传输
3. `proxy_use_fdpass`（`ProxyUseFdpass yes`）→ fd 传递模式
4. 默认 → 管道模式 `ssh_proxy_connect()`

---

## 2. 代理命令的展开：`expand_proxy_command()`

`sshconnect.c:116-136`：

```c
static char *
expand_proxy_command(const char *proxy_command, const char *user,
    const char *host, const char *host_arg, int port)
{
    char *tmp, *ret, strport[NI_MAXSERV];
    const char *keyalias = options.host_key_alias ?
        options.host_key_alias : host_arg;

    snprintf(strport, sizeof strport, "%d", port);
    xasprintf(&tmp, "exec %s", proxy_command);   // ← 关键：前置 "exec "
    ret = percent_expand(tmp,
        "h", host,        // %h → 目标主机（config HostName 或命令行 host）
        "k", keyalias,    // %k → HostKeyAlias
        "n", host_arg,    // %n → 命令行原始 host
        "p", strport,     // %p → 端口
        "r", options.user,// %r → 远程用户名
        (char *)NULL);
    free(tmp);
    return ret;
}
```

要点：
- **`exec ` 前缀**：让 shell 用代理命令替换自身进程（`execv` 后 PID 不变），
  避免遗留 shell 壳进程，保证 `proxy_command_pid` 就是实际命令的 PID。
- **令牌替换**（`percent_expand`）：
  - `%h` 目标主机名
  - `%k` HostKeyAlias（未设置则等于 `%n`）
  - `%n` 命令行原始主机参数
  - `%p` 端口
  - `%r` 远程用户
  - `%%` 字面 `%`
- 注意 `%h` 用的是**解析后**的主机（`HostName` 指令可改写），`%n` 是命令行原样。

---

## 3. 默认模式：管道传输 `ssh_proxy_connect()`

`sshconnect.c:224-...`（Unix 分支）：

```c
static int
ssh_proxy_connect(struct ssh *ssh, const char *host, const char *host_arg,
    u_short port, const char *proxy_command)
{
    char *command_string;
    int pin[2], pout[2];
    pid_t pid;
    char *shell;

    if ((shell = getenv("SHELL")) == NULL || *shell == '\0')
        shell = _PATH_BSHELL;    // 默认 /bin/sh

    /* 创建两条管道：pin = 喂给子进程 stdin；pout = 收子进程 stdout */
    if (pipe(pin) == -1 || pipe(pout) == -1)
        fatal("Could not create pipes ...");

    command_string = expand_proxy_command(proxy_command, options.user,
        host, host_arg, port);
    debug("Executing proxy command: %.500s", command_string);

    /* 子进程 */
    if ((pid = fork()) == 0) {
        char *argv[10];

        close(pin[1]);
        if (pin[0] != 0) { dup2(pin[0], 0); close(pin[0]); }  // stdin ← pin[0]
        close(pout[0]);
        if (dup2(pout[1], 1) == -1) perror("dup2 stdout");    // stdout → pout[1]
        close(pout[1]);

        /* stderr 保留给用户终端，便于看到代理命令的报错 */

        argv[0] = shell; argv[1] = "-c"; argv[2] = command_string; argv[3] = NULL;
        ssh_signal(SIGPIPE, SIG_DFL);
        execv(argv[0], argv);   // shell -c "exec <cmd>"
        perror(argv[0]);
        exit(1);
    }

    /* 父进程 */
    if (pid == -1) fatal("fork failed ...");
    proxy_command_pid = pid;

    close(pin[0]);   // 关掉自己手里的读端
    close(pout[1]);  // 关掉自己手里的写端

    /* 关键：SSH 包层使用 pout[0] 读、pin[1] 写 */
    if (ssh_packet_set_connection(ssh, pout[0], pin[1]) == NULL)
        return -1;
    return 0;
}
```

### 数据结构理解

```
        父进程(ssh)                          子进程(代理命令)
  读: pout[0]  <======== pipe(pout) ========  写: pout[1]  (子进程 stdout)
  写: pin[1]   ======== pipe(pin) ========>  读: pin[0]   (子进程 stdin)
```

`ssh_packet_set_connection(ssh, fd_read, fd_write)` 接受**两个独立的 fd**
（读和写可以不同），这正是管道模式能工作的原因：ssh 从子进程 stdout 读、
向子进程 stdin 写。子进程（如 `nc %h %p`、`connect.exe -H proxy %h %p`）
负责把 stdin/stdout 桥接到真实 TCP 连接。

### `ProxyCommand -` 的复用

`ssh_connect()` 里 `-` 分支用 `dup(STDIN_FILENO)/dup(STDOUT_FILENO)` 直接
把 ssh 自身 stdin/stdout 交给包层。常见用法：外部程序管道驱动 ssh
（如 `echo x | ssh -o ProxyCommand=- host`）。

---

## 4. fd 传递模式：`ssh_proxy_fdpass_connect()`

`sshconnect.c:142-219`（`ProxyUseFdpass yes` 时启用）：

```c
static int
ssh_proxy_fdpass_connect(struct ssh *ssh, const char *host,
    const char *host_arg, u_short port, const char *proxy_command)
{
    char *command_string;
    int sp[2], sock;
    pid_t pid;
    char *shell;

    if ((shell = getenv("SHELL")) == NULL)
        shell = _PATH_BSHELL;

    /* 先建一个 Unix socketpair 作为与子进程的“控制通道” */
    if (socketpair(AF_UNIX, SOCK_STREAM, 0, sp) == -1)
        fatal("Could not create socketpair ...");

    command_string = expand_proxy_command(proxy_command, options.user,
        host, host_arg, port);

    if ((pid = fork()) == 0) {
        /* 子进程：把 socketpair 一端 dup 到 stdin/stdout，然后 exec */
        close(sp[1]);
        if (sp[0] != 0) { dup2(sp[0], 0); }
        if (sp[0] != 1) { dup2(sp[0], 1); }
        if (sp[0] >= 2) close(sp[0]);
        execv(argv[0], argv);   // shell -c "exec <cmd>"
    }
    /* 父进程 */
    close(sp[0]);
    /* 核心：通过 SCM_RIGHTS 从 socketpair 接收子进程“回传的真实 socket fd” */
    if ((sock = mm_receive_fd(sp[1])) == -1)
        fatal("proxy dialer did not pass back a connection");
    close(sp[1]);
    while (waitpid(pid, NULL, 0) == -1) ...
    ssh_packet_set_connection(ssh, sock, sock);   // 真正的 TCP socket
    return 0;
}
```

### 与管道模式的本质区别

- **管道模式**：数据经过子进程的 stdin/stdout 中转，子进程充当桥接；
  代理命令本身**不是** TCP 连接方（如 `nc` 会建立 TCP 但数据都流经它）。
- **fdpass 模式**：代理命令（如 `ssh -W %h:%p jump`、`socat`）建立真实 TCP
  连接后，通过 `SCM_RIGHTS`（Unix domain socket 辅助数据）把那个**连接好的
  socket 文件描述符回传给 ssh 父进程**。之后 ssh 直接操作真实 socket，
  子进程可立即退出，不再中转数据（减少一次拷贝，也避免代理进程成为瓶颈/单点）。

> Windows 移植分支中 `ProxyUseFdpass` 未实现（`#ifdef WIN32_FIXME` 下直接
> `return 0`），因为 Windows 没有 `SCM_RIGHTS`。

---

## 5. 配置解析：`readconf.c` 如何拿到命令串

`readconf.c:1529-1541`（`oProxyCommand` 走 `parse_command`）：

```c
case oProxyCommand:
    charptr = &options->proxy_command;
parse_command:
    if (str == NULL) { ... }
    len = strspn(str, WHITESPACE "=");
    if (*activep && *charptr == NULL)
        *charptr = xstrdup(str + len);   // ← 取“关键字后剩余整行”
    argv_consume(&ac);
    break;
```

- `ProxyCommand` 的值 = **配置行关键字之后剩余的整行文本**，原样保留，
  包括双引号（如 `"C:/Program Files/Git/mingw64/bin/connect.exe" -H ...`）。
- 不做分词/去引号——**引号语义由最终执行时的 shell（Unix）或
  `CreateProcess`（Win32）解释**。这正是我们 kevinburke 解析结果里
  ProxyCommand 值保留引号的原因，与 OpenSSH 行为一致。
- 这也说明：我们不能简单把值当单个路径用，需要交给 shell/命令解释器执行。

---

## 6. Windows 移植（Win32-OpenSSH）实现差异

微软 `Win32-OpenSSH` 的 `sshconnect.c` 在 `#ifdef WIN32_FIXME` 分支里
用 `CreateProcess` + 两组 socketpair 实现：

```c
static int
ssh_proxy_connect(const char *host, u_short port, const char *proxy_command)
{
  #ifdef WIN32_FIXME
    PROCESS_INFORMATION pi = {0};
    STARTUPINFO si = {0};
    char *fullCmd = NULL;
    char strport[NI_MAXSERV] = {0};
    int sockin[2]  = {-1, -1};
    int sockout[2] = {-1, -1};
    int exitCode = -1;

    snprintf(strport, sizeof strport, "%hu", port);
    fullCmd = percent_expand(proxy_command, "h", host,
                                 "p", strport, (char *) NULL);   // ← 无 "exec " 前缀
    ...
    socketpair(sockin);
    socketpair(sockout);

    si.cb          = sizeof(STARTUPINFO);
    si.hStdInput   = (HANDLE) sfd_to_handle(sockin[0]);   // 子进程 stdin
    si.hStdOutput  = (HANDLE) sfd_to_handle(sockout[0]);  // 子进程 stdout
    si.hStdError   = GetStdHandle(STD_ERROR_HANDLE);      // stderr 保留
    si.wShowWindow = SW_HIDE;
    si.dwFlags     = STARTF_USESTDHANDLES;

    FAIL(CreateProcess(NULL, fullCmd, NULL, NULL, TRUE,
                       CREATE_NEW_PROCESS_GROUP, NULL, NULL, &si, &pi) == FALSE);

    proxy_command_handle = pi.hProcess;
    proxy_command_pid    = pi.dwProcessId;

    /* 注意：packet_set_connection(sockout[1], sockin[1])
       读 = sockout[1]（子进程 stdout 的对端），写 = sockin[1]（子进程 stdin 的对端） */
    packet_set_connection(sockout[1], sockin[1]);
    ...
  #else
    /* 原版 Unix 管道实现 */
  #endif
}
```

### 与 Unix 实现的差异

| 方面 | Unix 版 | Win32 版 |
|---|---|---|
| 子进程创建 | `fork + execv(shell, -c, "exec "+cmd)` | `CreateProcess(NULL, fullCmd, ...)` |
| 命令前缀 | `exec ` | 无（直接 CreateProcess 解析命令行） |
| 传输 | 两条 `pipe()` | 两组 `socketpair()`（sockin/sockout） |
| 环境变量 | `$SHELL` 或 `/bin/sh -c` | 无 shell，命令行直接交给 CreateProcess |
| stderr | 继承到用户终端 | `GetStdHandle(STD_ERROR_HANDLE)` 保留 |
| `%` 展开 | `%h %k %n %p %r` | `%h %p`（旧版） |
| 令牌 | 展开后带引号执行 | 同样保留引号，CreateProcess 自己解析 argv |
| fdpass | 支持 | 未实现 |
| 清理 | `kill(pid, SIGHUP)` | `TerminateProcess`（等价物） |

**对 Go/Windows 的重要启示**：
- Windows 上没有 `fork`/`exec`/`/bin/sh`，Go 里对应 `os/exec.Command`。
- Win32 版**不经过 shell**，命令串直接交给 `CreateProcess` 解析——
  所以用户 config 里写的 `"C:/Program Files/.../connect.exe" -H ... %h %p`
  这种带引号路径能工作，是因为 `CreateProcess` 自己按 C 命令行规则切分。
- Go 里最接近的做法：**自己实现 shell 风格的分词**（处理引号/空格），
  然后 `exec.Command(parts[0], parts[1:]...)`；或者直接用
  `cmd.exe /C <fullCmd>` 让 cmd 处理引号。两者取舍见第 8 节。

---

## 7. 生命周期、超时与清理

### 清理（Unix）

`sshconnect.c` 末尾：

```c
void
ssh_kill_proxy_command(void)
{
    if (proxy_command_pid > 1)
        kill(proxy_command_pid, SIGHUP);
}
```

- ssh 退出时向代理进程发 `SIGHUP`，**不 wait()**（防止代理命令 hang 导致
  ssh 卡住，交给 init 回收）。
- 会话内通过 `ssh_packet_set_connection` 的两根 fd 收发；关闭连接后
  ssh 主循环结束，再调用 kill 清理代理进程。

### 超时

- **直连**：`ssh_connect_direct()` 里 `timeout_connect()` 用 `poll` +
  剩余 `timeout_ms`（`ConnectTimeout`）做非阻塞连接超时（见 sshconnect.c:533）。
- **代理路径**：`ssh_connect` 本身**不**对代理命令设置超时；超时体现在
  后续 `kex_exchange_identification(ssh, timeout_ms, ...)`
  （sshconnect.c:1646）——等待服务器 banner 时受 `ConnectTimeout` 约束。
  也就是说 `ConnectTimeout` 同样能兜住"代理命令启动了但连不上"的场景，
  因为 banner 永远不会来。
- 子进程 stderr 保留，所以 `connect.exe` / `nc` 自己的报错（如
  `proxy connection failed`）能直接显示给用户。

---

## 8. 在我们 Go 客户端中的实现映射（设计要点）

结合 `internal/sshclient` 现状（`conn.go` 的 `NewClient` 用
`net.Dialer.DialContext` + `ssh.NewClientConn`），实现 ProxyCommand 的关键点：

### 8.1 传输层抽象

`x/crypto/ssh` 的 `NewClientConn` 需要 `net.Conn`。管道模式需要把
子进程的 stdin/stdout 包装成一个满足 `net.Conn` 接口的对象：

```
type proxyConn struct {
    read  io.Reader   // 子进程 stdout
    write io.Writer   // 子进程 stdin
    ...
}
func (c *proxyConn) Read(p []byte) (int, error)  { return c.read.Read(p) }
func (c *proxyConn) Write(p []byte) (int, error) { return c.write.Write(p) }
func (c *proxyConn) Close() error                { /* 杀进程 + 关管道 */ }
func (c *proxyConn) LocalAddr() net.Addr  { return dummyAddr{} }
func (c *proxyConn) RemoteAddr() net.Addr { return dummyAddr{} }
func (c *proxyConn) SetDeadline(t time.Time) error  { ... }
func (c *proxyConn) SetReadDeadline(t time.Time) error  { ... }
func (c *proxyConn) SetWriteDeadline(t time.Time) error  { ... }
```

- Go 的 `os/exec.Cmd`：`cmd.Stdin = 管道A读端`、`cmd.Stdout = 管道B写端`，
  `cmd.Stderr = os.Stderr`（或传给 stderr writer）。
- `os.Pipe()` 给每个方向一条管道；子进程继承管道句柄（Windows 上
  `os/exec` 自动处理句柄继承，等价于 Win32 的 socketpair+CreateProcess）。

### 8.2 命令执行方式（Windows 取舍）

- **方案 A：`exec.Command(parts...)` + 自写分词**——最贴近 Win32-OpenSSH
  的 CreateProcess 语义，无 shell 注入风险，但需要正确解析引号（
  `"C:/Program Files/x/connect.exe"`、单引号、转义）。
- **方案 B：`exec.Command("cmd.exe", "/C", fullCmd)`**——让 cmd 处理引号，
  简单且兼容管道/重定向，但有 shell 语义副作用。
- 本项目目标 Win7 兼容 + 简单性，建议 **A + 谨慎分词**（参考 Go
  `strings.Fields` 的自定义增强版），或提供 `-o ProxyCommand` 时打印
  "使用 cmd 解释"提示。

### 8.3 令牌展开

```go
func expandProxyCommand(raw, host, hostArg, port, user string) string {
    r := strings.NewReplacer(
        "%h", host,
        "%n", hostArg,
        "%p", strconv.Itoa(port),
        "%r", user,
        "%%", "%",
    )
    return r.Replace(raw)
}
```
（`%k` 需 HostKeyAlias，可后续补；先支持 `%h %n %p %r %%`。）

### 8.4 接入 `NewClient`

```go
var conn net.Conn
if opts.ProxyCommand != "" {
    conn, cleanup, err = startProxyCommand(opts, stderr)
    if err != nil { return nil, err }
} else {
    conn, err = dialer.DialContext(ctx, network, addr)
    ...
}
defer conn.Close()  // cleanup 内部会杀代理进程
// 握手超时：直接对 proxyConn 也做 SetDeadline（若实现 deadline）或
// 用 goroutine + select 兜底
sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, config)
```

### 8.5 差异提醒（与 OpenSSH 行为对齐）

- 代理命令未设置超时由 `ConnectTimeout` 兜底（banner 阶段）。
- 需要 `ssh_kill_proxy_command` 等价物：`Client.Close()` 时杀掉子进程。
- 保留子进程 stderr 给用户（打印 `connect.exe` 报错）。
- `ProxyCommand -`：直接 `os.Stdin/os.Stdout` 包装（可选实现）。
- `ProxyUseFdpass`：Windows 不支持，可忽略或警告。

---

## 9. 参考源码位置

| 文件 | 关键函数/行 |
|---|---|
| `sshconnect.c` | `expand_proxy_command` (116)、`ssh_proxy_fdpass_connect` (142)、`ssh_proxy_connect` (224)、`ssh_connect` (574)、`ssh_kill_proxy_command` |
| `readconf.c` | `oProxyCommand → parse_command` (1529) |
| `ssh.c` | 主流程调用 `ssh_connect` 与 `ssh_login` |
| Win32-OpenSSH `sshconnect.c` | `#ifdef WIN32_FIXME` 分支的 `CreateProcess` + `socketpair` 实现 |
