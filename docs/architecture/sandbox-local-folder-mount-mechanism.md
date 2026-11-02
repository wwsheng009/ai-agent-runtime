# 沙箱本地目录挂载机制分析：FUSE + 反向 WebSocket 隧道

> 分析对象：沙箱内 `/mnt/local/<folder>` 路径如何穿透到用户本机磁盘
> 核心组件：`tabbit-fuse`（沙箱侧 Go 守护进程）、`envd`（沙箱运行时探针）、`fuse.tabbit` 内核模块
> 实测环境：Debian 13 (trixie) / Linux 6.6.69-cube / x86_64 KVM 虚拟机
> 更新日期：2026-10-27
>
> 配套文档：功能与逻辑口径（能力清单、参数、限制、运维、未闭合项）见 `docs/architecture/fuse-local-mount.md`；本文保留原始实测证据链，供追溯与复核。

本文基于在沙箱内实测得到的内核挂载表、进程表、套接字表和读写行为，逆向梳理「用户本地文件夹被 @ 引用后，沙箱为何能像访问本地磁盘一样访问它」的完整机制、时序、安全边界与实际约束。

---

## 1. 结论先行

这不是把文件**复制上传**进沙箱，也不是 NFS/SMB 共享，而是：

> **沙箱侧挂载一个由用户态进程实现的虚拟文件系统（FUSE），该进程把每一个文件系统操作序列化后，经一条由用户电脑主动发起的长连接（WebSocket）转发回本机执行，再把结果原路返回。**

四个关键判断（均有实测支撑）：

| 判断 | 依据 |
|---|---|
| 不是网络文件系统 | `/proc/mounts` 中 NFS / CIFS / SMB / 9p 条目数为 **0** |
| 不是数据复制 | 本机改动沙箱立即可见；挂载点 `size` 与本地目录一致，非快照副本 |
| 是用户态文件系统 | 挂载类型 `fuse.tabbit`，设备源 `tabbit-localfs`，依赖 `/dev/fuse` |
| 是反向连接（本机主动连沙箱） | 沙箱侧为 **LISTEN** 端点 `19780`，对端为私网地址；帮助文本明确该端口为 *WebSocket listen address* |

一句话概括：**沙箱挂的是一个「假文件系统」，背后每个操作都被转发回你的电脑执行。数据始终在你本机，沙箱不持有你的磁盘凭据。**

---

## 2. 三层架构

```
┌─────────────────────── 用户电脑（Windows）───────────────────────┐
│  E:\projects\ai\ai-agent-runtime                                 │
│            ▲  实际读写发生在这里                                  │
│            │                                                     │
│  Tabbit 客户端本地桥接模块（实现隧道客户端）                       │
└────────────┼─────────────────────────────────────────────────────┘
             │  ① 本机主动发起 TCP + WebSocket 握手
             │     对外可见为私网地址 10.133.x.x → 沙箱 169.254.68.6:19780
             ▼
┌─────────────────────── 沙箱虚拟机（Linux）───────────────────────┐
│  ② tabbit-fuse（PID 82，s6 托管常驻）                             │
│        · WebSocket 服务端（--listen 0.0.0.0:19780）              │
│        · FUSE 文件系统实现者（向内核注册 fuse.tabbit）             │
│        · RPC 编解码 + 路径映射 + 超时控制                          │
│            ▲            │                                        │
│            │ ③ /dev/fuse（字符设备 10:229）                       │
│            │            ▼                                        │
│  ④ 内核 VFS ── fuse 驱动 ── 挂载点 /mnt/local/ai-agent-runtime    │
│            ▲                                                     │
│            │ ⑤ 普通进程 open/read/write/stat                      │
│  沙箱内 agent、脚本、Python、shell                               │
└──────────────────────────────────────────────────────────────────┘
```

三层职责划分清晰：

