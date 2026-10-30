# 17 aicli-gateway 在 gateway-edge 后面的公网接入与部署

> 文档状态：部署 runbook（2026-10-29 实测校准）
> 上游：[16 MCP 反向挂载穿透](./16-mcp-mount-tunnel.md)
> 对侧仓库：`E:\projects\ai\ai-gateway`（`cmd/gateway-edge`、`internal/edgeproxy`、`docs/ops/gateway-edge/*`）
> 目标机：`192.210.174.189`（Ubuntu 24.04 x86_64，域名 `ttai.online`，Cloudflare 前置 + DNS-01 证书）

---

## 0. 部署状态（2026-10-29 已上线并完成公网验收）

| 项 | 结果 |
|---|---|
| 目标主机 | `racknerd-933b75f` / Ubuntu / x86_64，`/opt/aicli-gateway`、`/opt/ai-gateway-edge` |
| 现有服务 | `aicli-gateway`（`127.0.0.1:8080`，`https://aicli.ttai.online`）、`ai-gateway-edge`（`:80/:443`，admin `127.0.0.1:19100`）、`ai-gateway`（`127.0.0.1:18443`） |
| Edge 版本 | 服务器二进制构建自 **`e710d1181`**（当前 ai-gateway HEAD，`v0.1.7-0.20261012230643-e710d1181212+dirty`），`configtest` 通过 → **未替换、未重启**（功能与本地构建一致，替换只会徒增 TLS 入口重启风险） |
| aicli-gateway | **v75 已上线**（2026-10-30T01:06:31Z，干净 release worktree 构建自 `843d7145` = `dc314b0d` + v75 控制台，`vcs.modified=false`）：`/opt/aicli-gateway/gateway`，SHA-256 `64e4a2da…f475`，回滚点 `gateway.before-20261030T010630Z`（v74）；`/api/health` 正常，控制台 `/` 与 `app.js?v=75` 已生效 |
| Edge 路由 | 既有规则已把 `aicli.ttai.online/*` 整站转发到 `aicli-console`(`127.0.0.1:8080`)，**无需改配置**；`/m/`、`/gw/agent` 天然随主机转发 |
| 公网验收 | 测试节点 `avcmh7sc` → `https://aicli.ttai.online/m/avcmh7sc/mcp`；`initialize` 200（Cloudflare `cf-ray` 可见）、`tools/list` 返回 `glob/grep/ls/view`、`tools/call ls` 读到服务器工作区 `hello.txt`；错令牌 401、未知 slug 404 |
| 清理 | 测试节点/设备令牌/挂载令牌/挂载记录全部撤销并清除（`mount-tokens.json` total=0、`mounts.json` 空），`live_nodes` 回到 **4**、`nodes=14`，与部署前基线一致 |

> 首次部署前（§1、§6 记录）代理不允许 22 端口，因此曾判定“无法部署”；随后代理放行 SSH，上述步骤全部真实执行。

### 0.1 当前实际部署口径（v68 校准）

| 项 | 当前事实 |
|---|---|
| 二进制 | `/opt/aicli-gateway/gateway`（服务单元 `aicli-gateway.service` 的 `ExecStart` 路径；不是历史文档里的 `/opt/aicli-gateway/bin/aicli-gateway`） |
| 数据目录 | `/etc/aicli-gateway`（`registry.json`、设备/挂载令牌、Web Push 等） |
| 控制台 | 站点根路径 `/`（`/console/` 是 SPA fallback，不是资源根；资源实际为 `/app.js`、`/style.css`） |
| 构建 | 主分支提交 `0605a46e`（v75）cherry-pick 到 v74 release build `dc314b0d` 的干净 worktree（`tmp/deploy/v75-head`，release build `843d7145`），`GOOS=linux GOARCH=amd64 CGO_ENABLED=0`，`-trimpath -ldflags "-s -w"`，二进制 `vcs.modified=false` |
| 部署时间 | 2026-10-30T01:06:31Z（`MainPID=2845553`） |
| 公网验收 | `/` 返回 `style.css?v=75` / `app.js?v=75`；`app.js?v=75` 200（343701 bytes，SHA-256 `7cea0b86…ac663`，与本地一致），含 `chat-stream-chunk` / 自适应纯文本流式 / 30fps rAF / 底部校准；`style.css?v=75` 含消息行与流式块两层 `content-visibility:auto`；`/api/health` 200，`live_nodes=6`、`nodes=6` |
| 回滚点 | `/opt/aicli-gateway/gateway.before-20261030T010630Z`（v74 SHA-256 `4cd06394…ec6f2`） |

