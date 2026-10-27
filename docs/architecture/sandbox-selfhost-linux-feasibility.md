# 用一台 Linux 服务器复刻沙箱架构：可行性分析与实施方案

> 目标：评估「能否在自己的一台 Linux 服务器上，实现与 Tabbit 沙箱等价的能力」
> 对照架构：`sandbox-command-execution-channel.md`（命令通道）+ `sandbox-local-folder-mount-mechanism.md`（文件通道）
> 参照环境：Debian 13 / Linux 6.6.69-cube / 3 vCPU / 4.1 GiB / `/dev/fuse` 可用 / cgroup v2
> 更新日期：2026-10-27

---

## 1. 结论先行

**可行，而且大部分组件都有成熟开源替代。但「复刻到哪一层」决定了工作量差一个数量级。**

必须先回答一个问题：**你的文件在哪里？沙箱在哪里？**

| 场景 | 文件位置 | 沙箱位置 | 是否需要反向隧道 | 工作量 |
|---|---|---|---|---|
| **A · 单机自用** | 服务器本机 | 服务器本机 | ❌ 不需要 | 1–2 天 |
| **B · 本机访问服务器** | 你的笔记本 / 工作站 | 服务器 | ✅ 必需 | 3–7 天 |
| **C · 多租户服务** | 各租户设备 | 服务器集群 | ✅ 必需 | 2–6 个月 |

**关键判断**：原架构之所以要「FUSE + 反向 WebSocket 隧道」，唯一原因是**文件在用户的 Windows 电脑上，而沙箱在云端虚拟机里**，两者隔着 NAT。如果你的文件就在服务器本机，这套 FUSE 隧道**完全是多余的** —— 一个 `bind mount` 就够了。

所以：

- 只想要「AI 在隔离环境里跑命令、读写我的项目」→ **场景 A，一天可跑通**
- 想要「笔记本上的文件夹被服务器沙箱直接读写」→ **场景 B，需要原样复刻隧道**
- 想做成对外服务 → **场景 C，安全是真正的难点，不是功能**

---

## 2. 组件映射：原架构 → 开源替代

| 原架构组件 | 职责 | 自建替代方案 | 成熟度 |
|---|---|---|---|
| KVM 虚拟机 | 隔离边界 | **Firecracker** / **Cloud Hypervisor** / **gVisor** / Docker / nsjail | ✅ 高 |
| `envd`（命令+RPC） | 执行代理 | 自写 Go 服务（gRPC/Connect）+ `fork/exec` | ✅ 简单 |
| `s6` | 进程监督 | **systemd** 单元 / s6-overlay | ✅ 高 |
| `prlimit` 注入 | 资源限制 | `prlimit` + **cgroup v2** | ✅ 高 |
| `tabbit-fuse` | 本地目录挂载 | **bind mount**（场景A）/ `bazil.org/fuse`（场景B） | ✅ 高 |
| 反向 WebSocket 隧道 | 穿透 NAT | **frp** / **cloudflared** / **Tailscale** / `ssh -R` | ✅ 高 |
| 云端中继集群 | 多节点负载 | **frps** / Cloudflare / 自建 gateway + Consul | ✅ 中 |
| virtiofs 共享 | 宿主↔沙箱 | Docker `-v` / virtiofsd / 9p | ✅ 高 |
| 快照与预热池 | 秒级启动 | Firecracker snapshot / **CRIU** / 镜像预热 | ⚠️ 中高 |
| 控制台与鉴权 | 多租户 | 自建 API + mTLS + token | ⚠️ 需自研 |

**好消息**：功能层面几乎没有缺口，全部有对应开源实现。
**坏消息**：**安全隔离强度**和**启动延迟**这两项，自建很难追平 KVM 快照池。

---

## 3. 场景 A：单机自用（推荐先做这个）

### 3.1 架构

```
你的 Linux 服务器
┌──────────────────────────────────────────────┐
│  AI Agent（进程/服务）                        │
│      │ ① HTTP / gRPC / 甚至直接 SSH           │
│      ▼                                       │
│  Docker 容器（沙箱）                          │
│   · 独立 PID/NET/MNT/UTS namespace           │
│   · cgroup v2 限制 CPU/内存/PID/磁盘          │
│   · seccomp + 非 root 用户                    │
│   · bind mount: /srv/project → /workspace    │
└──────────────────────────────────────────────┘
```

**没有反向隧道、没有 FUSE、没有中继。** 因为一切都在同一台机器上，Agent 直接通过 Docker API 或 SSH 进入容器即可。

