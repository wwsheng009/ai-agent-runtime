# 分层配置合并：项目级配置文件读写实测记录

- 日期：2026-09-17
- 设计文档：`docs/plan/layered-config-merge-design-20260917.md`
- 被测二进制：当前工作区构建的 `aicli.exe`（含 WIP 分层实现）
- 实测样例项目：`E:\temp\aicli-config-sample\project`（用户级层用隔离 fake home `E:\temp\aicli-config-sample\home`，不触碰真实 `~/.aicli`）

## 0. 复现步骤

```powershell
# 隔离 home + 清掉沙箱注入的 provider 环境变量
$env:USERPROFILE='E:\temp\aicli-config-sample\home'; $env:HOME=$env:USERPROFILE
Remove-Item Env:PROVIDERS_DEFAULT,Env:OPENAI_BASE_URL,Env:OPENAI_DEFAULT_MODEL,
  Env:ANTHROPIC_BASE_URL,Env:ANTHROPIC_DEFAULT_MODEL,Env:GLM_BASE_URL,Env:GLM_DEFAULT_MODEL

# 在样例项目目录（E:\temp\aicli-config-sample\project）执行
aicli init                  # 落用户级
aicli init --project        # 落项目级 ./.aicli/config.yaml
$env:AICLI_CONFIG_MERGE='off' | 'dry-run' | 'on'
aicli config --output json; aicli provider list; aicli provider show <name>
```

样例层内容：
- 用户级：`providers.default_provider=user-prov`、`providers.items.user-prov{protocol,base_url,enabled:true}`、`skills_runtime.config_file=user-runtime.yaml`、`aicli.chat.stream=false`
- 项目级：仅 `aicli.chat.stream=true`（v3）/ 覆盖 `providers.items.user-prov.enabled=false`（v5）/ 写 `providers.default_provider`（v1）/ 写 `default_provider: null`（v2）

## 1. 读取（分层加载）实测矩阵

| 场景 | off | dry-run | on |
|---|---|---|---|
| `init` 落点 | — | — | `aicli init` → 用户级；`aicli init --project` → 项目级 ✅ |
| v1：项目写 `default_provider=opencode.ai` + 自带 `project-prov` | 2 providers（opencode.ai, project-prov），用户层不可见 | 同 off ✅ | 3 providers（+user-prov），default=opencode.ai ✅ |
| v2：项目写 `default_provider: null`（屏蔽） | 1 provider，default='' | 同 off ✅ | 2 providers，default=''（**用户层 user-prov 被屏蔽**）✅ |
| v3：项目不写 providers（回落） | 1 provider，default='' | 同 off ✅ | 2 providers，default=**user-prov** ✅ |
| v5：项目只写 `items.user-prov.enabled=false`（嵌套部分覆盖） | user-prov 存在但 protocol/base_url 为空（`-`） | — | user-prov `enabled=false` + 保留用户层 `protocol=openai`/`base_url=https://user.example.invalid/v1` ✅ |
| 显式 `-c <项目文件>` + `on` | — | — | 只读该文件，**不合并**用户层（2 providers）✅ |
| dry-run 观测 | — | 日志输出 `layers="user:... > project:..."`、`fallback_keys=4`、`overridden_key_count=1`，且行为与 off 一致 ✅ | — |

结论：`off`/`dry-run` 行为与现状一致（零回归），`on` 实现键级回落、深合并、显式 `null` 屏蔽，L4 显式路径短路正确。

## 2. 写入（按层分摊）实测

| 场景 | 命令（on，除注明外） | config_path 落点 | 文件哈希变化 | 结论 |
|---|---|---|---|---|
| W1 禁用用户层 provider | `provider disable user-prov` | 用户级 | user: E5D4AF19→B6A38EEF；**proj 不变** | ✅ 读哪层写哪层 |
| W1b 还原 | `provider enable user-prov` | 用户级 | user 恢复为 E5D4AF19（**逐字节还原**，注释/键序保留） | ✅ R4 字面量保留 |
| W4 禁用项目层 provider（off） | `provider disable project-prov` | 项目级 | proj: 2B4360E3→81A2B81C；user 不变 | ✅ 旧单文件行为不变 |
| W7 禁用项目层 provider（on） | `provider disable project-prov` | 项目级 | proj 变化；user 不变 | ✅ 来源层优先 |
| W5 删除用户层 provider | `provider remove user-prov --yes` | 用户级（已正确改道） | 无改动 | ✅ 路由正确；被 default_provider 守卫拦截（符合预期） |
| W6 新键（两层都没有 `default_provider`） | `provider set-default user-prov` | 用户级 | user 变化；proj 不变 | ✅ 路由到 provider 条目所在层 |