- **客户端层**：只暴露用户显式授权的那一个文件夹，负责把 RPC 翻译成对本地磁盘的真实系统调用。
- **守护进程层**：不碰真实数据存储，只做「文件系统语义 ↔ RPC 消息」的双向翻译。
- **内核层**：让挂载点对沙箱内所有进程表现为一个普通目录，任何程序（`cat`、`python`、`git`、`gcc`）都无需改造即可使用。

---

## 3. 关键组件与实测参数

### 3.1 挂载条目

```
$ grep "/mnt/local" /proc/self/mounts
tabbit-localfs /mnt/local/ai-agent-runtime fuse.tabbit \
    rw,nosuid,nodev,relatime,user_id=0,group_id=0,allow_other,max_read=131072 0 0

$ grep "/mnt/local" /proc/self/mountinfo
69 122 0:68 / /mnt/local/ai-agent-runtime rw,nosuid,nodev,relatime \
    - fuse.tabbit tabbit-localfs rw,user_id=0,group_id=0,allow_other,max_read=131072
```

逐项解读：

| 字段 | 值 | 含义 |
|---|---|---|
| 挂载类型 | `fuse.tabbit` | 私有 FUSE 子类型，非标准 `fuse` / `fuseblk` |
| 设备源 | `tabbit-localfs` | 仅作标识，无真实块设备 |
| 设备号 | `0:68` | FUSE 连接的内核分配号，对应一条 `/dev/fuse` 会话 |
| `rw` | 读写 | 非只读映射（实测可成功写入、删除） |
| `nosuid`, `nodev` | 禁用 | 防止通过该挂载提权（suid 二进制、设备节点） |
| `allow_other` | 允许 | 沙箱内其他用户也可访问，便于多进程协作 |
| `user_id=0,group_id=0` | root | 挂载属主；实际权限由隧道对端决定 |
| `max_read=131072` | 128 KiB | 单次读取请求上限（内核侧） |

> ⚠️ 注意 `max_read=131072`（128 KiB）与守护进程的 `--max-read-bytes 1048576`（1 MiB）不一致：内核挂载选项取了更保守的值，实际单次往返被限制在 128 KiB。

### 3.2 守护进程

```
$ ps -eo pid,ppid,args | grep tabbit-fuse
   80     1 s6-supervise tabbit-fuse
   82    80 /usr/local/bin/tabbit-fuse \
             --listen 0.0.0.0:19780 \
             --mount-base /mnt/local \
             --log-level info \
             --max-read-bytes 1048576 \
             --max-write-bytes 1048576 \
             --max-mounts 16 \
             --op-timeout 30s \
             --ci-health-url http://127.0.0.1:49983/health \
             --ci-health-timeout 2s
```

- 由 **s6** 进程监督器托管（PID 80 为 supervisor，PID 82 为 worker），崩溃会被自动拉起。
- 二进制：`ELF 64-bit LSB executable, x86-64, statically linked, Go`（11.8 MB，含 debug_info，未 strip）——静态链接保证不依赖沙箱内 glibc 版本。

参数含义：

| 参数 | 值 | 作用 |
|---|---|---|
| `--listen` | `0.0.0.0:19780` | **WebSocket 监听地址**，隧道入口 |
| `--mount-base` | `/mnt/local` | 所有挂载点父目录，一个文件夹一个子目录 |
| `--max-mounts` | 16 | 并发挂载上限（对应最多 16 个 @ 文件夹） |
| `--max-read-bytes` / `--max-write-bytes` | 1 MiB | 单次 RPC 消息体上限 |
| `--op-timeout` | 30s | 单个 FUSE 操作超时（帮助默认值 10s，此处运行值 30s） |
| `--ci-health-url` | `http://127.0.0.1:49983/health` | 启动前置依赖：等 `envd` 就绪后才提供服务 |

`s6` 启动脚本 `/etc/services.d/tabbit-fuse/run` 中留有可被环境变量覆盖的开关（`TABBIT_FUSE_LISTEN`、`TABBIT_FUSE_MOUNT_BASE`、`TABBIT_FUSE_MAX_MOUNTS` 等），并有一段值得注意的注释：