### 3.2 最小可用实现（Docker）

```bash
# 1) 建一个受限镜像
cat > Dockerfile <<'EOF'
FROM debian:13-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
      bash coreutils curl git python3 ca-certificates \
    && rm -rf /var/lib/apt/lists/*
RUN useradd -m -u 1000 -s /bin/bash agent
USER agent
WORKDIR /workspace
EOF
docker build -t sandbox:base .

# 2) 启动（等价于原架构的挂载 + 资源限制）
docker run -d --name sb1 \
  --user 1000:1000 \
  --cpus 2 \
  --memory 2g --memory-swap 2g \
  --pids-limit 512 \
  --read-only --tmpfs /tmp:size=256m \
  --cap-drop ALL \
  --security-opt no-new-privileges \
  --security-opt seccomp=./seccomp.json \
  -v /srv/project:/workspace:rw \
  --network none \
  sandbox:base sleep infinity

# 3) 执行命令（一次一个进程，语义与原架构一致）
docker exec sb1 bash -lc 'ls -la /workspace'

# 4) 单文件大小限制（对齐原架构的 2 GB）
docker exec sb1 bash -lc 'prlimit --fsize=2000000000:2000000000 -- bash -lc "dd if=/dev/zero of=/workspace/x bs=1M count=5000"'
```

### 3.3 与原架构的等价度

| 能力 | 原架构 | 场景 A | 等价 |
|---|---|---|---|
| 命令执行 | `envd` RPC → fork bash | `docker exec` → fork bash | ✅ |
| 单文件 2 GB 上限 | `prlimit` 注入 | `prlimit` / cgroup | ✅ |
| 独立文件系统视图 | 虚拟机 | namespace + overlay | ✅ |
| 用户目录读写 | FUSE 隧道 | **bind mount** | ✅ 且更快 |
| 网络隔离 | VM NAT | `--network none` 或自定义 bridge | ✅ |
| 秒级重置 | VM 快照 | 容器重建（约 1–3 s） | ⚠️ 稍慢 |
| 内核级隔离 | KVM 硬件虚拟化 | 共享宿主内核 | ❌ **弱** |

**唯一实质差距**：Docker 共享宿主内核，容器逃逸风险高于 KVM。对「自用、跑自己代码」完全够用；对「跑不可信第三方代码」不够。

### 3.4 想要更强隔离：gVisor 或 nsjail

```bash
# gVisor：用户态内核，拦截全部 syscall，不需要 KVM
docker run --runtime=runsc ...        # 隔离接近 VM，代价是 syscall 密集任务变慢

# nsjail / bubblewrap：namespace + seccomp 轻量封装
nsjail --config nsjail.cfg -- /bin/bash -l -c 'ls /workspace'
```

选择建议：

| 威胁模型 | 推荐 |
|---|---|
| 跑自己的代码 | Docker + `cap-drop ALL` + 非 root |
| 跑不确定安全的开源项目 | **gVisor**（runsc） |
| 跑真正的不可信代码 | **Firecracker** 微虚拟机 |
| 只跑几条命令、要极轻量 | **nsjail** / **bubblewrap** |

---

## 4. 场景 B：本机文件夹被服务器沙箱读写（复刻 FUSE 隧道）

这是原架构的真实场景。要复刻它，必须解决「服务器上的沙箱如何访问你笔记本上的文件夹」。

### 4.1 为什么不能用 bind mount

```
bind mount 要求：源目录与挂载点在同一台机器的文件系统上
你的情况：      文件在笔记本，沙箱在服务器  → 跨机器，bind mount 无解
```

必须走网络文件系统或用户态代理。

### 4.2 三条路线

| 路线 | 实现 | 优点 | 缺点 |
|---|---|---|---|
| **① SSHFS** | 服务器 `sshfs user@laptop:/path /mnt/local/path` | 零开发，5 分钟可用 | 需笔记本可被 SSH 连入 |
| **② 反向隧道 + SSHFS** | 笔记本 `ssh -R` 到服务器，再挂 SSHFS | 穿透 NAT，无需公网 IP | 多一层，链路复杂 |
| **③ 自建 FUSE + 隧道** | 复刻 `tabbit-fuse` 架构 | 完全等价，可控 | 要写代码 |

### 4.3 路线①：SSHFS（最简单）

