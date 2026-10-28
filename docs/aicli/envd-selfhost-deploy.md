# envd 自托管部署与运维手册（Linux 目标机）

> 适用：ai-agent-runtime v0.5.x 的 `envd://` 传输。
> 本文记录 2026-10-28 在 Ubuntu 24.04.2 LTS（x86_64）上的实测部署，含访问令牌、防火墙加固与回滚步骤。
> 设计与协议细节见 `docs/architecture/envd-implementation-report.md`。

---

## 1. 组件与调用链

```text
本机 aicli / agent
  │  MCP over stdio
  ▼
aicli-envd-bridge（本机进程：MCP ↔ Connect-RPC）
  │  HTTP Basic user + X-Access-Token
  ▼
envd :49983（目标机，systemd 守护，root 运行）
  └─ 以 Basic user 身份 fork 进程 / 文件操作 / metrics
```

- **envd 只装在目标机**；本机只需要 `aicli-envd-bridge`。
- envd 不做路径围栏与命令过滤：路径 jail、profile、输出上限由 bridge 在本地执行。
- 令牌（`X-Access-Token`）只在 `/init` 安装后生效；未安装时端口是**无鉴权**的。

## 2. 已验证产物（2026-10-28）

| 项 | 值 |
|---|---|
| envd 版本 | `0.2.11`（`envd -version`） |
| 源码 | `github.com/e2b-dev/runtime` 提交 `fcf15acb`（2025-09-04，距沙箱实测版本一天） |
| 构建 | go1.27.1，`CGO_ENABLED=0`，`GOOS=linux GOARCH=amd64`，静态 ELF |
| sha256 | 以实际产物为准（本次：`1688024337fbe8106b1e317fd6b2219564c8642cd3f5fd755bdbe5fef183fb67`） |

> 沙箱内 buildinfo 的 `862d9d00…` 提交在公开仓库不存在（当时是 `+dirty` 构建），不能用于复现。

### 交叉编译（本机）

```bash
# 取指定日期的公开提交（浅历史即可）
git init e2b-runtime && cd e2b-runtime
git remote add origin https://github.com/e2b-dev/runtime
git fetch --shallow-since=2025-08-20 origin main
git checkout fcf15acb

# 整仓 go.work 会因 ugorji 双模块路径冲突；只保留 envd + shared
cat > go.work <<'EOF'
go 1.24.3

use (
	./packages/envd
	./packages/shared
)
EOF

cd ..
# 本地加固补丁：默认 info 日志、process_start 不记录 argv、stdout/stderr 与
# 请求体不落 journal（详见 §4“日志”）
git apply /path/to/ai-agent-runtime/scripts/envd/patches/0001-redact-process-logs.patch

cd packages/envd
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
GOPROXY=https://goproxy.cn,direct GOSUMDB=off \
go build -trimpath -ldflags "-s -w -X main.commitSHA=fcf15acb+logsafe+tls" -o envd .
```

> 网络受限环境可把 `GOPROXY` 换成可达的镜像；`goproxy.cn` 在本次两类环境均可达。
> 本地补丁同时提供 `-tls-cert` / `-tls-key`（见 §3 TLS），以及日志脱敏（见 §4）。

## 3. 目标机安装（root）

需要落地 5 个文件：

| 文件 | 作用 |
|---|---|
| `/usr/local/bin/envd` | 守护进程 |
| `/etc/envd/envd.env` | `ENVD_ACCESS_TOKEN=…`（600，root-only） |
| `/usr/local/sbin/envd-init-token.sh` | 启动后经 `/init` 安装令牌（幂等，失败不阻塞主进程） |
| `/usr/local/sbin/envd-firewall.sh` | 只放行回环 + 运维工作站到 49983（幂等） |
| `/etc/systemd/system/envd.service` | systemd unit |

`envd.service` 关键内容：

```ini
[Service]
Type=simple
EnvironmentFile=/etc/envd/envd.env
Environment=ENVD_LOG_LEVEL=info
ExecStartPre=/usr/local/sbin/envd-firewall.sh
ExecStart=/usr/local/bin/envd -isnotfc -port 49983 -tls-cert /etc/envd/tls.crt -tls-key /etc/envd/tls.key
ExecStartPost=-/usr/local/sbin/envd-init-token.sh
Restart=always
RestartSec=2
LimitNOFILE=1000000
```