> The required readiness dependency is envd on 49983. The optional code-interpreter service on 49999 is not used by tab-server's current commands/files workflow and must not block tabbit-fuse readiness.

这说明：**文件挂载能力与 code-interpreter 服务解耦**，即使 49999 的解释器服务异常，也不会阻塞挂载就绪。

### 3.3 依赖的运行时探针

```
$ ps -eo pid,ppid,args | grep -E "envd|uvicorn"
   25     1 s6-supervise envd
   42    25 /usr/bin/envd
   24     1 s6-supervise uvicorn
   50    24 /root/.server/.venv/bin/uvicorn main:app --host 0.0.0.0 --port 49999 ...

$ curl -s -o /dev/null -w "%{http_code}" http://127.0.0.1:49983/health
204
```

- `envd` 监听 IPv6 双栈 `:::49983`，`/health` 返回 **204 No Content**（无响应体，纯存活探针）。
- `tabbit-fuse` 以 `--ci-health-url` 轮询该端点，未就绪则不就义。这样保证隧道挂载只在沙箱运行时完整可用后才暴露给上层。

### 3.4 隧道连接实测

```
# 连续 3 次采样，间隔 2 秒
ESTAB 169.254.68.6:19780  <->  10.133.163.102:59208  state=01  inode=2676
ESTAB 169.254.68.6:19780  <->  10.133.163.102:59208  state=01  inode=2676
ESTAB 169.254.68.6:19780  <->  10.133.163.102:59208  state=01  inode=2676
```

关键观察：

1. **沙箱是被连接方**：`169.254.68.6:19780` 为本地监听端点（169.254.x.x 是 IPv4 链路本地地址），说明连接由外部发起。
2. **连接长期保持**：6 秒内同一个 `inode=2676` 不变，是长连接而非每操作新建。
3. **对端是私网中继**：`10.133.x.x` 属 RFC1918 私网段，并非用户公网 IP。说明流量经 Tabbit 中转服务，而非点对点直连；用户本机不直接对外暴露端口。
4. **重连时地址会变**：另一轮采样中观察到对端为 `10.133.167.54:56510`，与上述 `10.133.163.102:59208` 不同 —— 证明隧道断开重连后会落到不同的中继节点/端口，客户端具备自动重连能力。

另外观察到一条独立连接 `169.254.68.6:48322 → 169.254.0.71:80`（链路本地网关的 80 端口），推测为沙箱基础设施的元数据/控制面探测，**与文件隧道无关**（此判断为推断，未进一步验证）。

---

## 4. 一次文件访问的完整时序

以沙箱内执行 `cat /mnt/local/ai-agent-runtime/VERSION` 为例：

```
① shell 发起 open() 系统调用
        ↓
② 内核 VFS 按路径匹配到挂载点 /mnt/local/ai-agent-runtime
        ↓
③ fuse 驱动把请求打包成 FUSE 协议消息，写入 /dev/fuse
        ↓
④ tabbit-fuse 从 /dev/fuse 读出请求（FUSE_OPEN）
   解析出相对路径 "VERSION"，映射到远程根
        ↓
⑤ 序列化为 RPC，经已建立的 WebSocket 发出
   （同时启动 30s 操作超时计时器）
        ↓
⑥ 客户端桥接模块收到 RPC
   → 拼接本地真实路径 E:\projects\ai\ai-agent-runtime\VERSION
   → 执行真实的 Windows 文件 API
        ↓
⑦ 结果（文件句柄号 / 数据 / errno）沿 WebSocket 回传
        ↓
⑧ tabbit-fuse 把结果写回 /dev/fuse
        ↓
⑨ 内核把结果返回给 shell
```

读文件不是一次 RPC，而是**多个独立操作**，每个都单独往返：