> 运维注意：v70 部署脚本修正了 `curl | grep -q` 提前关管道导致的 curl 23 误判；健康与版本检查改为先落临时文件再 grep。
> v68 的首次探测过早、v69 的管道误判均已在后续脚本中修正。
> v73 首次构建虽标记 `f29f8730`，但工作区有未提交依赖改动（`vcs.modified=true`）；已改用干净 worktree 重建并覆盖部署，临时 v73 备份已删除，回滚点仍是 v72。
> v74 修复长会话流式卡顿：屏外消息行跳过布局、30fps rAF 合帧、长正文自适应纯文本追加、首屏延迟底部校准；400 行历史 + 29KB 实时正文实测 p95≈33ms、无 >50ms 长任务。
> v75 将长正文纯文本按行边界切成约 1.2KB 的独立流式块。桌面 400 行 + 29KB 实测 p95≈17ms、长任务 0；4× CPU 节流的 390×844 移动视口下 400 行 + 29KB 布局累计 0.78s（v74 为 9.9s）。短回复仍保留实时 Markdown，终稿始终按 Markdown 渲染。

## 1. 实测现状（本文所有结论的来源）

| 探测 | 结果 | 结论 |
|------|------|------|
| `GET https://ttai.online/health/ready` | `200`，`instance_id=racknerd-933b75f:18443`，`config.version=1574`，检查项含 `provider_groups`/`routes`/`background_job_state` | 根主机当前由 **ai-gateway 主服务**应答（不是 aicli-gateway） |
| `GET https://ttai.online/admin/edge/dev-tunnels` | `401` | Edge 管理面在同一主机、按路径路由，token 保护 |
| `GET/POST https://ttai.online/m/<slug>/mcp`（**部署前**） | `404`，`Content-Type: application/json; charset=utf-8`，`Content-Length: 155/156`，带 `access-control-allow-*` | 该 404 由**主网关**产生；当时挂载命名空间尚未部署（部署后同一路径变为 `401` + `WWW-Authenticate: Bearer realm="aicli-gateway-mount"`） |
| `https://gw.ttai.online` / `https://utc.ttai.online` | TLS 握手失败 | 通配主机路由**没有**为 aicli-gateway 配置 |
| 经代理 `127.0.0.1:10810` 探测 `192.210.174.189` 各端口（**旧代理**） | `443` TLS 成功；`80` 返回真实 HTTP 应答；`22/2222/…` 一律 `CONNECT 200` 后**立即 EOF**（含 `scripts/ssh-connect-proxy.py` 与 `connect.exe -H` 两种桥） | 旧代理只做 HTTP(S) 出口；换代理后 `22` 直接返回 `SSH-2.0-OpenSSH_9.6p1 Ubuntu`，部署得以执行（见 §6 的历史记录） |

Edge 的路由模型（读码确认，`internal/edgeproxy/config.go` + `configs/config.yaml`）：

```text
runtime.edge.edge_backend_groups[]: { name, backends: [url, ...] }
runtime.edge.edge_route_rules[]:    { name, match:{paths, path_prefixes, methods, hosts},
                                      backend_groups:[{name, weight}], group_strategy }
```
`internal/edgeproxy/classifier.go` 显式识别 WebSocket 升级请求（`Upgrade: websocket` → long-running），因此 `/gw/agent` 反向隧道可以经 Edge 建立；Edge 是**原样转发路径**的反向代理，不剥前缀，正好符合 16 号文档「网关剥自己的前缀」的前提。

---

## 2. 接入设计：内层 aicli-gateway + 外层 Edge

```text
外部 MCP 客户端 ──HTTPS──▶ ttai.online (edge)
                              │  edge_route_rules: /m/ , /gw/agent , /api/mounts
                              ▼
                        127.0.0.1:8080  aicli-gateway（loopback HTTP，无 TLS、无公网监听）
                              │  出站 wss（经 edge 的 /gw/agent）
                              ▼
                        用户机器上的 aicli-remote-tools 节点（挂载 MCP）
```

三条必须成立的约定：

1. **内层只绑回环**：`aicli-gateway --listen 127.0.0.1:8080`。公网入口唯一在 Edge。
2. **`--public-base-url https://aicli.ttai.online`**：REGISTER_ACK 回给节点的 `public_url` 必须是**公网**地址，否则节点操作员拿到的是 `127.0.0.1` 链接。
3. **Edge 对 `/m/` 不做自己的鉴权**：挂载令牌（`amt_`）由 aicli-gateway 校验；Edge 侧只需要不要把 `/m/` 落进 `deny_path_prefixes`，也不要把它塞进需要 admin token 的分组。

Edge 配置片段（追加到 `/opt/ai-gateway-edge/configs/config.yaml` 的 `runtime.edge` 下）：

```yaml
    edge_backend_groups:
      # …既有 default / web-control 等分组保持不动…
      - name: "aicli-gateway"
        backends:
          - "http://127.0.0.1:8080"

    edge_route_rules:
      # 挂载数据面 + 节点隧道 + 挂载管理面。三条路径都原样转发，不剥前缀。
      - name: "aicli-mcp-mounts"
        match:
          path_prefixes:
            - "/m/"            # 外部 MCP 客户端：POST /m/{slug}/mcp
            - "/gw/agent"      # 节点反向隧道（WebSocket 升级）
            - "/api/mounts"    # owner 会话下的挂载管理 API
            - "/api/health"    # 内层存活探针（可选，便于排障）
        backend_groups:
          - name: "aicli-gateway"
        group_strategy: "first_available"
```