## 3. 实测发现的缺口（已于同日修复，见 §6）

### GAP-1 `provider set-default` 跨层组合直接报错（高）

- 复现（v1：项目层写 `default_provider`，用户层写 `providers.items.user-prov`）：
  `AICLI_CONFIG_MERGE=on aicli provider set-default user-prov --json`
  → `{"ok":false,...,"error":"provider \"user-prov\" not found"}`
- 根因：`SetDefaultProviderConfig`（`internal/agentconfig/provider_management.go:341-355`）先用
  `routeConfigWritePath(configPath, "providers.default_provider", "providers.items."+name)` 选**一个**目标文件
  （两个 key 分属不同层时按层优先级取项目层），随后又要求 provider 条目存在于**该文件**内，
  于是跨层组合必然报 not found。
- 设计文档 §11 把该组合记作"会把 default 写入项目层（语义上可接受）"，实测是**硬失败**，比文档描述更严重。
- 建议修复：路由改为以"provider 条目的来源层"为准（条目不存在时才回落 `default_provider` 的来源层/最高层）；
  或在目标文件缺少条目时回退到条目所在层再写，而不是报错。

### GAP-2 `provider proxy set/remove` 完全没有分层改道（高）

- 复现：`AICLI_CONFIG_MERGE=on aicli provider proxy set user-prov --http http://127.0.0.1:7890 --json`
  → `{"ok":false,...,"error":"provider \"user-prov\" not found"}`
- 根因：`SetProviderProxyConfig`（`internal/agentconfig/provider_proxy.go:111`）与
  `RemoveProviderProxyConfig`（`:280`）直接 `readProviderConfigDocument(configPath)`，**没有调用
  `routeConfigWritePath`**（对照组：`provider_management.go:143/307/346`、`provider_persistence.go:74`、
  `chat_persistence.go:35`、`theme_persistence.go:31` 都有改道）。
  在 `on` 模式下 `cfg.ConfigFilePath = document.SourcePath`（最高存在层 = 项目层），
  用户层的 provider 因此在项目文件里"不存在"。
- 建议修复：两个函数入口加 `routeConfigWritePath(configPath, "providers.items."+name, "providers.items."+name+".proxy")`，
  并补 `config_write_route_test.go` 的 proxy 用例。

### 观察（非缺陷，待确认语义）

- `on` 模式下 `cfg.ConfigFilePath = document.SourcePath` = **最高存在层**（本项目样例中为项目层）；
  未被 origins 覆盖的新键会写到该文件（`WriteTargetForKeys` 返回空时调用方保留默认目标）。
  这与设计 §7 R5"新键写最高可写层（项目级被信任时优先，否则用户级）"在**项目层未过信任门禁**时的预期一致；
  但 §8 的 foldertrust 门禁目前在 `agentconfig` 侧尚未实现（`config_layers.go` 无任何 foldertrust 引用），
  因此"项目级被信任"这一条件实际上恒为真。建议在 P2 补门禁时一并明确该语义并加测试。
- `dry-run` 预览日志只在 `mode == dry-run` 且未显式 `-c` 时输出，字段为层链 + 键名（不含值），符合 §10 日志最小化要求。

## 4. 复现实测缺口的最短命令

```powershell
# GAP-1
Copy-Item variants\v1.yaml $proj -Force
$env:AICLI_CONFIG_MERGE='on'; aicli provider set-default user-prov --json   # provider not found
# GAP-2
aicli provider proxy set user-prov --http http://127.0.0.1:7890 --json      # provider not found
# 对照（可用）
aicli provider disable user-prov --json    # 正确改道用户层，项目文件零改动
```

## 5. 测试缺口（现有单测全绿但未覆盖上述组合）

`go test ./internal/agentconfig/ -count=1` 全绿（含 `TestWriteTargetForKeysPrefersSpecificThenHigherLayer`、
`TestUpdateProviderConfigWritesBackToOriginLayer`、`TestApplyMergedDocumentChangesWritesEachKeyToItsLayer`、
`TestLayeredConfigDocumentMergesDeeplyMasksNullsAndReplacesSlices` 等），说明 GAP-1/GAP-2 属于**覆盖缺口**而非回归：

