# 19 · 非本地访问 Token

> 状态：已实现（2026-11-21）
> 适用范围：`runtime-server` 直连访问、`aicli-gateway` 外部入口与节点反代。

## 1. 目标

- 本机（loopback）访问保持零 token：本机 UI、本机 CLI、本机 gateway agent 不受影响。
- 非 loopback 访问 `runtime-server` 的 `/api/*` 必须携带访问 token。
- gateway 外部入口继续支持 owner 会话；同时支持 Bearer access token，供无 Cookie 的程序化客户端使用。
- 本机凭据不跨网络：浏览器 / 网关不接触 runtime-server 的 access token。
- 通过 gateway 访问时，已注册的 agent 在回环转发中注入 gateway 设备令牌；runtime-server
  据此识别“已注册 gateway 路径”，不再要求用户输入 runtime access token。

## 2. 信任边界

```text
本机浏览器 / CLI
   │ loopback，无 token
   ▼
runtime-server 127.0.0.1:8101
   ▲
   │ loopback，无 token（gateway agent 注入本机写令牌）
   │
aicli-gateway agent
   ▲
   │ wss + 设备令牌（L2）
   │
aicli-gateway（公网）
   ▲
   │ owner 会话 Cookie 或 Bearer access token
   │
外网浏览器 / API 客户端
```

直接访问非 loopback 的 runtime-server 时，`Authorization: Bearer <token>` 或
`X-AICLI-Access-Token: <token>` 必须匹配 `server.access_token`。

## 3. runtime-server 配置

```yaml
server:
  access_token: "change-me"
```

或：

```bash
export SERVER_ACCESS_TOKEN='change-me'
```

规则：

- 空值 = 拒绝所有非 loopback 的 `/api/*` 请求（fail-closed）。
- 只保护 `/api/*`；`/`（SPA 静态资源）和 `/healthz` 保持可访问。
- 判定只使用直连 peer 地址，忽略 `X-Forwarded-For`，防止远程伪造 loopback。
- 修改后需重启 runtime-server。

### 3.1 浏览器登录页

非本地浏览器访问 runtime-server SPA 时，前端 `AccessGate` 会先调用：

```text
GET  /api/runtime/auth/access/status
POST /api/runtime/auth/access/login   {"token":"..."}
POST /api/runtime/auth/access/logout
```

登录成功后服务端签发 **HttpOnly + SameSite=Lax** 的短期会话 Cookie（不保存原始 token），
后续 `fetch`、SSE 与下载请求都会自动携带，避免逐请求设置 Authorization。本地 loopback
访问的 status 返回 `required=false`，不会出现登录页。

直连示例：

```bash
curl -H "Authorization: Bearer $SERVER_ACCESS_TOKEN" \
  http://runtime-host:8101/api/runtime/status
```

## 4. gateway 配置

```bash
aicli-gateway --access-token "$GW_ACCESS_TOKEN"
```

flagfile：

```yaml
access-token: "change-me"
```

或：

```bash
export AICLI_GATEWAY_ACCESS_TOKEN='change-me'
```

`/n/{node_id}/...` 现在接受：

- owner 会话 Cookie（浏览器控制台，原有行为）；
- `Authorization: Bearer <gateway access token>`（程序化客户端）。

示例：

```bash
curl -H "Authorization: Bearer $GW_ACCESS_TOKEN" \
  https://gateway.example.com/n/node-1/api/runtime/status
```

gateway 只负责外部入口鉴权；转发到本机 runtime-server 时走 loopback，因此不需要
把 gateway token 下发给节点。

### 4.1 gateway 注册后的免 token 访问

gateway agent 用设备令牌完成注册后，会把同一个设备令牌以
`X-AICLI-Gateway-Token` 注入到本机 loopback 转发请求。runtime-server 校验该头后，
把请求视为“已注册 gateway 路径”，跳过用户侧 runtime access token。

- 设备令牌只存在于节点本机内存与 gateway 注册链路，不进入浏览器。
- gateway 对外的 owner 会话 / `--access-token` 仍然必须成立。
- 外部浏览器不需要、也看不到 runtime access token；登录 gateway 后即可访问
  `/n/{node_id}/...`。
- 未登录浏览器直接打开 `/n/{node_id}/...` 时，gateway 会 302 到控制台登录页并带上
  `?next=<原始路径>`；登录成功后自动返回原节点 URL，不需要再次输入任何 token。
- 未注册或设备令牌不匹配时，该头不产生信任。

## 5. 头处理

以下头由 gateway / agent 视为本机凭据，调用方不得注入：

- `X-AICLI-Token`
- `X-AICLI-Access-Token`
- `X-AICLI-Gateway-Token`
- `X-Skills-Admin-Token`
- `Authorization`

gateway 代理层和 agent 转发层都会剥离这些头；响应中的同名头也会被过滤，避免本机
凭据回显到外网。

## 6. 已知边界

- 直接远程打开 runtime-server 的 SPA 会显示访问 Token 登录页；登录后使用
  HttpOnly 会话 Cookie。远程浏览器也可走 gateway 控制台，登录 gateway 后不再
  输入 runtime token。
- 本机反向代理（nginx 等）若把外网流量转发到 runtime-server 的 loopback 地址，
  runtime-server 会把该代理视为本地进程。此类部署必须由代理自行做外部鉴权。
- 当前仍是单 token；按路径/操作划分 scope、多 token 轮换与审计属于后续统一方案。