| FUSE 操作 | 作用 | 是否走隧道 |
|---|---|---|
| `LOOKUP` | 路径名 → inode | ✅ |
| `GETATTR` | 取 size / mode / mtime | ✅ |
| `OPEN` | 打开文件，协商标志 | ✅ |
| `READ` | 按 128 KiB 分块读数据 | ✅ 每块一次 |
| `RELEASE` | 关闭句柄 | ✅ |
| `WRITE` | 写入数据 | ✅ 每块一次 |
| `CREATE` / `MKDIR` / `UNLINK` | 新建 / 建目录 / 删除 | ✅ |
| `FLUSH` / `FSYNC` | 刷新缓存到持久存储 | ✅ |

推论（有行为证据支持）：因为**元数据也走隧道**，所以隧道中断时连 `ls`、`stat` 都会失败，而不只是文件内容读不到。本环境实测中 `stat` 正常返回 `size=6 mode=644`，`ls` 正常列出目录项，说明当时隧道健康。

---

## 5. 为什么必须是「反向」连接

沙箱运行在云端虚拟机内，位于 NAT 与防火墙之后，**无法主动连接用户家里的电脑**（无公网可达地址、无端口映射、用户也不应为此开放入站端口）。

因此方向被倒置：

```
传统做法（不可行）        本方案（反向隧道）
沙箱 ──X──> 用户电脑      用户电脑 ──> 沙箱:19780
（沙箱无权进入内网）        （本机主动出站，穿透 NAT）
```

这条设计带来三个直接好处：

1. **无需公网暴露**：用户电脑只需能出站访问，不必开任何入站端口，家庭宽带/公司内网均可工作。
2. **授权粒度最小**：客户端只代理用户显式选中的那一个文件夹，沙箱无法凭自身发起对 `C:\` 或 `D:\` 的任意访问 —— 请求路径必须落在已授权根之下。
3. **凭证不落沙箱**：沙箱内不存在 Windows 账号、磁盘凭据或 SMB 口令；它持有的仅是一条受限的连接。

---

## 6. 挂载生命周期

```
[1] 用户 @ 提及文件夹
        ↓  客户端向平台登记授权（文件夹路径 + mount id + 沙箱侧路径）
[2] 平台在沙箱内准备挂载
        ↓
[3] tabbit-fuse 启动，轮询 envd /health 直至 204
        ↓
[4] 客户端发起 TCP + WebSocket 握手 → 19780 建立长连接
        ↓
[5] tabbit-fuse 在 --mount-base 下创建挂载点
    挂载点命名 = 本地文件夹的 basename
        ↓   例：E:\projects\ai\ai-agent-runtime → /mnt/local/ai-agent-runtime
[6] 挂载生效，沙箱内路径可访问
        ↓