### TLS 证书（推荐）

```bash
mkdir -p /etc/envd && chmod 700 /etc/envd
openssl req -x509 -newkey rsa:2048 -sha256 -days 825 -nodes \
  -keyout /etc/envd/tls.key -out /etc/envd/tls.crt \
  -subj "/CN=envd" \
  -addext "subjectAltName=IP:127.0.0.1,IP:<target-ip>,DNS:localhost"
chmod 600 /etc/envd/tls.key
chmod 644 /etc/envd/tls.crt
```

- SAN 必须覆盖客户端实际连接的地址（IP 直连就写 IP，回环健康检查需要 `127.0.0.1`）。
- 客户端把 `tls.crt` 作为 CA（`--ca-file` / `?ca_file=` / `remote.tls.caFile`）。
- 证书轮换：替换两文件后 `systemctl restart envd`，并更新客户端 CA。

安装与启动：

```bash
install -m 755 envd /usr/local/bin/envd
mkdir -p /etc/envd && chmod 700 /etc/envd
install -m 600 -o root -g root envd.env /etc/envd/envd.env
install -m 755 -o root -g root envd-init-token.sh /usr/local/sbin/
install -m 755 -o root -g root envd-firewall.sh /usr/local/sbin/
install -m 644 -o root -g root envd.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now envd
```

### 令牌引导（首次与每次重启）

- `/init` 请求：`POST https://127.0.0.1:49983/init`（`--cacert /etc/envd/tls.crt`），body `{"accessToken":"<token>"}`；成功返回 **204**。
- 令牌只在内存中；重启后由 `ExecStartPost` 自动重装。若返回 **409**，说明进程里已有不同令牌，重启 envd 即可重置（令牌归零 → 重新引导）。
- 若希望换新令牌：改 `/etc/envd/envd.env` → `systemctl restart envd`。

`/usr/local/sbin/envd-init-token.sh` 参考实现：

```sh
#!/bin/sh
# Apply the envd access token after start; envd keeps it in memory only, so every
# restart needs this call before the port is considered protected.
set -u
. /etc/envd/envd.env
i=0
while [ $i -lt 30 ]; do
  code=$(curl -s --cacert /etc/envd/tls.crt -o /dev/null -w '%{http_code}' -X POST https://127.0.0.1:49983/init \
    -H 'Content-Type: application/json' -d "{\"accessToken\":\"${ENVD_ACCESS_TOKEN}\"}")
  case "$code" in
    204) echo "envd: access token applied"; exit 0 ;;
    409) echo "envd: a different access token is already installed; restart envd to reset" >&2; exit 1 ;;
  esac
  i=$((i+1))
  sleep 1
done
echo "envd: could not apply the access token" >&2
exit 1
```

### 验证矩阵

```bash
ss -ltn | grep 49983                                   # 监听
curl -s --cacert /etc/envd/tls.crt -o /dev/null -w '%{http_code}\n' https://127.0.0.1:49983/health   # 204
curl -s --cacert /etc/envd/tls.crt -o /dev/null -w '%{http_code}\n' https://127.0.0.1:49983/metrics  # 401（未带令牌）
. /etc/envd/envd.env
curl -s --cacert /etc/envd/tls.crt -o /dev/null -w '%{http_code}\n' -H "X-Access-Token: $ENVD_ACCESS_TOKEN" \
  https://127.0.0.1:49983/metrics                                                                     # 200
```

## 4. 安全模型与加固