- 缺 `SetProviderProxyConfig` / `RemoveProviderProxyConfig` 的分层改道用例（两函数当前根本没有改道调用）；
- 缺 `SetDefaultProviderConfig` 的"provider 条目与 default_provider 分属不同层"用例；
- 缺"项目层 provider + 用户层 default_provider"反向组合用例（对称场景，需一并覆盖）。

建议修复时同步补 3 个用例，并复用 `config_write_route_test.go` 的双层 fixture 写法。

## 6. 缺口修复记录（2026-09-17）

### 代码改动

| 缺口 | 文件 | 改动 |
|---|---|---|
| GAP-1 | `internal/agentconfig/provider_management.go`（`SetDefaultProviderConfig`） | 文件内存在性校验改为"文件内 **或** 合并视图内存在"：`mappingValue(itemsNode, name) == nil && !providerKnownToMergedConfig(name)`；路由仍落在 `providers.default_provider` 的来源层（保证写生效），不再因为 provider 条目在低层而报 not found。 |
| GAP-1 | `internal/agentconfig/config_write_route.go` | 新增 `providerKnownToMergedConfig(name)`：仅 `MergeModeOn` 时按合并视图（含预设层）做大小写不敏感查找；单文件/off 模式返回 false，保持原严格语义。 |
| GAP-2 | `internal/agentconfig/provider_proxy.go`（`SetProviderProxyConfig` / `RemoveProviderProxyConfig`） | 读取目标文档前追加 `routeConfigWritePath(configPath, "providers.items."+name+".proxy", "providers.items."+name)`，与 `SetProvidersEnabledConfig` 等保持一致；`RemoveProviderProxyConfig` 内部调用 `UpdateProviderConfig` 二次路由幂等。 |

### 新增回归测试

`internal/agentconfig/config_write_route_provider_test.go`（4 例，全部通过）：

1. `TestSetDefaultProviderConfigWritesAcrossLayers` — 项目层 pin `default_provider`、用户层持有 provider：写入落到项目层、用户层字节不变、重载后合并视图 `default_provider=user-prov`。
2. `TestSetProviderProxyConfigRoutesToProviderLayer` — proxy set/remove 均落到用户层，项目层字节不变。
3. `TestProviderWritesRejectUnknownProviders` — 两层都不存在的 provider 仍然报 not found，且不改动文件。
4. `TestProviderProxyWriteStaysFileLocalWithoutLayering` — off 模式保持单文件查找语义（不跨层）。

### 修复后真实二进制复测（同一 E:\temp 样例）

```powershell
Copy-Item variants\v1.yaml $proj -Force      # 项目层 pin default_provider，用户层持有 user-prov
$env:AICLI_CONFIG_MERGE='on'
aicli provider set-default user-prov --json
# → {"config_path":"...\\project\\.aicli\\config.yaml","default_provider":"user-prov","previous_default":"opencode.ai"}
#   user=E5D4AF198D 不变；proj 2B4360E3E8→DE7C325729
aicli provider proxy set user-prov --http http://127.0.0.1:7890 --json
# → {"config_path":"...\\home\\.aicli\\config.yaml","proxy":{"http":"http://127.0.0.1:7890","enabled":true}}
#   user→A26A6EFA19；proj=DE7C325729 不变
aicli provider proxy remove user-prov --json
# → {"config_path":"...\\home\\.aicli\\config.yaml","removed":true}
aicli provider list            # → user-prov DEFAULT=true（合并视图生效）
```

`off` 模式对照：`provider proxy set user-prov` 仍返回 `provider "user-prov" not found`（单文件语义未变，符合预期）。

### 回归验证

- `go test ./internal/agentconfig/ -count=1` → ok
- `go test ./internal/runtimeserver/ -count=1` → ok
- `go test ./internal/api/skills/ -count=1` → ok
- `go test ./cmd/aicli/commands/ -run 'Provider|Config' -count=1` → ok
- `go vet ./internal/agentconfig/` → 无输出（clean）

### 仍待确认/未覆盖

- 批量操作跨层（`provider remove/enable/disable` 多个条目分散多层）仍是最初设计记录的已知限制，本次未改动；
- 预设层独有 provider（如 `opencode.ai`）的 `proxy set` 在单文件/分层模式下都仍报 not found（需先有可写条目），属既有行为，未纳入本次修复；
- §8 foldertrust 门禁仍未实现（P2）。