```bash
# 服务器上装 sshfs，直接把笔记本目录挂进来
apt-get install -y sshfs

sshfs -o reconnect,ServerAliveInterval=15,ServerAliveCountMax=3,\
      allow_other,uid=1000,gid=1000,max_read=131072 \
      user@laptop.local:/home/user/project /mnt/local/project
```

注意 `max_read=131072`（128 KiB）—— 与原架构观测到的 `max_read=131072` **完全一致**，因为这是 FUSE 层的通用保守值。

这就是**原架构的核心思想用现成工具实现**：`sshfs` = FUSE 客户端 + SFTP 传输，`tabbit-fuse` = FUSE + 自定义 RPC + 隧道。二者是同构的。

### 4.4 路线②：反向隧道（笔记本无法被直连时）

笔记本在 NAT 后，服务器连不进去。方向倒置：

```bash
# 【笔记本】主动连服务器，把本地 22 端口映射到服务器的 2222
ssh -N -R 0.0.0.0:2222:localhost:22 user@server.example.com

# 【服务器】需要 GatewayPorts yes 才能监听 0.0.0.0
# /etc/ssh/sshd_config:  GatewayPorts yes
systemctl reload sshd

# 【服务器】通过隧道挂载笔记本目录
sshfs -p 2222 -o reconnect,allow_other user@localhost:/home/user/project /mnt/local/project
```

**这与原架构的 `169.254.68.6:19780 ← 10.133.x.x` 方向完全相同**：沙箱是「被连接方」，客户端主动出站，穿透 NAT。

### 4.5 路线③：用 frp 做更稳的隧道

```ini
# 【笔记本】frpc.ini
[common]
server_addr = server.example.com
server_port = 7000
token = <共享密钥>

[fs]
type = tcp
local_ip = 127.0.0.1
local_port = 22
remote_port = 2222
```

```ini
# 【服务器】frps.ini
[common]
bind_port = 7000
token = <共享密钥>
```

```bash
# 服务器端
./frps -c frps.ini
# 笔记本端
./frpc -c frpc.ini
# 之后服务器执行 sshfs -p 2222 ...
```

`frp` 比 `ssh -R` 更稳定：自带心跳、断线自动重连、多路复用。原架构中的「会话中断后自动重连到不同中继节点」在 frp 里对应多 server 配置 + 负载均衡。

### 4.6 路线④：Tailscale（最省心，推荐）

```bash
# 服务器与笔记本各装 Tailscale，加入同一 tailnet
curl -fsSL https://tailscale.com/install.sh | sh
tailscale up

# 之后二者仿佛同一内网，直接挂载
sshfs user@laptop:~/project /mnt/local/project
```

优点：自动穿透 NAT、端到端加密（WireGuard）、无需公网 IP、无需维护隧道。
缺点：依赖第三方协调服务（可自建 Headscale 替代）。

**如果你只是要「让服务器沙箱读写笔记本文件夹」，Tailscale + SSHFS 是投入产出比最高的组合 —— 一小时内可用，无需写一行代码。**

---

## 5. 场景 C：复刻真正的多租户架构

要追平原架构（KVM 隔离 + 快照池 + 中继集群），需要自研以下四块。

### 5.1 隔离层：Firecracker

```bash
# 需要 /dev/kvm
ls -l /dev/kvm

# 启动一个微虚拟机（约 5 MB 内存开销，启动 ~125 ms）
firecracker --api-sock /tmp/fc.sock --config-file vm.json
```

```json
{
  "boot-source": {
    "kernel_image_path": "/var/lib/fc/vmlinux",
    "boot_args": "console=ttyS0 reboot=k panic=1 pci=off"
  },
  "drives": [{
    "drive_id": "rootfs",
    "path_on_host": "/var/lib/fc/rootfs.ext4",
    "is_root_device": true,
    "is_read_only": false
  }],
  "machine-config": { "vcpu_count": 2, "mem_size_mib": 512 },
  "network-interfaces": [{
    "iface_id": "eth0",
    "host_dev_name": "tap0",
    "guest_mac": "AA:FC:00:00:00:01"
  }]
}
```

**原架构的 `cpu_count:3 / mem_total:4362629120` 用 Firecracker 就是 `vcpu_count:3 / mem_size_mib:4096`** —— 数值完全对得上，说明对方很可能就是同级别方案。

### 5.2 快照池：把启动延迟压到百毫秒