| 层 | 现状 | 建议 |
|---|---|---|
| 网络 | 仅放行回环 + 运维工作站（`iptables` 由 unit 幂等应用） | 换工作站/网段时编辑 `ALLOW` 列表并重启 unit；也可改用 SSH 隧道 |
| 传输 | HTTPS + HTTP/2（本地补丁的 `-tls-cert/-tls-key`） | 自签证书时必须分发 CA；证书 SAN 要覆盖实际连接地址 |
| 令牌 | `X-Access-Token` 头（TLS 内传输） | 按需轮换；不要写进 spec，使用环境变量 |
| 运行身份 | envd 以 root 运行，Basic user 决定命令身份 | 需要可用 root 启动；可选实验：以普通用户运行（未验证） |
| 日志 | 默认 `info` + 本地脱敏补丁：`process_start` 只记录可执行文件与参数个数，stdout/stderr、请求体不落 journal | 排障时才临时设 `ENVD_LOG_LEVEL=debug`（会记录参数与输出，含文件内容，慎用） |
| 审计/命令策略 | bridge 只有 jail/profile/输出上限 | 见报告 §8.3；按需补 audit 与命令白名单 |

防火墙脚本（节选）：

```sh
PORT=49983
ALLOW="<operator-ip>"   # 可放多个，空格分隔
iptables -C INPUT -i lo -p tcp --dport "$PORT" -j ACCEPT 2>/dev/null \
  || iptables -I INPUT 1 -i lo -p tcp --dport "$PORT" -j ACCEPT
for ip in $ALLOW; do
  iptables -C INPUT -p tcp --dport "$PORT" -s "$ip" -j ACCEPT 2>/dev/null \
    || iptables -I INPUT 2 -p tcp --dport "$PORT" -s "$ip" -j ACCEPT
done
iptables -C INPUT -p tcp --dport "$PORT" -j DROP 2>/dev/null \
  || iptables -A INPUT -p tcp --dport "$PORT" -j DROP
```

> 验证方法：临时删除放行规则后从工作站访问应超时（`000`）；恢复后应返回 `401`（未带令牌）。

## 5. 本机（客户端）接入

```bash
# 令牌：从目标机 /etc/envd/envd.env 读取，或自行保存
export AICLI_ENVD_TOKEN='<token>'

# 单串目标：workspace 必须是绝对路径；binary 指向本机 bridge；ca_file 指向目标机 tls.crt
aicli chat --remote \
  'envd://n9e@10.0.0.9:49983/home/n9e?binary=C:\tools\aicli-envd-bridge.exe&ca_file=C:\tools\envd-ca.crt&call_timeout=30m#ops' \
  --prompt "在远端执行 uptime"
```

等价的 `remote-targets.yaml`（项目 `.aicli/remote-targets.yaml` 或家目录）：

```yaml
remoteTargets:
  n9e:
    transport: envd
    ssh:
      host: 10.0.0.9
      user: n9e
      port: 49983
    remote:
      binary: C:\tools\aicli-envd-bridge.exe
      workspace: /home/n9e
      profile: ops
      toolPrefix: remote_10-0-0-9
      tokenEnv: AICLI_ENVD_TOKEN
      callTimeout: 30m
      tls:
        caFile: C:\tools\envd-ca.crt
```

- 工具名前缀：`<toolPrefix或remote_<host>>__<tool>`，例如 `remote_10-0-0-9__view`。
- 每个工具调用的默认上限是 **10 分钟**；长任务用 `?call_timeout=30m` 或 `remote.callTimeout` 覆盖。
- `write` 单次内容硬上限 **1 MiB**（argv 限制）；更大文件请分片或走 `shell`。
- `SendInput` / `SendSignal` / `UpdatePty` 是 unary RPC，HTTP/1.1 也可用；TLS 只用于加密与 HTTP/2。

### 5.1 低成本巡检（推荐）

把确定性采集固化成目标机脚本，agent 只做分析与写报告：

1. **部署（一次）**：用 `remote_<prefix>__write` 把 `scripts/envd/host-inspect.sh` 写入
   `.local/bin/host-inspect.sh`，再用 `remote_<prefix>__shell` 执行
   `chmod +x ~/.local/bin/host-inspect.sh`。
2. **每次巡检**：单次 `remote_<prefix>__shell` 调用 `~/.local/bin/host-inspect.sh`，
   把固定格式输出交给模型总结；建议配合
   `--reasoning-effort low --budget-tokens 20000`。
3. **效果**：实测单次调用返回约 3–10 KiB（取决于是否含 syslog 摘要），
   相比自由编排（一次巡检约 102 万 tokens）可降一个数量级。

脚本只读：不修改服务、不清理日志；`syslog` 不可读时回退 `sg adm`。

