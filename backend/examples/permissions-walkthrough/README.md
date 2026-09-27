# permissions-walkthrough：权限手册的可执行示例工程

这个目录把 [`docs/aicli/permissions.md`](../../../docs/aicli/permissions.md)
的规则语法、模式语义与硬门做成一个能跑、能自证的最小项目：

| 文件 | 作用 |
|------|------|
| `.aicli/permissions.yaml` | 项目级权限文件：每条规则对应手册的一个能力（含 `disable_bypass`） |
| `walkthrough_test.go` | 用真实 `internal/policy.Engine` 装载上面的文件，跑决策表并断言 `Type`/`Stage`/`Reason` |
| `doc.go` | 包声明（让 `go build ./...` 覆盖本目录） |
| `README.md` | 本文件 |

> **位置说明**：Go 的 `internal` 可见性规则要求导入 `backend/internal/policy` 的代码
> 必须位于 `backend/` 模块树内，所以示例工程放在 `backend/examples/permissions-walkthrough/`。
> 下文所有 Go 命令都在 **`backend/`（模块根）** 执行；`aicli` 命令在示例目录执行
> （CLI 以当前工作目录为项目根读取 `.aicli/permissions.yaml`，见手册 §2.1）。

## 一分钟上手

```bash
# 1) 从仓库根进入 Go 模块
cd backend

# 2) 跑示例自验：决策表 + disable_bypass 对照 + 硬门
go test ./examples/permissions-walkthrough/ -count=1

# 3) 想看每条子测试（= 决策表每一行）为什么是这个结果：
go test ./examples/permissions-walkthrough/ -count=1 -v
```

测试全绿即表示：README 下面表格里的每一个「期望行为」都刚被真实引擎复现过。
决策表本体在 `walkthrough_test.go`，每一行上方的注释写明了原因并指回手册章节。

## 示例 → 期望行为 → 手册

真实决策列是测试断言的原文（`Type` / `Stage` / `Reason` 关键片段）。
`Stage` 是区分「规则命中」与「默认模式 / 只读快车道」的关键（阶段表见手册 §3.1）。

| `.aicli/permissions.yaml` 条目 | 真实决策（测试断言） | 手册 |
|---|---|---|
| `disable_bypass: true` | bypass 请求被降级为 default：写请求 → `allow`/`ask`（批准后 `ask:approved`）；headless → `deny`/`headless_deny` | §1.3 |
| `Shell(git status:*)` / `Shell(git diff:*)` → `allow` | `git status` → `allow`/`rules`/`rules:allow_git_readonly` | §2.2 / §4.1 |
| 同上（同一 specifier 的复合命令） | `git diff --stat && git diff --cached` → `allow`/`rules`（每段都被 `git diff:*` 覆盖） | §2.2 / §6.5 |
| 同上（跨 specifier 的复合命令） | `git status --short && git diff --stat` → 规则不命中，`allow`/`readonly_auto`（只读命令表；见「已知差异」） | §4.1 |
| `Shell(git push:*)` → `ask` | `git push origin main` → 审批 `ask`（批准后 `allow`/`ask`/`ask:approved`；审批请求 reason=`rules:review_git_push`） | §4.2 / §3.3 |
| `Read(.env)` / `Read(**/*.pem)` → `ask` | `view .env`、`view certs/server.pem` → 审批（reason=`rules:review_secret_reads`） | §2.3 / §4.4 |
| `Edit(dist/**)` / `Edit(.git/**)` → `deny` | `edit dist/app.js`、`edit .git/config` → `deny`/`rules`/`rules:protect_generated`，不弹审批 | §4.3 / §4.4 |
| `Edit(src/**)` → `allow` | `edit src/main.go` → `allow`/`rules`；`edit lib/util.go` 不命中 → 回落 `ask`/`mode` | §2.3 / §1.1 |
| `WebFetch(domain:docs.example.com)` → `allow` | `fetch`/`download` 该主机 → `allow`/`rules`；`download` 其它域 → 回落 `ask`/`mode` | §2.4 |
| （无对应规则）`fetch https://example.com/...` | 规则不命中 → `allow`/`readonly_auto`/`taxonomy_readonly:fetch`（见「已知差异」） | §3.1 第 7 阶 |

## 硬门怎么验（不写在 yaml 里）

手册 §3.2 的三条硬门不通过 `rules` 配置；其中两条已被测试直接构造并固化：