[7] 会话结束 / 取消引用 / 连接断开 → 卸载 + 清理挂载点
```

命名规则有一个直接的可验证后果：**挂载点名字取自文件夹名，而非完整本地路径**。因此两个不同父目录下的同名文件夹（如 `D:\a\proj` 与 `E:\b\proj`）会争用同一挂载点 `/mnt/local/proj`，`--max-mounts 16` 又限制了并发数量。这解释了为什么 `mount id` 需要作为独立标识存在，而不能仅靠路径区分。

实测当前状态：`/mnt/local` 下仅有一个子目录 `ai-agent-runtime`，即占用 16 个槽位中的 1 个。

---

## 7. 安全边界分析

| 边界 | 机制 | 评价 |
|---|---|---|
| 授权范围 | 仅用户显式 @ 的文件夹，逐次授权 | ✅ 最小权限 |
| 挂载选项 | `nosuid,nodev` | ✅ 阻断 suid 提权与设备节点注入 |
| 身份映射 | `user_id=0,group_id=0` + `allow_other` | ⚠️ 沙箱内任意进程可访问该挂载 |
| 凭证隔离 | 沙箱侧无 Windows 凭据 | ✅ 仅一条受限隧道 |
| 传输加密 | WebSocket，通常为 wss | ⚠️ 未在沙箱内直接验证 TLS 终止位置 |
| 中继可见性 | 流量经 `10.133.x.x` 中转 | ⚠️ 中转侧理论上可见明文文件内容 |
| 写入风险 | `rw` 读写挂载 | ⚠️ 沙箱内进程可直接修改/删除本地文件 |

需要特别注意的两点：

1. **`allow_other` + root 属主**意味着沙箱内任何进程（包括被执行的不受信代码）都能读写该目录。若本地文件夹内含密钥、`.env`、SSH 私钥等敏感文件，它们对沙箱内代码**完全可见**。授权前应确认目录内容边界。
2. **读写而非只读**。沙箱内一次 `rm -rf` 会真实作用于用户磁盘，没有快照兜底（本机制不含版本回滚）。建议对重要目录改用只读授权模式（若平台支持），或先用 Git 保证可恢复。

---

## 8. 性能特征与实测约束

由于每个操作都是跨网络往返，性能模型与本地磁盘完全不同：

| 约束 | 数值 | 影响 |
|---|---|---|
| 单块读取上限 | 128 KiB（内核 `max_read`） | 读 100 MB 文件约需 800 次往返 |
| 单次 RPC 上限 | 1 MiB（守护进程） | 实际受内核侧 128 KiB 更紧约束 |
| 单操作超时 | 30s | 大目录遍历、大文件顺序读易触发 |
| 并发挂载 | 最多 16 个 | 超出无法新增 |
| 缓存 | 无本地数据缓存（纯代理） | 重复读同一文件仍走隧道 |
| 往返延迟 | 取决于到中继节点的 RTT | 跨地域时元数据密集操作明显变慢 |

实测参照：读取 `README.md`（约 11 KB）耗时约 **0.26 s**，而同等大小本地文件通常在 1 ms 量级 —— 差距主要来自进程启动 + 建立读上下文 + 网络往返，而非带宽。

对使用方式的直接影响：

- **适合**：读源码、改配置、小文件增删、`git` 操作（对象多但单文件小）。
- **不适合**：`node_modules` 全量扫描、大型二进制读写、数据库文件、频繁随机 IO、`grep -r` 全仓库。
- **建议**：需要重度 IO 时先把数据拷到 `/mnt/work`（沙箱本地盘）再处理，避免每步都跨隧道。

---

## 9. 实测异常：`close` 阶段间歇性 EIO

分析过程中稳定复现了一个异常，记录如下：

```
$ cat /mnt/local/ai-agent-runtime/VERSION
v0.5.0cat: /mnt/local/ai-agent-runtime/VERSION: Input/output error
$ echo "exit=$?"
exit=0        # 注意：数据已输出，但随后报错
```

**现象**：文件内容被正确读出（`v0.5.0`），但在关闭句柄阶段返回 `EIO`。`README.md`、`docs/README.md`、`docs/architecture/retry-architecture.md` 均出现同样情况，且连续 3 次重试结果一致（非偶发抖动）。

**判定**：错误发生在 `RELEASE` / `FLUSH` 阶段，而非 `OPEN` / `READ`。数据通路是通的，失败在收尾环节。

**可能原因**（按可能性排序，均待官方确认）：
1. 隧道侧对 `RELEASE` 的响应丢失或未回包，超时后由 FUSE 层报 `EIO`；
2. 客户端桥接在处理 Windows 句柄关闭时返回了非预期的错误码；
3. `FLUSH` 语义映射缺失（Windows 无完全对等的 flush-on-close 语义）。

**影响**：
- ✅ 不影响读取内容本身；
- ❌ 会让 `cat` / `head` 等命令返回非零退出码，**可能误伤依赖退出码的脚本与 CI 判断**；
- ❌ 某些程序（编辑器保存、`git` 写入后校验）可能因 `close` 失败而回滚或报错。

**规避手段**：

```bash
# 1) 用 dd 显式限定字节数，避免读到 EOF 后的收尾问题
dd if=/mnt/local/.../file bs=4096 count=1 2>/dev/null