注意 `/api/mounts` 是**精确前缀**，不要写成 `/api/`——后者会把主网关自己的 `/api/v1/...` 业务面抢走。

---

## 3. 内层服务：systemd 单元与环境

`/etc/systemd/system/aicli-gateway.service`：

```ini
[Unit]
Description=aicli-gateway (MCP mount gateway)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=aicli-gw
Group=aicli-gw
WorkingDirectory=/opt/aicli-gateway
ExecStart=/opt/aicli-gateway/bin/aicli-gateway \
  --config /opt/aicli-gateway/configs/gateway.yaml \
  --listen 127.0.0.1:8080 \
  --data-dir /opt/aicli-gateway/data \
  --public-base-url https://aicli.ttai.online
Restart=always
RestartSec=3
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
```

秘密走 `--config` flagfile（键名与 flag 同名，`gateway.yaml` 属 `0600`、属主为服务账号）——aicli-gateway **没有** `*-file` 形式的密钥参数，不要把密码写进 `ExecStart` 命令行（会进 `ps` 与 journald）：

```yaml
# /opt/aicli-gateway/configs/gateway.yaml
owner-password: "<控制台登录密码>"
device-token: "<节点注册用的 agw_ 设备令牌>"
rate-limit: 300
ban-threshold: 20
mount-token-ttl: 2160h        # 挂载令牌默认 90 天
mount-path-style: m           # /m/{slug}/mcp
proxy-max-request-body: 8388608
trusted-proxies: 127.0.0.1/32,::1/128
```

命令行 flag 优先于 flagfile，因此 unit 里的 `--listen/--data-dir/--public-base-url` 是显式事实来源；`configs/.env` 只放非秘密开关。

节点侧（用户机器）接入：

```powershell
$env:AICLI_GATEWAY_TOKEN = "<agw_ 设备令牌>"
$env:AICLI_REMOTE_TOKEN  = "<本机 MCP 的 Bearer，永不出机器>"
aicli-remote-tools serve --http --workspace D:\srv\app `
  --http-listen 127.0.0.1:18765 `
  --gateway-url https://aicli.ttai.online `          # 经 Edge 的 /gw/agent 建立隧道
  --gateway-self-name prod-readonly
# 输出：aicli-remote-tools: mount <slug> -> https://aicli.ttai.online/m/<slug>/mcp
```

节点注册时还会上报展示用快照：主机名、OS/arch、版本、实例、profile（含只读标记/工具前缀）、workspace、有效目录和工具清单。控制台「MCP 服务」卡片可直接展开检查 `工具（N）` 与 `目录（N）`，无需登录节点；这些标签只用于展示，不参与路由或授权（见 [16 §6.1](./16-mcp-mount-tunnel.md)）。

### 3.1 凭据只配一次：tokenFile（推荐，无需每次 export）

`aicli-remote-tools` 支持从 **0600 文件**读取两把令牌，优先级 **文件 > 环境变量**；文件缺失、为空或在 Unix 上组/其他用户可读都会**启动失败**（不会静默降级为“无令牌”）：

| 配置键（YAML） | 等价 flag | 内容 |
|---|---|---|
| `http.tokenFile` | `--http-token-file` | 本机 MCP 的 Bearer（`AICLI_REMOTE_TOKEN` 的值），永不出机器 |
| `gateway.tokenFile` | `--gateway-token-file` | 网关设备令牌 `agw_…`（控制台 `/api/devices` 领） |
| `gateway.mounts[].tokenFile` | `--mount ...,token_file=<f>` | 额外挂载各自的本地 Bearer |

**Linux（systemd，一次配置长期生效）**

```ini
# /etc/systemd/system/aicli-remote-tools.service
[Unit]
Description=aicli-remote-tools (MCP mount node)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=aicli
Group=aicli
WorkingDirectory=/srv/app
ExecStart=/usr/local/bin/aicli-remote-tools serve --config /etc/aicli-remote-tools/config.yaml
Restart=always
RestartSec=3
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=/var/lib/aicli-remote-tools

[Install]
WantedBy=multi-user.target
```

```yaml
# /etc/aicli-remote-tools/config.yaml（0644 即可，里面没有秘密）
workspace: /srv/app
http:
  listen: 127.0.0.1:18765
  path: /mcp
  tokenFile: /etc/aicli-remote-tools/mcp.token
gateway:
  url: https://aicli.ttai.online
  tokenFile: /etc/aicli-remote-tools/gateway.token
  identityFile: /var/lib/aicli-remote-tools/mount-id
  selfName: prod-readonly
  mounts:
    - name: k8s
      upstream: http://127.0.0.1:18766/mcp
      tokenFile: /etc/aicli-remote-tools/k8s.token