| 硬门 | 测试里的构造 | 实测结果 |
|---|---|---|
| 根/主目录断路器 | `shell` 执行 `rm -rf /`，bypass 模式 | 仍 `HardAsk`：必须人工批准（请求 reason=`shell_breaker:root_home_removal`）；`dont_ask` 下 `deny`/`shell_breaker` |
| 敏感写保护 | `write` 目标 `.env` | default 询问（reason=`sensitive_write:secret`）、`dont_ask` 拒绝；**raw bypass 会跳过它**（手册 §0.1/§3.2）；示例的 `disable_bypass` 先降级 default，门重新生效 |
| 外部目录门 | 需要会话工作区之外的路径与会话准入集合，测试未构造 | 手工验证见下节「外部目录」；语义见手册 §4.6 / §3.2 第 3 条 |

## 亲手验证（copy-paste）

前置：已按 [`docs/aicli/install.md`](../../../docs/aicli/install.md) 构建/安装 `aicli`。
先进入示例目录——CLI 以 cwd 为项目根读取权限文件：

```powershell
# 从仓库根
cd backend/examples/permissions-walkthrough
```

> `aicli exec` 默认关闭 tools（headless 安全默认），需要 `--enable-tools`
> （[`exec.md`](../../../docs/aicli/exec.md) §八）。headless 没有 AskHandler，
> 任何 ask 都会 fail closed 为 `headless_deny:approval_required`（手册 §3.3）——
> 这正是下面「ask 用 exec 看不到面板」的原因；想看面板用 `aicli chat`。

1. allow 规则命中（期望：不产生审批，直接执行并返回 `git status` 输出）：

   ```powershell
   aicli exec --enable-tools --prompt "运行 git status，把输出原样报告给我"
   ```

2. ask 规则（期望：headless 下拒绝，reason 含 `headless_deny:approval_required`）：

   ```powershell
   aicli exec --enable-tools --prompt "运行 git push origin main 并报告结果"
   ```

   想看真实审批面板（`[1] 仅本次允许 [2] 拒绝 [3] 查看完整参数 [5] 拒绝并说明原因 [6] 解释这次调用`）：

   ```powershell
   aicli chat
   # 输入：请运行 git push origin main
   # 面板的 reason 应显示 rules:review_git_push（手册 §4.2 / §3.3）
   ```

3. deny 规则（期望：工具失败且不弹审批；`--json` 事件流里能看到失败原因含 `protect_generated`）：

   ```powershell
   aicli exec --enable-tools --json --prompt "编辑 dist/app.js，把 log 改成 1"
   ```

4. `dont_ask`（CI 形态，期望：拒绝，reason 含 `sensitive_write:secret`；手册 §3.2 / §4.5）：

   ```powershell
   aicli exec --permission-mode dont_ask --enable-tools --prompt "向 .env 写入 FOO=1 并报告结果"
   ```

5. `disable_bypass`（期望：即使 `--yolo`，headless 写操作也不会静默放行，reason 含 `approval_required`；手册 §1.3）：

   ```powershell
   aicli exec --yolo --enable-tools --prompt "创建 notes-tmp.md 并写入 hello，然后报告结果"
   ```

   > 进程启动参数无法交互确认，只能靠引擎侧降级；交互式下
   > `/permission-mode bypass_permissions` 会被直接拒绝（手册 §1.3 / §1.4）。

6. 外部目录门（期望：工作区外路径先要一次准入，批准后该目录并入会话；手册 §4.6 / §3.2 第 3 条）：

   ```powershell
   aicli chat --add-dir ../../..     # 预准入仓库根（示例目录向上三级）
   # 或会话内：/add-dir ../../..   、/add-dir list
   ```

7. 规则语法只认 `rules[].tools`（`deny_tools`/`--deny-tool` 是精确工具名；手册 §2.7）：

   ```powershell
   aicli exec --deny-tool download --enable-tools --prompt "用 download 取 https://docs.example.com/data.csv"
   # 期望：download 被硬拒；写 Shell(download:*) 这类 specifier 到 deny_tools 会被装载期拒绝
   ```

## 已知差异（实测行为 vs 手册文字）

以下是本次落地示例时用真实引擎复现出的、与手册表述不一致的两处；**手册正文已按实测行为修正（2026-09-26）**，
示例测试按**实测行为**断言，下面的用例名是后续复现的锚点。