# 2) 读取时不因 close 失败而中断脚本
cat file 2>/dev/null || true

# 3) 需要可靠处理时，先复制到沙箱本地盘再操作
cp /mnt/local/.../file /mnt/work/ 2>/dev/null; echo "cp exit=$?"
```

> 该异常不代表挂载不可用 —— 目录遍历、`stat` 元数据、写入、`mkdir`、`rmdir` 均实测成功。它是收尾环节的局部缺陷。

---

## 10. 与其他方案的对比

| 方案 | 数据位置 | 是否需网络 | 实时性 | 本环境实测 |
|---|---|---|---|---|
| **FUSE + 反向隧道（本方案）** | 用户本机 | 是（反向长连接） | 实时双向 | ✅ 正在使用 |
| virtiofs | 宿主共享目录 | 否（PCI 共享内存） | 实时 | ✅ 用于 `/mnt/cos/*` |
| NFS / CIFS | 服务端 | 是（正向挂载） | 实时 | ❌ 挂载表无条目 |
| 上传复制 | 沙箱内副本 | 一次性 | 非实时 | ❌ 与实测行为不符 |
| Git clone / rsync 同步 | 沙箱内副本 | 周期性 | 延迟 | ❌ 无对应进程 |

同环境中两种机制并存，形成清晰对照：

```
/mnt/cos/artifacts          virtiofs     ← 沙箱宿主提供，无 IP，走 virtqueue
/mnt/skill-state            virtiofs     ← 同上
/mnt/cos/attachments        virtiofs     ← 同上（只读源）
/mnt/cos/skills/*           virtiofs     ← 同上
/mnt/local/ai-agent-runtime fuse.tabbit  ← 用户电脑，走反向 WebSocket
```

**为什么用户目录不能用 virtiofs？** 因为 virtiofs 要求共享目录位于**虚拟机宿主**一侧；而用户的文件夹在个人电脑上，与宿主是完全不同的机器，只能通过网络隧道跨越。这也是本方案必须自建 FUSE 而不能复用内核文件系统的根本原因。

---

## 11. 排障手册

| 症状 | 可能原因 | 诊断 | 处置 |
|---|---|---|---|
| `Input/output error` | 隧道断开 / 重连中 | 查 19780 是否有 ESTAB 连接 | 等待自动重连，或重新 @ 引用 |
| 挂载点不存在 | 尚未建立 / 已卸载 | `ls /mnt/local` | 重新引用文件夹 |
| 读取极慢 | 网络 RTT 高 / 大文件 | 观察文件大小与耗时 | 先拷到 `/mnt/work` |
| 操作超时 | 单操作超 30s | 是否大目录递归遍历 | 缩小操作粒度 |
| 新文件夹挂不上 | 已达 16 个上限 | `ls -d /mnt/local/*/ \| wc -l` | 取消部分引用 |
| 挂载点名字冲突 | 两文件夹同名 | 检查 basename | 重命名其中一个 |
| 写入后本机未见 | 缓存未刷 / 隧道延迟 | 重读确认 | 显式 `sync` 或重试 |

常用诊断命令（沙箱内执行）：

```bash
# 挂载状态
grep "/mnt/local" /proc/self/mounts

# 隧道连接
ss -tnp | grep 19780 || netstat -tn | grep 19780

# 守护进程
ps -eo pid,args | grep tabbit-fuse

# 运行时探针
curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:49983/health   # 期望 204

# 只查元数据（绕过数据通道）
stat /mnt/local/<folder>/<file>
```

---

## 12. 证据附录

所有结论均来自沙箱内实测，原始输出摘录：

```bash
# 1. 挂载表 —— 确认非网络文件系统
$ grep -cE " (nfs|nfs4|cifs|smb3|9p) " /proc/mounts
0