```bash
# 预热：启动一台、跑完初始化、打快照
curl --unix-socket /tmp/fc.sock -X PUT \
  http://localhost/snapshot/create \
  -d '{"snapshot_type":"Full","snapshot_path":"/var/lib/fc/snap"}'

# 之后用 load 代替 boot
curl --unix-socket /tmp/fc.sock -X PUT \
  http://localhost/snapshot/load \
  -d '{"snapshot_path":"/var/lib/fc/snap"}'
```

这是自建与商业方案差距最大的一环：**冷启动 1–3 s vs 快照恢复 100–200 ms**。做多租户时必须做预热池，否则并发一上来交付就崩。

### 5.3 中继层：复刻 `10.133.x.x`

原架构中继是个**私网集群**（观察到 `10.133.163.x`、`10.133.166.x`、`10.133.167.x` 多个节点，且每次会话落在不同节点）。自建等价物：

```
                    ┌─ 中继节点 1 ─┐
用户 ──> LB/Anycast ─┼─ 中继节点 2 ─┼─> 各沙箱的常驻连接
                    └─ 中继节点 3 ─┘
```

实现要点：

1. **沙箱侧 frpc 常驻连到中继**，中继维护 `sandbox_id → 连接` 路由表（etcd/Redis）。
2. **用户请求按 sandbox_id 路由**到持有该连接的节点。
3. **控制面与数据面分两条隧道**（原架构如此：`envd:49983` 与 `tabbit-fuse:19780` 来自不同中继 IP）。分开的好处是故障隔离 —— 大文件传输不会堵塞命令响应。

### 5.4 执行代理：自写 envd

```go
// 极简版：HTTP + fork/exec + 流式输出
package main

import (
	"encoding/json"
	"net/http"
	"os/exec"
	"syscall"
)

func runHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cmd string   `json:"cmd"`
		Cwd string   `json:"cwd"`
		Argv []string `json:"argv"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	cmd := exec.Command("/bin/bash", "-l", "-c",
		"prlimit --fsize=2000000000:2000000000 -- bash -lc "+shellQuote(req.Cmd))
	cmd.Dir = req.Cwd
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,                    // 独立进程组，便于整组 kill
	}
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
	}
	json.NewEncoder(w).Encode(map[string]any{
		"stdout": string(out), "exit_code": code,
	})
}

func shellQuote(s string) string { return "'" + stringReplace(s, "'", `'\''`) + "'" }
```

这段代码对应原架构 `POST /process.Process/Start` 的行为，包括：
- `bash -l`（login shell，加载 profile）
- `prlimit --fsize=2000000000:2000000000`（单文件 2 GB 硬限制）
- 每命令独立进程（不做会话复用）

要更接近原架构，把 HTTP 换成 Connect/gRPC：

```proto
service Process {
  rpc Start(StartRequest) returns (stream StartResponse);  // 流式回传 stdout/stderr
  rpc List(ListRequest) returns (ListResponse);
}
```

用 `stream` 实现边执行边输出，避免长命令超时。

### 5.5 安全加固清单（这一项最容易漏）

原架构本身也有可议之处，自建时不要照抄：

| 风险 | 原架构表现 | 自建应做 |
|---|---|---|
| 执行身份过高 | 命令以 root 运行 | 固定非 root UID（`--user 1000:1000`） |
| 管理 API 无鉴权 | `/process.Process/List` 回环上直接 200 | **mTLS 或 token 校验**，绑定 `127.0.0.1` |
| 挂载过宽 | FUSE `allow_other` + root 属主 | 精确 UID 映射，必要时 `nosuid,nodev,noexec` |
| 磁盘耗尽 | `prlimit` 只限单文件，靠磁盘总量兜底 | cgroup v2 `io.max` + XFS project quota |
| Fork 炸弹 | `pids.max` 兜底 | cgroup `pids.max` 必设 |
| 网络外泄 | 沙箱可访问内网 | 默认 `--network none`；需联网时用出口白名单代理 |
| 容器逃逸 | KVM 隔离 | 非特权容器 + seccomp + `no-new-privileges`；敏感场景换 gVisor |

---

## 6. 资源限制的完整实现

原架构只看到 `prlimit`，实际生产还需要 cgroup 兜底。三层配合：

### 6.1 RLIMIT（单进程级，随 fork 继承）

```bash
prlimit --fsize=2000000000:2000000000 \
        --nofile=1000000:1000000 \
        --nproc=16624:16624 \
        -- bash -lc '<cmd>'