1. **`§4.1` 复合命令的 allow 覆盖粒度**：手册说 `git status && git log` 可由同一条规则里的
   多条 specifier「都覆盖」。实测 allow 的「每段都命中」只在**单个 specifier** 内判定：
   `Shell(git status:*)` 与 `Shell(git diff:*)` 拼不出 `git status && git diff` 的放行
   （`compound_across_specifiers_not_covered_by_allow` 断言 `Stage=readonly_auto` 而不是
   `rules`，证明规则未命中；该命令整体只读才被 §3.1 第 7 阶兜住）。
   影响：手册 §0.2/§4.1 的只读 git recipe 对复合读命令并不生效（只是恰好被只读命令表放行）。
   **是否要让 allow 支持「规则级多条 specifier 组合覆盖」属于放宽权限的产品决策**，本轮只修手册、未动实现。
2. **`§1.1` 模式矩阵的「网络」行**：手册把 `fetch` 列在 default 下「询问」。实测 `fetch`
   在引擎 taxonomy 里是 `ReadOnly=true`（`backend/internal/policy/taxonomy.go`），
   未命中规则时走 §3.1 第 7 阶只读快车道（`taxonomy_readonly:fetch`），**不会询问**；
   会写盘的 `download` 才按手册询问。`web_search` 同 `fetch` 路径（`taxonomy.go`），手册该行已拆成
   「网络只读 / 网络写盘」两行。

两处差异的**手册正文已修正**（见 §1.1 与 §4.1）；若后续决定改实现（放宽 allow 的组合覆盖），
请同步更新本示例的断言与上表。

## 维护提示

- `.aicli/permissions.yaml` 被仓库根 `.gitignore` 的 `.aicli/` 规则覆盖：修改后需要
  `git add -f backend/examples/permissions-walkthrough/.aicli/permissions.yaml` 才会进版本控制。
- 改动 yaml 后务必重跑 `go test ./examples/permissions-walkthrough/`（在 `backend/` 下执行）：
  决策表断言就是本示例的“说明书”，测试挂了说明规则语义或文档已经漂移。

## 截图怎么出

**仓库暂不存放截图文件**（二进制产物容易与真实 UI 脱节；本示例不提交、也不伪造截图）。
需要配图时，请在真实终端按下面步骤采集。

统一设置：

- 终端：Windows Terminal / iTerm2 / GNOME Terminal，深色主题；
- 窗口约 1600×900（或 ≥120×36 字符、字号 14–16），保证审批面板不折行；
- PNG 输出；引用到文档时建议宽度 ≤ 1280px，单图只留一个焦点（裁掉无关窗口）。

**A. 审批面板 `[1]`–`[6]`（核心图）**

1. `cd backend/examples/permissions-walkthrough`
2. `aicli chat`（权限模式保持 `default`）
3. 输入 `请运行 git push origin main`，等审批面板出现；
4. 确认面板上有 reason `rules:review_git_push` 与选项行——`[1] 仅本次允许 [2] 拒绝
   [3] 查看完整参数 [5] 拒绝并说明原因 [6] 解释这次调用`（只读场景另有
   `[4] 会话/团队内复用 10 分钟`，手册 §3.3）；
5. 截图整帧（命令 + reason + 选项行），建议名 `permissions-approval-panel.png`。

**B. 模式面板**

1. `aicli chat` 内输入 `/mode`（或 `/permission-mode`）查看当前模式与可选项；
   `/mode:accept_edits` 可演示切换（切 `bypass_permissions` 需要本机终端二次确认，
   手册 §0.3 / §1.4）；
2. 截图模式列表 + 当前模式行，建议名 `permissions-mode-panel.png`。

**C. grants 面板**

1. 触发一次写审批并选择记住（会话/项目作用域），再输入 `/grants list`
   （`/grants status` 看作用域、`/grants revoke ...` 收窄；手册 §5.2）；
2. 截图授权列表，建议名 `permissions-grants.png`。
   注意：project 记忆会写 `<workspace>/.aicli/grants.json`，示例目录里生成的它不要提交。

**D. headless 拒绝（可选）**

```powershell
aicli exec --permission-mode dont_ask --enable-tools --prompt "向 .env 写入 FOO=1 并报告结果"
```

截取终端输出中的拒绝原因（含 `sensitive_write`）。

落地约定：仓库当前没有截图目录；若要纳入，建议 `docs/aicli/assets/` 并在手册
§3.3/§5.1 旁引用（先与文档负责人确认目录与体积，单图建议 < 300KB）。
面板选项与行为以 `docs/aicli/interactive-mode.md` §3 与 `permissions.md` §3.3 为准，
不要用合成图代替真实终端采集。