```

```bash
sudo install -d -m 700 -o aicli -g aicli /etc/aicli-remote-tools /var/lib/aicli-remote-tools
sudo sh -c 'printf "%s\n" "agw_…" > /etc/aicli-remote-tools/gateway.token'
sudo sh -c 'openssl rand -hex 32 > /etc/aicli-remote-tools/mcp.token'
sudo chown aicli:aicli /etc/aicli-remote-tools/*.token
sudo chmod 600 /etc/aicli-remote-tools/*.token
sudo systemctl daemon-reload && sudo systemctl enable --now aicli-remote-tools
```

**Windows（服务 / 计划任务）**：同样用 `--config` + `tokenFile`，令牌文件放在 `%ProgramData%\aicli-remote-tools\`，并用 ACL 只授予服务账号读取（Windows 不认 Unix 权限位，因此该检查只在 Unix 生效）。若用 NSSM 注册服务，把 `--config` 写进 Arguments 即可；不要再依赖用户级 `setx`（换用户/换会话就失效）。

**那客户端那把 `amt_` 呢？** 它属于消费方机器：`remote-targets.yaml` 里用 `token_env`（配合系统级环境变量/凭据管理器），或直接在 `mcp.yaml` 的 `headers.Authorization` 写死并限制文件 ACL。它和节点侧的这两把令牌是**两套独立凭据**，不要复用。

### 3.2 直接复用 aicli TUI 已配置的 gateway 连接

如果同一台机器上 aicli 的 `/gateway config` 已经配好（`gateway.url / token / proxy_url / name`），节点侧只需要一行开关，**不需要再写 url、也不一定需要 gateway token 文件**：

```yaml
# /etc/aicli-remote-tools/config.yaml
workspace: /srv/app
http:
  listen: 127.0.0.1:18765
  path: /mcp
  tokenFile: /etc/aicli-remote-tools/mcp.token
gateway:
  useAICLIConfig: true          # 复用 TUI 的 gateway.*
  identityFile: /var/lib/aicli-remote-tools/mount-id
  selfName: prod-readonly
```

```bash
# 等价命令行（不传 --gateway-url / --gateway-token-*）
aicli-remote-tools serve --http --workspace /srv/app \
  --http-token-file /etc/aicli-remote-tools/mcp.token \
  --gateway-from-aicli --gateway-self-name prod-readonly \
  --gateway-identity-file /var/lib/aicli-remote-tools/mount-id
```

- 分层搜索与 aicli 完全一致（项目 `.aicli/config.yaml` → `~/.aicli/config.yaml`）；需要指定文件时用 `--gateway-aicli-config /path/config.yaml`。
- `proxy_url` 也会被复用，所以“TUI 能通过本地代理连上网关”的机器，节点同样能连。
- 优先级（高 → 低）：`AICLI_GATEWAY_*` 环境变量 → 节点显式字段/flag → 节点 `tokenFile/tokenEnv` → aicli 的 `gateway.token` → aicli 的 `url/name/proxy_url`（完整表见 [16 §8.5](./16-mcp-mount-tunnel.md)）。
- 配置在 `EnableGateway` 时读一次；改完 TUI 配置后重启节点即可生效（不重启不会偷偷换连接，避免半路换网关）。

**实测（2026-10-29，服务器上模拟 TUI 写出的 config.yaml）**：

```text
aicli config: gateway.url=http://127.0.0.1:8080 token=*** name="from-tui"
启动命令只带 --gateway-from-aicli --gateway-aicli-config ...（无 --gateway-url / 无 AICLI_GATEWAY_*）
→ aicli-remote-tools: mount 157hclvf -> https://aicli.ttai.online/m/157hclvf/mcp
→ initialize 200 text/event-stream（serverInfo toolkit-mcp）
→ 清理后 /api/mounts 为空、无残留进程
```

### 3.3 极简启动：只跑 `aicli-remote-tools serve`

```powershell
cd D:\work
aicli-remote-tools serve
```

首跑会自动完成（无需 `init`、无需任何 flag）：

1. 无传输参数 → `--http`；
2. workspace = 当前目录；
3. 生成 `~/.aicli/remote-tools/mcp.token`（0600，已有则保留）；
4. 检测到 aicli 的 `gateway.*` → 自动复用（控制台里会立即出现节点与挂载）；
5. 生成 `~/.aicli/remote-tools/config.yaml`（已有则不覆盖）；
6. 有网关时端口用 **127.0.0.1:0（随机）**，因为挂载按实际绑定地址转发；日志会打印真实端口。本地模式才固定 `18765`。

关闭自动化：`--no-auto-init`（不建配置/令牌）、`--no-gateway`（只本地、不连云网关）。显式建配置仍可用 `aicli-remote-tools init`（输出可审阅、支持 `--force`）。

配置发现顺序与 aicli 一致：**当前目录 `.aicli/remote-tools/config.yaml` → 用户目录 `~/.aicli/remote-tools/config.yaml`**；`--config` 显式指定最优先。

| 参数 | 现在的默认值 | 说明 |
|---|---|---|
| 传输 | **http**（省略 `--http/--stdio` 时） | `--stdio` 仍必须显式（SSH 场景） |
| `--workspace` | **当前目录** | envd 后端仍要求显式绝对路径 |
| `--http-listen` / `--http-path` | 本地模式 `127.0.0.1:18765`；有网关时 `127.0.0.1:0`（随机）/ `/mcp` | 日志打印真实端口 |
| `--http-token-file` | `~/.aicli/remote-tools/mcp.token`（**首跑自动创建**） | 已有文件永不轮换；`--no-auto-init` 可关闭 |
| `--gateway-identity-file` | `~/.aicli/data/gateway-agent/mount-id` | 一般不用传 |
| `--instance <id>` | 未指定时按 machine ID + workspace 派生 `auto-<hostname>-<8hex>` | 一键区分：派生 `.../gateway-agent/instances/<id>/mount-id`，selfName/节点名默认 `<id>`；**并发实例必须不同** |
| `--gateway-self-name` | `self` | 改公网 slug 时用 `--gateway-self-slug` |
| `--profile` | `all` | 默认暴露全部工具；最小权限用 `readonly` |

自动初始化的三个安全约定：**已有 config 不覆盖**、**已有 token 永不轮换**（避免让所有客户端失效）、生成的配置里**不落任何秘密**（令牌只存在于 0600 文件）。

**多实例：默认自动，按需指定**：

```powershell
aicli-remote-tools.exe serve --instance backend  --workspace E:\...\backend
aicli-remote-tools.exe serve --instance frontend --workspace E:\...\frontend
```

- **不传 `--instance` 也能区分**：新 workspace 默认用 `auto-<hostname>-<sha256(machine ID + workspace绝对路径)[:4] hex>`，identity 在 `instances/auto-…/mount-id`；无需任何输入。
- **A/B 同 `/home`**：两台机器的 machine-id/MachineGuid 不同，hash、instance、identity 目录和 slug 都不同；即使 `/home` 是共享盘也不会共用 identity。若两台机器显式用**同一个** `--instance`，第二个进程会被 `mount-id.lock` 拒绝并提示 `--instance`。
- **hostname 变化**：auto ID 里的 hostname 会变，从而换 URL；需要长期固定时用 `--instance` / `--gateway-self-slug` pin。
- **升级兼容**：如果旧的共享 `~/.aicli/data/gateway-agent/mount-id` 存在，第一个启动的 machine+workspace 通过 `legacy-workspace` JSON 标记继承它（现有 `5n9qmef8` 保留原 URL），之后启动的 machine/workspace 自动使用 hash；旧版单行 marker 会自动升级为 JSON。
- **重复检查**：同一 identity 被第二个 `serve` 占用时，gateway 会 disabled，并打印 `mount identity ... already in use ... start this service with --instance <id>`；本地 HTTP 仍可用，避免直接崩溃。不同 machine/workspace 误绑定同一 auto 目录时同样报错。
- `--instance <id>` 等价于 `--gateway-identity-file ~/.aicli/data/gateway-agent/instances/<id>/mount-id --gateway-self-name <id> --gateway-name <id>`；不写 YAML 也能区分。
- 也可写进配置（适合 systemd/计划任务）：`gateway: { instance: backend }`，然后 `serve --config ...` 不再需要命令行参数。
- 每个**并发** `serve` 进程必须用不同 instance；共享 identity 会让两个进程在每次 REGISTER 时互相把对方的挂载标成 stale。
- 本地监听默认 `127.0.0.1:0`；若 config 写死了 `18765`，第二个实例加 `--http-listen 127.0.0.1:0`。本地 `mcp.token` 默认共享；要隔离用 `--http-token-file`。
- **迁移注意**：现有默认实例（mount name=`self`）会在第一次启动时通过 `legacy-workspace` 继承旧 identity，URL 不变；如果该标记已被别的 machine/workspace 占用，才会改用 auto hash。想给老实例换标签但保留 URL：`--instance <id> --gateway-identity-file <原 mount-id> --gateway-self-name self`（控制台节点名可另用 `--gateway-name <id>`）；只复制 `mount-id` 但让 `selfName` 变成 `<id>` 仍会换 URL。

**本机实测（Windows，`serve` 无任何参数）**：

```text
cd <workspace>; serve   → no transport flag; defaulting to --http
                        → token file ...\mcp.token: created
                        → gateway auto-enabled from aicli config
                        → wrote config ...\config.yaml
                        → http transport on 127.0.0.1:56553/mcp workspace=<cwd>
                        → mount 5n9qmef8 -> https://aicli.ttai.online/m/5n9qmef8/mcp
                        → 本地令牌 401（无）/ initialize 200（有）
清理：挂载/节点已删除，mounts 为空
```

**多实例实测（`--instance`）**：

```text
serve --instance ui-check --workspace <temp>
  → banner: instance=ui-check
  → identity: ~/.aicli/data/gateway-agent/instances/ui-check/mount-id (33 B)
  → mount ke1ctau7 -> https://aicli.ttai.online/m/ke1ctau7/mcp
  → 默认实例 5n9qmef8 同时在线，token=ggg 未受影响
停止测试实例 → ke1ctau7 stale → 删除挂载/节点 → remaining ui-check = 0
```

**默认自动实例实测（不传 `--instance`）**：

```text
serve --workspace C:\...\rt-auto-check2
  → instance: auto-thinkbook14-c5209673（hostname + machine/workspace hash）
  → workspace metadata: {"machine":"2b746ae9...","workspace":"c:\\users\\...\\rt-auto-check2"}
  → mount 6kos796v -> https://aicli.ttai.online/m/6kos796v/mcp
  → 默认实例 5n9qmef8 同时在线
同 workspace 再起一个进程（仍不传 --instance）
  → gateway disabled: mount identity "...\auto-thinkbook14-c5209673\mount-id"
    is already in use by another remote-tools process;
    start this service with --instance <id> to get a distinct identity
清理 → 6kos796v / node 删除 → instances/auto-thinkbook14-c5209673 删除
```

### 3.4 控制台：MCP 服务页（令牌管理）+ 日志页 + 会话动作收纳

**MCP 服务页**（侧栏「MCP 服务」）：读取 `/api/mounts`，逐行展示名称、`/m/{slug}/mcp`、公网地址、节点、本地路径、令牌数与最近心跳，并用状态色区分 **在线 / 离线 / 已禁用**。注册快照还会显示 **主机名 · OS/arch、profile（含只读标记/工具前缀）、workspace、版本·实例·backend**；`工具（N）` 与 `目录（N）` 可展开检查。旧节点缺少这些字段时显示 `—`，不会用本地路径猜测。动作：

- **启用 / 禁用** → `POST /api/mounts/{slug}/enable|disable`（禁用后公网入口立即 403，节点与地址保留）；
- **复制地址**（一次点击，含 toast 反馈）；
- **令牌** → 打开该挂载的令牌管理（见下）；
- **日志** → 跳到 `#logs?slug={slug}`，自动只显示这个挂载的调用；
- **删除**（仅离线挂载可用，避免误删在线入口）。

**MCP 令牌管理**（每张挂载卡片的「令牌」按钮）：

- 列表展示该挂载的令牌投影：`name / prefix / 有效|已撤销|已过期 / 签发时间 / 最近使用 / 到期 / 作用域`；**完整令牌永远不回显**（网关只存 SHA-256）。
- 签发：填写调用方名称 + 有效期。有效期可选 **30 / 90 / 365 天、指定天数（1–3650）、不过期**；选「指定天数」时出现天数输入框，选「不过期」时显示“不会自动失效，泄露后只能手动撤销”的警告。响应中的 `amt_…` **只在弹窗显示一次**，带复制按钮；落盘只有 prefix + hash。API 用 `never_expires: true`（或 `ttl_seconds: -1`）表示不过期，省略 `ttl_seconds` 仍按默认 90 天。
- 撤销：下一次请求立即 401；撤销后才能「删除记录」（两段式，避免静默注销）。
- `GET /api/mounts/{slug}/tokens` 已按挂载过滤；`DELETE` 会校验令牌确实属于该 slug，不能用 A 挂载的 URL 撤销 B 挂载的令牌。

**日志页**（侧栏「日志」）：

- **MCP 调用**：读取 `GET /api/mounts/logs` 的**内存环形缓冲**（最近 1000 条，重启清空）。包含 2xx/4xx/5xx 全量调用：`status / slug / method / path / token_id / client_ip / duration / reason`，以及解析出的 `command`（MCP JSON-RPC 方法，如 `tools/call:read_file`）、调用方 `user_agent`、请求/返回体大小 `request_bytes` / `response_bytes`（请求体仅在带 Content-Length 且低于 1 MiB  inspect 上限时解析命令，否则只记声明长度）；支持按挂载、状态过滤，默认 5s 自动刷新（可关）。
- **审计事件**：读取 `GET /api/audit`（持久化 `audit.log`，只记 4xx/5xx 与安全事件），支持 `type` 前缀（`mount`/`tunnel`/`login`/`device`/`rate`）与 `q` 关键词过滤；同样可自动刷新。
- **边界**：控制台展示的是网关自己的审计 + MCP 调用环，不是任意 journald 进程日志；进程级排障仍用 `journalctl -u aicli-gateway`，两者在 §5 的排障表中并列。

该视图惰性加载：只有打开时才请求 `/api/mounts`、`/api/mounts/logs` 或 `/api/audit`；离开日志页即停止轮询。SSE 节点快照变化时若 MCP 页或日志页正打开会自动刷新挂载列表。

**会话卡片动作收纳**：卡片只保留 `打开` 与 `⋯`（以及按需出现的审批/展开按钮），详情、会话、新终端、恢复、关窗口、移除全部收进 `⋯` 弹出菜单；点击空白处或 `Esc` 关闭，`aria-expanded` 同步，小屏不再出现一排按钮。

**`serve --http` 启动横幅**：绑定成功后打印 endpoint（含 `:0` 的真实端口）、workspace、工具数量与清单、**凭据来源**（文件路径或环境变量名，绝不打印令牌值）以及 `aicli chat --remote` 示例；无令牌时额外给出回环警告。shell 可见但 allowlist 为空时，横幅会打印 `shell.allowedCommands` / `shell.allowedCommandPatterns` 的最小配置示例和文档链接，而不是只说 “every command refused”；`doctor` 同时输出 `shell visible`、allow/deny commands/patterns、`denyShellMetachars`、`envAllowlist` 与 `commandTimeout`。

**发布必须同 bump 资源版本**：console 的 `index.html`、`app.js` 的 `sw.js?v=N` 与 `sw.js` 的 `CACHE/SHELL` 是同一套手动版本（本次 **v64**）。Cloudflare 对 `*.js/*.css` 有 4 小时边缘缓存，不 bump 就会出现“部署成功但页面还是旧按钮”。`cmd/gateway/console_assets_test.go` 已加一致性断言，版本漂移会让测试失败。

**真机视觉验收（390×844 移动视口，2026-10-29）**：

```text
MCP 服务页：title=MCP 服务 · summary="1 个 · 在线 1" · card=self
           动作 = 复制地址 / 令牌 / 日志 / 禁用
令牌弹窗：title=MCP 令牌 · self；空态=还没有令牌
         签发 ui-check → 明文 amt_… 只出现一次（长度 47，未回显）
         列表：有效 · prefix=amt_H-dm9NXv · 签发/到期时间 · [撤销]
         挂载计数 0 → 1 → 撤销 → 0；删除记录后回到空态
         关闭弹窗后 #mount-token-reveal hidden=true、innerHTML=""（明文不留在 DOM）
有效期：never → 列表 meta=不过期 + 警告可见；custom 7 天 → 到期=2026/11/5
       已有令牌保留未动（listBefore=listAfter=1，只清理 ui-never / ui-7d）
日志页：MCP 调用=2 条 200（slug 5n9qmef8，POST /mcp，含 token_id/duration）
       状态=errors → “没有匹配的调用记录”
       审计=9 条 mount 事件，chip=mount/token_revoked；q=5n9qmef8 过滤生效
       自动刷新开→关→开；tabs=MCP 调用/审计事件
移动端：390×844 无横向滚动；令牌弹窗 358×464 完整可见；日志 time 换行到第二行
       侧栏 8 项含「日志」，点导航后自动收起
会话页：8 张卡片；首卡内联 = 回答 / 展开 / 打开 / ⋯；Escape 关闭菜单
```

**注册元数据验收（v64，2026-10-29，公网控制台）**：

```text
MCP 卡片：auto-thinkbook14-6d0aeecb / self 均在线
主机    ：thinkbook14 · windows/amd64
Profile ：all
工作区  ：E:\projects\itsm\itsm / E:\projects\ai\ai-agent-runtime\backend
版本    ：0.1.0 · 实例 auto-thinkbook14-6d0aeecb · local（legacy self 无实例）
工具    ：工具（9）展开后 = append_write/apply_patch/edit/glob/grep/ls/shell/view/write
目录    ：目录（1）展开后 = 各自 workspace
布局    ：390×844 无横向滚动；展开动作可点击
落盘    ：mounts.json 含 hostname/tool_count/tools/roots；API 401 边界不变
```

---

## 4. 构建与部署步骤

```powershell
# 本地（Windows）交叉编译
cd E:\projects\ai\ai-agent-runtime\backend
$env:GOOS="linux"; $env:GOARCH="amd64"; $env:CGO_ENABLED="0"
go build -p 1 -trimpath -ldflags "-s -w" -o ..\tmp\deploy\mount-p0\aicli-gateway .\cmd\gateway
go build -p 1 -trimpath -ldflags "-s -w" -o ..\tmp\deploy\mount-p0\aicli-remote-tools .\cmd\aicli-remote-tools
cd E:\projects\ai\ai-gateway
go build -p 1 -trimpath -ldflags "-s -w" -o E:\projects\ai\ai-agent-runtime\tmp\deploy\mount-p0\gateway-edge .\cmd\gateway-edge
```

产物与本轮构建哈希（重建会变，以 `Get-FileHash` 为准）：

| 产物 | 大小 | SHA-256 |
|------|------|---------|
| `aicli-gateway` | 13,033,632 | `3f955754a41c58004152ab6247c8963e5e685f1ee3108267a23c1c7674d5e56e` |
| `gateway-edge` | 21,950,624 | `de9d4cc4d55bf2b4c1f6590364cf966fdb865792e9e4a0b36c60a118521b4fb0` |
| `aicli-remote-tools` | 12,394,656 | `569abfa215144c85f0e2c37a5bbc8a2706ac79185661271c062e3168e7d0951f` |

上传 + 部署（**需要可用的 SSH/等价通道，见 §6**）：

```bash
# 服务器上（stage 目录为上传落点）
bash deploy-aicli-gateway-edge.sh --target both --stage-dir /root/stage --dry-run   # 先校验
bash deploy-aicli-gateway-edge.sh --target both --stage-dir /root/stage             # 实部署
bash deploy-aicli-gateway-edge.sh --target aicli --rollback                          # 需要时回滚
```

脚本行为（对齐 `ai-gateway` 的 runbook 约定）：`.next` 暂存 → SHA-256 必须匹配 → Edge 走 `configtest`（先 `set -a; . configs/.env`）→ `cp -p` 备份 `*.before-<UTC>` → `mv` 原子替换 → `systemctl restart` → 健康探针重试 → **失败自动回滚**。服务/目录不存在时直接报错退出，不会顺手创建或覆盖别的东西。

---

## 5. 验收清单

```bash
# 1) 内层活着（SSH 内）
curl -fsS http://127.0.0.1:8080/api/health                       # {"status":"ok","nodes":N,...}

# 2) Edge 路由通（公网）
curl -sS https://aicli.ttai.online/api/health                    # 内层 JSON，不是主网关的 404
curl -sS -o /dev/null -w '%{http_code}\n' \
  -X POST https://aicli.ttai.online/m/00000000/mcp `#` -H 'Accept: application/json, text/event-stream'
#   期望 401（先鉴权后查表：未知 slug 也必须 401，不给枚举 oracle）

# 3) 节点经 Edge 建隧道（WebSocket 升级是否被 Edge 放行）
#     节点日志出现 “mount <slug> -> https://aicli.ttai.online/m/<slug>/mcp” 即代表 REGISTER_ACK 正常
#     若这里失败而本地直连内层成功，问题定位在 Edge 的 upgrade 透传

# 4) 挂载调用
curl -sS -X POST https://aicli.ttai.online/m/<slug>/mcp \
  -H "Authorization: Bearer amt_<token>" \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"curl","version":"0"}}}'
#   期望 200 + serverInfo；随后 tools/list、tools/call

# 5) 治理面
GET  /api/mounts                    # 列表含 public_url、tokens 计数
POST /api/mounts/<slug>/tokens      # 明文只返回一次；落盘只有 prefix + sha256
GET  /api/mounts/<slug>/tokens      # 该挂载的令牌投影（prefix / 时间 / revoked）
DELETE /api/mounts/<slug>/tokens/<id>   # 撤销后下一次调用即 401
GET  /api/mounts/logs?slug=&limit= # 最近挂载调用（内存环形，含 2xx）
GET  /api/audit?limit=&type=&q=     # 审计事件（type 前缀 + 关键词）
POST /api/mounts/<slug>/disable     # 急停：403 mount_disabled

# 6) 指标
curl -sS http://127.0.0.1:8080/metrics | grep aicli_gateway_mount_
```

Windows 本地已通过的等价用例见 16 号文档 §17.4（`initialize`/`tools/list`/`tools/call` + 401/403/404/502/429 矩阵）。

---

## 6. 访问通道阻塞记录（历史，已被新代理解除）

> **2026-10-29 更新**：代理更换后 `22` 端口可用，§4 的部署与 §5 的验收均已真实执行（结果见 §0）。
> 以下内容保留为「为什么第一次判定不可部署」的证据，供下次判断代理能力时复用。

**（当时）结论：给定凭据与该代理下无法登录服务器，因此“上传 + 替换二进制 + 重启 + 公网验证”没有执行。** 不是没做，是被网络事实挡住：

- `127.0.0.1:10810` 对 `192.210.174.189:22`（以及 `2222/8022/8443/9022/2022/2200`）的 `CONNECT` 都回 `200 Connection established`，但**随后立即 EOF**；对 `80/443` 才真正打通（443 TLS 成功、80 拿到真实 HTTP 应答）。
- 同一代理下用仓库自带的 `scripts/ssh-connect-proxy.py`（带 `Proxy-Connection: keep-alive`）与 `connect.exe -H` 两种桥接方式，OpenSSH 均报 `Connection closed by UNKNOWN port 65535`。
- 密码认证需要交互或 `sshpass`（本机无 `sshpass`，且 `BatchMode` 明确不允许隐藏窗口吃密码）；即便能喂密码，隧道本身也起不来。

可选解法（任选其一即可让本文 §4/§5 直接跑通）：

1. 放开代理对该目标 `22` 端口的 CONNECT 出站（或给我另一个允许 SSH 的代理地址/端口）。
2. 服务器侧把 sshd 挂到已确认可达的 `443`/`80` 之外的可代理端口（例如 `sshd` 第二监听 `8443`，但前提同样是代理放行该端口）。
3. 由你在能连通的网络里执行 §4 的两条命令（脚本已就绪，含校验、备份、原子替换、健康检查与自动回滚）。
4. 走 Edge 管理面：`/admin/config/reload` 与 backend-group reload 接口**可以**在不重启的前提下加/改路由规则，但它需要 admin token，且**不能**替我们安装内层二进制——只能作为 §2 路由部分的补充通道，不能替代部署。

拿到可用通道后，我会按 §4 部署、按 §5 验收，并把实测输出写回本节。