# 2. 挂载条目
$ grep "/mnt/local" /proc/self/mounts
tabbit-localfs /mnt/local/ai-agent-runtime fuse.tabbit rw,nosuid,nodev,relatime,
    user_id=0,group_id=0,allow_other,max_read=131072 0 0

# 3. 守护进程与其参数
$ ps -eo pid,ppid,args | grep tabbit-fuse
   80     1 s6-supervise tabbit-fuse
   82    80 /usr/local/bin/tabbit-fuse --listen 0.0.0.0:19780 \
             --mount-base /mnt/local --max-read-bytes 1048576 \
             --max-write-bytes 1048576 --max-mounts 16 --op-timeout 30s \
             --ci-health-url http://127.0.0.1:49983/health --ci-health-timeout 2s

# 4. 帮助文本确认端口语义
$ /usr/local/bin/tabbit-fuse --help | head -12
  -listen string
        WebSocket listen address (default "0.0.0.0:19780")
  -mount-base string
        Base directory for FUSE mount points (default "/mnt/local")
  -max-mounts int
        Max concurrent FUSE mounts (default 16)
  -op-timeout duration
        Timeout per FUSE operation (default 10s)

# 5. 二进制形态
$ file /usr/local/bin/tabbit-fuse
ELF 64-bit LSB executable, x86-64, statically linked, Go, with debug_info, not stripped

# 6. 隧道长连接（多次采样一致）
::1 19780 LISTEN
169.254.68.6:19780  <->  10.133.163.102:59208  ESTAB  inode=2676

# 7. 运行时探针
$ curl -s -o /dev/null -w "%{http_code}" http://127.0.0.1:49983/health
204

# 8. 读写可用性
$ cat /mnt/local/ai-agent-runtime/VERSION
v0.5.0                                        # 内容正确
$ stat -c "%n size=%s mode=%a" .../VERSION
/mnt/local/ai-agent-runtime/VERSION size=6 mode=644    # 元数据正确
$ mkdir /mnt/local/ai-agent-runtime/.probe_dir && rmdir ...
mkdir OK / rmdir OK                            # 写入与删除可用

# 9. 只读 vs 读写对照（同一沙箱内）
/mnt/cos/attachments        virtiofs     (只读源)
/mnt/local/ai-agent-runtime fuse.tabbit  (rw 实测可写)
```

---

## 13. 尚未确认的部分（诚实标注）

以下为推断或未能验证，不应作为确定结论引用：

1. **WebSocket 是否加密（wss/TLS）**：沙箱内只能看到 TCP 连接，TLS 终止位置与证书链未验证。
2. **RPC 的具体编码格式**：未反序列化消息体，协议字段、版本协商、错误码映射均未知。
3. **中继节点（10.133.x.x）的具体角色**：是纯 TCP 转发、还是做协议解包，未验证；若是后者则中转侧可见文件明文。
4. **`EIO` 的确切根因**：仅定位到 `close` 阶段，未取得守护进程日志或客户端侧错误码。
5. **`169.254.0.71:80` 连接用途**：推测为基础设施元数据/健康探测，未证实。
6. **`mount id` 的实际用途**：仅从「路径 basename 命名会冲突」反推其必要性，未找到消费该 id 的代码。

---

## 14. 小结

这套机制的本质是一次**语义降级再重建**：把 Windows 本地磁盘的文件系统语义，降级为可跨网络传输的 RPC 消息流，再在 Linux 沙箱内用 FUSE 重建为一棵以假乱真的目录树。

它用「逐操作代理」换来了三样东西——**数据不出本机、授权粒度到单个文件夹、零客户端安装成本**；代价则是**每个 IO 都是网络往返**，以及连接断开时整个目录瞬间失效的**单点依赖**。

理解这一点后，正确的使用姿势就很明确：把它当作「挂在网络另一端的薄代理」，小文件编辑与源码阅读很顺手，重 IO 与大文件处理则应先落到沙箱本地盘。