```

对应原架构实测值：`FSIZE=2000000000`、`NOFILE=1000000`、`NPROC=16624`。

**特点**：hard limit 锁死，普通用户无法自行提升 → 这就是「单文件大小硬限制不可绕过」的技术根源。

### 6.2 cgroup v2（整组资源，防累积）

```bash
CG=/sys/fs/cgroup/sandbox/$ID
mkdir -p $CG

# CPU：2 核
echo "200000 100000" > $CG/cpu.max
# 内存：2 GiB，超限 OOM 而非拖垮宿主
echo 2147483648 > $CG/memory.max
echo 0 > $CG/memory.swap.max
# 进程数：防 fork 炸弹
echo 512 > $CG/pids.max
# 磁盘：限制设备写入带宽（配合 XFS 配额约束总量）
echo "8:0 rbps=104857600 wbps=52428800" > $CG/io.max

echo $$ > $CG/cgroup.procs
```

### 6.3 磁盘总量配额（`prlimit` 管不到的部分）

```bash
# XFS 项目配额 —— 限制总量而非单文件
xfs_quota -x -c 'project -s -p /srv/sandbox/sb1 1001' /srv
xfs_quota -x -c 'limit -p bhard=1g 1001' /srv

# 或 btrfs qgroup
btrfs qgroup limit 1G /srv/sandbox/sb1
```

原架构的 `disk_total ≈ 1.00 GiB` 就是这种总量限制 —— 注意它**不是** `prlimit` 给的，`prlimit` 只限单文件 2 GB，两个限制是不同层次。

### 6.4 三层对照

| 层 | 管什么 | 原架构 | 自建 |
|---|---|---|---|
| RLIMIT | 单进程单文件大小 | `prlimit --fsize=2e9` | 同 |
| cgroup | 整组 CPU/内存/PID | 虚拟机边界天然限制 | **必须显式设** |
| 配额 | 磁盘总量 | ~1 GiB | XFS/btrfs 配额 |

**自建时最容易被忽略的是 cgroup**：容器默认不限制，一条 `:(){ :|:& };:` 能拖垮整台服务器。

---

## 7. 与原架构的差距（诚实评估）

| 维度 | 原架构 | 自建能做到 | 差距 |
|---|---|---|---|
| 功能完整性 | 命令 + 文件 + 解释器 | **可 100% 等价** | 无 |
| 隔离强度 | KVM 硬件虚拟化 | Firecracker 可等价；Docker 弱 | 取决于选型 |
| 启动延迟 | 快照 ~100 ms 级 | 需自建快照池才追平 | 中 |
| 文件 IO 延迟 | FUSE 隧道（网络往返） | **bind mount 更快**；SSHFS 相近 | 自建可能更好 |
| 隧道稳定性 | 多中继节点 + 自动切换 | frp 多节点 / Tailscale | 中 |
| 规模弹性 | 多节点调度 | 需自研调度器 | 大 |
| 运维成本 | 平台托管 | 全部自理 | 大 |
| 安全审计 | 供应商负责 | 全部自担 | 大 |

**最容易被低估的不是功能，是运维与安全责任。**

---

## 8. 分阶段落地路线图

### 第一阶段（1–2 天）：跑通最小闭环

```bash
目标：AI 能在隔离容器里执行命令 + 读写项目目录
```

1. Docker 建受限镜像（非 root、cap-drop、seccomp）
2. bind mount 项目目录
3. cgroup 限制 CPU/内存/PID
4. 用 `docker exec` 或自写 20 行 HTTP 服务执行命令

**checkpoint**：`ls /workspace` 能看到项目文件；`dd` 写超 2 GB 被 SIGXFSZ 拦住。

### 第二阶段（3–7 天）：加上隧道与流式输出

```bash
目标：笔记本文件夹可被服务器沙箱直接读写
```

1. Tailscale 或 frp 打通网络
2. `sshfs -o allow_other,max_read=131072` 挂载笔记本目录
3. 执行代理改为 gRPC/Connect + `stream` 流式回传
4. 加 token/mTLS 鉴权

**checkpoint**：服务器上改文件，笔记本立即可见；长命令输出边执行边返回。

### 第三阶段（2–6 周）：隔离升级与快照

1. Docker → gVisor（或 Firecracker）
2. 实现预热池 + 快照恢复
3. 磁盘配额（XFS project quota）
4. 网络出口白名单

**checkpoint**：冷启动 < 300 ms；并发 50 个沙箱不互相影响。

### 第四阶段（按需）：多租户化

1. 中继集群（frps + 路由表）
2. 控制面/数据面隧道分离
3. 调度器 + 监控 + 审计日志

---

## 9. 硬件与软件基线

### 9.1 场景 A/B 单机

| 项 | 建议 |
|---|---|
| CPU | 4 核起（每沙箱 1–2 核） |
| 内存 | 8 GB 起（每沙箱 1–2 GB） |
| 磁盘 | SSD / NVMe，预留 3× 并发沙箱容量 |
| 内核 | ≥ 5.10（cgroup v2 + overlay2 稳定）|
| 必备 | `overlay` / `cgroup2` / `/dev/fuse`（用 FUSE 时）|

### 9.2 场景 C 集群

| 项 | 建议 |
|---|---|
| CPU | 16 核起，开启嵌套虚拟化 |
| 内存 | 64 GB 起 |
| 必备 | `/dev/kvm`（Firecracker）、`/dev/fuse`、NVMe 本地盘 |
| 网络 | 内网 10 GbE，中继独立网段 |
| 单机承载 | 轻量沙箱（1C/512M）约 30–50 个并发 |

### 9.3 内核前提自查

```bash
# 参照本沙箱实测：以下能力全部具备
ls -l /dev/fuse                    # crw------- 10,229  → FUSE 可用
stat -fc %T /sys/fs/cgroup         # cgroup2fs          → cgroup v2 可用
ls /proc/self/ns/                  # cgroup ipc mnt net pid user uts time → namespace 齐备
ls -l /dev/kvm                     # 场景 C 必需
cat /proc/sys/user/max_user_namespaces   # 非 root 容器需要
```

---

## 10. 一句话选型建议

| 你的情况 | 直接选 |
|---|---|
| 只想要 AI 在隔离环境跑命令、改项目文件 | **Docker + bind mount**（场景 A） |
| 要让服务器读写你笔记本上的文件夹 | **Tailscale + SSHFS**（场景 B，最省心） |
| 跑不确定安全的代码 | **Docker + gVisor（runsc）** |
| 跑真正不可信的第三方代码 | **Firecracker** |
| 要对外提供服务 | 别从零造，先看 **E2B / OpenSandbox / Daytona** 这类开源沙箱平台 |

> 自建之前先想清楚：**你要的是「沙箱」，还是「沙箱 + 隧道 + 调度 + 计费 + 审计」这一整套平台。** 前者一两天，后者半年起。

---

## 11. 未验证与边界说明

本文为设计推演，以下内容**未经实机搭建验证**，落地前需自行测试：

1. **Firecracker 快照恢复的实际延迟**：文中「100–200 ms」为公开资料区间，非本环境实测。
2. **gVisor 的 syscall 兼容性缺口**：部分工具（如某些 Go 程序的 `io_uring`、`docker-in-docker`）在 runsc 下可能失败，需按 workload 验证。
3. **SSHFS 在大文件顺序读下的吞吐**：受加密与往返延迟影响明显，未做基准测试。
4. **frp 多节点自动切换的行为**：需实测断线重连时间与路由表收敛速度。
5. **XFS project quota 与 overlay2 的叠加效果**：配额作用在底层目录还是 overlay 上层，需按实际存储驱动确认。
6. **文中的 Go 代码为示意**：缺少错误处理、并发控制、超时 kill、资源清理，不可直接用于生产。
7. **原架构中继是否做协议解包**：若做，则中继侧可见明文；自建时建议全程 mTLS 端到端加密，不依赖中继可信。

---

## 12. 小结

原架构的三个核心思想，在自建时都成立，但可以大幅简化：

1. **隔离靠边界，不靠自律** —— 无论 KVM 还是 namespace，关键是限制在启动时强制施加，不依赖被执行代码配合。这与 `prlimit` 的思路一致。
2. **方向倒置以穿透 NAT** —— 沙箱永远是被连接方。若你的文件就在本机，这一层可以整个删掉。
3. **控制面与数据面分离** —— 命令通道与文件通道用两条独立隧道，故障隔离。自建时同样建议分开。

**最容易的路径往往不是「复刻」，而是「替换掉不需要的部分」。** 原架构的 FUSE 隧道是为「文件在远端」这一前提付的代价；你的文件在服务器上时，这个代价不必再付。

---

## 13. 关联文档

- `sandbox-command-execution-channel.md` —— 命令通道解剖（`envd`、Connect 协议、`prlimit` 注入）
- `sandbox-local-folder-mount-mechanism.md` —— 文件通道解剖（FUSE、反向 WebSocket、`max_read` 约束）
