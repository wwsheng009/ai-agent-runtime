# aicli-mesh 快速上手

> 目标：从零跑通「两个进程互相看见、互相调用、事后可复盘、退出可对账」。
> 权威 CLI 参考：[../aicli/mesh-cli.md](../aicli/mesh-cli.md)；本页只给最短路径与成功信号。

## 0. 准备工具

任选其一：

```powershell
# A. 独立二进制（推荐：脚本里更明确）
pwsh scripts/build.ps1 -Tools aicli-mesh
./backend/aicli-mesh.exe version        # 或把产物目录加入 PATH

# B. 内置别名（只部署了单个 aicli 时）
aicli mesh version
```

`version --json` 会打印 `mesh_root` 与 `mesh_root_source`（`env` / `aicli-home` / `user-home`）——
**先确认根目录符合预期**，后面所有命令读写的都是它。

> 实验隔离：`$env:AICLI_MESH_DIR = "$env:TEMP\mesh-lab"` 可把整套实验关进临时目录，不影响真实网格。

## 1. 体检：doctor

```powershell
aicli-mesh doctor            # 人读：[ok]/[告警]/[问题]
aicli-mesh doctor --json     # 机器读：problems/warnings 计数
```

成功信号：`problems=0`（告警可以先用 `gc` 自愈）。退出码 5 表示有「问题」，逐项看 `checks[].detail`。
检查项与处置见 [troubleshooting.md](./troubleshooting.md) §2。

## 2. 起两个节点

节点 = 一个**带回环控制面**的 aicli 进程：

```powershell
# 窗口 A
aicli chat --yolo --pprof

# 窗口 B
aicli chat --yolo --pprof
```

- `--pprof`：开启回环 HTTP 控制面，端口**自选**（网格负责发现，脚本不需要记端口）；
- 要固定端口用 `--web-port 51234`（1–65535，**不接受 0**）；
- 两者都不带的**纯 TUI** 节点仍在网格里（`ls` 能看到），但**没有端点**：`url` 退出码 3、
  `call` 返回 `mesh_no_endpoint`。

## 3. 发现：ls

```powershell
aicli-mesh ls
```

```text
STATE  NODE                         PID    SESSION                          WORKSPACE                     ADDR                    AGE     OWN
-----  ---------------------------  -----  -------------------------------  ----------------------------  ----------------------  ------  ----
live   node-22024-20260924T013534Z  22024  session_20260924093535_4wCDwhqu  E:\projects\ai\ai-agent-runtime  http://127.0.0.1:63910  8s      peer
```

| 列 | 读法 |
|----|------|
| `STATE` | `live`（心跳新鲜）/ `stale`（进程已退或心跳过期）/ `stopped`（自己声明退出）/ `unknown`（档案不可读） |
| `ADDR` | 端点 base URL；**空**说明该节点没开控制面 |
| `OWN` | `owner` 本节点 / `peer` 其它活节点 / `conflict` 双占用（要处理）/ `-` 无归属 |

```powershell
aicli-mesh ls --live --probe       # 只看活节点，并确认端口真的通
(aicli-mesh ls --json | ConvertFrom-Json).counts.live   # 脚本消费
```

**硬契约**：`--live` / `--workspace` 只裁剪 `nodes[]`，`counts` 恒为**全量口径**——
过滤过的视图不会假装网格更小，也不会「过滤掉一半冲突」。

## 4. 定位与地址：show / url

```powershell
aicli-mesh show session_20260924093535     # 端点/令牌提示/绑定/租约/日志尾部
aicli-mesh url  session_20260924093535     # 可直接打开的 web URL（不含令牌）
aicli-mesh url  session_20260924093535 --with-token   # 唯一令牌披露路径（别贴到会被转发的地方）
```

目标解析（第一个命中的胜出）：`pid:<PID>` → 节点 ID → 节点 ID 前缀 → 会话 ID → 会话 ID 前缀。
**歧义不猜**：多个匹配时退出码 2，并列出全部候选（含 `pid:` 提示）。

## 5. 跨进程调用：screen / send / call

```powershell
# 只读：读另一个进程的屏幕（不打扰它）
aicli-mesh screen session_20260924093535 --tail 20

# 写：让另一个进程跑一轮 prompt 并等它结束（必须显式 --allow-write）
aicli-mesh send session_20260924093535 "只回复两个字：收到" --allow-write
# → 收到

# 通用形式：op 白名单 9 项（只读 5 项 / 写 4 项），写 op 同样要 --allow-write
aicli-mesh call session_20260924093535 node.info --json
aicli-mesh call session_20260924093535 cancel --allow-write
```

成功信号：`send` 打印助手正文；`--json` 时 `status=ok`。
`busy` → 退出码 4（目标已有 invoke 在等），`refused` → 6，`not_found` → 2，`unreachable`/`timeout` → 3。

## 6. 实时与复盘：watch

```powershell
aicli-mesh watch --since 5m                # 回放最近 5 分钟，然后继续尾随（Ctrl-C 结束）
aicli-mesh watch --since 1h --once --json  # 脚本：只回放，跑完就退
```

`watch` 直接 tail `journal/*.ndjson`，**不依赖任何节点存活**：进程全退之后仍然能复盘
（这是它与 `GET /web/api/mesh/events` SSE 扇入的分工——后者需要双方都活着）。

## 7. 收尾：stop / gc

```powershell
# 优雅停止（目标必须自己开了 --mesh-allow-stop=true，否则 refused + 退出码 6）
aicli-mesh stop session_20260924093535

# 目标卡死 / 没开 --pprof：直接终止进程（无收尾，残留档案交给 gc）
aicli-mesh stop session_20260924093535 --force

# 清理：先看计划，再执行（存活进程的档案永不删）
aicli-mesh gc
aicli-mesh gc --apply --purge-legacy --prune-bindings
```

## 8. 一页速查

| 我想…… | 命令 |
|--------|------|
| 看全网格 | `aicli-mesh ls` / `aicli-mesh ls --live --probe` |
| 看某个目标的一切 | `aicli-mesh show <目标>` |
| 拿地址 | `aicli-mesh url <目标> [--with-token]` |
| 读屏幕 | `aicli-mesh screen <目标> --tail N` |
| 跑一轮 prompt | `aicli-mesh send <目标> "<prompt>" --allow-write` |
| 复用/拉起 + 窗口 URL | `aicli-mesh open <会话> [--bin PATH]` |
| 停节点 | `aicli-mesh stop <目标> [--force]` |
| 复盘 | `aicli-mesh watch [--since 1h --once --json]` |
| 清理 | `aicli-mesh gc [--apply]` |
| 体检 | `aicli-mesh doctor [--json]` |

下一步：改名部署 / 多套安装看 [spawn-and-binaries.md](./spawn-and-binaries.md)；
状态、租约与对账语义看 [lifecycle.md](./lifecycle.md)。