### 5.2 审计日志（opt-in）

```bash
# 单串写法
envd://n9e@10.0.0.9:49983/home/n9e?binary=...&audit_log=C:\logs\envd-audit.jsonl#ops
```

```yaml
    remote:
      auditLog: C:\logs\envd-audit.jsonl
```

- 每次 `tools/call` 追加一行 JSON：`ts / tool / profile / ok / duration_ms /
  argument_bytes / target / user`。
- **只记录元数据**：参数内容与返回内容不写入审计（避免把文件正文、命令输出复制进日志）。
- 审计文件由 bridge 以 0600 创建/追加；写入失败会打到 bridge stderr，但不会失败工具调用。

## 6. 运维与排障

| 症状 | 可能原因 | 处置 |
|---|---|---|
| 连接超时 / `000` | 防火墙未放行、服务未启动 | 检查 `iptables -S INPUT \| grep 49983`、`systemctl status envd` |
| `401` + `no user specified` | 请求未带 Basic user | 目标 user 必须是目标机系统用户 |
| `401` + `unauthorized access` | 令牌缺失/错误 | 设置 `AICLI_ENVD_TOKEN`；确认 `/init` 已执行 |
| `/init` 返回 409 | 进程内已有不同令牌 | `systemctl restart envd` 后重新引导 |
| TLS 握手失败 / `x509: certificate is valid for ...` | 证书 SAN 不含实际连接地址 | 重签证书，把客户端连接用的 IP/DNS 加入 SAN |
| 日志刷 `socat: executable file not found` | envd 的 port-forwarder 需要 socat + 169.254.0.21 | 普通服务器属预期噪音，可忽略；不要为此安装 socat |
| 长命令在中途失败 | 调用超时（默认 10m） | 提高 `callTimeout` 或拆分任务 |
| 报告/输出被截断 | bridge 输出上限 400 KiB、view 1 MiB | 分页读取或写文件后分段取 |

日志：`journalctl -u envd -n 100 --no-pager`；健康：`GET /health`（无需令牌）。

## 7. 回滚与卸载

```bash
systemctl disable --now envd
rm -f /usr/local/bin/envd /usr/local/sbin/envd-init-token.sh /usr/local/sbin/envd-firewall.sh
rm -rf /etc/envd /etc/systemd/system/envd.service
systemctl daemon-reload
# 清理防火墙规则（按 -S 输出逐条删除；或重启宿主）
iptables -D INPUT -i lo -p tcp --dport 49983 -j ACCEPT
iptables -D INPUT -s <operator-ip> -p tcp --dport 49983 -j ACCEPT
iptables -D INPUT -p tcp --dport 49983 -j DROP
```

## 8. 已验证记录（2026-10-28）

- 目标机：Ubuntu 24.04.2 LTS，4 vCPU / 32 GiB，x86_64；envd `0.2.11`。
- TLS：自签证书（SAN `127.0.0.1` / 服务器 IP / `localhost`）；`curl --cacert` 下 health 204、无令牌 401、带令牌 200。
- 令牌：`/init` 204（https）；重启后自动重装；无令牌 401 / 有令牌 200。
- 防火墙：临时移除放行后工作站访问超时（000），恢复后 204；回环始终 204。
- 集成测试：`ENVD_TEST_ADDR=https://<host>:49983 ENVD_TEST_USER=n9e ENVD_TEST_TOKEN=<token> ENVD_TEST_CA_FILE=<tls.crt> go test -run '^TestLive' ./internal/envdclient/...` → **10/10 PASS**（含 stdin、SIGTERM、PTY、WatchDir）。
- manager 级 e2e：`AICLI_ENVD_TEST_BRIDGE=<bridge> ENVD_TEST_ADDR=https://<host>:49983 ENVD_TEST_USER=n9e ENVD_TEST_WORKSPACE=/home/n9e ENVD_TEST_TOKEN=<token> ENVD_TEST_CA_FILE=<tls.crt> go test -run TestManager_EnvDBridge_LiveSmoke ./internal/mcp/manager/...` → PASS。
- bridge 冒烟 + aicli agent 真实巡检：见 `docs/architecture/envd-implementation-report.md` §9。
